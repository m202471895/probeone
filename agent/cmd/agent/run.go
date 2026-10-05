package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/m202471895/probeone/agent/internal/config"
)

// run 是 Agent 的主流程。
//
// P0 阶段只完成配置加载与校验，确保验收项（可编译、配置正确、无危险调用）达标。
// P2 会在这里接入采集循环与 gRPC 上报。
func run(configPath string, checkOnly bool) error {
	// 配置权限：含密钥的配置文件必须是 0600
	if err := config.EnsureConfigPermissions(configPath); err != nil {
		return fmt.Errorf("配置文件 %s 不可读: %w", configPath, err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	log := newLogger(cfg.Log.Level)
	slog.SetDefault(log)

	log.Info("ProbeOne Agent 启动中",
		slog.String("version", Version),
		slog.String("server", cfg.Server.Addr),
		slog.String("uuid", cfg.Auth.UUID),
		slog.String("secret", maskSecret(cfg.Auth.Secret)),
		slog.Int("interval_sec", cfg.Collect.IntervalSec),
		slog.Any("metrics", cfg.Metrics()),
	)

	if checkOnly {
		fmt.Println("配置校验通过")
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// P2 将在此启动采集循环
	<-ctx.Done()
	log.Info("收到停机信号，Agent 退出")
	return nil
}

// newLogger 创建结构化日志。
// Agent 侧同样用 JSON，便于服务端或日志系统统一解析。
func newLogger(level string) *slog.Logger {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn", "warning":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}

// maskSecret 掩码密钥，避免日志泄露。
func maskSecret(s string) string {
	if len(s) <= 8 {
		return "***"
	}
	return s[:2] + "***" + s[len(s)-2:]
}
