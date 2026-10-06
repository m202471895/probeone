// Command inituser 创建初始 owner 用户。
//
// 为什么需要独立命令而不是靠注册接口：
// PROBEONE_ALLOW_REGISTRATION 默认关闭（防止公网上被随意注册产生账号），
// 首个 owner 只能由部署者在服务器上手动创建。
//
// 密码哈希直接走 internal/auth，与登录路径共用同一份实现——
// 自己另写一套会导致登录永远失败，且这种失败极难排查。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/m202471895/probeone/server/internal/auth"
	"github.com/m202471895/probeone/server/internal/config"
	"github.com/m202471895/probeone/server/internal/migrate"
	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/store"
	"github.com/m202471895/probeone/server/internal/store/postgres"
	"github.com/m202471895/probeone/server/internal/store/sqlite"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envIntOr(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "创建失败: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		driver   = flag.String("driver", envOr("PROBEONE_DB_DRIVER", "sqlite"), "数据库驱动: sqlite | postgres")
		dbPath   = flag.String("db", envOr("PROBEONE_DB_PATH", "./data/probeone.db"), "数据库路径（SQLite）")
		username = flag.String("user", "admin", "用户名")
		password = flag.String("pass", "", "密码（不要用命令行传明文，脚本化时用环境变量）")
		role     = flag.String("role", "owner", "角色: owner/admin/viewer")
	)
	flag.Parse()

	if *password == "" {
		return fmt.Errorf("必须提供密码（-pass 或脚本注入）")
	}
	if len(*password) < 10 {
		return fmt.Errorf("密码至少 10 位")
	}

	ctx := context.Background()

	// 打开库并跑迁移——首次创建用户时表可能还不存在
	cfg := &config.DatabaseConfig{Driver: *driver, Path: *dbPath}
	var opener store.Opener
	dialect := migrate.DialectSQLite

	switch *driver {
	case "sqlite":
		abs, err := filepath.Abs(*dbPath)
		if err != nil {
			return fmt.Errorf("解析数据库路径失败: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
			return fmt.Errorf("创建数据目录失败: %w", err)
		}
		cfg.Path = abs
		opener = sqlite.Open
	case "postgres":
		// PG 的连接参数从环境变量读，不走 flag：
		// DSN 里含密码，进命令行会留在 shell history 与 ps 输出里。
		cfg.DSN = os.Getenv("PROBEONE_DB_DSN")
		cfg.Host = envOr("PROBEONE_DB_HOST", "127.0.0.1")
		cfg.Port = envIntOr("PROBEONE_DB_PORT", 5432)
		cfg.User = envOr("PROBEONE_DB_USER", "probeone")
		cfg.Password = os.Getenv("PROBEONE_DB_PASSWORD")
		cfg.Database = envOr("PROBEONE_DB_NAME", "probeone")
		cfg.SSLMode = envOr("PROBEONE_DB_SSLMODE", "require")
		opener = postgres.Open
		dialect = migrate.DialectPostgres
	default:
		return fmt.Errorf("不支持的驱动: %s", *driver)
	}

	handle, err := opener(cfg)
	if err != nil {
		return fmt.Errorf("打开数据库失败: %w", err)
	}
	defer func() { _ = handle.Close() }()

	m := migrate.NewWithDialect(handle, migrate.Builtin(), ".", dialect)
	if err := m.Up(ctx); err != nil {
		return fmt.Errorf("执行迁移失败: %w", err)
	}

	db, err := store.Open(cfg, opener)
	if err != nil {
		return fmt.Errorf("初始化仓储失败: %w", err)
	}

	// 已存在同名用户时不覆盖——避免脚本重跑把已有密码改掉
	if _, err := db.Users.GetByUsername(ctx, *username); err == nil {
		fmt.Printf("用户 %s 已存在，未改动\n", *username)
		return nil
	}

	r := model.Role(*role)
	if !r.Valid() {
		return fmt.Errorf("非法角色: %s", *role)
	}

	id, err := db.Users.Create(ctx, store.CreateUserInput{
		Username: *username,
		Password: *password,
		Role:     r,
	})
	if err != nil {
		return fmt.Errorf("创建用户失败: %w", err)
	}

	fmt.Printf("已创建用户 id=%d username=%s role=%s\n", id, *username, r)
	// 明文密码不打印——命令行参数会留在 shell history 与 ps 输出里
	fmt.Println("密码已用 argon2id 哈希入库，明文不落盘")
	return nil
}

// 确保 auth 包被引用：密码哈希由它完成，这里只是显式说明依赖来源。
var _ = auth.HashPassword
