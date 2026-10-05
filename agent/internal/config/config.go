// Package config 实现 Agent 的配置加载。
//
// Agent 的安全约束（PRD 9.2）：
//   - 不写入除自身配置目录与日志之外的任何文件
//   - 不监听任何端口
//   - 不读取监控无关的文件
// 因此配置项刻意做得很小：Agent 只需要知道"连哪、拿什么身份、采什么、多久一次"。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是 Agent 的完整配置。
type Config struct {
	Server  Server  `yaml:"server"`
	Auth    Auth    `yaml:"auth"`
	Collect Collect `yaml:"collect"`
	Network Network `yaml:"network"`
	Buffer  Buffer  `yaml:"buffer"`
	Log     Log     `yaml:"log"`
}

type Server struct {
	Addr                   string `yaml:"addr"`
	CAFile                 string `yaml:"ca_file"`
	CertFile               string `yaml:"cert_file"`
	KeyFile                string `yaml:"key_file"`
	ServerCertFingerprint  string `yaml:"server_cert_fingerprint"`
	InsecureSkipVerify     bool   `yaml:"insecure_skip_verify"`
}

type Auth struct {
	UUID   string `yaml:"uuid"`
	Secret string `yaml:"secret"`
}

type Collect struct {
	IntervalSec int      `yaml:"interval_sec"`
	Metrics     []string `yaml:"metrics"`
	Privileged  []string `yaml:"privileged"`
}

type Network struct {
	DialTimeoutSec     int    `yaml:"dial_timeout_sec"`
	KeepaliveSec       int    `yaml:"keepalive_sec"`
	PublicIPLookup     string `yaml:"public_ip_lookup"`
}

type Buffer struct {
	Enabled   bool `yaml:"enabled"`
	MaxPoints int  `yaml:"max_points"`
}

type Log struct {
	Level      string `yaml:"level"`
	File       string `yaml:"file"`
	MaxSizeMB  int    `yaml:"max_size_mb"`
	MaxBackups int    `yaml:"max_backups"`
}

// 默认值。
const (
	DefaultIntervalSec  = 10
	DefaultDialTimeout  = 10
	DefaultKeepalive    = 30
	DefaultLogSizeMB    = 20
	DefaultLogBackups   = 3
	DefaultBufferPoints = 120
	// 配置文件权限：仅属主可读写
	ConfigFileMode os.FileMode = 0o600
)

// Load 从文件加载配置。
// 找不到配置文件时返回错误——Agent 不猜测服务端地址，
// 猜错连到别的面板比连不上更危险。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件 %s 失败: %w", path, err)
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// applyDefaults 填充默认值。
//
// 注意：这里必须用 == 0 而不是 <= 0。
// 若用 <= 0，用户写的 -1 或 99999 会被悄悄改写成默认值，
// Validate 的范围检查永远看不到错误输入，"温柔的失败"比直接报错危险得多。
func (c *Config) applyDefaults() {
	if c.Collect.IntervalSec == 0 {
		c.Collect.IntervalSec = DefaultIntervalSec
	}
	if c.Network.DialTimeoutSec <= 0 {
		c.Network.DialTimeoutSec = DefaultDialTimeout
	}
	if c.Network.KeepaliveSec <= 0 {
		c.Network.KeepaliveSec = DefaultKeepalive
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Log.MaxSizeMB <= 0 {
		c.Log.MaxSizeMB = DefaultLogSizeMB
	}
	if c.Log.MaxBackups <= 0 {
		c.Log.MaxBackups = DefaultLogBackups
	}
	if c.Buffer.MaxPoints <= 0 {
		c.Buffer.MaxPoints = DefaultBufferPoints
	}
	// metrics 未配置时采集全部非特权指标
	if len(c.Collect.Metrics) == 0 {
		c.Collect.Metrics = []string{"cpu", "mem", "disk", "net", "load", "uptime"}
	}
}

// Validate 校验配置。
func (c *Config) Validate() error {
	var errs []string
	push := func(format string, args ...any) {
		errs = append(errs, fmt.Sprintf(format, args...))
	}

	if strings.TrimSpace(c.Server.Addr) == "" {
		push("server.addr 未配置：Agent 必须明确知道连接哪个服务端")
	}
	if strings.TrimSpace(c.Auth.UUID) == "" {
		push("auth.uuid 未配置")
	}
	if strings.TrimSpace(c.Auth.Secret) == "" {
		push("auth.secret 未配置")
	}
	if c.Collect.IntervalSec < 1 || c.Collect.IntervalSec > 3600 {
		push("collect.interval_sec = %d 超出合理范围（1-3600）", c.Collect.IntervalSec)
	}

	// 生产环境不得关闭证书校验（PRD 9.2 A6）
	if c.Server.InsecureSkipVerify {
		push("server.insecure_skip_verify 不应在生产环境开启：它会让人流量劫持变得不可检测")
	}

	// 证书文件必须成对
	if (c.Server.CertFile == "") != (c.Server.KeyFile == "") {
		push("server.cert_file 与 server.key_file 必须同时设置")
	}
	// 自定义 CA 与证书 pin 同时配置是冗余的，容易让人困惑
	if c.Server.CAFile != "" && c.Server.ServerCertFingerprint != "" {
		push("server.ca_file 与 server.server_cert_fingerprint 同时配置是冗余的，二选一即可")
	}

	if len(errs) > 0 {
		return fmt.Errorf("Agent 配置校验失败：\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// Interval 返回采集间隔。
func (c *Config) Interval() time.Duration {
	return time.Duration(c.Collect.IntervalSec) * time.Second
}

// Metrics 返回启用的指标项。
func (c *Config) Metrics() []string { return c.Collect.Metrics }

// WantsMetric 判断某指标是否启用。
func (c *Config) WantsMetric(name string) bool {
	for _, m := range c.Collect.Metrics {
		if m == name {
			return true
		}
	}
	return false
}

// IsPrivileged 判断某指标是否需要特权。
// 非特权模式下跳过这些指标，而不是失败——保证低权用户也能跑。
func (c *Config) IsPrivileged(name string) bool {
	for _, p := range c.Collect.Privileged {
		if p == name {
			return true
		}
	}
	return false
}

// EnsureConfigPermissions 确保配置文件权限为 0600。
// Agent 可能读取含密钥的配置，权限过宽会泄露（PRD 9.2 A6）。
func EnsureConfigPermissions(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	mode := info.Mode().Perm()
	if mode&0o077 != 0 {
		return os.Chmod(path, ConfigFileMode)
	}
	return nil
}

// ResolvePath 相对路径转绝对路径。
func ResolvePath(base, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}
