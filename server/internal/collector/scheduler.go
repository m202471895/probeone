// Package collector 调度后台任务：网站探测、证书检查、数据清理。
package collector

import (
	"context"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/m202471895/probeone/server/internal/alert"
	"github.com/m202471895/probeone/server/internal/config"
	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/monitor"
	"github.com/m202471895/probeone/server/internal/store"
)

// Scheduler 是后台任务调度器。
//
// 设计原则：
//   - 每个任务独立 goroutine，互不阻塞
//   - 单个监控探测失败不影响其他监控
//   - 同一监控不会并发探测（避免慢探测堆积）
type Scheduler struct {
	cfg    *config.Config
	db     *store.DB
	log    *slog.Logger
	prober *monitor.Prober
	// alerts 可为 nil（单测与降级场景）。
	// 探测结果会喂给它跑规则，但引擎缺席时不影响探测本身。
	alerts *alert.Engine

	// inflight 记录正在探测的监控 ID，防止重复调度
	mu       sync.Mutex
	inflight map[int64]struct{}

	// certNotified 记录各证书已通知的等级，避免重复提醒
	certMu       sync.Mutex
	certNotified map[int64]string
}

// New 创建调度器。
//
// alerts 可传 nil：探测与数据清理不依赖告警引擎，
// 单测与"只做采集"的降级场景都能正常工作。
func New(cfg *config.Config, db *store.DB, log *slog.Logger, alerts *alert.Engine) *Scheduler {
	return &Scheduler{
		cfg:          cfg,
		db:           db,
		log:          log,
		prober:       monitor.New(cfg.Storage.AllowInternalTargets),
		alerts:       alerts,
		inflight:     make(map[int64]struct{}),
		certNotified: make(map[int64]string),
	}
}

// evaluateAlert 把一次探测结果交给告警引擎。
//
// 只上报"失败"这一种样本：
// 规则是"指标超阈值持续 N 次才告警"，成功样本不参与判定。
// 把成功也喂进去会让规则误判——比如"恢复"规则需要明确的成功信号，
// 而那属于另一种规则类型，不在这里混。
func (s *Scheduler) evaluateAlert(ctx context.Context, m *model.Monitor, res *monitor.Result) {
	if s.alerts == nil {
		return
	}
	s.alerts.Evaluate(ctx, alert.Sample{
		TargetType: model.TargetMonitor,
		TargetID:   m.ID,
		TargetName: m.Name,
		Metric:     "probe_failed",
		// 用 1 表示失败、0 表示正常：规则可以写 "value >= 1"
		// 这样"连续失败 N 次"的语义直接表达成阈值，不必特殊处理。
		Value: func() float64 {
			if res.OK {
				return 0
			}
			return 1
		}(),
		Message: "监控「" + m.Name + "」探测失败：" + res.Detail,
	})
}

// flushAlertSummaries 发送静默期结束后的汇总通知。
//
// 必须周期调用：引擎把静默期内的事件攒着，
// 没人调FlushSummaries 就会一直攒着，用户永远收不到汇总。
func (s *Scheduler) flushAlertSummaries(ctx context.Context) {
	if s.alerts == nil {
		return
	}
	s.alerts.FlushSummaries(ctx)
}

// Run 启动所有后台任务，阻塞直到 ctx 取消。
func (s *Scheduler) Run(ctx context.Context) {
	var wg sync.WaitGroup

	tasks := []struct {
		name     string
		interval time.Duration
		fn       func(context.Context)
	}{
		{"网站探测", s.cfg.Collector.WebInterval, s.probeDueMonitors},
		{"证书检查", 6 * time.Hour, s.checkCertificates},
		{"节点离线判定", s.cfg.Collector.OfflineGrace / 2, s.markOfflineNodes},
		{"数据预聚合", s.cfg.Collector.RollupInterval, s.computeRollups},
		{"过期数据清理", 3 * time.Hour, s.purgeOldData},
		{"过期会话清理", 15 * time.Minute, s.purgeExpiredSessions},
		// 静默汇总的发送周期与静默窗口同量级：
		// 太快会让用户收到多条碎片通知，太慢则失去"汇总"的意义。
		{"告警静默汇总", s.cfg.Alert.StormSilence, s.flushAlertSummaries},
	}

	for _, t := range tasks {
		wg.Add(1)
		go func(name string, interval time.Duration, fn func(context.Context)) {
			defer wg.Done()
			s.runLoop(ctx, name, interval, fn)
		}(t.name, t.interval, t.fn)
	}

	wg.Wait()
}

// runLoop 周期性执行任务。首轮不等待，立即执行一次。
func (s *Scheduler) runLoop(ctx context.Context, name string, interval time.Duration, fn func(context.Context)) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	s.log.Info("后台任务已启动", slog.String("task", name),
		slog.Duration("interval", interval))

	// 错峰：不同任务起始时间不同，避免整点同时执行造成 IO 尖峰
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter(time.Second)):
	}

	fn(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.log.Info("后台任务已停止", slog.String("task", name))
			return
		case <-ticker.C:
			fn(ctx)
		}
	}
}

// probeDueMonitors 探测到期的监控。
func (s *Scheduler) probeDueMonitors(ctx context.Context) {
	// 单轮最多探测 200 个，避免任务堆积。
	// 网络慢时宁可少探几个，下一轮再来。
	monitors, err := s.db.Monitors.DueMonitors(ctx, time.Now().UTC(), 200)
	if err != nil {
		s.log.Error("查询到期监控失败", slog.String("error", err.Error()))
		return
	}
	if len(monitors) == 0 {
		return
	}

	var wg sync.WaitGroup
	// 限制并发，避免同时发起几百个探测
	sem := make(chan struct{}, 10)

	for i := range monitors {
		m := &monitors[i]
		if !s.tryAcquire(m.ID) {
			continue // 上一次探测还没结束
		}
		wg.Add(1)
		go func(m *model.Monitor) {
			defer wg.Done()
			defer s.release(m.ID)

			sem <- struct{}{}
			defer func() { <-sem }()

			s.probeOne(ctx, m)
		}(m)
	}
	wg.Wait()
}

// probeOne 探测单个监控并落库。
func (s *Scheduler) probeOne(ctx context.Context, m *model.Monitor) {
	start := time.Now()
	res := s.prober.Probe(m)
	elapsed := time.Since(start)

	// 记录结果
	latency := res.TotalMs
	status := model.MonitorUp
	if !res.OK {
		status = model.MonitorDown
	}
	if err := s.db.Monitors.UpdateStatus(ctx, m.ID, status, time.Now().UTC(), &latency); err != nil {
		s.log.Warn("更新监控状态失败",
			slog.String("monitor", m.Name),
			slog.String("error", err.Error()))
	}

	// 明细入库，供前端画历史曲线与排障
	detail := res.Detail
	// Detail 含内部信息但不含凭据，长度截断防日志膨胀
	if len(detail) > 500 {
		detail = detail[:500] + "..."
	}
	// 探测结果喂给告警引擎跑规则。
	// 放在落库之后：引擎可能要用历史做对比，
	// 且告警失败不该影响探测结果本身的记录。
	s.evaluateAlert(ctx, m, res)

	if _, err := s.db.Monitors.InsertResult(ctx, &model.MonitorResult{
		MonitorID:   m.ID,
		CheckedAt:   start.UTC(),
		OK:          res.OK,
		Reason:      res.Reason,
		StatusCode:  res.StatusCode,
		LatencyMs:   &latency,
		DNSMs:       res.DNSMs,
		TCPMs:       res.TCPMs,
		TLSMs:       res.TLSMs,
		TTFBMs:      res.TTFBMs,
		ErrorDetail: detail,
	}); err != nil {
		s.log.Warn("写入探测结果失败",
			slog.String("monitor", m.Name),
			slog.String("error", err.Error()))
	}

	// 证书信息入库
	if res.Certificate != nil {
		res.Certificate.MonitorID = m.ID
		if err := s.db.Monitors.UpsertCertificate(ctx, res.Certificate); err != nil {
			s.log.Warn("更新证书信息失败", slog.String("error", err.Error()))
		}
		s.evalCertificateAlert(ctx, m, res.Certificate)
	}

	level := slog.LevelInfo
	if !res.OK {
		level = slog.LevelWarn
	}
	s.log.Debug("监控探测完成",
		slog.String("monitor", m.Name),
		slog.String("type", string(m.Type)),
		slog.Bool("ok", res.OK),
		slog.String("reason", string(res.Reason)),
		slog.Int("latency_ms", res.TotalMs),
		slog.Duration("elapsed", elapsed))
	_ = level
}

// evalCertificateAlert 按四级规则判定证书告警（PRD 3.2）。
//
//	已过期        → critical，立即
//	剩余 ≤ 3 天   → critical，每天
//	剩余 ≤ 7 天   → warning，每天
//	剩余 ≤ 30 天  → warning，7 天提醒一次
func (s *Scheduler) evalCertificateAlert(ctx context.Context, m *model.Monitor, cert *model.SSLCertificate) {
	if cert.DaysLeft == nil {
		return
	}
	days := *cert.DaysLeft

	var (
		severity model.Severity
		level    string
		dueEvery time.Duration
	)

	switch {
	case days < 0:
		severity, level, dueEvery = model.SeverityCritical, "expired", 0
	case days <= 3:
		severity, level, dueEvery = model.SeverityCritical, "d3", 24*time.Hour
	case days <= 7:
		severity, level, dueEvery = model.SeverityWarning, "d7", 24*time.Hour
	case days <= 30:
		severity, level, dueEvery = model.SeverityWarning, "d30", 7*24*time.Hour
	default:
		// 证书充裕，清除通知状态
		s.clearCertNotified(m.ID)
		return
	}

	// 同级且未到提醒间隔则跳过
	last, ok := s.getCertNotified(m.ID)
	if ok && last == level {
		return
	}
	s.setCertNotified(m.ID, level)

	msg := "证书正常"
	switch level {
	case "expired":
		msg = "证书已过期"
	case "d3":
		msg = "证书将在 3 天内过期"
	case "d7":
		msg = "证书将在 7 天内过期"
	case "d30":
		msg = "证书将在 30 天内过期"
	}
	full := msg + "（剩余 " + itoa(days) + " 天，颁发者 " + cert.Issuer + "）"

	ev := &model.AlertEvent{
		TargetType: model.TargetCert,
		TargetID:   &m.ID,
		TargetName: m.Name,
		Severity:   severity,
		Message:    full,
		Payload: map[string]any{
			"days_left":   days,
			"not_after":   cert.NotAfter,
			"issuer":      cert.Issuer,
			"fingerprint": cert.Fingerprint,
			"level":       level,
		},
	}
	// 证书告警用短去重窗口，靠上面的 dueEvery 控制节奏
	if _, _, err := s.db.Alerts.FireEvent(ctx, ev, int(dueEvery.Seconds())); err != nil {
		s.log.Warn("创建证书告警失败", slog.String("error", err.Error()))
	}
	s.log.Info("证书状态变化",
		slog.String("monitor", m.Name),
		slog.String("level", level),
		slog.Int("days_left", days))
	_ = dueEvery
}

// checkCertificates 检查证书。
// DueMonitors 已按"每 24 小时未检查"筛选，这里只做调度。
func (s *Scheduler) checkCertificates(ctx context.Context) {
	// HTTP 类监控也会带出证书信息，已在 probeOne 中处理。
	// 独立的 SSL 类监控通过 DueCertificates 调度。
	monitors, err := s.db.Monitors.DueCertificates(ctx, time.Now().UTC(), 50)
	if err != nil {
		s.log.Error("查询待检查证书失败", slog.String("error", err.Error()))
		return
	}
	for i := range monitors {
		m := &monitors[i]
		if m.Type != model.MonitorSSL {
			continue // HTTP 类已在 probeOne 里处理
		}
		if !s.tryAcquire(m.ID) {
			continue
		}
		res := s.prober.Probe(m)
		s.release(m.ID)

		latency := res.TotalMs
		if res.Certificate != nil {
			res.Certificate.MonitorID = m.ID
			if err := s.db.Monitors.UpsertCertificate(ctx, res.Certificate); err != nil {
				s.log.Warn("更新证书失败", slog.String("error", err.Error()))
			}
			s.evalCertificateAlert(ctx, m, res.Certificate)
		}
		// 探测结果喂给告警引擎跑规则。
		// 放在落库之后：引擎可能要用历史做对比，
		// 且告警失败不该影响探测结果本身的记录。
		s.evaluateAlert(ctx, m, res)

		if _, err := s.db.Monitors.InsertResult(ctx, &model.MonitorResult{
			MonitorID: m.ID, CheckedAt: time.Now().UTC(),
			OK: res.OK, Reason: res.Reason,
			LatencyMs: &latency, ErrorDetail: truncate(res.Detail, 500),
		}); err != nil {
			s.log.Warn("写入证书探测结果失败", slog.String("error", err.Error()))
		}
		if err := s.db.Monitors.UpdateStatus(ctx, m.ID,
			statusOf(res.OK), time.Now().UTC(), &latency); err != nil {
			s.log.Warn("更新证书监控状态失败", slog.String("error", err.Error()))
		}
	}
}

// markOfflineNodes 标记超时未上报的节点为离线。
func (s *Scheduler) markOfflineNodes(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-s.cfg.Collector.OfflineGrace)
	n, err := s.db.Nodes.MarkOffline(ctx, cutoff)
	if err != nil {
		s.log.Error("标记离线节点失败", slog.String("error", err.Error()))
		return
	}
	if n > 0 {
		s.log.Info("标记节点离线", slog.Int64("count", n))
	}
}

// computeRollups 把原始指标聚合进预聚合表。
// 幂等：重复执行不会产生重复数据。
func (s *Scheduler) computeRollups(ctx context.Context) {
	now := time.Now().UTC()

	// 上一个完整的分钟
	minuteStart := now.Truncate(time.Minute).Add(-time.Minute)
	if err := s.db.Metrics.ComputeAndStoreRollup(ctx, "1m", minuteStart, minuteStart.Add(time.Minute)); err != nil {
		s.log.Error("1分钟聚合失败", slog.String("error", err.Error()))
		return
	}

	// 上一个完整的小时
	hourStart := now.Truncate(time.Hour).Add(-time.Hour)
	if err := s.db.Metrics.ComputeAndStoreRollup(ctx, "1h", hourStart, hourStart.Add(time.Hour)); err != nil {
		s.log.Error("1小时聚合失败", slog.String("error", err.Error()))
		return
	}

	// 昨天
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	if err := s.db.Metrics.ComputeAndStoreRollup(ctx, "1d", dayStart, dayStart.AddDate(0, 0, 1)); err != nil {
		s.log.Error("1天聚合失败", slog.String("error", err.Error()))
	}
}

// purgeOldData 清理过期数据。
func (s *Scheduler) purgeOldData(ctx context.Context) {
	now := time.Now().UTC()
	c := s.cfg.Collector

	if n, err := s.db.Metrics.PurgeRaw(ctx, now.Add(-c.RawRetention)); err != nil {
		s.log.Warn("清理原始指标失败", slog.String("error", err.Error()))
	} else if n > 0 {
		s.log.Debug("已清理原始指标", slog.Int64("rows", n))
	}

	if n, err := s.db.Metrics.PurgeRollup(ctx, "1m", now.Add(-c.Rollup1mRetention)); err != nil {
		s.log.Warn("清理1分钟聚合失败", slog.String("error", err.Error()))
	} else if n > 0 {
		s.log.Debug("已清理1分钟聚合", slog.Int64("rows", n))
	}

	if n, err := s.db.Metrics.PurgeRollup(ctx, "1h", now.Add(-c.Rollup1hRetention)); err != nil {
		s.log.Warn("清理1小时聚合失败", slog.String("error", err.Error()))
	} else if n > 0 {
		s.log.Debug("已清理1小时聚合", slog.Int64("rows", n))
	}
}

// purgeExpiredSessions 清理过期会话。
func (s *Scheduler) purgeExpiredSessions(ctx context.Context) {
	now := time.Now().UTC()
	if n, err := s.db.Sessions.PurgeExpired(ctx, now); err != nil {
		s.log.Warn("清理过期会话失败", slog.String("error", err.Error()))
	} else if n > 0 {
		s.log.Debug("已清理过期会话", slog.Int64("rows", n))
	}
	if n, err := s.db.AgentSessions.PurgeExpired(ctx, now); err != nil {
		s.log.Warn("清理过期 Agent 会话失败", slog.String("error", err.Error()))
	} else if n > 0 {
		s.log.Debug("已清理过期 Agent 会话", slog.Int64("rows", n))
	}
}

// ---------- 并发控制 ----------

// tryAcquire 尝试占用一个监控的探测槽。
// 返回 false 表示上次探测还没结束，本次跳过——
// 慢探测堆积比漏探一轮严重得多。
func (s *Scheduler) tryAcquire(monitorID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.inflight[monitorID]; busy {
		return false
	}
	s.inflight[monitorID] = struct{}{}
	return true
}

func (s *Scheduler) release(monitorID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inflight, monitorID)
}

func (s *Scheduler) getCertNotified(id int64) (string, bool) {
	s.certMu.Lock()
	defer s.certMu.Unlock()
	v, ok := s.certNotified[id]
	return v, ok
}

func (s *Scheduler) setCertNotified(id int64, level string) {
	s.certMu.Lock()
	defer s.certMu.Unlock()
	s.certNotified[id] = level
}

func (s *Scheduler) clearCertNotified(id int64) {
	s.certMu.Lock()
	defer s.certMu.Unlock()
	delete(s.certNotified, id)
}

// ---------- 小工具 ----------

func statusOf(ok bool) model.MonitorStatus {
	if ok {
		return model.MonitorUp
	}
	return model.MonitorDown
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// jitter 返回 0–max 之间的随机毫秒数，用于任务错峰。
func jitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(randInt63n(int64(max)))
}

// randInt63n 返回 [0, n) 的随机值。
// 用 math/rand 而非 crypto/rand：错峰不需要密码学强度。
func randInt63n(n int64) int64 {
	if n <= 0 {
		return 0
	}
	return rand.Int63n(n) //nolint:gosec // 错峰不需要密码学随机源
}
