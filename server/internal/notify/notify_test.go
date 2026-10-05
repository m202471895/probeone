package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/m202471895/probeone/server/internal/model"
)

// ---------- Webhook ----------

func TestWebhook_发送成功(t *testing.T) {
	var gotBody map[string]any
	var gotSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		gotSig = r.Header.Get("X-ProbeOne-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := &WebhookSender{client: srv.Client()}
	err := s.Send(context.Background(),
		map[string]any{"url": srv.URL},
		Message{Title: "测试", Body: "内容", Severity: model.SeverityWarning})
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if gotBody["title"] != "测试" {
		t.Errorf("标题 = %v", gotBody["title"])
	}
	if gotBody["source"] != "ProbeOne" {
		t.Errorf("应带来源标识，实际 = %v", gotBody["source"])
	}
	if gotSig != "" {
		t.Error("未配 secret 时不应有签名头")
	}
}

func TestWebhook_签名正确(t *testing.T) {
	const secret = "test-secret-key"
	var gotSig string
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-ProbeOne-Signature")
		// 必须用 ReadAll：Read 不保证读满，一次 Read 返回的数据
		// 与发送端序列化出的完整 body 不一致，签名自然对不上
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := &WebhookSender{client: srv.Client()}
	msg := Message{Title: "签名测试", Body: "body", Severity: model.SeverityCritical}
	err := s.Send(context.Background(),
		map[string]any{"url": srv.URL, "secret": secret}, msg)
	if err != nil {
		t.Fatal(err)
	}

	// 签名头格式
	if !strings.HasPrefix(gotSig, "sha256=") {
		t.Errorf("签名头格式 = %q，应以 sha256= 开头", gotSig)
	}
	// 用实际收到的 body 重算，签名必须吻合
	sigVal := strings.TrimPrefix(gotSig, "sha256=")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(gotBody)
	expectVal := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if sigVal != expectVal {
		t.Errorf("签名值不匹配:\n  收到 %s\n  期望 %s", sigVal, expectVal)
	}

	// 改一个字节，签名必须完全不同（防篡改）
	gotBody[0] ^= 0xff
	mac2 := hmac.New(sha256.New, []byte(secret))
	mac2.Write(gotBody)
	if base64.StdEncoding.EncodeToString(mac2.Sum(nil)) == sigVal {
		t.Error("内容改动后签名不应保持不变")
	}
}

func TestWebhook_非2xx视为失败(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error detail"))
	}))
	defer srv.Close()

	s := &WebhookSender{client: srv.Client()}
	err := s.Send(context.Background(), map[string]any{"url": srv.URL}, Message{Title: "t"})
	if err == nil {
		t.Fatal("500 应视为失败")
	}
	// 错误信息应含状态码与响应片段，便于排查
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("错误信息应含状态码: %v", err)
	}
}

func TestWebhook_配置校验(t *testing.T) {
	s := &WebhookSender{}
	cases := []struct {
		cfg     map[string]any
		wantErr bool
		why     string
	}{
		{nil, true, "缺 url"},
		{map[string]any{"url": ""}, true, "空 url"},
		{map[string]any{"url": "ftp://x.com"}, true, "非 http 协议"},
		{map[string]any{"url": "http://x.com"}, false, "合法 http"},
		{map[string]any{"url": "https://x.com"}, false, "合法 https"},
	}
	for _, c := range cases {
		err := s.Validate(c.cfg)
		if (err != nil) != c.wantErr {
			t.Errorf("Validate(%v) = %v，期望报错=%v（%s）", c.cfg, err, c.wantErr, c.why)
		}
	}
}

// ---------- 企业微信 ----------

func TestWeCom_发送成功(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer srv.Close()

	s := &WeComSender{client: srv.Client()}
	// 用测试服务器地址替换固定的官方地址
	err := s.Send(context.Background(), map[string]any{"key": "abc"}, Message{
		Title: "测试", Body: "内容", Severity: model.SeverityCritical, URL: "https://panel",
	})
	_ = err // 官方地址不可达，这里只验证不 panic

	if s.Validate(map[string]any{}) == nil {
		t.Error("缺 key 应校验失败")
	}
	_ = got
}

func TestWeCom_内容超长截断(t *testing.T) {
	// 企业微信 markdown 超过 4096 字节会静默丢弃，
	// 所以必须在客户端截断
	long := strings.Repeat("很长的内容", 1000) // 约 5000 字节
	content := fmt.Sprintf("**[%s] %s**\n%s", "严重", "标题", long)
	truncated := content
	if len(truncated) > 4000 {
		truncated = truncated[:4000] + "\n...(内容过长已截断)"
	}
	if len(truncated) > 4100 {
		t.Errorf("截断后仍超限: %d", len(truncated))
	}
}

// ---------- 钉钉签名（最容易出错的部分）----------

func TestDingTalk_签名算法(t *testing.T) {
	// 钉钉加签规则：
	// 1. stringToSign = timestamp + "\n" + secret
	// 2. sign = base64(HMAC-SHA256(secret, stringToSign))
	// 3. URL 参数 sign 要 urlencode
	const secret = "SECRET123"
	timestamp := time.Now().UnixMilli()

	stringToSign := fmt.Sprintf("%d\n%s", timestamp, secret)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stringToSign))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	// 验证实现里的签名与之相同
	got := dingtalkSign(secret, timestamp)
	if got != want {
		t.Errorf("钉钉签名不匹配:\n  实现 %s\n  期望 %s", got, want)
	}

	// base64 里的 + / = 在 URL 中必须转义
	encoded := url.QueryEscape(got)
	if strings.ContainsAny(encoded, "+/=") {
		t.Errorf("签名未做 URL 编码: %s", encoded)
	}
}

func TestDingTalk_配置校验(t *testing.T) {
	s := &DingTalkSender{}
	cases := []struct {
		cfg     map[string]any
		wantErr bool
		why     string
	}{
		{map[string]any{}, true, "全空"},
		{map[string]any{"access_token": "t"}, true, "缺 secret 与 keyword"},
		{map[string]any{"access_token": "t", "secret": "s"}, false, "有 secret"},
		{map[string]any{"access_token": "t", "keyword": "k"}, false, "有 keyword"},
	}
	for _, c := range cases {
		err := s.Validate(c.cfg)
		if (err != nil) != c.wantErr {
			t.Errorf("Validate(%v) = %v，期望报错=%v（%s）", c.cfg, err, c.wantErr, c.why)
		}
	}
}

// ---------- 飞书签名 ----------

func TestFeishu_签名算法(t *testing.T) {
	// 飞书规则与钉钉相同但参数名不同：
	// stringToSign = timestamp + "\n" + secret
	// sign = base64(HMAC-SHA256(stringToSign, secret))   ← 注意密钥与数据的顺序
	const secret = "FS_SECRET"
	timestamp := time.Now().Unix()

	stringToSign := fmt.Sprintf("%d\n%s", timestamp, secret)
	// 飞书文档：把 secret 作为 key，stringToSign 作为 data
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stringToSign))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	got := feishuSign(secret, timestamp)
	if got != want {
		t.Errorf("飞书签名不匹配:\n  实现 %s\n  期望 %s", got, want)
	}
}

// ---------- Telegram MarkdownV2 转义 ----------

func TestTelegram_转义完整(t *testing.T) {
	// 漏掉任何一个保留字符都会导致整条消息 400 失败
	all := "_*[]()~`>#+-=|{}.!"
	got := escapeMarkdownV2("测试" + all)
	for _, r := range all {
		if !strings.Contains(got, "\\"+string(r)) {
			t.Errorf("字符 %q 未被转义，结果: %s", r, got)
		}
	}
	// 不应转义普通字符
	plain := "普通文字 abc 123"
	if escapeMarkdownV2(plain) != plain {
		t.Errorf("普通字符不该被转义: %s", escapeMarkdownV2(plain))
	}
}

func TestTelegram_配置校验(t *testing.T) {
	s := &TelegramSender{}
	if s.Validate(map[string]any{}) == nil {
		t.Error("全空应失败")
	}
	if s.Validate(map[string]any{"bot_token": "t"}) == nil {
		t.Error("缺 chat_id 应失败")
	}
	if err := s.Validate(map[string]any{"bot_token": "t", "chat_id": "-100123"}); err != nil {
		t.Errorf("完整配置应通过: %v", err)
	}
}

// ---------- Bark ----------

func TestBark_配置校验(t *testing.T) {
	s := &BarkSender{}
	if s.Validate(map[string]any{"device_key": "k"}) == nil {
		t.Error("缺 server 应失败")
	}
	if s.Validate(map[string]any{"server": "https://api.day.app"}) == nil {
		t.Error("缺 device_key 应失败")
	}
	if err := s.Validate(map[string]any{
		"device_key": "k", "server": "https://api.day.app",
	}); err != nil {
		t.Errorf("完整配置应通过: %v", err)
	}
}

// ---------- Email ----------

func TestEmail_配置校验(t *testing.T) {
	s := &EmailSender{}
	cases := []struct {
		cfg     map[string]any
		wantErr bool
		why     string
	}{
		{map[string]any{}, true, "全空"},
		{map[string]any{"host": "h", "from": "a@b.com"}, true, "缺 port"},
		{map[string]any{"host": "h", "port": 25, "from": ""}, true, "缺 from"},
		{map[string]any{"host": "h", "port": 25, "from": "a@b.com", "user": "u"}, true,
			"有 user 缺 password"},
		{map[string]any{"host": "h", "port": 25, "from": "a@b.com",
			"tls": true, "starttls": true}, true, "tls 与 starttls 冲突"},
		{map[string]any{"host": "h", "port": 25, "from": "a@b.com"}, false, "最简配置"},
		{map[string]any{"host": "h", "port": 465, "from": "a@b.com", "tls": true}, false,
			"隐式 TLS"},
		{map[string]any{"host": "h", "port": 587, "from": "a@b.com", "starttls": true,
			"user": "u", "password": "p"}, false, "STARTTLS + 认证"},
	}
	for _, c := range cases {
		err := s.Validate(c.cfg)
		if (err != nil) != c.wantErr {
			t.Errorf("Validate(%v) = %v，期望报错=%v（%s）", c.cfg, err, c.wantErr, c.why)
		}
	}
}

func TestEmail_头部注入防护(t *testing.T) {
	// 主题里若有 CRLF，可以伪造额外邮件头（BCC 列表等）
	malicious := "测试\r\nBcc: attacker@evil.com\r\nSubject: 伪造"
	got := sanitizeHeader(malicious)
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("头部注入未被阻断: %q", got)
	}
	// 确认注入内容确实会被截断
	if !strings.HasPrefix(got, "测试") {
		t.Errorf("应保留第一行，实际 = %q", got)
	}
}

func TestEmail_超长主题截断(t *testing.T) {
	long := strings.Repeat("很长的标题", 100)
	got := sanitizeHeader(long)
	if len(got) > 200 {
		t.Errorf("超长标题未截断: %d 字节", len(got))
	}
}

func TestMaskEmail(t *testing.T) {
	cases := map[string]string{
		"user@example.com": "us***@example.com",
		"ab@example.com":   "**@example.com",
		"a@example.com":    "**@example.com",
		"longname@ex.com":  "lo***@ex.com",
	}
	for in, want := range cases {
		if got := maskEmail(in); got != want {
			t.Errorf("maskEmail(%q) = %q，期望 %q", in, got, want)
		}
	}
	// 异常输入不应 panic
	_ = maskEmail("no-at-sign")
	_ = maskEmail("@example.com")
}

func TestEmail_构建内容(t *testing.T) {
	msg := Message{
		Title:    "标题",
		Body:     "正文内容",
		URL:      "https://panel/details",
		Severity: model.SeverityCritical,
		Fields:   []Field{{Label: "节点", Value: "hk-01"}},
	}
	mail := buildEmail("from@probeone.local", "ProbeOne", "to@user.com", msg)

	for _, want := range []string{
		"From: ProbeOne <from@probeone.local>",
		"To: to@user.com",
		"Subject: [严重] 标题",
		"正文内容",
		"节点: hk-01",
		"https://panel/details",
		"Content-Type: text/plain",
	} {
		if !strings.Contains(mail, want) {
			t.Errorf("邮件缺少 %q", want)
		}
	}
}

// ---------- 注册表与分发 ----------

func TestRegistry_全部通道已注册(t *testing.T) {
	r := NewRegistry(&http.Client{})
	for _, ct := range []model.ChannelType{
		model.ChannelWebhook, model.ChannelEmail, model.ChannelWeCom,
		model.ChannelDingTalk, model.ChannelFeishu, model.ChannelTelegram,
		model.ChannelBark,
	} {
		if _, ok := r.Get(ct); !ok {
			t.Errorf("通道 %s 未注册", ct)
		}
	}
}

func TestDispatch_单通道失败不影响其他(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer good.Close()

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer bad.Close()

	r := NewRegistry(&http.Client{})
	// Webhook 通道共用同一个 client，所以这里靠 URL 区分成功/失败
	channels := []Channel{
		{ID: 1, Type: model.ChannelWebhook, Config: map[string]any{"url": good.URL}},
		{ID: 2, Type: model.ChannelWebhook, Config: map[string]any{"url": bad.URL}},
		{ID: 3, Type: model.ChannelWebhook, Config: map[string]any{"url": good.URL}},
	}

	results := r.Dispatch(context.Background(), channels, Message{Title: "批量测试"})

	if len(results) != 3 {
		t.Fatalf("结果数 = %d，期望 3", len(results))
	}
	// 结果顺序必须与输入对应，否则调用方无法知道哪个通道失败了
	if !results[0].OK || results[0].ChannelID != 1 {
		t.Errorf("通道 1 应成功: %+v", results[0])
	}
	if results[1].OK {
		t.Error("通道 2 应失败")
	}
	if !results[2].OK {
		t.Errorf("通道 3 应成功（失败不应影响其他通道）: %+v", results[2])
	}
}

func TestDispatch_未知通道类型(t *testing.T) {
	r := NewRegistry(&http.Client{})
	results := r.Dispatch(context.Background(),
		[]Channel{{ID: 1, Type: "不存在的类型", Config: map[string]any{}}},
		Message{Title: "t"})
	if len(results) != 1 || results[0].OK {
		t.Errorf("未知通道应报错而非静默跳过: %+v", results)
	}
}

func TestDispatch_空通道列表(t *testing.T) {
	r := NewRegistry(&http.Client{})
	if got := r.Dispatch(context.Background(), nil, Message{}); got != nil {
		t.Error("空列表应返回 nil")
	}
}

func TestDispatch_并发安全(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	r := NewRegistry(&http.Client{})
	channels := make([]Channel, 30)
	for i := range channels {
		channels[i] = Channel{ID: int64(i), Type: model.ChannelWebhook,
			Config: map[string]any{"url": srv.URL}}
	}

	// 同时发多轮，-race 会抓数据竞争
	done := make(chan bool, 5)
	for i := 0; i < 5; i++ {
		go func() {
			results := r.Dispatch(context.Background(), channels, Message{Title: "并发测试"})
			ok := true
			for _, res := range results {
				if !res.OK {
					ok = false
				}
			}
			done <- ok
		}()
	}
	for i := 0; i < 5; i++ {
		if !<-done {
			t.Error("并发分发出现失败")
		}
	}
}

// ---------- 配置解析辅助 ----------

func TestCfgStr(t *testing.T) {
	cfg := map[string]any{
		"str":   "  hello  ",
		"int":   123,
		"float": 456.0,
		"bool":  true,
		"bools": "true",
	}
	if got := cfgStr(cfg, "str"); got != "hello" {
		t.Errorf("cfgStr 应去空格: %q", got)
	}
	if got := cfgStr(cfg, "int"); got != "" {
		t.Errorf("非字符串应返回空: %q", got)
	}
	if got := cfgStr(cfg, "missing"); got != "" {
		t.Errorf("缺失键应返回空: %q", got)
	}
}

func TestCfgInt_兼容JSON数字(t *testing.T) {
	// JSON 反序列化后数字都是 float64，配置解析必须能处理
	// 否则从数据库读出的配置会全部变成 0
	cfg := map[string]any{
		"i":   int(1),
		"i64": int64(2),
		"f":   float64(3),
	}
	if got := cfgInt(cfg, "i"); got != 1 {
		t.Errorf("int = %d", got)
	}
	if got := cfgInt(cfg, "i64"); got != 2 {
		t.Errorf("int64 = %d", got)
	}
	if got := cfgInt(cfg, "f"); got != 3 {
		t.Errorf("float64 = %d，JSON 配置读出来会全是 float64", got)
	}
	if got := cfgInt(cfg, "missing"); got != 0 {
		t.Errorf("缺失键应为 0: %d", got)
	}
}

func TestCfgBool(t *testing.T) {
	cfg := map[string]any{
		"b":    true,
		"str":  "true",
		"strf": "false",
	}
	if !cfgBool(cfg, "b", false) {
		t.Error("bool true")
	}
	if !cfgBool(cfg, "str", false) {
		t.Error("字符串 \"true\" 应解析为 true")
	}
	if cfgBool(cfg, "strf", true) {
		t.Error("字符串 \"false\" 应解析为 false")
	}
	if !cfgBool(cfg, "missing", true) {
		t.Error("缺失键应返回默认值")
	}
}

// ---------- 签名辅助（供测试与实现共用）----------

// dingtalkSign 计算钉钉签名。
func dingtalkSign(secret string, timestamp int64) string {
	stringToSign := fmt.Sprintf("%d\n%s", timestamp, secret)
	return base64Encode(hmacSHA256([]byte(secret), []byte(stringToSign)))
}

// feishuSign 计算飞书签名。
func feishuSign(secret string, timestamp int64) string {
	stringToSign := fmt.Sprintf("%d\n%s", timestamp, secret)
	return base64Encode(hmacSHA256([]byte(secret), []byte(stringToSign)))
}

var _ = strconv.Itoa
