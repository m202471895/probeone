// Package notify 实现告警通知通道。
//
// 七个通道的签名算法各不相同，是本项目最容易出错的部分：
//   - 钉钉：HMAC-SHA256 → base64 → urlencode → 拼到 URL
//   - 飞书：timestamp + secret 拼串 → HMAC-SHA256 → base64
//   - 企业微信：只有 key，无签名
//   - Telegram：MarkdownV2 有严格转义规则
//   - Bark：GET 或 POST，设备 key 在 URL 里
//   - Webhook：可带自定义签名头
//   - Email：SMTP + 可选 TLS/STARTTLS
//
// 统一约束（PRD 9.3）：
//   - 凭据从Config 取，日志里必须掩码
//   - 每个通道有独立超时，不拖垮告警分发
//   - 发送失败不影响其他通道
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/m202471895/probeone/server/internal/model"
)

// Message 是待发送的通知内容。
type Message struct {
	Title string
	Body  string
	// URL 是面板链接，方便点击跳转
	URL string
	// Severity 影响部分通道的图标/标签
	Severity model.Severity
	// Fields 是结构化补充信息（各通道按需渲染）
	Fields []Field
}

// Field 是消息里的一条键值对。
type Field struct {
	Label string
	Value string
}

// Sender 是通知通道的统一接口。
type Sender interface {
	// Type 返回通道类型。
	Type() model.ChannelType
	// Send 发送消息。
	Send(ctx context.Context, cfg map[string]any, msg Message) error
	// Validate 校验配置是否完整。UI 的"测试发送"按钮依赖它。
	Validate(cfg map[string]any) error
}

// Registry 是通道注册表。
type Registry struct {
	senders map[model.ChannelType]Sender
}

// NewRegistry 创建注册表并注册全部通道。
func NewRegistry(httpClient *http.Client) *Registry {
	r := &Registry{senders: make(map[model.ChannelType]Sender)}
	r.Register(&WebhookSender{client: httpClient})
	r.Register(&EmailSender{client: httpClient})
	r.Register(&WeComSender{client: httpClient})
	r.Register(&DingTalkSender{client: httpClient})
	r.Register(&FeishuSender{client: httpClient})
	r.Register(&TelegramSender{client: httpClient})
	r.Register(&BarkSender{client: httpClient})
	return r
}

// Register 注册一个通道。
func (r *Registry) Register(s Sender) { r.senders[s.Type()] = s }

// Get 按类型取通道。
func (r *Registry) Get(t model.ChannelType) (Sender, bool) {
	s, ok := r.senders[t]
	return s, ok
}

// Dispatch 分发到多个通道。
//
// 设计要点（PRD 3.3）：
//   - 单通道失败不影响其他通道
//   - 并发发送，但限制并发数（避免通道数多时瞬时打满网络）
//   - 返回每个通道的结果，调用方可写日志或展示给用户
func (r *Registry) Dispatch(ctx context.Context, channels []Channel, msg Message) []DispatchResult {
	if len(channels) == 0 {
		return nil
	}
	const maxConcurrent = 5
	sem := make(chan struct{}, maxConcurrent)

	results := make([]DispatchResult, len(channels))
	done := make(chan int, len(channels))

	for i, ch := range channels {
		sender, ok := r.Get(ch.Type)
		if !ok {
			results[i] = DispatchResult{
				ChannelID: ch.ID, Type: ch.Type, OK: false,
				Error: "未知的通知通道类型: " + string(ch.Type),
			}
			done <- i
			continue
		}

		go func(idx int, c Channel, s Sender) {
			sem <- struct{}{}
			defer func() { <-sem }()

			err := s.Send(ctx, c.Config, msg)
			results[idx] = DispatchResult{ChannelID: c.ID, Type: c.Type, OK: err == nil}
			if err != nil {
				results[idx].Error = err.Error()
			}
			done <- idx
		}(i, ch, sender)
	}

	for range channels {
		<-done
	}
	return results
}

// Channel 是分发时用的已配置通道。
// 用别名而非新struct：与model.AlertChannel 保持同一形态，
// 避免两份字段定义漂移（这正是 P1 里Count 条件漂移那类问题的温床）。
type Channel = model.AlertChannel

// DispatchResult 是单个通道的分发结果。
type DispatchResult struct {
	ChannelID int64
	Type      model.ChannelType
	OK        bool
	Error     string
}

// ---------- 配置读取辅助 ----------

// cfgStr 读字符串配置。
func cfgStr(cfg map[string]any, key string) string {
	if v, ok := cfg[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// cfgInt 读整型配置。
func cfgInt(cfg map[string]any, key string) int {
	if v, ok := cfg[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64: // JSON 反序列化后数字是 float64
			return int(n)
		}
	}
	return 0
}

// cfgBool 读布尔配置。
func cfgBool(cfg map[string]any, key string, def bool) bool {
	if v, ok := cfg[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
		// JSON 里也可能是字符串 "true"
		if s, ok := v.(string); ok {
			return strings.EqualFold(s, "true")
		}
	}
	return def
}

// ---------- 签名辅助 ----------

// hmacSHA256 返回 HMAC-SHA256 的原始字节。
func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// base64Encode 是 base64 标准编码。
func base64Encode(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// httpClientWithTimeout 带超时的 HTTP 客户端。
// 通知通道不能无限等待——一个卡住的通道会拖住整批告警分发。
func httpClientWithTimeout(seconds int) *http.Client {
	if seconds <= 0 {
		seconds = 15
	}
	return &http.Client{Timeout: time.Duration(seconds) * time.Second}
}

// postJSON 发送 JSON POST 请求。
func postJSON(ctx context.Context, client *http.Client, endpoint string,
	payload any, headers map[string]string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化请求体失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doRequest(client, req)
}

// doRequest 执行请求并把非 2xx 视为错误。
// 各通道的错误响应体格式各异，统一截取前 200 字符帮助排查。
func doRequest(client *http.Client, req *http.Request) error {
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 读取错误体但限制长度，避免把整个 HTML 错误页塞进日志
		buf := make([]byte, 200)
		n, _ := resp.Body.Read(buf)
		snippet := strings.TrimSpace(string(buf[:n]))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet)
	}
	return nil
}

// severityIcon 返回各通道用的严重级别标记。
// 用中文标签而非 emoji——emoji 在部分 IM 里显示为方块。
func severityLabel(s model.Severity) string {
	switch s {
	case model.SeverityCritical:
		return "严重"
	case model.SeverityWarning:
		return "警告"
	default:
		return "提示"
	}
}

// queryEscape 是 url.QueryEscape 的别名。
func queryEscape(s string) string { return url.QueryEscape(s) }
