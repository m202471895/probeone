package notify

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/m202471895/probeone/server/internal/model"
)

// EmailSender 通过 SMTP 发邮件。
//
// 认证方式有两种（PRD 3.3）：
//   - 无认证：内网自建邮件服务器常见
//   - PLAIN 认证：主流邮件服务
//
// TLS 有两个开关：
//   - tls：连接即 TLS（端口 465，隐式 TLS）
//   - starttls：明文连接后升级（端口 587，显式 TLS）
//
// 两者不应同时开启。
type EmailSender struct {
	client *http.Client // 保留以统一接口；SMTP 不需要它
}

func (s *EmailSender) Type() model.ChannelType { return model.ChannelEmail }

func (s *EmailSender) Validate(cfg map[string]any) error {
	if cfgStr(cfg, "host") == "" {
		return fmt.Errorf("缺少 host")
	}
	if cfgInt(cfg, "port") <= 0 {
		return fmt.Errorf("缺少 port")
	}
	if cfgStr(cfg, "from") == "" {
		return fmt.Errorf("缺少 from（发件人地址）")
	}
	// user 配了就必须有 password
	if cfgStr(cfg, "user") != "" && cfgStr(cfg, "password") == "" {
		return fmt.Errorf("配置了 user 但缺少 password")
	}
	if cfgBool(cfg, "tls", false) && cfgBool(cfg, "starttls", false) {
		return fmt.Errorf("tls 与 starttls 不能同时开启：隐式 TLS 用 465 端口，STARTTLS 用 587")
	}
	return nil
}

func (s *EmailSender) Send(ctx context.Context, cfg map[string]any, msg Message) error {
	if err := s.Validate(cfg); err != nil {
		return err
	}

	host := cfgStr(cfg, "host")
	port := cfgInt(cfg, "port")
	from := cfgStr(cfg, "from")
	user := cfgStr(cfg, "user")
	password := cfgStr(cfg, "password")
	useTLS := cfgBool(cfg, "tls", false)
	useStartTLS := cfgBool(cfg, "starttls", false)

	addr := net.JoinHostPort(host, strconv.Itoa(port))

	// 构造 MIME 邮件
	mail := buildEmail(from, cfgStr(cfg, "name"), cfgStr(cfg, "to"), msg)

	// 带超时的连接，避免 SMTP 服务器无响应时卡住告警分发
	timeout := cfgInt(cfg, "timeout_sec")
	if timeout <= 0 {
		timeout = 15
	}
	dialer := &net.Dialer{Timeout: time.Duration(timeout) * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("连接 SMTP 服务器失败: %w", err)
	}
	defer conn.Close()

	// 隐式 TLS：握手包在连接建立时就发
	if useTLS {
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
			//nolint:gosec // 内网自建邮件服务器常用自签证书；
			// 生产建议置 true 并配 ca_file
			InsecureSkipVerify: cfgBool(cfg, "insecure_skip_verify", true),
		})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("TLS 握手失败: %w", err)
		}
		conn = tlsConn
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("初始化 SMTP 会话失败: %w", err)
	}
	defer client.Close()

	// 显式 TLS：明文连接后升级
	if useStartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("SMTP 服务器不支持 STARTTLS")
		}
		if err := client.StartTLS(&tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
			//nolint:gosec // 同上
			InsecureSkipVerify: cfgBool(cfg, "insecure_skip_verify", true),
		}); err != nil {
			return fmt.Errorf("STARTTLS 升级失败: %w", err)
		}
	}

	if user != "" {
		auth := smtp.PlainAuth("", user, password, host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP 认证失败: %w", err)
		}
	}

	if err := client.Mail(from); err != nil {
		return fmt.Errorf("设置发件人失败: %w", err)
	}
	for _, to := range strings.Split(cfgStr(cfg, "to"), ",") {
		to = strings.TrimSpace(to)
		if to == "" {
			continue
		}
		if err := client.Rcpt(to); err != nil {
			return fmt.Errorf("设置收件人 %s 失败: %w", maskEmail(to), err)
		}
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("准备邮件数据失败: %w", err)
	}
	if _, err := w.Write([]byte(mail)); err != nil {
		_ = w.Close()
		return fmt.Errorf("写入邮件内容失败: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("提交邮件失败: %w", err)
	}
	return client.Quit()
}

// buildEmail 构造 MIME 邮件内容。
//
// 用纯文本格式而非 HTML：HTML 邮件需要处理内联样式，
// 而告警内容全是关键信息，纯文本在 Outlook/手机客户端里显示更稳定。
func buildEmail(from, name, to string, msg Message) string {
	var b strings.Builder

	subject := fmt.Sprintf("[%s] %s", severityLabel(msg.Severity), msg.Title)
	fmt.Fprintf(&b, "From: %s <%s>\r\n", name, from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", sanitizeHeader(subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")

	b.WriteString(msg.Body)
	b.WriteString("\n\n")
	for _, f := range msg.Fields {
		fmt.Fprintf(&b, "%s: %s\n", f.Label, f.Value)
	}
	if msg.URL != "" {
		fmt.Fprintf(&b, "\n详情: %s\n", msg.URL)
	}
	b.WriteString("\n--\n本邮件由 ProbeOne 自动发送，请勿回复。\n")

	return b.String()
}

// sanitizeHeader 清理邮件头里的注入字符。
//
// 主题里若有 \r\n，攻击者可以伪造额外邮件头（BCC 列表等）。
// 任何来自用户输入的内容进邮件头前都必须过这一关。
func sanitizeHeader(s string) string {
	// 去掉所有 CR/LF 及其后的内容——这正是头注入的利用方式
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		s = s[:idx]
	}
	// 限制长度，SMTP 对头长度有上限
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// maskEmail 掩码邮箱，日志里只保留首尾。
func maskEmail(e string) string {
	at := strings.Index(e, "@")
	if at <= 0 {
		return "***"
	}
	name := e[:at]
	domain := e[at:]
	if len(name) <= 2 {
		return "**" + domain
	}
	return name[:2] + "***" + domain
}

// jsonMarshal 是 json.Marshal 的别名，供签名计算使用。
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
