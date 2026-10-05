package monitor

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/m202471895/probeone/server/internal/model"
)

// allowPrivate 是测试用的探测器：允许访问 127.0.0.1。
// 生产环境默认 false（SSRF 防护）。
var allowPrivate = true

func TestHTTP_正常访问(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html><body>hello world</body></html>"))
	}))
	defer srv.Close()

	p := New(allowPrivate)
	res := p.Probe(&model.Monitor{
		Name: "test", Type: model.MonitorHTTP, Target: srv.URL,
		TimeoutSec: 5,
		Config:     model.MonitorConfig{ExpectStatus: []int{200}},
	})

	if !res.OK {
		t.Fatalf("应成功，实际失败: reason=%s detail=%s", res.Reason, res.Detail)
	}
	if res.Reason != model.ReasonOK {
		t.Errorf("reason = %s", res.Reason)
	}
	if res.StatusCode == nil || *res.StatusCode != 200 {
		t.Errorf("状态码 = %v", res.StatusCode)
	}
	if res.TTFBMs == nil {
		t.Error("未记录首字节耗时")
	}
	if res.TotalMs < 0 {
		t.Error("总耗时应 >= 0")
	}
}

func TestHTTP_状态码不符(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := New(allowPrivate)
	res := p.Probe(&model.Monitor{
		Name: "test", Type: model.MonitorHTTP, Target: srv.URL, TimeoutSec: 5,
		Config: model.MonitorConfig{ExpectStatus: []int{200}},
	})

	if res.OK {
		t.Fatal("500 响应不应判定为正常")
	}
	if res.Reason != model.ReasonStatusMismatch {
		t.Errorf("reason = %s，应为 status_mismatch", res.Reason)
	}
	if res.StatusCode == nil || *res.StatusCode != 500 {
		t.Errorf("状态码 = %v", res.StatusCode)
	}
	// 详情里应含实际状态码，便于排障
	if !strings.Contains(res.Detail, "500") {
		t.Errorf("详情应含实际状态码，实际: %s", res.Detail)
	}
}

func TestHTTP_关键字缺失(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>error page</html>"))
	}))
	defer srv.Close()

	p := New(allowPrivate)
	res := p.Probe(&model.Monitor{
		Name: "test", Type: model.MonitorHTTP, Target: srv.URL, TimeoutSec: 5,
		Config: model.MonitorConfig{
			ExpectStatus:   []int{200},
			ExpectKeywords: []string{"登录成功", "dashboard"},
		},
	})

	if res.OK {
		t.Fatal("关键字缺失不应判定为正常")
	}
	if res.Reason != model.ReasonContentMismatch {
		t.Errorf("reason = %s，应为 content_mismatch", res.Reason)
	}
}

func TestHTTP_关键字不区分大小写(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>Welcome to DASHBOARD</html>"))
	}))
	defer srv.Close()

	p := New(allowPrivate)
	res := p.Probe(&model.Monitor{
		Name: "test", Type: model.MonitorHTTP, Target: srv.URL, TimeoutSec: 5,
		Config: model.MonitorConfig{
			ExpectStatus:   []int{200},
			ExpectKeywords: []string{"dashboard"},
		},
	})
	if !res.OK {
		t.Errorf("关键字匹配应忽略大小写: %s", res.Detail)
	}
}

func TestHTTP_响应超时(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
	}))
	defer srv.Close()

	p := New(allowPrivate)
	res := p.Probe(&model.Monitor{
		Name: "test", Type: model.MonitorHTTP, Target: srv.URL, TimeoutSec: 1,
	})

	if res.OK {
		t.Fatal("超时不应判定为正常")
	}
	if res.Reason != model.ReasonTimeout {
		t.Errorf("reason = %s，应为 timeout（超时与不通是不同的问题）", res.Reason)
	}
}

func TestHTTP_连接被拒绝(t *testing.T) {
	// 占用一个端口后立即关闭，得到"拒绝连接"
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()

	p := New(allowPrivate)
	res := p.Probe(&model.Monitor{
		Name: "test", Type: model.MonitorHTTP, Target: "http://" + addr, TimeoutSec: 3,
	})

	if res.OK {
		t.Fatal("连接被拒不应判定为正常")
	}
	if res.Reason != model.ReasonConnRefused {
		t.Errorf("reason = %s，应为 conn_refused", res.Reason)
	}
}

func TestHTTP_响应过慢(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	p := New(allowPrivate)
	res := p.Probe(&model.Monitor{
		Name: "test", Type: model.MonitorHTTP, Target: srv.URL, TimeoutSec: 5,
		Config: model.MonitorConfig{ExpectStatus: []int{200}, MaxLatencyMs: 50},
	})

	if res.OK {
		t.Fatal("超阈值不应判定为正常")
	}
	if res.Reason != model.ReasonSlow {
		t.Errorf("reason = %s，应为 slow", res.Reason)
	}
}

func TestHTTP_协议非法(t *testing.T) {
	p := New(allowPrivate)
	res := p.Probe(&model.Monitor{
		Name: "test", Type: model.MonitorHTTP, Target: "ftp://example.com", TimeoutSec: 3,
	})
	if res.OK {
		t.Fatal("非 http(s) 协议应被拒绝")
	}
	if res.Reason != model.ReasonBadRequest {
		t.Errorf("reason = %s，应为 bad_request", res.Reason)
	}
}

func TestHTTP_不跟随重定向(t *testing.T) {
	// 3xx 是可观测的状态码。跟随跳转会掩盖
	// "网站改了域名但没配重定向"这类问题
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/new", http.StatusMovedPermanently)
	}))
	defer srv.Close()

	p := New(allowPrivate)
	res := p.Probe(&model.Monitor{
		Name: "test", Type: model.MonitorHTTP, Target: srv.URL, TimeoutSec: 5,
		Config: model.MonitorConfig{ExpectStatus: []int{301}},
	})
	if !res.OK {
		t.Errorf("301 在未配期望时应算正常（重定向是常见状态）: %s", res.Detail)
	}
	if res.StatusCode == nil || *res.StatusCode != 301 {
		t.Errorf("状态码 = %v，应为 301（未被跟随）", res.StatusCode)
	}
}

func TestHTTP_大响应体被截断(t *testing.T) {
	// 2MB 响应，必须被 1MB 上限截断且不 OOM
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := strings.Repeat("A", 64*1024)
		for i := 0; i < 32; i++ {
			_, _ = w.Write([]byte(chunk))
		}
	}))
	defer srv.Close()

	p := New(allowPrivate)
	res := p.Probe(&model.Monitor{
		Name: "test", Type: model.MonitorHTTP, Target: srv.URL, TimeoutSec: 10,
		Config: model.MonitorConfig{ExpectStatus: []int{200}},
	})
	if !res.OK {
		t.Errorf("大响应体应正常处理: %s", res.Detail)
	}
}

func TestTCP_端口通(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	_, portStr, _ := net.SplitHostPort(l.Addr().String())
	port, _ := strconv.Atoi(portStr)

	p := New(allowPrivate)
	res := p.Probe(&model.Monitor{
		Name: "test", Type: model.MonitorTCP, Target: "127.0.0.1", TimeoutSec: 5,
		Config: model.MonitorConfig{Port: port},
	})

	if !res.OK {
		t.Fatalf("端口应连通: %s", res.Detail)
	}
	if res.TCPMs == nil {
		t.Error("未记录 TCP 耗时")
	}
}

func TestTCP_端口不通(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()

	_, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)

	p := New(allowPrivate)
	res := p.Probe(&model.Monitor{
		Name: "test", Type: model.MonitorTCP, Target: "127.0.0.1", TimeoutSec: 3,
		Config: model.MonitorConfig{Port: port},
	})

	if res.OK {
		t.Fatal("端口不通不应判定为正常")
	}
	if res.Reason != model.ReasonConnRefused {
		t.Errorf("reason = %s，应为 conn_refused", res.Reason)
	}
}

func TestTCP_未配置端口(t *testing.T) {
	p := New(allowPrivate)
	res := p.Probe(&model.Monitor{
		Name: "test", Type: model.MonitorTCP, Target: "127.0.0.1", TimeoutSec: 3,
	})
	if res.OK || res.Reason != model.ReasonBadRequest {
		t.Errorf("未配端口应报 bad_request，实际 = %s", res.Reason)
	}
}

func TestDNS_解析成功(t *testing.T) {
	p := New(allowPrivate)
	res := p.Probe(&model.Monitor{
		Name: "test", Type: model.MonitorDNS, Target: "localhost", TimeoutSec: 5,
		Config: model.MonitorConfig{Record: "A"},
	})
	// localhost 能解析就算过；解析失败也不算测试失败（取决于环境）
	if !res.OK && res.Reason != model.ReasonDNSError {
		t.Errorf("reason = %s，应为 ok 或 dns_error", res.Reason)
	}
}

func TestDNS_期望值不匹配(t *testing.T) {
	p := New(allowPrivate)
	res := p.Probe(&model.Monitor{
		Name: "test", Type: model.MonitorDNS, Target: "localhost", TimeoutSec: 5,
		Config: model.MonitorConfig{
			Record:    "A",
			ExpectIPs: []string{"203.0.113.99"}, // 故意写一个不可能的值
		},
	})
	if res.OK {
		t.Fatal("期望值不匹配不应判定为正常")
	}
	if res.Reason != model.ReasonContentMismatch {
		t.Errorf("reason = %s，应为 content_mismatch", res.Reason)
	}
}

func TestDNS_未知记录类型(t *testing.T) {
	p := New(allowPrivate)
	res := p.Probe(&model.Monitor{
		Name: "test", Type: model.MonitorDNS, Target: "example.com", TimeoutSec: 3,
		Config: model.MonitorConfig{Record: "TXT"},
	})
	if res.OK || res.Reason != model.ReasonBadRequest {
		t.Errorf("TXT 在 P4 应明确报不支持，实际 = %s", res.Reason)
	}
	// 错误信息要说清支持哪些
	if !strings.Contains(res.Detail, "A/AAAA/CNAME") {
		t.Errorf("详情应列出支持的类型，实际 = %s", res.Detail)
	}
}

func Test未知类型(t *testing.T) {
	p := New(allowPrivate)
	res := p.Probe(&model.Monitor{Name: "x", Type: "ftp", Target: "x"})
	if res.OK || res.Reason != model.ReasonUnknown {
		t.Errorf("未知类型应报 unknown，实际 = %s", res.Reason)
	}
}

// ---------- SSRF 防护 ----------

func TestSSRF_拦截内网地址(t *testing.T) {
	// 这是最重要的安全测试：监控系统最常见的被滥用方式
	p := New(false) // 禁止内网
	cases := []struct {
		name   string
		target string
	}{
		{"回环", "http://127.0.0.1:8000"},
		{"回环别名", "http://localhost:8000"},
		{"私有A段", "http://10.0.0.1/"},
		{"私有B段", "http://172.16.0.1/"},
		{"私有C段", "http://192.168.1.1/"},
		{"云元数据", "http://169.254.169.254/latest/meta-data/"},
		{"链路本地", "http://169.254.1.1/"},
		{"零地址", "http://0.0.0.0/"},
		{"CGNAT", "http://100.64.0.1/"},
		{"组播", "http://224.0.0.1/"},
		{"保留段", "http://240.0.0.1/"},
		{"IPv6回环", "http://[::1]:8000"},
		{"IPv6 ULA", "http://[fc00::1]/"},
		{"IPv6链路本地", "http://[fe80::1]/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := p.Probe(&model.Monitor{
				Name: "ssrf", Type: model.MonitorHTTP, Target: c.target, TimeoutSec: 2,
			})
			if res.OK {
				t.Fatalf("%s 应被拦截却通过了", c.target)
			}
			if res.Reason != model.ReasonBlocked {
				t.Errorf("reason = %s，应为 blocked", res.Reason)
			}
			// 拦截信息要告诉用户怎么解决
			if !strings.Contains(res.Detail, "ALLOW_INTERNAL_TARGETS") {
				t.Errorf("详情应说明如何放行，实际 = %s", res.Detail)
			}
		})
	}
}

func TestSSRF_允许内网开关(t *testing.T) {
	// 显式开启后应放行——用户确实有监控内网服务的场景
	p := New(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("internal ok"))
	}))
	defer srv.Close()

	res := p.Probe(&model.Monitor{
		Name: "internal", Type: model.MonitorHTTP, Target: srv.URL, TimeoutSec: 3,
	})
	if !res.OK {
		t.Errorf("开启后应放行内网访问: %s", res.Detail)
	}
}

func TestIsPublicIP(t *testing.T) {
	public := []string{
		"8.8.8.8", "1.1.1.1", "47.246.0.1", // 阿里云公网
		"2001:4860:4860::8888",
		// 172.16.0.0/12 的边界外侧——这两个是公网 IP，
		// 私有段只夹在 172.16.0.0 – 172.31.255.255 之间。
		// 边界写错会让 SSRF 防护误拦正常站点，或漏过内网地址。
		"172.15.0.1", "172.32.0.1",
	}
	private := []string{
		"127.0.0.1", "10.0.0.1", "172.16.0.1", "172.31.255.255",
		"192.168.0.1", "169.254.169.254", "100.64.0.1",
		"0.0.0.0", "224.0.0.1", "240.0.0.1",
		"::1", "fc00::1", "fe80::1", "::",
		// 172.16/12 私有段的两端
		"172.16.0.1", "172.31.255.255",
	}
	for _, s := range public {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("测试用例 %q 不是合法 IP", s)
		}
		if !isPublicIP(ip) {
			t.Errorf("%s 应判定为公网", s)
		}
	}
	for _, s := range private {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("测试用例 %q 不是合法 IP", s)
		}
		if isPublicIP(ip) {
			t.Errorf("%s 应判定为内网/保留", s)
		}
	}
}

func TestSSRF_解析失败不误判为拦截(t *testing.T) {
	// 域名解析失败应交给上层按 dns_error 处理，
	// 不能混成"SSRF 拦截"——两者的排查方向完全不同
	_, err := resolveAndCheck("this-domain-definitely-does-not-exist-xyz123.invalid", false)
	if err != nil {
		t.Errorf("解析失败不应在此处报错，实际 = %v", err)
	}
}

// ---------- 判定辅助 ----------

func TestStatusMatches(t *testing.T) {
	cases := []struct {
		code   int
		expect []int
		want   bool
	}{
		{200, nil, true}, {201, nil, true}, {301, nil, true},
		{404, nil, false}, {500, nil, false}, {199, nil, false}, {400, nil, false},
		{200, []int{200}, true}, {201, []int{200}, false},
		{200, []int{200, 201, 204}, true}, {500, []int{200}, false},
	}
	for _, c := range cases {
		if got := statusMatches(c.code, c.expect); got != c.want {
			t.Errorf("statusMatches(%d, %v) = %v，期望 %v", c.code, c.expect, got, c.want)
		}
	}
}

func TestClassifyNetError(t *testing.T) {
	// 连接拒绝
	if got := classifyNetError(fmt.Errorf("dial tcp 1.2.3.4:80: connect: connection refused")); got != model.ReasonConnRefused {
		t.Errorf("连接拒绝应识别为 conn_refused，实际 = %s", got)
	}
	// DNS 失败
	if got := classifyNetError(fmt.Errorf("lookup example.invalid: no such host")); got != model.ReasonDNSError {
		t.Errorf("DNS 失败应识别为 dns_error，实际 = %s", got)
	}
	// 证书问题
	if got := classifyNetError(fmt.Errorf("x509: certificate has expired")); got != model.ReasonTLSError {
		t.Errorf("证书问题应识别为 tls_error，实际 = %s", got)
	}
	// nil 应为 ok
	if got := classifyNetError(nil); got != model.ReasonOK {
		t.Errorf("nil 应为 ok，实际 = %s", got)
	}
}

func TestContainsAllKeywords(t *testing.T) {
	body := "<html>Hello Dashboard, user admin logged in</html>"
	if !containsAllKeywords(body, []string{"dashboard", "admin"}) {
		t.Error("所有关键字都应命中")
	}
	if !containsAllKeywords(body, []string{"DASHBOARD"}) {
		t.Error("大小写应被忽略")
	}
	if containsAllKeywords(body, []string{"dashboard", "notexist"}) {
		t.Error("缺一个关键字就不该匹配")
	}
	if !containsAllKeywords(body, nil) {
		t.Error("未配关键字时不应失败")
	}
	// 空关键字应被跳过
	if !containsAllKeywords(body, []string{"dashboard", ""}) {
		t.Error("空关键字应被跳过而非导致失败")
	}
}

// ---------- 证书 ----------

func TestSSL_真实证书(t *testing.T) {
	// 用 httptest 生成自签证书，验证证书解析与剩余天数计算
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "https://")
	h, _, _ := net.SplitHostPort(host)

	// 端口必须用 httptest 实际分配的，不能写死 443——
	// httptest 监听的是随机高位端口，写死 443 会连到别的服务上
	_, portStr, _ := net.SplitHostPort(host)
	port, _ := strconv.Atoi(portStr)

	p := New(allowPrivate)
	res := p.Probe(&model.Monitor{
		Name: "ssl", Type: model.MonitorSSL, Target: h, TimeoutSec: 5,
		Config: model.MonitorConfig{Port: port},
	})

	// httptest 的自签证书 CN 是 example.com，与 127.0.0.1 不匹配，
	// 但我们设了 InsecureSkipVerify，所以握手应成功
	if !res.OK {
		t.Errorf("证书探测应成功: %s", res.Detail)
	}
	if res.Certificate == nil {
		t.Fatal("未解析出证书")
	}
	if res.Certificate.DaysLeft == nil {
		t.Error("未计算剩余天数")
	}
	if res.Certificate.Fingerprint == "" {
		t.Error("未生成证书指纹")
	}
	if res.Certificate.Issuer == "" {
		t.Error("未解析颁发者")
	}
}

func TestSSL_临期阈值(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")
	h, portStr, _ := net.SplitHostPort(host)
	port, _ := strconv.Atoi(portStr)

	p := New(allowPrivate)
	// 阈值设 36500 天（远超任何真实证书寿命）→ 应判为 cert_expiring
	res := p.Probe(&model.Monitor{
		Name: "ssl", Type: model.MonitorSSL, Target: h, TimeoutSec: 5,
		Config: model.MonitorConfig{Port: port, WarnDaysLeft: 36500},
	})
	if res.Reason != model.ReasonCertExpiring {
		t.Errorf("超阈值应报 cert_expiring，实际 = %s", res.Reason)
	}
	// 注意：OK 仍为 true——证书还在有效期内，服务是通的
	if !res.OK {
		t.Error("临期不应判定为服务不可用")
	}
}

func TestSSL_握手失败(t *testing.T) {
	// 对一个只讲 HTTP 不讲 TLS 的端口做证书探测
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	_, portStr, _ := net.SplitHostPort(l.Addr().String())
	port, _ := strconv.Atoi(portStr)

	p := New(allowPrivate)
	res := p.Probe(&model.Monitor{
		Name: "ssl", Type: model.MonitorSSL, Target: "127.0.0.1", TimeoutSec: 3,
		Config: model.MonitorConfig{Port: port},
	})
	if res.OK {
		t.Fatal("非 TLS 端口应探测失败")
	}
	if res.Reason != model.ReasonTLSError {
		t.Errorf("reason = %s，应为 tls_error", res.Reason)
	}
}
