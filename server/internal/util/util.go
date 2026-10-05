// Package util 提供通用工具：敏感值掩码、随机凭据生成、请求 ID。
package util

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
)

// ---------- 敏感值掩码 ----------

// Mask 返回用于日志的掩码值。
//
// 长度在 8 及以下的输入全部打码——短的密钥打码后基本没信息量，
// 全部打码可避免"通过掩码长度推断密钥长度"，也避免残留字符被反推。
// 超过 8 位的保留首尾各 2 位，便于运维在多条日志中辨认是同一个凭据。
func Mask(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "***"
	}
	return s[:2] + "***" + s[len(s)-2:]
}

// ---------- 随机凭据 ----------

// RandomToken 生成 n 字节随机 token，返回 base64url 编码。
// 用于会话 ID 等场景。
func RandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成随机 token 失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// RandomHex 生成 n 字节随机值，返回十六进制。
// 用于 UUID、握手 session 等场景。
func RandomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成随机值失败: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// RandomPassword 生成符合密码策略的随机初始密码。
// 保证至少包含大小写字母与数字，长度可配置。
func RandomPassword(length int) (string, error) {
	if length < 12 {
		length = 12
	}
	const (
		lower   = "abcdefghijkmnopqrstuvwxyz" // 去掉 l，易混淆
		upper   = "ABCDEFGHJKLMNPQRSTUVWXYZ" // 去掉 O、I
		digits  = "23456789"                // 去掉 0、1
		all     = lower + upper + digits
	)
	// 先各取一个，保证类别齐全
	out := make([]byte, 0, length)
	for _, set := range []string{lower, upper, digits} {
		c, err := pick(set)
		if err != nil {
			return "", err
		}
		out = append(out, c)
	}
	for len(out) < length {
		c, err := pick(all)
		if err != nil {
			return "", err
		}
		out = append(out, c)
	}
	// Fisher-Yates 洗牌，用随机源避免固定模式
	b := make([]byte, len(out))
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("洗牌失败: %w", err)
	}
	for i := len(out) - 1; i > 0; i-- {
		j := int(b[i]) % (i + 1)
		out[i], out[j] = out[j], out[i]
	}
	return string(out), nil
}

func pick(set string) (byte, error) {
	b := make([]byte, 1)
	for {
		if _, err := rand.Read(b); err != nil {
			return 0, fmt.Errorf("生成随机字符失败: %w", err)
		}
		// 取模偏置可忽略（密码强度不依赖此处的完美均匀）
		if int(b[0]) < 256-(256%len(set)) {
			return set[int(b[0])%len(set)], nil
		}
	}
}

// ---------- 请求 ID ----------

// RequestIDHeader 是贯穿请求的 ID 头。
const RequestIDHeader = "X-Request-Id"

// SHA256Hex 返回字符串的 SHA256 十六进制。
// 用于 session token 存储——数据库只存哈希，不存明文（PRD 9.4）。
func SHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ---------- IP 处理 ----------

// ClientIP 从请求中提取客户端 IP。
//
// 注意：只有在配置了 trust_proxy 时才采信 X-Forwarded-For，
// 否则任何人都能伪造该头。trustedNets 为空表示不信任任何反代。
func ClientIP(remoteAddr string, xff string, trustedNets []*net.IPNet) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return host
	}

	// 不信任任何反代：直接用连接来源
	if len(trustedNets) == 0 {
		return ip.String()
	}
	// 连接来源本身不在信任网段内 → 不可信
	if !ipInNets(ip, trustedNets) {
		return ip.String()
	}
	// 来源可信，采信 XFF 的最左侧一跳
	if xff == "" {
		return ip.String()
	}
	first := strings.TrimSpace(strings.Split(xff, ",")[0])
	if fip := net.ParseIP(first); fip != nil {
		return fip.String()
	}
	return ip.String()
}

func ipInNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
