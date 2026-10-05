package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig 在临时目录写入配置文件并返回路径。
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	return p
}

const minimalConfig = `
server:
  addr: "probe.example.com:8008"
auth:
  uuid: "test-uuid-1234"
  secret: "test-secret-abcdef123456"
collect:
  interval_sec: 10
`

func TestLoad_最小配置(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatalf("最小配置应加载成功: %v", err)
	}
	if cfg.Server.Addr != "probe.example.com:8008" {
		t.Errorf("addr = %s", cfg.Server.Addr)
	}
	if cfg.Collect.IntervalSec != 10 {
		t.Errorf("interval = %d，期望 10", cfg.Collect.IntervalSec)
	}
}

func TestLoad_默认值填充(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.Network.DialTimeoutSec != DefaultDialTimeout {
		t.Errorf("拨号超时应默认为 %d，实际 = %d", DefaultDialTimeout, cfg.Network.DialTimeoutSec)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("日志级别应默认为 info，实际 = %s", cfg.Log.Level)
	}
	// metrics 未配置时应采集全部非特权指标
	if len(cfg.Metrics()) != 6 {
		t.Errorf("默认指标数 = %d，期望 6：%v", len(cfg.Metrics()), cfg.Metrics())
	}
	// 缓冲应默认开启
	if !cfg.Buffer.Enabled {
		t.Error("离线缓冲应默认开启（断网期间数据需补传）")
	}
	if cfg.Buffer.MaxPoints != DefaultBufferPoints {
		t.Errorf("缓冲点数应默认为 %d，实际 = %d", DefaultBufferPoints, cfg.Buffer.MaxPoints)
	}
	if cfg.Buffer.MaxBytes != DefaultBufferMaxBytes {
		t.Errorf("缓冲字节上限应默认为 %d，实际 = %d", DefaultBufferMaxBytes, cfg.Buffer.MaxBytes)
	}
	// 未特权标记时，不应把任何指标判为特权
	for _, m := range cfg.Metrics() {
		if cfg.IsPrivileged(m) {
			t.Errorf("指标 %s 不应被标记为特权", m)
		}
	}
}

func TestValidate_必填项(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "缺 server.addr",
			content: "auth:\n  uuid: u\n  secret: s\n",
			wantErr: "server.addr",
		},
		{
			name:    "缺 auth.uuid",
			content: "server:\n  addr: a:1\nauth:\n  secret: s\n",
			wantErr: "auth.uuid",
		},
		{
			name:    "缺 auth.secret",
			content: "server:\n  addr: a:1\nauth:\n  uuid: u\n",
			wantErr: "auth.secret",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, c.content))
			if err == nil {
				t.Fatalf("应校验失败")
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("错误信息应含 %q，实际: %v", c.wantErr, err)
			}
		})
	}
}

func TestValidate_禁止关闭证书校验(t *testing.T) {
	// 生产环境关闭证书校验 = 流量劫持不可检测，必须拒绝
	content := `
server:
  addr: "a:1"
  insecure_skip_verify: true
auth:
  uuid: u
  secret: s
`
	_, err := Load(writeConfig(t, content))
	if err == nil {
		t.Fatal("insecure_skip_verify=true 必须被拒绝")
	}
	if !strings.Contains(err.Error(), "insecure_skip_verify") {
		t.Errorf("错误信息应指出证书校验问题，实际: %v", err)
	}
}

func TestValidate_证书文件必须成对(t *testing.T) {
	content := `
server:
  addr: "a:1"
  cert_file: /path/c.pem
auth:
  uuid: u
  secret: s
`
	_, err := Load(writeConfig(t, content))
	if err == nil {
		t.Fatal("只设 cert_file 不设 key_file 必须失败")
	}
	if !strings.Contains(err.Error(), "key_file") {
		t.Errorf("错误信息应指出 key_file，实际: %v", err)
	}
}

func TestValidate_CA与pin冗余(t *testing.T) {
	content := `
server:
  addr: "a:1"
  ca_file: /ca.pem
  server_cert_fingerprint: "aa:bb"
auth:
  uuid: u
  secret: s
`
	_, err := Load(writeConfig(t, content))
	if err == nil {
		t.Fatal("同时配 CA 与指纹应报错（二选一）")
	}
}

func TestValidate_采集间隔范围(t *testing.T) {
	// 负数与超大值必须被拒绝。
	// 注意 0 是合法输入，含义是"用默认值"（见 TestLoad_默认值填充）。
	for _, v := range []string{"-1", "99999"} {
		content := `
server:
  addr: "a:1"
auth:
  uuid: u
  secret: s
collect:
  interval_sec: ` + v + `
`
		if _, err := Load(writeConfig(t, content)); err == nil {
			t.Errorf("interval_sec=%s 应被拒绝", v)
		}
	}

	// 0 应回落到默认值而非报错
	content := `
server:
  addr: "a:1"
auth:
  uuid: u
  secret: s
collect:
  interval_sec: 0
`
	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("interval_sec=0 应回落到默认值，实际报错: %v", err)
	}
	if cfg.Collect.IntervalSec != DefaultIntervalSec {
		t.Errorf("interval_sec=0 应回落为 %d，实际 = %d", DefaultIntervalSec, cfg.Collect.IntervalSec)
	}
}

func TestWantsMetric(t *testing.T) {
	content := `
server:
  addr: "a:1"
auth:
  uuid: u
  secret: s
collect:
  metrics: [cpu, mem]
  privileged: [sensors]
`
	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if !cfg.WantsMetric("cpu") {
		t.Error("cpu 应被启用")
	}
	if cfg.WantsMetric("disk") {
		t.Error("disk 未在配置中，不应被启用")
	}
	if !cfg.IsPrivileged("sensors") {
		t.Error("sensors 应被标记为特权指标")
	}
	if cfg.IsPrivileged("cpu") {
		t.Error("cpu 不应被标记为特权")
	}
}

func TestLoad_文件不存在(t *testing.T) {
	// Agent 不猜测服务端地址，配置缺失必须直接失败
	_, err := Load(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if err == nil {
		t.Fatal("配置文件不存在时必须失败")
	}
}

func TestLoad_YAML格式错误(t *testing.T) {
	_, err := Load(writeConfig(t, "server:\n  addr: [unclosed\n"))
	if err == nil {
		t.Fatal("YAML 格式错误必须失败")
	}
	if !strings.Contains(err.Error(), "解析") {
		t.Errorf("错误信息应说明解析失败，实际: %v", err)
	}
}

func TestEnsureConfigPermissions(t *testing.T) {
	t.Run("权限过宽时收紧", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := EnsureConfigPermissions(p); err != nil {
			t.Fatalf("收紧权限失败: %v", err)
		}
		info, _ := os.Stat(p)
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("权限 = %o，期望 600", perm)
		}
	})

	t.Run("权限已正确时不改动", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := EnsureConfigPermissions(p); err != nil {
			t.Fatalf("不应报错: %v", err)
		}
		info, _ := os.Stat(p)
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("权限 = %o，应保持 600", perm)
		}
	})
}

func TestInterval(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
server:
  addr: "a:1"
auth:
  uuid: u
  secret: s
collect:
  interval_sec: 15
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Interval().Seconds() != 15 {
		t.Errorf("Interval() = %v，期望 15s", cfg.Interval())
	}
}
