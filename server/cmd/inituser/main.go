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

	"github.com/m202471895/probeone/server/internal/auth"
	"github.com/m202471895/probeone/server/internal/config"
	"github.com/m202471895/probeone/server/internal/migrate"
	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/store"
	"github.com/m202471895/probeone/server/internal/store/sqlite"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "创建失败: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		dbPath   = flag.String("db", "./data/probeone.db", "数据库路径")
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
	abs, err := filepath.Abs(*dbPath)
	if err != nil {
		return fmt.Errorf("解析数据库路径失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return fmt.Errorf("创建数据目录失败: %w", err)
	}

	cfg := &config.DatabaseConfig{Driver: "sqlite", Path: abs}
	handle, err := sqlite.Open(cfg)
	if err != nil {
		return fmt.Errorf("打开数据库失败: %w", err)
	}
	defer func() { _ = handle.Close() }()

	m := migrate.New(handle, migrate.Builtin(), ".")
	if err := m.Up(ctx); err != nil {
		return fmt.Errorf("执行迁移失败: %w", err)
	}

	db, err := store.Open(cfg, sqlite.Open)
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
