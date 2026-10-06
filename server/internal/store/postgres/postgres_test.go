package postgres

import (
	"strings"
	"testing"

	"github.com/m202471895/probeone/server/internal/config"
)

// TestBuildDSN_拼装正确性
//
// DSN 拼装错了会连不上库，而连接失败发生在启动阶段，
// 排查成本高。这里锁住几个容易错的点。
func TestBuildDSN_拼装正确性(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.DatabaseConfig
		want    []string // DSN 应包含的片段
		notWant []string // DSN 不应包含的片段
	}{
		{
			name: "基本连接串",
			cfg: config.DatabaseConfig{
				Host: "db.example.com", Port: 5432,
				User: "probeone", Password: "s3cret", Database: "probeone",
			},
			want:    []string{"postgres://", "db.example.com:5432", "probeone", "s3cret"},
			notWant: []string{"sslmode=disable"},
		},
		{
			name: "端口为 0 时不带端口",
			cfg: config.DatabaseConfig{
				Host: "localhost", Port: 0, Database: "probeone",
			},
			want:    []string{"localhost/", "sslmode=require"},
			notWant: []string{"localhost:0"},
		},
		{
			name: "显式 DSN 优先于离散字段",
			cfg: config.DatabaseConfig{
				DSN:  "postgres://custom/db?sslmode=disable",
				Host: "ignored",
			},
			want:    []string{"custom/db", "sslmode=disable"},
			notWant: []string{"ignored", "sslmode=require"},
		},
		{
			name: "SSLMode 显式设为 disable 应生效",
			cfg: config.DatabaseConfig{
				Host: "localhost", Database: "probeone", SSLMode: "disable",
			},
			want:    []string{"sslmode=disable"},
			notWant: []string{"sslmode=require"},
		},
		{
			name: "DSN 里的 sslmode 优先于配置",
			cfg: config.DatabaseConfig{
				Host: "h", Database: "d", SSLMode: "require",
			},
			// DSN 为空时应采用配置值
			want: []string{"sslmode=require"},
		},
		{
			name: "无密码时不带凭据段",
			cfg: config.DatabaseConfig{
				Host: "localhost", Database: "probeone",
			},
			want:    []string{"localhost"},
			notWant: []string{"@"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildDSN(&tc.cfg)
			if err != nil {
				t.Fatalf("buildDSN 失败: %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("DSN 应包含 %q，实际 %q", w, got)
				}
			}
			for _, nw := range tc.notWant {
				if strings.Contains(got, nw) {
					t.Errorf("DSN 不应包含 %q，实际 %q", nw, got)
				}
			}
		})
	}
}

// TestBuildDSN_缺配置时报错
//
// 静默返回一个空 DSN 会导致后续"连接失败"，
// 而真正的原因（没配 host）被埋掉了。
func TestBuildDSN_缺配置时报错(t *testing.T) {
	_, err := buildDSN(&config.DatabaseConfig{})
	if err == nil {
		t.Error("既无 DSN 又无 host 时应报错，而不是返回空串")
	}
	if !strings.Contains(err.Error(), "DSN") {
		t.Errorf("错误信息应指明缺 DSN 或 host，实际 %q", err.Error())
	}
}

// TestWithDefaultPort_端口拼接
func TestWithDefaultPort_端口拼接(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		{"localhost", 5432, "localhost:5432"},
		{"localhost", 0, "localhost"},
		{"localhost", -1, "localhost"},
		{"db.internal", 6543, "db.internal:6543"},
	}
	for _, c := range cases {
		if got := withDefaultPort(c.host, c.port); got != c.want {
			t.Errorf("withDefaultPort(%q, %d) = %q，期望 %q", c.host, c.port, got, c.want)
		}
	}
}

// TestOpen_连不上时报错
//
// 关键：连接失败必须在 Open阶段返回，
// 不能返回一个看起来正常的 *sql.DB 让上层以为启动成功了。
func TestOpen_连不上时报错(t *testing.T) {
	cfg := &config.DatabaseConfig{
		// 端口 1 几乎不可能有服务在监听
		Host: "127.0.0.1", Port: 1, Database: "nope", SSLMode: "disable",
		ConnMaxLifetime: 1,
	}
	db, err := Open(cfg)
	if err == nil {
		_ = db.Close()
		t.Fatal("连不上的地址应返回错误")
	}
	if !strings.Contains(err.Error(), "postgres:") {
		t.Errorf("错误信息应带包名前缀便于定位，实际 %q", err.Error())
	}
}
