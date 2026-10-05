package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoad_默认配置(t *testing.T) {
	t.Setenv("PROBEONE_MASTER_KEY", "0123456789abcdef0123456789abcdef") // 32 字符
	clearExtra(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("加载默认配置应成功，实际错误: %v", err)
	}
	if cfg.Server.HTTPPort != 8000 {
		t.Errorf("默认 HTTP 端口 = %d，期望 8000", cfg.Server.HTTPPort)
	}
	if cfg.Server.GRPCPort != 8008 {
		t.Errorf("默认 gRPC 端口 = %d，期望 8008", cfg.Server.GRPCPort)
	}
	if cfg.Database.Driver != "sqlite" {
		t.Errorf("默认数据库 = %s，期望 sqlite", cfg.Database.Driver)
	}
	if cfg.Log.Format != "json" {
		t.Errorf("默认日志格式 = %s，期望 json", cfg.Log.Format)
	}
	if cfg.Agent.DefaultReportInterval != 10*time.Second {
		t.Errorf("默认报告间隔 = %v，期望 10s", cfg.Agent.DefaultReportInterval)
	}
	if cfg.Visibility.PublicIPMask != "hide" {
		t.Errorf("公网 IP 默认掩码 = %s，期望 hide（最保守）", cfg.Visibility.PublicIPMask)
	}
}

func TestLoad_主密钥缺失必须失败(t *testing.T) {
	clearExtra(t)
	t.Setenv("PROBEONE_MASTER_KEY", "")

	_, err := Load()
	if err == nil {
		t.Fatal("主密钥为空时必须启动失败（PRD T12）")
	}
	if !strings.Contains(err.Error(), "MASTER_KEY") {
		t.Errorf("错误信息应指出 MASTER_KEY，实际: %v", err)
	}
}

func TestLoad_主密钥过短必须失败(t *testing.T) {
	clearExtra(t)
	t.Setenv("PROBEONE_MASTER_KEY", "short")

	_, err := Load()
	if err == nil {
		t.Fatal("主密钥过短时必须启动失败")
	}
	if !strings.Contains(err.Error(), "长度不足") {
		t.Errorf("错误信息应说明长度不足，实际: %v", err)
	}
}

func TestLoad_端口相同必须失败(t *testing.T) {
	clearExtra(t)
	t.Setenv("PROBEONE_MASTER_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("PROBEONE_HTTP_PORT", "9000")
	t.Setenv("PROBEONE_GRPC_PORT", "9000")

	_, err := Load()
	if err == nil {
		t.Fatal("HTTP 与 gRPC 端口相同时必须启动失败（端口隔离是核心安全设计）")
	}
	if !strings.Contains(err.Error(), "端口") {
		t.Errorf("错误信息应指出端口冲突，实际: %v", err)
	}
}

func TestLoad_通配信任反代必须失败(t *testing.T) {
	clearExtra(t)
	t.Setenv("PROBEONE_MASTER_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("PROBEONE_TRUST_PROXY", "0.0.0.0/0")

	_, err := Load()
	if err == nil {
		t.Fatal("TRUST_PROXY=0.0.0.0/0 时必须启动失败（可被伪造绕过限流）")
	}
	if !strings.Contains(err.Error(), "TRUST_PROXY") {
		t.Errorf("错误信息应指出 TRUST_PROXY，实际: %v", err)
	}
}

func TestLoad_数据库驱动非法必须失败(t *testing.T) {
	clearExtra(t)
	t.Setenv("PROBEONE_MASTER_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("PROBEONE_DB_DRIVER", "mysql")

	_, err := Load()
	if err == nil {
		t.Fatal("不支持的数据库驱动必须启动失败")
	}
	if !strings.Contains(err.Error(), "sqlite") {
		t.Errorf("错误信息应列出支持的驱动，实际: %v", err)
	}
}

func TestLoad_postgres必须配DSN(t *testing.T) {
	clearExtra(t)
	t.Setenv("PROBEONE_MASTER_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("PROBEONE_DB_DRIVER", "postgres")
	t.Setenv("PROBEONE_DB_DSN", "")

	_, err := Load()
	if err == nil {
		t.Fatal("postgres 未配 DSN 时必须启动失败")
	}
	if !strings.Contains(err.Error(), "DSN") {
		t.Errorf("错误信息应指出 DSN 缺失，实际: %v", err)
	}
}

func TestLoad_TLS必须成对(t *testing.T) {
	clearExtra(t)
	t.Setenv("PROBEONE_MASTER_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("PROBEONE_TLS_CERT", "/path/cert.pem")
	t.Setenv("PROBEONE_TLS_KEY", "")

	_, err := Load()
	if err == nil {
		t.Fatal("只设证书不设私钥时必须启动失败")
	}
}

func TestLoad_非法日志级别(t *testing.T) {
	clearExtra(t)
	t.Setenv("PROBEONE_MASTER_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("PROBEONE_LOG_LEVEL", "verbose")

	_, err := Load()
	if err == nil {
		t.Fatal("非法日志级别必须启动失败")
	}
}

func TestLoad_非法公网IP掩码(t *testing.T) {
	clearExtra(t)
	t.Setenv("PROBEONE_MASTER_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("PROBEONE_VISIBILITY_PUBLIC_IP_MASK", "no_such_mode")

	_, err := Load()
	if err == nil {
		t.Fatal("非法 IP 掩码模式必须启动失败")
	}
}

func TestLoad_时长支持纯秒数(t *testing.T) {
	clearExtra(t)
	t.Setenv("PROBEONE_MASTER_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("PROBEONE_WEB_INTERVAL", "20")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("纯秒数写法应被接受: %v", err)
	}
	if cfg.Collector.WebInterval != 20*time.Second {
		t.Errorf("纯秒数解析结果 = %v，期望 20s", cfg.Collector.WebInterval)
	}
}

func TestLoad_时长支持Go语法(t *testing.T) {
	clearExtra(t)
	t.Setenv("PROBEONE_MASTER_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("PROBEONE_WEB_INTERVAL", "2m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Go 时长语法应被接受: %v", err)
	}
	if cfg.Collector.WebInterval != 2*time.Minute {
		t.Errorf("Go 语法解析结果 = %v，期望 2m", cfg.Collector.WebInterval)
	}
}

func TestLoad_非法时长必须失败(t *testing.T) {
	clearExtra(t)
	t.Setenv("PROBEONE_MASTER_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("PROBEONE_WEB_INTERVAL", "abc")

	_, err := Load()
	if err == nil {
		t.Fatal("非法时长必须启动失败")
	}
}

func TestTrustProxyCIDRs(t *testing.T) {
	t.Run("解析合法条目", func(t *testing.T) {
		c := &Config{Server: ServerConfig{TrustProxy: []string{
			"127.0.0.1", "::1", "172.17.0.0/16", "10.0.0.5",
		}}}
		nets, invalid := c.TrustProxyCIDRs()
		if len(invalid) != 0 {
			t.Errorf("不应有解析失败项，实际: %v", invalid)
		}
		if len(nets) != 4 {
			t.Errorf("应解析出 4 个网段，实际 = %d", len(nets))
		}
	})

	t.Run("保留非法条目供告警", func(t *testing.T) {
		c := &Config{Server: ServerConfig{TrustProxy: []string{
			"127.0.0.1", "not-an-ip", "999.999.0.0/16",
		}}}
		_, invalid := c.TrustProxyCIDRs()
		if len(invalid) != 2 {
			t.Errorf("应检出 2 个非法条目，实际 = %v", invalid)
		}
	})
}

func TestSplitAndTrim(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"   ", 0},
		{"127.0.0.1", 1},
		{"127.0.0.1,::1", 2},
		{" 127.0.0.1 , ::1 ", 2},
		{"127.0.0.1,,::1", 2},
		{",", 0},
	}
	for _, c := range cases {
		if got := len(splitAndTrim(c.in)); got != c.want {
			t.Errorf("splitAndTrim(%q) 得到 %d 项，期望 %d", c.in, got, c.want)
		}
	}
}

// clearExtra 清除测试可能受其他用例影响的环境变量。
// 用 t.Setenv 设为空，让 Load 走默认值分支。
func clearExtra(t *testing.T) {
	t.Helper()
	keys := []string{
		"PROBEONE_TLS_CERT", "PROBEONE_TLS_KEY", "PROBEONE_MTLS_CA",
		"PROBEONE_TRUST_PROXY", "PROBEONE_DB_DSN", "PROBEONE_PUBLIC_URL",
		"PROBEONE_ALLOW_REGISTRATION", "PROBEONE_ALLOW_INTERNAL_TARGETS",
		"PROBEONE_COOKIE_SECURE", "PROBEONE_VISIBILITY_STATUS_NODES",
		"PROBEONE_VISIBILITY_PUBLIC_IP_MASK", "PROBEONE_WEB_INTERVAL",
		"PROBEONE_LOG_LEVEL", "PROBEONE_LOG_FORMAT", "PROBEONE_DB_PATH",
		"PROBEONE_DB_DRIVER", "PROBEONE_DB_MAX_OPEN_CONNS", "PROBEONE_DB_MAX_IDLE_CONNS",
		"PROBEONE_DB_CONN_MAX_LIFETIME", "PROBEONE_AGENT_HANDSHAKE_TIMEOUT",
		"PROBEONE_AGENT_MAX_FAILURES", "PROBEONE_AGENT_LOCK_DURATION",
		"PROBEONE_AGENT_HARD_LOCK_AFTER", "PROBEONE_AGENT_HARD_LOCK_HOURS",
		"PROBEONE_AGENT_REPORT_INTERVAL", "PROBEONE_AGENT_HEARTBEAT",
		"PROBEONE_AGENT_SESSION_TTL_MULT", "PROBEONE_OFFLINE_GRACE",
		"PROBEONE_ROLLUP_INTERVAL", "PROBEONE_RETENTION_RAW_DAYS",
		"PROBEONE_RETENTION_ROLLUP1M_DAYS", "PROBEONE_RETENTION_ROLLUP1H_DAYS",
		"PROBEONE_RETENTION_MONITOR_DAYS", "PROBEONE_RETENTION_AUDIT_DAYS",
		"PROBEONE_ALERT_DEDUP_WINDOW", "PROBEONE_ALERT_STORM_THRESHOLD",
		"PROBEONE_ALERT_STORM_SILENCE", "PROBEONE_ALERT_MAX_QUEUE",
		"PROBEONE_PASSWORD_MIN_LENGTH", "PROBEONE_MAX_LOGIN_FAILURES",
		"PROBEONE_LOGIN_LOCK_MINUTES", "PROBEONE_LOG_MAX_SIZE_MB",
		"PROBEONE_LOG_MAX_BACKUPS", "PROBEONE_VISIBILITY_RELOAD_SEC",
	}
	for _, k := range keys {
		t.Setenv(k, "")
	}
	// 说明：设成空串即可让配置回落到默认值——
	// Lookup 对空串返回"未设置"（见 config.go 中的 lookup 注释），
	// 这样既不用真的删除环境变量，也保证了每个用例从默认值出发。
}
