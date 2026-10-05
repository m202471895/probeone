// Package monitor 实现网站监控的探测器。
//
// P4 范围：HTTP、TCP、DNS、SSL 四类。
// ICMP（Ping）推迟到 P8 之后——它需要 raw socket或 setcap，
// 而"TCP 端口能否连通"在 VPS 场景下诊断价值更高且零权限障碍。
//
// 核心约定（PRD 3.2）：**失败必须记录具体原因枚举**，不能只存一个 bool。
// "连接被拒绝"和"响应超时"对排障的指向完全不同。
package monitor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/m202471895/probeone/server/internal/model"
)

// Result 是一次探测的结果。
type Result struct {
	OK     bool
	Reason model.FailReason
	// 下面是分段耗时，用于定位"慢在哪一步"
	DNSMs      *int
	TCPMs      *int
	TLSMs      *int
	TTFBMs     *int
	TotalMs    int
	StatusCode *int
	// Detail 是面向排障的简短说明（不含响应体内容）
	Detail string
	// Certificate 在 SSL/HTTPS 探测时填充
	Certificate *model.SSLCertificate
}

// maxBodyBytes 限制读取的响应体大小。
//
// 为什么要限：监控目标可能返回几百 MB 的文件（全量备份、镜像仓库），
// 读完会把服务端内存吃光。1MB 足够做关键字匹配。
const maxBodyBytes = 1 << 20

// maxKeywordSearch 只在响应体前 maxKeywordSearch 字节里找关键字。
// 超出部分的页面底部（如巨大的页脚）不影响业务判断。
const maxKeywordSearch = 512 << 10

// Prober 是各类探测器的统一入口。
type Prober struct {
	client       *http.Client
	allowPrivate bool
}

// New 创建探测器。
// allowPrivate=false 时禁止访问内网地址（SSRF 防护，PRD T11）。
func New(allowPrivate bool) *Prober {
	return &Prober{
		client: &http.Client{
			// 不自动跟随跳转：3xx 是可观测的状态码，
			// 跟随跳转会掩盖"网站改了域名但没配重定向"这类问题
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Transport: &http.Transport{
				DisableKeepAlives:     false,
				MaxIdleConns:          10,
				IdleConnTimeout:       30 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
				ResponseHeaderTimeout: 15 * time.Second,
			},
		},
		allowPrivate: allowPrivate,
	}
}

// Probe 按监控类型分发到对应的探测器。
func (p *Prober) Probe(m *model.Monitor) *Result {
	switch m.Type {
	case model.MonitorHTTP:
		return p.probeHTTP(m)
	case model.MonitorTCP:
		return p.probeTCP(m)
	case model.MonitorDNS:
		return p.probeDNS(m)
	case model.MonitorSSL:
		return p.probeSSL(m)
	default:
		return &Result{
			OK: false, Reason: model.ReasonUnknown,
			Detail: "未知的监控类型: " + string(m.Type),
		}
	}
}

// ---------- HTTP ----------

// probeHTTP 的判定顺序（PRD 3.2，不可调换）：
//
//	超时/连接错误 → status_mismatch → content_mismatch → slow → 成功
//
// 顺序有讲究：先看"能不能通"，再看"内容对不对"，最后才看"够不够快"。
// 反过来会让"页面正常但慢"被误报成"内容不匹配"。
func (p *Prober) probeHTTP(m *model.Monitor) *Result {
	start := time.Now()
	timeout := time.Duration(m.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	u, err := url.Parse(m.Target)
	if err != nil {
		return &Result{OK: false, Reason: model.ReasonDNSError,
			Detail: "URL 解析失败: " + err.Error()}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return &Result{OK: false, Reason: model.ReasonBadRequest,
			Detail: "URL 协议必须是 http 或 https，当前: " + u.Scheme}
	}

	// SSRF 防护：解析域名得到 IP 后检查是否内网
	// 解析结果本身不用——HTTP 客户端会自己再解析一次，
	// 保持"连接时用的域名"与"校验时用的 IP"解耦，
	// 避免 DNS rebinding 场景下的绕过。
	if _, err := resolveAndCheck(m.Target, p.allowPrivate); err != nil {
		return &Result{OK: false, Reason: model.ReasonBlocked, Detail: err.Error()}
	}

	method := m.Config.Method
	if method == "" {
		method = http.MethodGet
	}
	reqCtx, cancel := contextWithTimeout(timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, method, m.Target, nil)
	if err != nil {
		return &Result{OK: false, Reason: model.ReasonBadRequest,
			Detail: "构造请求失败: " + err.Error()}
	}
	// 显式设置 UA：部分站点对空 UA 直接 403
	req.Header.Set("User-Agent", "ProbeOne/1.0 (+https://github.com/m202471895/probeone)")
	req.Header.Set("Accept", "*/*")
	for k, v := range m.Config.Headers {
		req.Header.Set(k, v)
	}

	// 手动控制重定向，以便记录首跳耗时
	client := *p.client
	client.Timeout = timeout
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	resp, err := client.Do(req)
	if err != nil {
		return classifyHTTPError(err, start, timeout)
	}
	defer resp.Body.Close()

	ttfbMs := int(time.Since(start).Milliseconds())

	// 证书信息（HTTPS 时）
	var cert *model.SSLCertificate
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		cert = certFromState(resp.TLS)
	}

	// 读状态码
	code := resp.StatusCode

	// 读响应体（限量）
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	totalMs := int(time.Since(start).Milliseconds())
	if readErr != nil && len(body) == 0 {
		return &Result{OK: false, Reason: model.ReasonTimeout,
			TTFBMs: &ttfbMs, TotalMs: totalMs,
			Detail: "读取响应体失败: " + readErr.Error()}
	}

	res := &Result{
		StatusCode:  &code,
		TTFBMs:      &ttfbMs,
		TotalMs:     totalMs,
		Certificate: cert,
	}

	// 判定 1：状态码
	if !statusMatches(code, m.Config.ExpectStatus) {
		res.Reason = model.ReasonStatusMismatch
		res.Detail = fmt.Sprintf("状态码 %d 不在期望范围 %v", code, m.Config.ExpectStatus)
		return res
	}

	// 判定 2：关键字
	if len(m.Config.ExpectKeywords) > 0 {
		if !containsAllKeywords(string(body), m.Config.ExpectKeywords) {
			res.Reason = model.ReasonContentMismatch
			res.Detail = fmt.Sprintf("响应体缺少关键字 %v", m.Config.ExpectKeywords)
			return res
		}
	}

	// 判定 3：响应时间
	if m.Config.MaxLatencyMs > 0 && totalMs > m.Config.MaxLatencyMs {
		res.Reason = model.ReasonSlow
		res.Detail = fmt.Sprintf("响应 %dms 超过阈值 %dms", totalMs, m.Config.MaxLatencyMs)
		return res
	}

	res.OK = true
	res.Reason = model.ReasonOK
	res.Detail = fmt.Sprintf("状态码 %d，耗时 %dms", code, totalMs)
	return res
}

// ---------- TCP ----------

// probeTCP 测端口连通性。
//
// 为什么 TCP 比 ICMP 更实用：VPS 场景下"80/443 端口通不通"
// 直接决定网站能不能访问，而 ICMP 常被云厂商的 DDoS 防护限速甚至全禁，
// 拿到"通"也不代表业务正常。
func (p *Prober) probeTCP(m *model.Monitor) *Result {
	start := time.Now()

	host := m.Target
	port := m.Config.Port
	if port == 0 {
		return &Result{OK: false, Reason: model.ReasonBadRequest, Detail: "未配置端口"}
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		// target 里没带端口，补上
		host = net.JoinHostPort(host, fmt.Sprint(port))
	}

	ips, err := resolveAndCheck(host, p.allowPrivate)
	if err != nil {
		return &Result{OK: false, Reason: model.ReasonBlocked, Detail: err.Error()}
	}

	timeout := time.Duration(m.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	//逐个 IP尝试：第一个成功即算通。
	// 多 IP 时有的通有的不通是常见的（如DNS 轮询到不同机房）
	var lastErr error
	for _, ip := range ips {
		d := net.Dialer{Timeout: timeout}
		conn, err := d.Dial("tcp", net.JoinHostPort(ip, fmt.Sprint(port)))
		if err != nil {
			lastErr = err
			continue
		}
		tcpMs := int(time.Since(start).Milliseconds())
		_ = conn.Close()
		return &Result{OK: true, Reason: model.ReasonOK,
			TCPMs: &tcpMs, TotalMs: tcpMs,
			Detail: fmt.Sprintf("TCP 连接成功 (%s:%d)，耗时 %dms", ip, port, tcpMs)}
	}

	totalMs := int(time.Since(start).Milliseconds())
	res := &Result{TotalMs: totalMs}
	if lastErr != nil {
		res.Reason = classifyNetError(lastErr)
		res.Detail = fmt.Sprintf("TCP 连接失败: %v", lastErr)
	} else {
		res.Reason = model.ReasonConnRefused
		res.Detail = "目标未解析出可用 IP"
	}
	return res
}

// ---------- DNS ----------

func (p *Prober) probeDNS(m *model.Monitor) *Result {
	start := time.Now()
	timeout := time.Duration(m.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	record := m.Config.Record
	if record == "" {
		record = "A"
	}

	resolver := net.Resolver{}
	if m.Config.Resolver != "" {
		// 自定义 DNS：直接指定服务器
		resolver = net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{Timeout: timeout}
				return d.DialContext(ctx, network, m.Config.Resolver)
			},
		}
	}

	lookupHost := strings.TrimSuffix(m.Target, ".")
	var ips []net.IP
	var lookupErr error

	switch record {
	case "A", "AAAA":
		var addrs []net.IPAddr
		addrs, lookupErr = resolver.LookupIPAddr(contextWithTimeoutValue(timeout), lookupHost)
		for _, a := range addrs {
			ips = append(ips, a.IP)
		}
	case "CNAME":
		var name string
		name, lookupErr = resolver.LookupCNAME(contextWithTimeoutValue(timeout), lookupHost)
		if lookupErr == nil {
			totalMs := int(time.Since(start).Milliseconds())
			// ExpectIPs 对 CNAME 同样有效：把期望的 CNAME 放进去即可
			if len(m.Config.ExpectIPs) > 0 && containsAny(name, m.Config.ExpectIPs) {
				return &Result{OK: true, Reason: model.ReasonOK, TotalMs: totalMs,
					Detail: "CNAME 匹配: " + name}
			}
			return &Result{OK: false, Reason: model.ReasonContentMismatch, TotalMs: totalMs,
				Detail: "CNAME 不匹配，实际为: " + name}
		}
	case "MX", "TXT", "NS":
		return &Result{OK: false, Reason: model.ReasonBadRequest,
			Detail: "P4暂不支持记录类型 " + record + "，请用 A/AAAA/CNAME"}
	default:
		return &Result{OK: false, Reason: model.ReasonBadRequest,
			Detail: "未知的记录类型: " + record}
	}

	dnsMs := int(time.Since(start).Milliseconds())

	if lookupErr != nil {
		return &Result{OK: false, Reason: model.ReasonDNSError, DNSMs: &dnsMs, TotalMs: dnsMs,
			Detail: "DNS 解析失败: " + lookupErr.Error()}
	}
	if len(ips) == 0 {
		return &Result{OK: false, Reason: model.ReasonDNSError, DNSMs: &dnsMs, TotalMs: dnsMs,
			Detail: "DNS 解析结果为空"}
	}

	strs := make([]string, 0, len(ips))
	for _, ip := range ips {
		strs = append(strs, ip.String())
	}
	if len(m.Config.ExpectIPs) > 0 {
		if !containsAnyExpected(strs, m.Config.ExpectIPs) {
			return &Result{OK: false, Reason: model.ReasonContentMismatch,
				DNSMs: &dnsMs, TotalMs: dnsMs,
				Detail: fmt.Sprintf("解析结果 %v 不含期望值 %v", strs, m.Config.ExpectIPs)}
		}
	}

	return &Result{OK: true, Reason: model.ReasonOK, DNSMs: &dnsMs, TotalMs: dnsMs,
		Detail: fmt.Sprintf("解析成功: %s", strings.Join(strs, ", "))}
}

// ---------- SSL 证书 ----------

// probeSSL 只做证书检查，不发 HTTP 请求。
// 端口默认443。
func (p *Prober) probeSSL(m *model.Monitor) *Result {
	start := time.Now()

	host := m.Target
	port := m.Config.Port
	if port == 0 {
		port = 443
	}
	if !strings.Contains(host, ":") {
		host = net.JoinHostPort(host, fmt.Sprint(port))
	}

	ips, err := resolveAndCheck(m.Target, p.allowPrivate)
	if err != nil {
		return &Result{OK: false, Reason: model.ReasonBlocked, Detail: err.Error()}
	}

	timeout := time.Duration(m.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	addr := host
	if len(ips) > 0 {
		// 用解析出的 IP 连，但ServerName 仍用域名（SNI + 证书校验）
		addr = net.JoinHostPort(ips[0], fmt.Sprint(port))
	}

	dialer := &net.Dialer{Timeout: timeout}
	// InsecureSkipVerify=true：这里只是要读证书，不是要验证连接。
	// 证书有效性由我们自己判断（剩余天数），交给 Go 默认逻辑会
	// 在证书过期时直接报连不上，反而拿不到过期时间。
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		InsecureSkipVerify: true, // #nosec G402 —— 见上方注释
		ServerName:         m.Target,
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		totalMs := int(time.Since(start).Milliseconds())
		return &Result{OK: false, Reason: model.ReasonTLSError, TotalMs: totalMs,
			Detail: "TLS 握手失败: " + err.Error()}
	}
	defer conn.Close()

	tlsMs := int(time.Since(start).Milliseconds())
	state := conn.ConnectionState()
	cert := certFromState(&state)
	if cert == nil {
		return &Result{OK: false, Reason: model.ReasonTLSError, TLSMs: &tlsMs, TotalMs: tlsMs,
			Detail: "未获取到证书"}
	}

	res := &Result{
		TLSMs:       &tlsMs,
		TotalMs:     tlsMs,
		Certificate: cert,
	}

	if cert.DaysLeft == nil {
		res.Reason = model.ReasonTLSError
		res.Detail = "无法解析证书有效期"
		return res
	}

	// 证书已过期 → 失败
	if *cert.DaysLeft < 0 {
		res.Reason = model.ReasonTLSExpired
		res.Detail = fmt.Sprintf("证书已于 %s 过期", cert.NotAfter.Format("2006-01-02"))
		return res
	}

	// 证书本身有效。是否判定为"失败"取决于阈值：
	// 剩余不足阈值时给 warning（这是需要行动的信号，但服务还活着）
	// 0 表示只检查是否过期，不设临期告警
	threshold := m.Config.WarnDaysLeft
	if threshold > 0 && *cert.DaysLeft < threshold {
		res.OK = true
		res.Reason = model.ReasonCertExpiring
		res.Detail = fmt.Sprintf("证书将在 %d 天后过期（阈值 %d 天）", *cert.DaysLeft, threshold)
		return res
	}

	res.OK = true
	res.Reason = model.ReasonOK
	res.Detail = fmt.Sprintf("证书有效，剩余 %d 天", *cert.DaysLeft)
	return res
}

// ---------- 证书解析 ----------

// certFromState 从 TLS 连接状态提取证书信息。
func certFromState(state *tls.ConnectionState) *model.SSLCertificate {
	if state == nil || len(state.PeerCertificates) == 0 {
		return nil
	}
	leaf := state.PeerCertificates[0]

	daysLeft := int(time.Until(leaf.NotAfter).Hours() / 24)

	return &model.SSLCertificate{
		Subject:     leaf.Subject.String(),
		Issuer:      issuerName(leaf),
		Serial:      leaf.SerialNumber.String(),
		NotBefore:   timePtr(leaf.NotBefore),
		NotAfter:    timePtr(leaf.NotAfter),
		DaysLeft:    &daysLeft,
		Fingerprint: fingerprint(leaf),
	}
}

func issuerName(c *x509.Certificate) string {
	if c.Issuer.CommonName != "" {
		return c.Issuer.CommonName
	}
	if len(c.Issuer.Organization) > 0 {
		return c.Issuer.Organization[0]
	}
	//兜底：用 Organization 全体
	if len(c.Issuer.OrganizationalUnit) > 0 {
		return c.Issuer.OrganizationalUnit[0]
	}
	return "(未知颁发者)"
}

// fingerprint 生成证书指纹（SHA256，取前 16 字节）。
// 用于在证书更换时识别"换了一张新证书"。
func fingerprint(c *x509.Certificate) string {
	sum := sha256Sum(c.Raw)
	if len(sum) < 16 {
		return ""
	}
	return fmt.Sprintf("%x", sum[:16])
}

// ---------- 判定辅助 ----------

// statusMatches 判断状态码是否符合期望。
// 未配置期望时，2xx/3xx 都算正常——重定向是常见的正常状态。
func statusMatches(code int, expect []int) bool {
	if len(expect) == 0 {
		return code >= 200 && code < 400
	}
	for _, e := range expect {
		if e == code {
			return true
		}
	}
	// 支持通配如 2xx（配置为 200 时不匹配 201）
	// 这里不做通配展开，避免配置歧义
	return false
}

// containsAllKeywords 检查响应体是否包含所有关键字（不区分大小写）。
func containsAllKeywords(body string, keywords []string) bool {
	if len(body) > maxKeywordSearch {
		body = body[:maxKeywordSearch]
	}
	lower := strings.ToLower(body)
	for _, kw := range keywords {
		if kw == "" {
			continue
		}
		if !strings.Contains(lower, strings.ToLower(kw)) {
			return false
		}
	}
	return true
}

// classifyHTTPError 把 Go 的网络错误映射为失败原因枚举。
func classifyHTTPError(err error, start time.Time, timeout time.Duration) *Result {
	totalMs := int(time.Since(start).Milliseconds())
	res := &Result{TotalMs: totalMs, Reason: classifyNetError(err), Detail: err.Error()}

	// 超时要单独识别：它是"慢"而不是"不通"，
	// 混在连接错误里会让用户误以为网站挂了
	if isTimeout(err) {
		res.Reason = model.ReasonTimeout
		res.Detail = fmt.Sprintf("请求超时（超过 %v）", timeout)
	}
	return res
}

// classifyNetError 把网络错误分类。
func classifyNetError(err error) model.FailReason {
	if err == nil {
		return model.ReasonOK
	}
	if isTimeout(err) {
		return model.ReasonTimeout
	}
	if isDNSError(err) {
		return model.ReasonDNSError
	}
	if isRefused(err) {
		return model.ReasonConnRefused
	}
	// x509 证书问题
	if strings.Contains(err.Error(), "x509:") ||
		strings.Contains(err.Error(), "certificate") {
		return model.ReasonTLSError
	}
	return model.ReasonUnknown
}

func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	if errorsAs(err, &ne) {
		return ne.Timeout()
	}
	s := err.Error()
	return strings.Contains(s, "timeout") ||
		strings.Contains(s, "deadline exceeded") ||
		strings.Contains(s, "context canceled")
}

func isDNSError(err error) bool {
	if err == nil {
		return false
	}
	var de *net.DNSError
	if errorsAs(err, &de) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "no such host") ||
		strings.Contains(s, "server misbehaving")
}

func isRefused(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "connection refused") ||
		strings.Contains(s, "no route to host") ||
		strings.Contains(s, "network is unreachable")
}
