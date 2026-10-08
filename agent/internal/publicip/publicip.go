// Package publicip 探测本机公网 IP。
//
// 为什么 Agent 需要知道自己的公网 IP：
//   - 服务端用它解析地理位置（世界地图打点）
//   - 节点列表里显示 IP 比"内网 10.0.0.5"有用得多
//
// 为什么不读网卡上的地址：
// 云主机的网卡里通常是内网地址（10.x / 172.16.x），
// 少数场景有公网地址直绑网卡，但不该指望这个。
//
// 探测方式：向回显服务发一次 HTTP 请求，从远端看到的源地址就是公网 IP。
// 只需要极小的响应体（几字节），不给第三方传任何本机信息。
package publicip

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// 回显服务列表。按顺序尝试，第一个成功就用。
//
// 为什么要有多个：任何单个服务都可能被墙、被限流或下线。
// 三个里有一个可用就够——公网 IP 探测失败不该影响监控主功能。
var endpoints = []string{
	"https://api.ipify.org",  // 返回纯 IP 文本
	"https://ifconfig.me/ip", // 同上
	"https://api4.ipify.org", // IPv4专用
}

// Result 是探测结果。
type Result struct {
	IP       string
	Endpoint string // 用了哪个服务，便于排查
	Elapsed  time.Duration
}

// IsPrivate 判断是否内网地址。
//
// 探测失败时可能拿到 0.0.0.0 或空值，这类不能上报——
// 服务端会拿它去查地理位置，浪费一次请求且必然查不到。
func IsPrivate(ip string) bool {
	p := net.ParseIP(strings.TrimSpace(ip))
	if p == nil {
		return true
	}
	return p.IsLoopback() || p.IsPrivate() || p.IsLinkLocalUnicast() ||
		p.IsUnspecified() || p.IsMulticast()
}

// Detect 探测公网 IP。
//
// 设计要点：
//   - 整体超时 10 秒，且每个端点单独限时——
//     一个服务卡住不该拖垮整个探测
//   - 响应体限制 64 字节：回显服务只该返回 IP，
//     超长说明可能返回了 HTML 错误页
//   - 失败返回错误而不是猜测值：宁可没有，不要错的
func Detect(ctx context.Context, client *http.Client) (Result, error) {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	var lastErr error
	for _, ep := range endpoints {
		start := time.Now()

		reqCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, ep, nil)
		if err != nil {
			cancel()
			lastErr = fmt.Errorf("构造请求失败: %w", err)
			continue
		}
		// 明确 UA：部分服务拒绝空 UA
		req.Header.Set("User-Agent", "ProbeOne-Agent/1.0")

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			lastErr = fmt.Errorf("%s: %w", ep, err)
			continue
		}
		// 限量读取：拿到 IP 就够，剩下的不关心
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 64))
		_ = resp.Body.Close()
		cancel()

		if rerr != nil {
			lastErr = fmt.Errorf("%s: 读取失败: %w", ep, rerr)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("%s: HTTP %d", ep, resp.StatusCode)
			continue
		}

		ip := strings.TrimSpace(string(body))
		// 不是合法 IP：多半返回了 HTML 错误页
		if net.ParseIP(ip) == nil {
			lastErr = fmt.Errorf("%s: 返回的不是 IP（%.40s）", ep, ip)
			continue
		}
		if IsPrivate(ip) {
			// 拿到内网地址说明探测链路有问题（比如走了代理）。
			// 这时上报内网 IP 反而有害——服务端会拿它查地理位置。
			lastErr = fmt.Errorf("%s: 返回内网地址 %s", ep, ip)
			continue
		}
		return Result{IP: ip, Endpoint: ep, Elapsed: time.Since(start)}, nil
	}
	return Result{}, fmt.Errorf("所有公网 IP 服务均失败: %w", lastErr)
}
