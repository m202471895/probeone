// Command probeone 是 ProbeOne 服务端入口。
//
// 职责边界：只做装配与生命周期管理，不含业务逻辑。
// 启动顺序：加载配置 → 初始化日志 → 建连接池 → 执行迁移 →
// 启动 HTTP/gRPC → 等待信号 → 优雅停机。
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/m202471895/probeone/server/internal/alert"
	"github.com/m202471895/probeone/server/internal/apperr"
	"github.com/m202471895/probeone/server/internal/collector"
	"github.com/m202471895/probeone/server/internal/config"
	"github.com/m202471895/probeone/server/internal/grpcsvc"
	"github.com/m202471895/probeone/server/internal/httpapi"
	httpa "github.com/m202471895/probeone/server/internal/httpapi/alert"
	httpaudit "github.com/m202471895/probeone/server/internal/httpapi/auditlog"
	httpauth "github.com/m202471895/probeone/server/internal/httpapi/auth"
	httpmonitor "github.com/m202471895/probeone/server/internal/httpapi/monitor"
	httpnode "github.com/m202471895/probeone/server/internal/httpapi/node"
	httpstatus "github.com/m202471895/probeone/server/internal/httpapi/statuspage"
	httpuser "github.com/m202471895/probeone/server/internal/httpapi/user"
	httpvis "github.com/m202471895/probeone/server/internal/httpapi/visibility"
	"github.com/m202471895/probeone/server/internal/logging"
	"github.com/m202471895/probeone/server/internal/migrate"
	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/monitor"
	"github.com/m202471895/probeone/server/internal/notify"
	"github.com/m202471895/probeone/server/internal/store"
	"github.com/m202471895/probeone/server/internal/store/postgres"
	"github.com/m202471895/probeone/server/internal/store/sqlite"
	"github.com/m202471895/probeone/server/internal/util"
	"github.com/m202471895/probeone/server/internal/visibility"
	"github.com/m202471895/probeone/server/internal/webembed"
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

	// ---------- 4. 数据库 ----------
	var opener store.Opener
	switch cfg.Database.Driver {
	case "sqlite":
		opener = sqlite.Open
	case "postgres":
		opener = postgres.Open
	default:
		return fmt.Errorf("不支持的数据库驱动: %s（支持 sqlite / postgres）", cfg.Database.Driver)
	}

	db, err := store.Open(&cfg.Database, opener)
	if err != nil {
		return fmt.Errorf("打开数据库失败: %w", err)
	}
	defer func() {
		if err := db.SQL.Close(); err != nil {
			log.Error("关闭数据库失败", slog.String("error", err.Error()))
		}
	}()

	// 迁移在业务组件之前跑完：所有仓储初始化都假设表已存在
	m := migrate.NewWithDialect(db.SQL, migrate.Builtin(), ".", dialectFor(cfg.Database.Driver),
		migrate.WithLogger(func(f string, a ...any) { log.Debug(f, a...) }))
	if err := m.Up(ctx); err != nil {
		return fmt.Errorf("执行数据库迁移失败: %w", err)
	}
	ver, err := m.Version(ctx)
	if err != nil {
		return fmt.Errorf("读取迁移版本失败: %w", err)
	}
	log.Info("数据库迁移完成", slog.Int64("version", ver))

	// ---------- 5. 业务组件 ----------
	// 脱敏引擎要在任何 HTTP 组件之前就绪：
	// 它是状态页的最后一道防线，缺了它公开接口会直接吐原始数据。
	visEngine, err := visibility.New(policyStoreAdapter{db.Visibility})
	if err != nil {
		return fmt.Errorf("加载可见性策略失败: %w", err)
	}
	log.Info("脱敏引擎已就绪", slog.Int64("version", visEngine.Version()))

	// 通知通道。Registry 只存 Sender，按类型分发时按需取——
	// 通道配置存在数据库里，引擎派发时现查，避免这里再做一份内存副本。
	registry := notify.NewRegistry(&http.Client{Timeout: 15 * time.Second})

	alertCfg := alert.Config{
		DedupWindow:    cfg.Alert.DedupWindow,
		StormThreshold: cfg.Alert.StormThreshold,
		StormSilence:   cfg.Alert.StormSilence,
	}
	alertEngine := alert.New(alertCfg, db, log, registry)

	// ---------- 6. gRPC（Agent 接入） ----------
	grpcSvc := grpcsvc.New(cfg, db, log)
	grpcSrv := grpcsvc.NewServer(cfg, grpcSvc, log)
	grpcAddr := fmt.Sprintf(":%d", cfg.Server.GRPCPort)
	go func() {
		log.Info("gRPC 服务已启动", slog.String("addr", grpcAddr))
		if err := grpcSrv.Serve(ctx, log); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("gRPC 服务异常退出", slog.String("error", err.Error()))
		}
	}()

	// ---------- 6.5 后台任务 ----------
	// 调度器启动了 7 个周期任务：网站探测、证书检查、节点离线判定、
	// 数据预聚合、过期数据清理、过期会话清理、告警静默汇总。
	//
	// 用 ctx 绑生命周期而不是 Stop()：ctx 取消时所有任务一起退出，
	// 停机流程只剩一步，不用维护第二套停止机制。
	scheduler := collector.New(cfg, db, log, alertEngine)
	schedDone := make(chan struct{})
	go func() {
		defer close(schedDone)
		scheduler.Run(ctx)
	}()
	log.Info("后台任务已启动",
		slog.Duration("探测间隔", cfg.Collector.WebInterval),
		slog.Duration("离线判定间隔", cfg.Collector.OfflineGrace/2),
		slog.Duration("预聚合间隔", cfg.Collector.RollupInterval))

	// ---------- 7. HTTP ----------
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Server.HTTPPort),
		Handler:           buildHandler(log, cfg, db, visEngine, registry, alertEngine),
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

	// ---------- 8. 等待退出信号 ----------
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("收到停机信号，开始优雅停机")
	}

	// ---------- 9. 优雅停机 ----------
	// 顺序：先停 HTTP（不再接新请求），再停 gRPC（处理完存量上报），最后关库
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("HTTP 优雅停机失败: %w", err)
	}

	// 等后台任务收尾。它们都是短周期的（最长的证书检查也只是每轮结束就返回），
	// 15 秒足够全部退出。超时则记日志继续——
	// 不能因为后台任务不肯退就卡住整个停机。
	select {
	case <-schedDone:
		log.Info("后台任务已全部退出")
	case <-shutdownCtx.Done():
		log.Warn("后台任务未在超时内退出，强制结束")
	}
	// 告警引擎没有后台循环（Evaluate 由采集器调用，
	// FlushSummaries 需要外部定时触发），因此无需显式停止。
	// 这点要写清楚，否则后来者会以为漏了。
	log.Info("已安全退出")
	return nil
}

func dialectFor(driver string) migrate.Dialect {
	if driver == "postgres" {
		return migrate.DialectPostgres
	}
	return migrate.DialectSQLite
}

// hasTrustProxy 判断是否配置了可信代理。
func hasTrustProxy(cfg *config.Config) bool {
	nets, _ := cfg.TrustProxyCIDRs()
	return len(nets) > 0
}

// policyStoreAdapter 适配 store.VisibilityRepository 到 visibility.PolicyStore。
//
// 两者方法签名不同：前者带 context（为了走ctx 超时与取消），
// 后者不带（脱敏是纯内存操作，不需要）。这里做一层薄适配，
// 而不是反过来改 visibility 的接口——那会让热更新路径也带上无用的 ctx。
type policyStoreAdapter struct {
	repo store.VisibilityRepository
}

func (a policyStoreAdapter) Policies(scope model.VisibilityScope) (
	[]model.VisibilityPolicy, error) {
	return a.repo.Policies(context.Background(), scope)
}

// buildHandler 组装 HTTP 路由与中间件。
//
// 中间件顺序（不可调换）：
//
//	Recovery   —— 最外层，panic 兜底
//	AccessLog —— 记录访问日志，需包在 Recovery 内才能记录到 panic
//	Security   —— 安全头
//	Visibility —— 兜底扫描（万一某个新接口忘了接脱敏，这里拦住）
func buildHandler(
	log *slog.Logger,
	cfg *config.Config,
	db *store.DB,
	visEngine *visibility.Engine,
	registry *notify.Registry,
	alertEngine *alert.Engine,
) http.Handler {
	mux := http.NewServeMux()
	// TrustProxyCIDRs 是方法。配了任何可信代理才采信 X-Forwarded-For，
	// 否则任何人伪造该头就能污染审计日志。
	_, invalidProxies := cfg.TrustProxyCIDRs()
	trustProxy := len(invalidProxies) == 0 && hasTrustProxy(cfg)
	// 会话有效期。SecurityConfig 里没这个字段（PRD 有列但未实现），
	// 这里给一个保守值：7 天。过期后会话自动失效，
	// 长期令牌的风险高于短期的使用摩擦。
	const sessionTTL = 7 * 24 * time.Hour

	authn := httpapi.NewAuthenticator(db)

	// ---------- 健康检查（无需鉴权） ----------
	// /healthz 是存活探针：进程还在就返回 200
	// /ready  是就绪探针：数据库不通就不该被加进负载均衡
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		if err := db.SQL.PingContext(r.Context()); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			// 不返回 err.Error()：可能含连接串细节
			_, _ = w.Write([]byte(`{"status":"unavailable"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		httpapi.OK(w, r, map[string]any{"version": util.Version})
	})

	// ---------- 业务路由 ----------
	httpauth.New(db, trustProxy, sessionTTL).Register(mux, authn)
	// 节点接口要生成安装命令，因此需要知道 gRPC 端口——
	// 安装脚本用--server 连 Agent，走的是 gRPC 而不是 HTTP。
	nodeHandler := httpnode.New(db, trustProxy, cfg.Server.PublicURL)
	nodeHandler.SetGRPCAddr(grpcAdvertiseAddr(cfg))
	nodeHandler.Register(mux, authn)
	// 探针与告警引擎是两条独立的线：
	// 探针负责"实际发请求测量"，引擎负责"拿样本跑规则发通知"。
	// "立即检查"只需要前者，不必等后者。
	monitorHandler := httpmonitor.New(db, trustProxy, alertEngine)
	monitorHandler.SetProber(monitor.New(cfg.Storage.AllowInternalTargets), log)
	// 同一开关也要喂给 handler 本身：创建监控时的 SSRF 校验
	// 与实际探测必须用同一个allowPrivate，
	// 否则会出现"能创建但探测被拒"或反过来的不一致。
	monitorHandler.SetAllowInternalTargets(cfg.Storage.AllowInternalTargets)
	monitorHandler.Register(mux, authn)
	httpa.New(db, trustProxy, registry).Register(mux, authn)
	httpaudit.New(db).Register(mux, authn)
	httpuser.New(db, trustProxy).Register(mux, authn)
	httpvis.New(db, visEngine, trustProxy).Register(mux, authn)
	httpstatus.New(db, visEngine, trustProxy).Register(mux, authn)

	// ---------- 前端静态资源 ----------
	mountWebUI(mux)

	// ---------- 中间件 ----------
	var h http.Handler = mux
	h = httpapi.SecurityHeaders(h)
	h = httpapi.AccessLog(log)(h)
	h = httpapi.Recovery(log)(h)
	return h
}

// mountWebUI 挂载内嵌的前端产物。
//
// 必须做 SPA 回退：前端用 history 路由（/nodes、/alerts等），
// 用户在 /nodes 按刷新时请求的是 /nodes 这个路径，
// 而它对应的文件不存在。没有回退就会 404——
// 表现是"点导航正常，刷新就白屏"。
//
// 回退的边界：只对**看起来像前端路由**的路径回退。
// /api/xxx 找不到必须返回 404 JSON 而不是 index.html，
// 否则前端会把 HTML 当 JSON 解析，错误信息变得莫名其妙。
func mountWebUI(mux *http.ServeMux) {
	if !webembed.Available() {
		// 编译时没带前端产物。给出明确提示，
		// 而不是让人对着 404 猜是不是部署漏了东西。
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			httpapi.Fail(w, r, apperr.New("WEB_UI_MISSING",
				"前端资源未内嵌，请先执行 scripts/sync-web.sh 后重新编译", 500))
		})
		return
	}

	webFS, err := webembed.FS()
	if err != nil {
		log.Printf("挂载前端失败: %v", err)
		return
	}
	fileServer := http.FileServer(http.FS(webFS))

	// 静态资源挂在 /assets/ 下（Vite 的默认产物目录）。
	// 直接命中真实文件，不走 SPA 回退。
	mux.Handle("GET /assets/", fileServer)

	/*
	 * Agent 安装脚本。
	 *
	 * 单独路由而不是丢给 fileServer：
	 * http.FileServer 靠扩展名推断 Content-Type，
	 * .sh 会被当成纯文本甚至 application/octet-stream。
	 * 浏览器/curl 一般不 care，但明确给 text/x-shellscript 更稳。
	 *
	 * Content-Disposition 用 attachment 让浏览器下载而不是显示——
	 * 用户误点链接时不会看到一堆脚本内容。
	 */
	mux.HandleFunc("GET /install.sh", func(w http.ResponseWriter, r *http.Request) {
		f, err := webFS.Open("install.sh")
		if err != nil {
			httpapi.Fail(w, r, apperr.NotFound("INSTALL_SCRIPT_MISSING",
				"安装脚本未包含在当前构建中"))
			return
		}
		defer func() { _ = f.Close() }()
		w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="install.sh"`)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = io.Copy(w, f)
	})

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		// 根路径与 index.html 都直接给文件
		if r.URL.Path == "/" {
			index, err := webFS.Open("index.html")
			if err == nil {
				defer func() { _ = index.Close() }()
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = io.Copy(w, index)
				return
			}
		}

		// API 路径绝不回退到 index.html：
		// 前端会把 HTML 当 JSON 解析，报错变得难以定位。
		if strings.HasPrefix(r.URL.Path, "/api/") {
			httpapi.Fail(w, r, apperr.NotFound("NOT_FOUND", "接口不存在"))
			return
		}

		// 真实文件存在就直接给（favicon.ico、robots.txt 等）
		if f, err := webFS.Open(strings.TrimPrefix(r.URL.Path, "/")); err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}

		// 其余一律当前端路由，回退到 index.html
		index, err := webFS.Open("index.html")
		if err != nil {
			httpapi.Fail(w, r, apperr.Internal(err, "前端资源不可用"))
			return
		}
		defer func() { _ = index.Close() }()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.Copy(w, index)
	})
}

// grpcAdvertiseAddr 生成 Agent 应连接的 gRPC 地址（host:port）。
//
// 为什么要单独算：Agent 连的是 gRPC 端口（默认 8008），
// 而面板地址是 HTTP 端口（8000）。两者的主机名相同、端口不同。
// 直接复用 PUBLIC_URL 会让Agent 连到 HTTP 端口上去——
// 握手会失败，而报错发生在 Agent 侧，难定位。
func grpcAdvertiseAddr(cfg *config.Config) string {
	host := hostFromPublicURL(cfg.Server.PublicURL)
	if host == "" {
		return ""
	}
	/*
	 * 总是用 GRPCPort 覆盖端口部分。
	 *
	 * 之前写成"PUBLIC_URL 里已有端口就不改"，结果 PUBLIC_URL 的
	 * 8000（HTTP 端口）被当成 gRPC 端口用了，Agent 连上去握手失败。
	 *
	 * 只保留主机名——主机名在两种协议下是同一个，
	 * 而端口必须是各自服务实际监听的。
	 */
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	// IPv6 字面量必须加方括号，否则 JoinHostPort 产出非法地址
	return net.JoinHostPort(name, strconv.Itoa(cfg.Server.GRPCPort))
}

// hostFromPublicURL 从公开 URL 提取 host:port。
//
// 拼在安装命令里给用户看，写错会导致一键安装直接失败。
func hostFromPublicURL(raw string) string {
	if raw == "" {
		return ""
	}
	s := raw
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimPrefix(s, prefix)
			break
		}
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return s
}
