// Package sqlite 提供 SQLite 存储实现。
//
// 选型：modernc.org/sqlite（纯 Go，免 CGO）。
// 免 CGO 的意义：可交叉编译、可用 Alpine 基础镜像构建、
// 不需要系统装 gcc，部署环节少一个出错点。
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // 注册 driver

	"github.com/m202471895/probeone/server/internal/config"
)

// 文件权限（PRD T12：数据库文件不可被其他用户读取）
const (
	DirPerm  os.FileMode = 0o700
	FilePerm os.FileMode = 0o600
)

// Open 打开（必要时创建）SQLite 数据库。
func Open(cfg *config.DatabaseConfig) (*sql.DB, error) {
	if cfg.Path == "" {
		return nil, fmt.Errorf("sqlite:数据库路径未配置")
	}

	// 确保目录存在且权限收紧
	dir := filepath.Dir(cfg.Path)
	if err := os.MkdirAll(dir, DirPerm); err != nil {
		return nil, fmt.Errorf("sqlite:创建数据目录失败: %w", err)
	}
	// MkdirAll 不会改已存在目录的权限，需显式收紧
	if err := os.Chmod(dir, DirPerm); err != nil {
		return nil, fmt.Errorf("sqlite:设置数据目录权限失败: %w", err)
	}

	// DSN 参数说明：
	//   _pragma=journal_mode(WAL)  并发读写不互斥，采集写入与面板查询并行
	//   _pragma=busy_timeout(5000) 遇到锁等待 5 秒而不是立刻报错
	//   _pragma=foreign_keys(1)     启用外键约束（SQLite 默认关闭）
	//   _pragma=synchronous(NORMAL) WAL 下兼顾安全与性能
	//   _time_format=sqlite  让驱动把 TIMESTAMP 列直接扫描为 time.Time。
	//     不加这个参数，驱动会把时间当字符串返回，扫描进 time.Time 会报
	//     "unsupported Scan, storing driver.Value type string into type *time.Time"。
	//     写入时同样按 SQLite 原生时间格式存储，排序与比较才有正确语义
	//     （按字符串比较只在 ISO 格式下偶然成立，不可靠）。
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"+
			"&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_time_format=sqlite",
		cfg.Path)

	handle, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite:打开数据库失败: %w", err)
	}

	// 关键：SQLite 不支持并发写，MaxOpenConns 必须为 1。
	// 若放开会导致 "database is locked"。这是 SQLite 模式的固有约束，
	// 需要高并发写入的场景应改用 PostgreSQL。
	handle.SetMaxOpenConns(1)
	handle.SetMaxIdleConns(1)
	handle.SetConnMaxLifetime(0) // SQLite 连接不因超时失效

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := handle.PingContext(ctx); err != nil {
		_ = handle.Close()
		return nil, fmt.Errorf("sqlite:连接测试失败: %w", err)
	}

	// 验证文件权限
	if err := enforceFilePerm(cfg.Path); err != nil {
		_ = handle.Close()
		return nil, err
	}

	return handle, nil
}

// enforceFilePerm 确保数据库文件权限为 0600。
// 数据库里存有 agent_secret 的哈希、登录 token 哈希、通知通道凭据，
// 权限过宽等于把凭据送人。
func enforceFilePerm(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 尚未创建，Ping 成功说明内存库或已建
		}
		return fmt.Errorf("sqlite:检查数据库文件失败: %w", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		if err := os.Chmod(path, FilePerm); err != nil {
			return fmt.Errorf("sqlite:收紧数据库文件权限失败: %w", err)
		}
	}
	return nil
}

// IsSupported 判断配置是否指向 SQLite。
func IsSupported(cfg *config.DatabaseConfig) bool {
	return strings.EqualFold(cfg.Driver, "sqlite")
}
