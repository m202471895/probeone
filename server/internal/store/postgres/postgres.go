// Package postgres 提供 PostgreSQL 存储实现。
//
// 选型：pgx/v5（PG 官方 Go 驱动，非 CGO）。
// 与 SQLite 选 modernc 的理由一致——免 CGO，便于交叉编译与Alpine 镜像构建。
//
// 与 SQLite 版的差异集中在三处，其余仓储代码通过 sqlbase.Dialector 抹平：
//  1. 占位符：? → $1, $2（由 pgx 的 Rebind 处理）
//  2. 布尔与时间的读写类型
//  3. DSN 格式与连接池参数
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // 注册 "pgx" driver

	"github.com/m202471895/probeone/server/internal/config"
)

// driverName 是 pgx stdlib 注册的驱动名。
const driverName = "pgx"

// Open 打开 PostgreSQL 连接池。
func Open(cfg *config.DatabaseConfig) (*sql.DB, error) {
	dsn, err := buildDSN(cfg)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres:打开连接失败: %w", err)
	}

	// 启动即探活：连不上要在启动阶段失败，
	// 而不是等第一个请求打进来才暴露。迁移依赖表已存在，
	// 数据库不可用时后续所有操作都会失败。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres:连接测试失败: %w", err)
	}

	applyPoolSettings(db, cfg)
	return db, nil
}

// buildDSN 组装连接串。
//
// 允许直接给 DSN，也支持由 host/port/user/password/dbname 拼装——
// 两种方式在部署时都常见（容器环境常给完整 DSN，
// 裸机部署更容易给离散字段）。
func buildDSN(cfg *config.DatabaseConfig) (string, error) {
	if cfg.DSN != "" {
		// 显式 DSN 优先，不做二次加工——用户可能带了 sslmode 等参数
		return cfg.DSN, nil
	}
	if cfg.Host == "" {
		return "", fmt.Errorf("postgres:未配置 DSN 或 host")
	}

	u := &url.URL{
		Scheme: "postgres",
		Host:   withDefaultPort(cfg.Host, cfg.Port),
		Path:   "/" + cfg.Database,
	}
	if cfg.User != "" {
		if cfg.Password != "" {
			u.User = url.UserPassword(cfg.User, cfg.Password)
		} else {
			u.User = url.User(cfg.User)
		}
	}
	// sslmode 缺省为 require：远程连接明文传密码风险太高。
	// 本地/容器内网可显式设 disable。
	if !hasSSLMode(u.RawQuery) {
		mode := cfg.SSLMode
		if mode == "" {
			mode = "require"
		}
		q := u.Query()
		q.Set("sslmode", mode)
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

// withDefaultPort 拼出 host:port。port 非正数时只返回 host，
// 让 PG 走默认端口 5432。
func withDefaultPort(host string, port int) string {
	if port > 0 {
		return fmt.Sprintf("%s:%d", host, port)
	}
	return host
}

func hasSSLMode(raw string) bool {
	return strings.Contains(raw, "sslmode=")
}

// applyPoolSettings 设置连接池参数。
func applyPoolSettings(db *sql.DB, cfg *config.DatabaseConfig) {
	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 25
	}
	maxIdle := cfg.MaxIdleConns
	if maxIdle <= 0 || maxIdle > maxOpen {
		maxIdle = maxOpen / 5
	}
	lifetime := cfg.ConnMaxLifetime
	if lifetime <= 0 {
		lifetime = time.Hour
	}

	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(lifetime)
	// PG 的 WAL 清理与负载均衡都建议限制连接寿命，
	// 让过期连接被回收而不是长期占用。
	db.SetConnMaxIdleTime(5 * time.Minute)
}
