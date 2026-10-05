// Package store 提供存储层入口。
//
// 分层约定（PRD 第 5 章）：Repository 接口定义在 store 包，
// 由 sqlite / postgres 两个子包分别实现。上层服务只依赖接口，
// 不感知具体数据库——这样从 SQLite 切到 PostgreSQL 只换一行构造代码。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/m202471895/probeone/server/internal/config"
	"github.com/m202471895/probeone/server/internal/store/sqlbase"
)

// DB 是所有 Repository 的组合根。
// 上层通过它拿到各领域仓储，自己不直接碰 sql.DB。
type DB struct {
	// 句柄。保留导出是因为健康检查与备份需要访问，
	// 但业务代码必须走 Repository，不得直接 Query。
	SQL *sql.DB

	Users         UserRepository
	Sessions      SessionRepository
	Nodes         NodeRepository
	Metrics       MetricRepository
	Monitors      MonitorRepository
	Alerts        AlertRepository
	Channels      ChannelRepository
	Visibility    VisibilityRepository
	Audit         AuditRepository
	Settings      SettingsRepository
	AgentSessions AgentSessionRepository
}

// Opener 是打开数据库连接的函数签名，由 sqlite / postgres 子包实现。
type Opener func(cfg *config.DatabaseConfig) (*sql.DB, error)

// Open 按配置打开数据库并组装所有 Repository。
func Open(cfg *config.DatabaseConfig, opener Opener) (*DB, error) {
	handle, err := opener(cfg)
	if err != nil {
		return nil, err
	}
	d := sqlbase.DialectSQLite
	if cfg.Driver == "postgres" {
		d = sqlbase.DialectPostgres
	}
	return NewWithDialect(handle, d), nil
}

// New 用已有句柄组装 Repository。
// 迁移已经执行过，或调用方想注入测试用的 mock 句柄时使用。
func New(handle *sql.DB) *DB {
	return NewWithDialect(handle, sqlbase.DialectSQLite)
}

// NewWithDialect 按指定方言组装 Repository。
// 方言决定占位符风格（SQLite 的 ? / PostgreSQL 的 $n）与时间比较写法。
func NewWithDialect(handle *sql.DB, d sqlbase.Dialect) *DB {
	return &DB{
		SQL:           handle,
		Users:         &userRepo{db: handle, d: d},
		Sessions:      &sessionRepo{db: handle, d: d},
		Nodes:         &nodeRepo{db: handle, d: d},
		Metrics:       &metricRepo{db: handle, d: d},
		Monitors:      &monitorRepo{db: handle, d: d},
		Alerts:        &alertRepo{db: handle, d: d},
		Channels:      &channelRepo{db: handle, d: d},
		Visibility:    &visibilityRepo{db: handle, d: d},
		Audit:         &auditRepo{db: handle, d: d},
		AgentSessions: &agentSessionRepo{db: handle, d: d},
		Settings:      &settingsRepo{db: handle, d: d},
	}
}

// Close 关闭连接池。
func (d *DB) Close() error {
	if d == nil || d.SQL == nil {
		return nil
	}
	return d.SQL.Close()
}

// Ping 验证连接可用，供 /ready 探针使用。
func (d *DB) Ping(ctx context.Context) error {
	if d == nil || d.SQL == nil {
		return fmt.Errorf("数据库未初始化")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return d.SQL.PingContext(ctx)
}

// txer 是能执行事务的句柄。*sql.DB 与 *sql.Tx 都满足它，
// 借此让 Repository 方法既能独立执行也能参与外层事务。
type txer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

// InTx 在事务中执行 fn，panic 或 error 时回滚。
//
// 关键设计：fn 收到的是 txer 而非 *DB。仓储方法都基于 txer 实现，
// 因此事务内可以继续调用其他仓储方法，全部落在**同一条连接**上。
//
// 不要用 &DB{SQL: r.db} 造临时 DB 来跑事务。SQLite 下连接池被限制为
// 单连接（MaxOpenConns=1，因为 SQLite 不支持并发写），
// 事务已占住那条连接，事务外的 Exec 会阻塞，
// 表现为 FOREIGN KEY constraint failed 或 database is locked。
func InTx(ctx context.Context, db *sql.DB, fn func(tx txer) error) error {
	if db == nil {
		return fmt.Errorf("数据库未初始化")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p) // 保留原始堆栈，由 recovery 中间件处理
		}
	}()
	if err := fn(tx); err != nil {
		// 忽略回滚失败：原始错误更有诊断价值
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("提交事务失败: %w", err)
	}
	return nil
}
