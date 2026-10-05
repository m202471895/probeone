// Command probeone 是 ProbeOne 服务端入口。
//
// 职责边界：只做装配与生命周期管理，不含业务逻辑。
// 启动顺序：加载配置 → 初始化日志 → 建连接池 → 执行迁移 → 启动 HTTP/gRPC → 等待信号 → 优雅停机。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/m202471895/probeone/server/internal/config"
	"github.com/m202471895/probeone/server/internal/httpapi"
	"github.com/m202471895/probeone/server/internal/logging"
	"github.com/m202471895/probeone/server/internal/util"
)

func main() {
	if err := run(); err != nil {
		// 此时日志系统可能尚未就绪，直接写 stderr
		fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// ---------- 1. 配置 ----------
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// ---------- 2. 日志 ----------
	log := logging.New(cfg.Log.Level, cfg.Log.Format)
	slog.SetDefault(log)

	log.Info("ProbeOne 启动中",
		slog.String("version", util.Version),
		slog.String("db_driver", cfg.Database.Driver),
		slog.Int("http_port", cfg.Server.HTTPPort),
		slog.Int("grpc_port", cfg.Server.GRPCPort),
	)

	// 信任反代配置告警（PRD 13 章：配错的后果都很糟）
	_, invalid := cfg.TrustProxyCIDRs()
	for _, item := range invalid {
		log.Warn("PROBEONE_TRUST_PROXY 含无法解析的条目，已忽略",
			slog.String("value", item))
	}

	// ---------- 3. 优雅停机上下文 ----------
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ---------- 4. HTTP 服务 ----------
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Server.HTTPPort),
		Handler:           buildHandler(log, cfg),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("HTTP 服务已启动", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("HTTP 服务异常退出: %w", err)
			return
		}
		errCh <- nil
	}()

	// ---------- 5. 等待退出信号 ----------
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("收到停机信号，开始优雅停机")
	}

	// ---------- 6. 优雅停机 ----------
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("优雅停机失败: %w", err)
	}
	log.Info("已安全退出")
	return nil
}

// buildHandler 组装 HTTP 路由。
//
// 中间件顺序（不可调换）：
//
//	Recovery   ——最外层，panic 兜底
//	Slog       ——记录访问日志，需包在 Recovery 内才能记录到 panic
//	Security   ——安全头
//	Visibility ——脱敏（含兜底扫描），必须在所有 handler 之前
//	路由
func buildHandler(log *slog.Logger, cfg *config.Config) http.Handler {
	mux := http.NewServeMux()

	// 健康检查：无需鉴权。/healthz 是存活探针，/ready 是就绪探针
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// API 路由在后续 Phase 挂载
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"version":"` + util.Version + `"}}`))
	})

	var h http.Handler = mux
	h = httpapi.SecurityHeaders(h)
	h = httpapi.AccessLog(log)(h)
	h = httpapi.Recovery(log)(h)
	return h
}
