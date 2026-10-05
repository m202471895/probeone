package monitor

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 本文件放 SSRF 防护、错误判定与小工具。

// contextWithTimeout 返回带超时的上下文。
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		d = 10 * time.Second
	}
	return context.WithTimeout(context.Background(), d)
}

// contextWithTimeoutValue 供需要 context.Context 参数的函数使用。
func contextWithTimeoutValue(d time.Duration) context.Context {
	ctx, cancel := contextWithTimeout(d)
	// 这里不能立即 cancel——caller 会用它做网络请求。
	// 交给 GC 回收（context 在超时后自动释放），或者由调用方负责。
	_ = cancel
	return ctx
}

func errorsAs(err error, target any) bool { return errors.As(err, target) }

func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

func timePtr(t time.Time) *time.Time {
	u := t.UTC()
	return &u
}

// ---------- SSRF 防护（PRD T11）----------

// resolveAndCheck 解析目标地址并检查是否允许访问。
//
// 为什么必须做：监控系统被当作"内网探测器"用是最常见的攻击手法之一。
// 攻击者把监控目标设成 http://169.254.169.254/（云厂商元数据服务），
// 就能拿到实例的临时凭据；设成 http://127.0.0.1:6379 就能打 Redis。
//
// allowPrivate=false 时（默认）拒绝所有非公网地址。
func resolveAndCheck(target string, allowPrivate bool) ([]string, error) {
	host := extractHost(target)
	if host == "" {
		return nil, fmt.Errorf("无法从目标中提取主机名: %s", target)
	}

	// IP 字面量直接判断
	if ip := net.ParseIP(host); ip != nil {
		if !allowPrivate && !isPublicIP(ip) {
			return nil, fmt.Errorf("目标 %s 是内网/保留地址，已被 SSRF 防护拦截。"+
				"如确需监控内网服务，请设置 PROBEONE_ALLOW_INTERNAL_TARGETS=true", host)
		}
		return []string{ip.String()}, nil
	}

	// 域名：解析后逐个检查
	ips, err := net.LookupIP(host)
	if err != nil {
		// 解析失败不在这里报错，交给上层探测器按dns_error 处理——
		// 这类错误的排查方向与 SSRF 拦截不同，不该混为一谈
		return nil, nil
	}
	if len(ips) == 0 {
		return nil, nil
	}

	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		if !allowPrivate && !isPublicIP(ip) {
			// 只要有一个解析结果是内网就整体拒绝。
			// 攻击者可以用"DNS 轮询到内网"的技巧绕过逐个检查。
			return nil, fmt.Errorf("目标 %s 解析到内网/保留地址 %s，已被 SSRF 防护拦截。"+
				"如确需监控内网服务，请设置 PROBEONE_ALLOW_INTERNAL_TARGETS=true", host, ip)
		}
		out = append(out, ip.String())
	}
	return out, nil
}

// extractHost 从目标中提取主机名。
// 支持完整 URL（http://host:port/path）与裸主机名（host:port）。
func extractHost(target string) string {
	if strings.Contains(target, "://") {
		u, err := url.Parse(target)
		if err != nil {
			return ""
		}
		return u.Hostname()
	}
	// 裸地址：去掉端口
	if h, _, err := net.SplitHostPort(target); err == nil {
		return h
	}
	return strings.TrimSpace(target)
}

// isPublicIP 判断是否为公网可路由地址。
//
// 涵盖 IPv4 与 IPv6 的全部保留段。判断依据是 IANA 特殊用途地址登记表。
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	// 回环
	if ip.IsLoopback() {
		return false
	}
	// 未指定地址（0.0.0.0 / ::）
	if ip.IsUnspecified() {
		return false
	}
	// 组播
	if ip.IsMulticast() {
		return false
	}
	// 链路本地（169.254.0.0/16，含云元数据 169.254.169.254；fe80::/10）
	if ip.IsLinkLocalUnicast() {
		return false
	}
	// 唯一本地地址 fc00::/7
	if len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc {
		return false
	}

	if v4 := ip.To4(); v4 != nil {
		return isPublicIPv4(v4)
	}
	return isPublicIPv6(ip)
}

// isPublicIPv4 判断 IPv4 是否为公网地址。
func isPublicIPv4(ip net.IP) bool {
	// 10.0.0.0/8 私有
	if ip[0] == 10 {
		return false
	}
	// 172.16.0.0/12 私有
	if ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31 {
		return false
	}
	// 192.168.0.0/16 私有
	if ip[0] == 192 && ip[1] == 168 {
		return false
	}
	// 100.64.0.0/10 CGNAT（运营商级 NAT）
	if ip[0] == 100 && ip[1] >= 64 && ip[1] <= 127 {
		return false
	}
	// 192.0.0.0/24 IETF 协议保留
	if ip[0] == 192 && ip[1] == 0 && ip[2] == 0 {
		return false
	}
	// 192.0.2.0/24 文档示例
	if ip[0] == 192 && ip[1] == 0 && ip[2] == 2 {
		return false
	}
	// 198.51.100.0/24 文档示例
	if ip[0] == 198 && ip[1] == 51 && ip[2] == 100 {
		return false
	}
	// 203.0.113.0/24 文档示例
	if ip[0] == 203 && ip[1] == 0 && ip[2] == 113 {
		return false
	}
	// 198.18.0.0/15 基准测试
	if ip[0] == 198 && (ip[1] == 18 || ip[1] == 19) {
		return false
	}
	// 240.0.0.0/4 保留（240–255）
	if ip[0] >= 240 {
		return false
	}
	// 0.0.0.0/8
	if ip[0] == 0 {
		return false
	}
	return true
}

// isPublicIPv6 判断 IPv6 是否为公网地址。
func isPublicIPv6(ip net.IP) bool {
	// ::/128 与 ::1/128 已在 IsUnspecified/IsLoopback 覆盖
	// 2001:db8::/32 文档示例
	if ip[0] == 0x20 && ip[1] == 0x01 && ip[2] == 0x0d && ip[3] == 0xb8 {
		return false
	}
	// 2001:2::/48 基准测试
	if ip[0] == 0x20 && ip[1] == 0x01 && ip[2] == 0x00 && ip[3] == 0x02 {
		return false
	}
	// 2001:10::/28 ORCHID
	if ip[0] == 0x20 && ip[1] == 0x01 && ip[2] == 0x00 && (ip[3]&0xf0) == 0x10 {
		return false
	}
	// ::ffff:0:0/96 IPv4 映射地址 —— 转成 IPv4 再判
	if v4 := ip.To4(); v4 != nil {
		return isPublicIPv4(v4)
	}
	return true
}

// ---------- 端口解析辅助 ----------

// parsePort 从配置值解析端口。
// 允许配置成字符串（DNS 探针用它存期望的 IP），解析失败返回 0。
func parsePort(v int) (int, bool) {
	if v < 1 || v > 65535 {
		return 0, false
	}
	return v, true
}

func itoa(v int) string { return strconv.Itoa(v) }

// ---------- 匹配辅助 ----------

// containsAnyExpected 判断 actual 列表中是否有任一项命中 expect。
// 比较时忽略大小写：DNS 记录大小写不敏感。
func containsAnyExpected(actual, expect []string) bool {
	for _, a := range actual {
		for _, e := range expect {
			if strings.EqualFold(a, e) {
				return true
			}
		}
	}
	return false
}

// containsAny 判断 s 是否命中 expect 中任一项。
func containsAny(s string, expect []string) bool {
	for _, e := range expect {
		if strings.EqualFold(s, e) {
			return true
		}
	}
	return false
}
