// Package config 提供集中式配置加载。
//
// 设计原则（PRD 13 章）：
//   - 全部配置来自环境变量，启动时集中校验，校验失败立即退出（快速失败）
//   - 任何配置项取值非法都必须导致启动失败，不允许静默回落到默认值：
//     用户把采集间隔从 10s 改成 15s 却因拼写错误没生效还毫无察觉，
//     这类"温柔的失败"在监控系统里比直接启动失败危险得多
//   - 密钥只从环境变量读取，绝不从默认值兜底，绝不写入代码库
//   - 校验错误一次性汇总返回，便于用户一次改完，而不是逐个报错
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 是服务端的完整配置。
type Config struct {
	Server     ServerConfig
	Database   DatabaseConfig
	Agent      AgentServerConfig
	Collector  CollectorConfig
	Alert      AlertConfig
	Security   SecurityConfig
	Log        LogConfig
	Storage    StorageConfig
	Visibility VisibilityConfig
}

type ServerConfig struct {
	HTTPPort  int
	GRPCPort  int
	PublicURL string
	TLSCert   string
	TLSKey    string
	MTLSCA    string
	// TrustProxy 声明哪些来源 IP 可信为反向代理。
	// 只有来自这些网段的请求才采信 X-Forwarded-For。
	// 配错的后果见PRD 13 章：留空会让审计失效，填 0.0.0.0/0 会被伪造绕过。
	TrustProxy []string
}

type DatabaseConfig struct {
	Driver string // sqlite | postgres
	// Path 是 SQLite 数据库文件路径。
	Path string
	// DSN 是 PostgreSQL 的完整连接串。
	// 给了它就忽略下面的离散字段——容器部署常直接给 DSN。
	DSN string

	// 以下字段仅 PostgreSQL 使用。DSN 为空时用于拼装连接串。
	Host     string
	Port     int
	User     string
	Password string
	// Database 是库名。
	Database string
	// SSLMode 覆盖 DSN 里的 sslmode。默认 require
	//（远程连接明文传密码风险太高）。
	SSLMode string

	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

type AgentServerConfig struct {
	HandshakeTimeout      time.Duration
	MaxFailuresBeforeLock int
	LockDuration          time.Duration
	HardLockAfter         int
	HardLockHours         int
	DefaultReportInterval time.Duration
	DefaultHeartbeat      time.Duration
	SessionTTLMultiplier  int
}

type CollectorConfig struct {
	WebInterval       time.Duration
	OfflineGrace      time.Duration
	RollupInterval    time.Duration
	RawRetention      time.Duration
	Rollup1mRetention time.Duration
	Rollup1hRetention time.Duration
	MonitorRetention  time.Duration
	AuditRetention    time.Duration
}

type AlertConfig struct {
	DedupWindow    time.Duration
	StormThreshold int
	StormSilence   time.Duration
	MaxQueueSize   int
}

type SecurityConfig struct {
	MasterKey         string
	AllowRegistration bool
	PasswordMinLength int
	MaxLoginFailures  int
	LoginLockMinutes  int
	CookieSecure      bool
}

type LogConfig struct {
	Level      string
	Format     string
	MaxSizeMB  int
	MaxBackups int
}

type StorageConfig struct {
	AllowInternalTargets bool
}

type VisibilityConfig struct {
	StatusShowNodes bool
	PublicIPMask    string
	ReloadInterval  time.Duration
}

// Load 从环境变量读取配置并校验。
// 返回错误时，调用方应打印错误并以非零码退出，不允许带着无效配置启动。
func Load() (*Config, error) {
	var errs []string
	addErr := func(format string, args ...any) {
		errs = append(errs, fmt.Sprintf(format, args...))
	}

	cfg := &Config{
		Server: ServerConfig{
			HTTPPort:   envInt("PROBEONE_HTTP_PORT", 8000, addErr),
			GRPCPort:   envInt("PROBEONE_GRPC_PORT", 8008, addErr),
			PublicURL:  envString("PROBEONE_PUBLIC_URL", ""),
			TLSCert:    envString("PROBEONE_TLS_CERT", ""),
			TLSKey:     envString("PROBEONE_TLS_KEY", ""),
			MTLSCA:     envString("PROBEONE_MTLS_CA", ""),
			TrustProxy: splitAndTrim(envString("PROBEONE_TRUST_PROXY", "")),
		},
		Database: DatabaseConfig{
			Driver:          envString("PROBEONE_DB_DRIVER", "sqlite"),
			Path:            envString("PROBEONE_DB_PATH", "./data/probeone.db"),
			DSN:             envString("PROBEONE_DB_DSN", ""),
			Host:            envString("PROBEONE_DB_HOST", "127.0.0.1"),
			Port:            envInt("PROBEONE_DB_PORT", 5432, addErr),
			User:            envString("PROBEONE_DB_USER", "probeone"),
			Password:        envString("PROBEONE_DB_PASSWORD", ""),
			Database:        envString("PROBEONE_DB_NAME", "probeone"),
			SSLMode:         envString("PROBEONE_DB_SSLMODE", "require"),
			MaxOpenConns:    envInt("PROBEONE_DB_MAX_OPEN_CONNS", 25, addErr),
			MaxIdleConns:    envInt("PROBEONE_DB_MAX_IDLE_CONNS", 5, addErr),
			ConnMaxLifetime: envDuration("PROBEONE_DB_CONN_MAX_LIFETIME", time.Hour, addErr),
		},
		Agent: AgentServerConfig{
			HandshakeTimeout:      envDuration("PROBEONE_AGENT_HANDSHAKE_TIMEOUT", 10*time.Second, addErr),
			MaxFailuresBeforeLock: envInt("PROBEONE_AGENT_MAX_FAILURES", 5, addErr),
			LockDuration:          envDuration("PROBEONE_AGENT_LOCK_DURATION", 5*time.Minute, addErr),
			HardLockAfter:         envInt("PROBEONE_AGENT_HARD_LOCK_AFTER", 20, addErr),
			HardLockHours:         envInt("PROBEONE_AGENT_HARD_LOCK_HOURS", 24, addErr),
			DefaultReportInterval: envDuration("PROBEONE_AGENT_REPORT_INTERVAL", 10*time.Second, addErr),
			DefaultHeartbeat:      envDuration("PROBEONE_AGENT_HEARTBEAT", 30*time.Second, addErr),
			SessionTTLMultiplier:  envInt("PROBEONE_AGENT_SESSION_TTL_MULT", 3, addErr),
		},
		Collector: CollectorConfig{
			WebInterval:       envDuration("PROBEONE_WEB_INTERVAL", 15*time.Second, addErr),
			OfflineGrace:      envDuration("PROBEONE_OFFLINE_GRACE", 50*time.Second, addErr),
			RollupInterval:    envDuration("PROBEONE_ROLLUP_INTERVAL", 5*time.Minute, addErr),
			RawRetention:      envDays("PROBEONE_RETENTION_RAW_DAYS", 7, addErr),
			Rollup1mRetention: envDays("PROBEONE_RETENTION_ROLLUP1M_DAYS", 30, addErr),
			Rollup1hRetention: envDays("PROBEONE_RETENTION_ROLLUP1H_DAYS", 365, addErr),
			MonitorRetention:  envDays("PROBEONE_RETENTION_MONITOR_DAYS", 90, addErr),
			AuditRetention:    envDays("PROBEONE_RETENTION_AUDIT_DAYS", 180, addErr),
		},
		Alert: AlertConfig{
			DedupWindow:    envDuration("PROBEONE_ALERT_DEDUP_WINDOW", 30*time.Minute, addErr),
			StormThreshold: envInt("PROBEONE_ALERT_STORM_THRESHOLD", 3, addErr),
			StormSilence:   envDuration("PROBEONE_ALERT_STORM_SILENCE", time.Hour, addErr),
			MaxQueueSize:   envInt("PROBEONE_ALERT_MAX_QUEUE", 10000, addErr),
		},
		Security: SecurityConfig{
			MasterKey:         envString("PROBEONE_MASTER_KEY", ""),
			AllowRegistration: envBool("PROBEONE_ALLOW_REGISTRATION", false, addErr),
			PasswordMinLength: envInt("PROBEONE_PASSWORD_MIN_LENGTH", 10, addErr),
			MaxLoginFailures:  envInt("PROBEONE_MAX_LOGIN_FAILURES", 3, addErr),
			LoginLockMinutes:  envInt("PROBEONE_LOGIN_LOCK_MINUTES", 15, addErr),
			CookieSecure:      envBool("PROBEONE_COOKIE_SECURE", true, addErr),
		},
		Log: LogConfig{
			Level:      envString("PROBEONE_LOG_LEVEL", "info"),
			Format:     envString("PROBEONE_LOG_FORMAT", "json"),
			MaxSizeMB:  envInt("PROBEONE_LOG_MAX_SIZE_MB", 100, addErr),
			MaxBackups: envInt("PROBEONE_LOG_MAX_BACKUPS", 5, addErr),
		},
		Storage: StorageConfig{
			AllowInternalTargets: envBool("PROBEONE_ALLOW_INTERNAL_TARGETS", false, addErr),
		},
		Visibility: VisibilityConfig{
			StatusShowNodes: envBool("PROBEONE_VISIBILITY_STATUS_NODES", true, nil),
			PublicIPMask:    envString("PROBEONE_VISIBILITY_PUBLIC_IP_MASK", "hide"),
			ReloadInterval:  envDuration("PROBEONE_VISIBILITY_RELOAD_SEC", 60*time.Second, addErr),
		},
	}

	if err := cfg.validate(addErr); err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("配置校验失败：\n  - %s", strings.Join(errs, "\n  - "))
	}
	return cfg, nil
}

// validate 做跨字段的一致性检查。addErr 为 nil 时跳过（用于可选项目的解析）。
func (c *Config) validate(addErr func(string, ...any)) error {
	var errs []string
	push := func(format string, args ...any) {
		errs = append(errs, fmt.Sprintf(format, args...))
	}

	// 端口校验
	if c.Server.HTTPPort < 1 || c.Server.HTTPPort > 65535 {
		push("PROBEONE_HTTP_PORT 非法：%d（应在 1-65535）", c.Server.HTTPPort)
	}
	if c.Server.GRPCPort < 1 || c.Server.GRPCPort > 65535 {
		push("PROBEONE_GRPC_PORT 非法：%d（应在 1-65535）", c.Server.GRPCPort)
	}
	if addErr == nil {
		return nil
	}
	if c.Server.HTTPPort == c.Server.GRPCPort {
		push("HTTP 与 gRPC 端口不能相同：均为 %d。端口隔离是本项目的核心安全设计（PRD 4.2）", c.Server.HTTPPort)
	}

	// 数据库
	switch c.Database.Driver {
	case "sqlite":
		if c.Database.Path == "" {
			push("使用 sqlite 时必须设置 PROBEONE_DB_PATH")
		}
	case "postgres":
		if c.Database.DSN == "" {
			push("使用 postgres 时必须设置 PROBEONE_DB_DSN")
		}
	default:
		push("PROBEONE_DB_DRIVER 只支持 sqlite 或 postgres，当前：%s", c.Database.Driver)
	}

	// TLS 配置成对出现
	if (c.Server.TLSCert == "") != (c.Server.TLSKey == "") {
		push("PROBEONE_TLS_CERT 与 PROBEONE_TLS_KEY 必须同时设置")
	}

	// 主密钥：唯一不允许默认值的配置
	if c.Security.MasterKey == "" {
		push("PROBEONE_MASTER_KEY 未设置。生成方式：openssl rand -base64 32")
	} else if len(c.Security.MasterKey) < 32 {
		push("PROBEONE_MASTER_KEY 长度不足 32 字符，当前 %d。请用 openssl rand -base64 32 生成", len(c.Security.MasterKey))
	}

	// 信任反代：检测危险的通配配置
	for _, tp := range c.Server.TrustProxy {
		if tp == "0.0.0.0/0" || tp == "::/0" || tp == "*" {
			push("PROBEONE_TRUST_PROXY 不能设为 %s：任何人都能伪造 X-Forwarded-For，审计与限流将全部失效", tp)
		}
	}

	// 日志级别
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		push("PROBEONE_LOG_LEVEL 只支持 debug/info/warn/error，当前：%s", c.Log.Level)
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		push("PROBEONE_LOG_FORMAT 只支持 json 或 text，当前：%s", c.Log.Format)
	}

	// 可见性掩码
	switch c.Visibility.PublicIPMask {
	case "hide", "country_only", "first_two_octets", "first_three_octets":
	default:
		push("PROBEONE_VISIBILITY_PUBLIC_IP_MASK 不支持：%s", c.Visibility.PublicIPMask)
	}

	if len(errs) > 0 {
		return fmt.Errorf("配置校验失败：\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// TrustProxyCIDRs 解析信任反代网段。
// 返回解析失败的条目，便于启动日志提示。
func (c *Config) TrustProxyCIDRs() (nets []*net.IPNet, invalid []string) {
	for _, item := range c.Server.TrustProxy {
		if strings.Contains(item, "/") {
			_, n, err := net.ParseCIDR(item)
			if err != nil {
				invalid = append(invalid, item)
				continue
			}
			nets = append(nets, n)
			continue
		}
		ip := net.ParseIP(item)
		if ip == nil {
			invalid = append(invalid, item)
			continue
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return nets, invalid
}

// ---------- 解析辅助 ----------
//
// 所有解析函数都遵循同一条规则：**空值视为未设置**，回落到默认值。
// 这不是偷懒，而是 Docker Compose /宝塔面板的客观现实：
// 它们经常把未填写的变量以空串传进容器，若空串被当作有效值，
// 整型与布尔项会直接解析失败，导致服务无法启动。
// 唯一例外是 PROBEONE_MASTER_KEY——它必须显式提供，
// 由 validate 负责报错（见 TestLoad_主密钥缺失必须失败）。

// lookup 读取环境变量，空串视为未设置。
func lookup(key string) (string, bool) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	return v, true
}

// envString 读取字符串配置。字符串本身不解析失败，非法值的校验统一在 validate 做。
func envString(key, def string) string {
	if v, ok := lookup(key); ok {
		return v
	}
	return def
}

func envInt(key string, def int, addErr func(string, ...any)) int {
	raw, ok := lookup(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		if addErr != nil {
			addErr("环境变量 %s 不是合法整数：%q", key, raw)
		}
		return def
	}
	return n
}

func envBool(key string, def bool, addErr func(string, ...any)) bool {
	raw, ok := lookup(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		if addErr != nil {
			addErr("环境变量 %s 不是合法布尔值（应用 true/false/1/0）：%q", key, raw)
		}
		return def
	}
	return b
}

func envDuration(key string, def time.Duration, addErr func(string, ...any)) time.Duration {
	raw, ok := lookup(key)
	if !ok {
		return def
	}
	// 先按纯秒数解析（便于 PROBEONE_WEB_INTERVAL=15 这种直觉写法）
	if n, err := strconv.Atoi(raw); err == nil {
		return time.Duration(n) * time.Second
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		if addErr != nil {
			addErr("环境变量 %s 格式非法：%q（可用 30s、5m、1h 或纯秒数 30）", key, raw)
		}
		return def
	}
	return d
}

func envDays(key string, def int, addErr func(string, ...any)) time.Duration {
	return time.Duration(envInt(key, def, addErr)) * 24 * time.Hour
}

func splitAndTrim(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
