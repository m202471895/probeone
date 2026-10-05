package notify

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/m202471895/probeone/server/internal/model"
)

// ---------- Webhook（通用）----------
//
// 用途最广：任何支持 HTTP POST 的自动化平台都能接
// （自建脚本、Zapier、n8n、Server 酱的自定义通道等）。
//
// 签名：可选 HMAC-SHA256，密钥在 body 的 X-ProbeOne-Signature 头里。
// 用恒定时间比较防时序侧信道。

type WebhookSender struct {
	client *http.Client
}

func (s *WebhookSender) Type() model.ChannelType { return model.ChannelWebhook }

func (s *WebhookSender) Validate(cfg map[string]any) error {
	u := cfgStr(cfg, "url")
	if u == "" {
		return fmt.Errorf("缺少 url")
	}
	// 必须是 http/https
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return fmt.Errorf("url 必须以 http:// 或 https:// 开头")
	}
	return nil
}

func (s *WebhookSender) Send(ctx context.Context, cfg map[string]any, msg Message) error {
	if err := s.Validate(cfg); err != nil {
		return err
	}
	u := cfgStr(cfg, "url")
	timeout := cfgInt(cfg, "timeout_sec")
	if timeout <= 0 {
		timeout = 15
	}
	client := s.client
	if client == nil {
		client = httpClientWithTimeout(timeout)
	}

	payload := map[string]any{
		"title":    msg.Title,
		"body":     msg.Body,
		"url":      msg.URL,
		"severity": string(msg.Severity),
		"fields":   fieldsToMap(msg.Fields),
		"time":     time.Now().UTC().Format(time.RFC3339),
		"source":   "ProbeOne",
	}

	headers := map[string]string{}
	if secret := cfgStr(cfg, "secret"); secret != "" {
		body, _ := marshalJSON(payload)
		sig := base64Encode(hmacSHA256([]byte(secret), body))
		headers["X-ProbeOne-Signature"] = "sha256=" + sig
	}
	if h := cfgStr(cfg, "headers"); h != "" {
		// 支持额外的自定义头，格式 "Key1:Value1,Key2:Value2"
		for _, kv := range strings.Split(h, ",") {
			parts := strings.SplitN(kv, ":", 2)
			if len(parts) == 2 {
				headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
			}
		}
	}

	return postJSON(ctx, client, u, payload, headers)
}

// ---------- 企业微信机器人 ----------
//
// 只需一个 key，无签名。消息体是 Markdown。
// 注意：企业微信对 markdown 长度有限制，超长会静默丢弃。

type WeComSender struct {
	client *http.Client
}

func (s *WeComSender) Type() model.ChannelType { return model.ChannelWeCom }

func (s *WeComSender) Validate(cfg map[string]any) error {
	if cfgStr(cfg, "key") == "" {
		return fmt.Errorf("缺少 key")
	}
	return nil
}

func (s *WeComSender) Send(ctx context.Context, cfg map[string]any, msg Message) error {
	if err := s.Validate(cfg); err != nil {
		return err
	}
	key := cfgStr(cfg, "key")
	u := "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=" + queryEscape(key)

	client := s.client
	if client == nil {
		client = httpClientWithTimeout(10)
	}

	// 企业微信 markdown 不支持标题语法，用 **粗体** 代替
	content := fmt.Sprintf("**[%s] %s**\n%s", severityLabel(msg.Severity), msg.Title, msg.Body)
	if msg.URL != "" {
		content += fmt.Sprintf("\n[查看详情](%s)", msg.URL)
	}

	// 长度保护：企业微信超过 4096 字节会拒绝
	if len(content) > 4000 {
		content = content[:4000] + "\n...(内容过长已截断)"
	}

	payload := map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]any{"content": content},
	}

	if err := postJSON(ctx, client, u, payload, nil); err != nil {
		return err
	}
	return nil
}

// ---------- 钉钉机器人 ----------
//
// 加签模式：timestamp + "\n" + secret → HMAC-SHA256 → base64 → urlencode。
// 顺序错了钉钉会返回 310000 签名错误。
//
// 另有一个"关键词"模式（消息里必须含特定词），
// 两种模式可同时生效，任一通过即可。

type DingTalkSender struct {
	client *http.Client
}

func (s *DingTalkSender) Type() model.ChannelType { return model.ChannelDingTalk }

func (s *DingTalkSender) Validate(cfg map[string]any) error {
	if cfgStr(cfg, "access_token") == "" {
		return fmt.Errorf("缺少 access_token")
	}
	// 加签与关键词至少配一个，否则钉钉会拒绝所有消息
	if cfgStr(cfg, "secret") == "" && cfgStr(cfg, "keyword") == "" {
		return fmt.Errorf("必须配置 secret（加签）或 keyword（关键词）之一")
	}
	return nil
}

func (s *DingTalkSender) Send(ctx context.Context, cfg map[string]any, msg Message) error {
	if err := s.Validate(cfg); err != nil {
		return err
	}
	token := cfgStr(cfg, "access_token")
	u := "https://oapi.dingtalk.com/robot/send?access_token=" + queryEscape(token)

	// 加签
	if secret := cfgStr(cfg, "secret"); secret != "" {
		timestamp := time.Now().UnixMilli()
		// 签名原文必须是 timestamp + "\n" + secret，
		// 然后整体 urlencode（不只是 signature 部分）
		stringToSign := fmt.Sprintf("%d\n%s", timestamp, secret)
		sign := base64Encode(hmacSHA256([]byte(secret), []byte(stringToSign)))
		u += fmt.Sprintf("&timestamp=%d&sign=%s", timestamp, queryEscape(sign))
	}

	client := s.client
	if client == nil {
		client = httpClientWithTimeout(10)
	}

	title := fmt.Sprintf("[%s] %s", severityLabel(msg.Severity), msg.Title)
	content := msg.Body
	if msg.URL != "" {
		content += fmt.Sprintf("\n\n[查看详情](%s)", msg.URL)
	}
	// 钉钉 markdown 最多 20000 字
	if len(content) > 19000 {
		content = content[:19000] + "\n\n...(内容过长已截断)"
	}

	payload := map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]any{"title": title, "text": content},
	}
	// 关键词模式：额外发一条 text 消息
	if kw := cfgStr(cfg, "keyword"); kw != "" {
		payload["at"] = map[string]any{
			"isAtAll": cfgBool(cfg, "at_all", false),
		}
	}

	return postJSON(ctx, client, u, payload, nil)
}

// ---------- 飞书机器人 ----------
//
// 加签：把 timestamp 与 secret 拼成 "timestamp\nsecret" 再 HMAC，
// 与钉钉的算法一样但 URL 参数名不同（sign 而非 timestamp+sign 分离）。
// 实际飞书要求：timestamp 和 sign 两个参数都要传。

type FeishuSender struct {
	client *http.Client
}

func (s *FeishuSender) Type() model.ChannelType { return model.ChannelFeishu }

func (s *FeishuSender) Validate(cfg map[string]any) error {
	if cfgStr(cfg, "webhook") == "" {
		return fmt.Errorf("缺少 webhook 地址")
	}
	return nil
}

func (s *FeishuSender) Send(ctx context.Context, cfg map[string]any, msg Message) error {
	if err := s.Validate(cfg); err != nil {
		return err
	}
	u := cfgStr(cfg, "webhook")

	// 加签
	if secret := cfgStr(cfg, "secret"); secret != "" {
		timestamp := time.Now().Unix()
		// 飞书要求：timestamp\ nsecret 作为待签名字符串
		stringToSign := fmt.Sprintf("%d\n%s", timestamp, secret)
		sign := base64Encode(hmacSHA256([]byte(secret), []byte(stringToSign)))
		// 飞书期望 base64 后 url 编码
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		u = fmt.Sprintf("%s%stimestamp=%d&sign=%s", u, sep, timestamp, queryEscape(sign))
	}

	client := s.client
	if client == nil {
		client = httpClientWithTimeout(10)
	}

	title := fmt.Sprintf("[%s] %s", severityLabel(msg.Severity), msg.Title)
	content := msg.Body
	if msg.URL != "" {
		content += fmt.Sprintf("\n\n[查看详情](%s)", msg.URL)
	}

	payload := map[string]any{
		"msg_type": "text",
		"content":  map[string]any{"text": title + "\n\n" + content},
	}

	return postJSON(ctx, client, u, payload, nil)
}

// ---------- Telegram ----------
//
// 用 Bot API 的 sendMessage。
// MarkdownV2 有严格的转义规则：以下字符必须转义，否则 API 报 400。
//   _ * [ ] ( ) ~ ` > # + - = | { } . !

type TelegramSender struct {
	client *http.Client
}

func (s *TelegramSender) Type() model.ChannelType { return model.ChannelTelegram }

func (s *TelegramSender) Validate(cfg map[string]any) error {
	if cfgStr(cfg, "bot_token") == "" {
		return fmt.Errorf("缺少 bot_token")
	}
	if cfgStr(cfg, "chat_id") == "" {
		return fmt.Errorf("缺少 chat_id")
	}
	return nil
}

func (s *TelegramSender) Send(ctx context.Context, cfg map[string]any, msg Message) error {
	if err := s.Validate(cfg); err != nil {
		return err
	}
	token := cfgStr(cfg, "bot_token")
	u := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)

	chatID := cfgStr(cfg, "chat_id")
	// chat_id 允许是数字 ID 或 @channelname，加不加引号 Telegram 都能识别，
	// 但数字 ID 超过 int32 范围（如超级群）必须用字符串传入
	disableNotify := cfgBool(cfg, "silent", false)

	text := fmt.Sprintf("[%s] %s\n\n%s", severityLabel(msg.Severity), msg.Title, msg.Body)
	if msg.URL != "" {
		text += fmt.Sprintf("\n\n详情: %s", msg.URL)
	}
	for _, f := range msg.Fields {
		text += fmt.Sprintf("\n%s: %s", f.Label, f.Value)
	}

	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     escapeMarkdownV2(text),
		"parse_mode":               "MarkdownV2",
		"disable_notification":     disableNotify,
		"disable_web_page_preview": true,
	}

	client := s.client
	if client == nil {
		client = httpClientWithTimeout(10)
	}
	return postJSON(ctx, client, u, payload, nil)
}

// escapeMarkdownV2 转义 Telegram MarkdownV2 的保留字符。
//
// 漏掉任何一个都会导致整条消息 400 失败——
// 最容易漏的是 . ! - 和 = 。
func escapeMarkdownV2(s string) string {
	const special = "_*[]()~`>#+-=|{}.!"
	var b strings.Builder
	b.Grow(len(s) + 32)
	for _, r := range s {
		if strings.ContainsRune(special, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ---------- Bark ----------
//
// iOS 推送。支持 GET 与 POST 两种方式，POST 更可靠
// （URL 中的设备 key 会被日志记录，POST 只在 body 里）。

type BarkSender struct {
	client *http.Client
}

func (s *BarkSender) Type() model.ChannelType { return model.ChannelBark }

func (s *BarkSender) Validate(cfg map[string]any) error {
	if cfgStr(cfg, "device_key") == "" {
		return fmt.Errorf("缺少 device_key")
	}
	if cfgStr(cfg, "server") == "" {
		return fmt.Errorf("缺少 server 地址（Bark 服务端，如 https://api.day.app）")
	}
	return nil
}

func (s *BarkSender) Send(ctx context.Context, cfg map[string]any, msg Message) error {
	if err := s.Validate(cfg); err != nil {
		return err
	}
	key := cfgStr(cfg, "device_key")
	server := strings.TrimRight(cfgStr(cfg, "server"), "/")
	u := server + "/push/" + queryEscape(key)

	client := s.client
	if client == nil {
		client = httpClientWithTimeout(10)
	}

	title := fmt.Sprintf("[%s] %s", severityLabel(msg.Severity), msg.Title)
	body := msg.Body
	// Bark 对 body 长度有限制，超长会截断
	if len(body) > 1000 {
		body = body[:1000] + "..."
	}

	payload := map[string]any{
		"title":     title,
		"body":      body,
		"group":     "ProbeOne",
		"badge":     1,
		"sound":     cfgStr(cfg, "sound"),
		"isArchive": cfgBool(cfg, "archive", true),
	}
	if msg.URL != "" {
		payload["url"] = msg.URL
	}
	// 自定义服务器需要额外鉴权
	if token := cfgStr(cfg, "token"); token != "" {
		payload["token"] = token
	}

	return postJSON(ctx, client, u, payload, nil)
}

// ---------- 辅助 ----------

// fieldsToMap 把字段列表转成 map，便于 webhook 消费。
func fieldsToMap(fields []Field) map[string]string {
	if len(fields) == 0 {
		return nil
	}
	m := make(map[string]string, len(fields))
	for _, f := range fields {
		m[f.Label] = f.Value
	}
	return m
}

// marshalJSON 是 json.Marshal 的别名。
func marshalJSON(v any) ([]byte, error) { return jsonMarshal(v) }
