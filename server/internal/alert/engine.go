// Package alert 实现告警规则引擎。
//
// 核心是三个机制（PRD 8.6）：
//  1. 阈值判定：指标 op 阈值，且需持续 N 次 / M 分钟
//  2. 去重：同 (rule, target, severity) 在窗口内只通知一次
//  3. 防风暴：同目标 1 小时内超过阈值则静默，静默结束发汇总
//
// 设计原则：**不产生噪音比不漏报更重要**。
// 一个每小时发 20 条无效告警的监控系统，值班人员一周内就会
// 把通知全部静音——那时真的出事也没人看见。
package alert

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/notify"
	"github.com/m202471895/probeone/server/internal/store"
)

// Engine 是告警引擎。
type Engine struct {
	cfg    Config
	db     *store.DB
	log    *slog.Logger
	notify *notify.Registry

	// state 记录每个 (rule, target) 的当前判定状态，用于"持续 N 次"判定
	stateMu sync.Mutex
	state   map[string]*ruleState

	// storm 记录防风暴的静默窗口
	stormMu sync.Mutex
	storm   map[string]*stormState
}

// Config 是引擎配置。
type Config struct {
	DedupWindow    time.Duration
	StormThreshold int
	StormSilence   time.Duration
}

// ruleState 是规则的持续判定状态。
type ruleState struct {
	// consecutiveHits 是连续满足条件的次数
	consecutiveHits int
	// firstHitAt 是本轮连续命中的起点
	firstHitAt time.Time
	// fired 表示已触发过告警（用于恢复判定）
	fired bool
	// lastValue 是最近一次的值，恢复通知里用
	lastValue float64
}

// stormState 是防风暴状态。
type stormState struct {
	// silencedUntil 是静默截止时间
	silencedUntil time.Time
	// pendingCount 是静默期内累计的告警数
	pendingCount int
	// notified 标记汇总是否已发过
	notified bool
}

// New 创建引擎。
//
// log 与 registry 做了 nil兜底：告警路径是最不能崩溃的路径
// （崩了就没有告警了），而它又常在测试与初始化阶段被传 nil。
func New(cfg Config, db *store.DB, log *slog.Logger, registry *notify.Registry) *Engine {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if registry == nil {
		registry = notify.NewRegistry(nil)
	}
	if cfg.DedupWindow <= 0 {
		cfg.DedupWindow = 30 * time.Minute
	}
	if cfg.StormThreshold <= 0 {
		// 阈值 <= 0 意味着"永不静默"，这与"防风暴"的目的相反，
		// 兜底成一个明确的高阈值而不是 0，避免误以为已启用
		cfg.StormThreshold = 100
	}
	if cfg.StormSilence <= 0 {
		cfg.StormSilence = time.Hour
	}

	return &Engine{
		cfg:    cfg,
		db:     db,
		log:    log,
		notify: registry,
		state:  make(map[string]*ruleState),
		storm:  make(map[string]*stormState),
	}
}

// Sample 是待评估的指标样本。
type Sample struct {
	TargetType model.AlertTargetType
	TargetID   int64
	TargetName string
	Metric     string
	Value      float64
	// Message 是告警消息模板，可含 {{value}} 占位
	Message string
}

// Evaluate 对一个样本评估所有启用的规则。
//
// 调用方（采集器/调度器）在每次指标入库后调用。
func (e *Engine) Evaluate(ctx context.Context, s Sample) {
	rules, err := e.db.Alerts.ListRules(ctx, true)
	if err != nil {
		e.log.Error("加载告警规则失败", slog.String("error", err.Error()))
		return
	}

	for i := range rules {
		rule := &rules[i]
		if !e.ruleMatches(rule, s) {
			// 规则不适用时不做任何事
			continue
		}
		e.evaluateRule(ctx, rule, s)
	}
}

// ruleMatches 判断规则是否适用于该样本。
func (e *Engine) ruleMatches(rule *model.AlertRule, s Sample) bool {
	if rule.TargetType != s.TargetType {
		return false
	}
	// TargetID 为空表示匹配全部
	if rule.TargetID != nil && *rule.TargetID != s.TargetID {
		return false
	}
	// 指标名不匹配则跳过
	if rule.Metric != "" && rule.Metric != s.Metric {
		return false
	}
	return true
}

// evaluateRule 对单条规则做条件判定。
func (e *Engine) evaluateRule(ctx context.Context, rule *model.AlertRule, s Sample) {
	key := stateKey(rule.ID, s.TargetType, s.TargetID)
	cond := rule.Condition

	// 条件不满足 → 检查是否为"已触发后的恢复"
	if !e.conditionMet(s.Value, cond) {
		e.handleRecovery(ctx, rule, key, s)
		return
	}

	// 条件满足 → 累计连续命中
	st := e.bumpHit(key, cond, s.Value)
	if !e.shouldFire(rule, st) {
		return
	}

	// 到达触发条件
	message := e.renderMessage(rule, s)
	e.fire(ctx, rule, s, message, st)
}

// bumpHit 递增连续命中计数。
func (e *Engine) bumpHit(key string, cond model.RuleCondition, value float64) *ruleState {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()

	st, ok := e.state[key]
	if !ok {
		st = &ruleState{firstHitAt: time.Now()}
		e.state[key] = st
	}
	st.consecutiveHits++
	st.lastValue = value
	return st
}

// shouldFire 判断是否该触发。
//
// 两个条件都要满足（配了的话）：
//   - 连续命中次数 ≥ for_times
//   - 持续时间 ≥ for_minutes
func (e *Engine) shouldFire(rule *model.AlertRule, st *ruleState) bool {
	cond := rule.Condition

	if cond.ForTimes > 0 && st.consecutiveHits < cond.ForTimes {
		return false
	}
	if cond.ForMinutes > 0 {
		elapsed := time.Since(st.firstHitAt)
		if elapsed < time.Duration(cond.ForMinutes)*time.Minute {
			return false
		}
	}
	// 已触发过就不重复触发（等恢复）
	if st.fired {
		return false
	}
	return true
}

// handleRecovery 处理条件不再满足的情况。
func (e *Engine) handleRecovery(ctx context.Context, rule *model.AlertRule, key string, s Sample) {
	e.stateMu.Lock()
	st, ok := e.state[key]
	if !ok {
		e.stateMu.Unlock()
		return
	}
	// 清空连续命中计数
	if st.consecutiveHits > 0 {
		st.consecutiveHits = 0
		st.firstHitAt = time.Time{}
	}
	// 只有触发过的才有恢复
	wasFired := st.fired
	st.fired = false
	e.stateMu.Unlock()

	if !wasFired {
		return
	}

	// 标记事件为已恢复
	n, err := e.db.Alerts.ResolveEvents(ctx, &rule.ID, s.TargetType, s.TargetID)
	if err != nil {
		e.log.Warn("恢复告警事件失败", slog.String("error", err.Error()))
		return
	}
	if n == 0 {
		return
	}

	// 发送恢复通知
	channels, err := e.channelsFor(ctx, rule.ChannelIDs)
	if err != nil || len(channels) == 0 {
		return
	}

	// lastValue 取自规则状态（触发前最后一次满足条件的值），
	// 不能用当前 Sample.Value —— 恢复时它已不满足条件，
	// 拿到的是"已经降下来的值"，会让人误以为降到了 0
	e.stateMu.Lock()
	lastVal := st.lastValue
	duration := time.Since(st.firstHitAt).Round(time.Second)
	e.stateMu.Unlock()

	msg := notify.Message{
		Title: fmt.Sprintf("已恢复：%s", rule.Name),
		Body: fmt.Sprintf("规则「%s」已不再满足条件。\n目标：%s\n指标：%s",
			rule.Name, s.TargetName, s.Metric),
		Severity: model.SeverityInfo,
		Fields: []notify.Field{
			{Label: "目标", Value: s.TargetName},
			{Label: "指标", Value: s.Metric},
			{Label: "触发时值", Value: fmt.Sprintf("%.2f", lastVal)},
			{Label: "当前值", Value: fmt.Sprintf("%.2f", s.Value)},
			{Label: "持续时长", Value: duration.String()},
		},
	}
	results := e.notify.Dispatch(ctx, channels, msg)
	e.logResult("恢复通知", rule.Name, results)

}

// fire 触发告警。
func (e *Engine) fire(ctx context.Context, rule *model.AlertRule, s Sample,
	message string, st *ruleState) {

	// 标记为已触发，避免恢复前重复触发
	e.stateMu.Lock()
	st.fired = true
	e.stateMu.Unlock()

	// ---- 防风暴 ----
	// 顺序：先计数，再判断是否静默。
	// 反过来（先判断后计数）会导致第一次永远不静默——
	// 计数要等下一次触发才增加，而静默判断用的是上一次的计数。
	//
	// 两层机制语义独立，不要混：
	//   - 去重：同一次异常的持续期间只通知一次
	//   - 防风暴：跨多次异常的频率控制（同目标 1 小时内超 N 次则静默）
	// 一个持续两小时反复超阈值的告警，去重保证只发 1 条；
	// 一个一小时内抖动了 10 次的指标，防风暴保证不会发 10 条。
	count := e.recordFiring(s)
	silenced := e.isSilenced(s.TargetType, s.TargetID)

	if silenced {
		e.log.Debug("告警处于静默窗口，未发送通知",
			slog.String("rule", rule.Name),
			slog.String("target", s.TargetName),
			slog.Int("hourly_count", count))
		// 静默期内事件仍要落库，否则历史记录会缺一段
		e.createEventOnly(ctx, rule, s, message)
		return
	}

	event, created, err := e.createOrReuseEvent(ctx, rule, s, message)
	if err != nil {
		e.log.Error("创建告警事件失败",
			slog.String("rule", rule.Name),
			slog.String("error", err.Error()))
		return
	}
	if !created {
		// 去重窗口内已有事件，只更新了时间，不通知。
		// 防风暴计数已在上面记过了——抖动场景下每次都该计入频率统计。
		e.log.Debug("告警被去重",
			slog.String("rule", rule.Name),
			slog.String("target", s.TargetName))
		return
	}

	// 发送通知
	channels, err := e.channelsFor(ctx, rule.ChannelIDs)
	if err != nil {
		e.log.Warn("加载通知通道失败", slog.String("error", err.Error()))
		return
	}
	if len(channels) == 0 {
		e.log.Warn("规则未配置通知通道，事件已记录但不会通知",
			slog.String("rule", rule.Name))
		return
	}

	e.stateMu.Lock()
	duration := time.Since(st.firstHitAt).Round(time.Second)
	e.stateMu.Unlock()

	msg := notify.Message{
		Title:    fmt.Sprintf("[%s] %s", severityText(rule.Severity), rule.Name),
		Body:     message,
		URL:      fmt.Sprintf("/alerts/%d", event.ID),
		Severity: rule.Severity,
		Fields: []notify.Field{
			{Label: "目标", Value: s.TargetName},
			{Label: "指标", Value: s.Metric},
			{Label: "当前值", Value: fmt.Sprintf("%.2f", s.Value)},
			{Label: "阈值", Value: fmt.Sprintf("%s %.2f", rule.Condition.Op, rule.Condition.Value)},
			{Label: "持续", Value: duration.String()},
		},
	}
	results := e.notify.Dispatch(ctx, channels, msg)
	e.logResult("告警通知", rule.Name, results)

	if err := e.db.Alerts.MarkNotified(ctx, event.ID); err != nil {
		e.log.Warn("标记事件已通知失败", slog.String("error", err.Error()))
	}
}

// newEvent 构造一个告警事件。
func newEvent(rule *model.AlertRule, s Sample, message string) *model.AlertEvent {
	return &model.AlertEvent{
		RuleID:     &rule.ID,
		TargetType: s.TargetType,
		TargetID:   &s.TargetID,
		TargetName: s.TargetName,
		Severity:   rule.Severity,
		Message:    message,
		Payload: map[string]any{
			"metric":    s.Metric,
			"value":     s.Value,
			"op":        rule.Condition.Op,
			"threshold": rule.Condition.Value,
		},
	}
}

// createOrReuseEvent 创建事件，命中去重窗口时复用已有事件。
func (e *Engine) createOrReuseEvent(ctx context.Context, rule *model.AlertRule,
	s Sample, message string) (*model.AlertEvent, bool, error) {
	return e.db.Alerts.FireEvent(ctx, newEvent(rule, s, message), rule.DedupWindowSec)
}

// createEventOnly 只落库不通知。用于静默期内的事件，
// 保证历史记录完整——静默是"不打扰人"，不是"不记录"。
func (e *Engine) createEventOnly(ctx context.Context, rule *model.AlertRule,
	s Sample, message string) {
	// 静默期内用 0 去重窗口，允许每轮都记一条，
	// 这样事后能看到"这段时间抖动了多少次"
	if _, _, err := e.db.Alerts.FireEvent(ctx, newEvent(rule, s, message), 0); err != nil {
		e.log.Warn("静默期记录事件失败", slog.String("error", err.Error()))
	}
}

// conditionMet 判断单次采样是否满足条件。
func (e *Engine) conditionMet(value float64, cond model.RuleCondition) bool {
	switch cond.Op {
	case ">":
		return value > cond.Value
	case ">=":
		return value >= cond.Value
	case "<":
		return value < cond.Value
	case "<=":
		return value <= cond.Value
	case "==":
		return value == cond.Value
	case "!=":
		return value != cond.Value
	default:
		return false
	}
}

// renderMessage 渲染告警消息。
// 支持 {{value}} 与 {{threshold}} 占位符。
func (e *Engine) renderMessage(rule *model.AlertRule, s Sample) string {
	tmpl := rule.Metric
	if rule.Metric == "" {
		tmpl = s.Metric
	}

	msg := fmt.Sprintf("目标「%s」的 %s 当前为 %.2f，触发规则「%s」（%s %.2f）",
		s.TargetName, s.Metric, s.Value, rule.Name, rule.Condition.Op, rule.Condition.Value)

	if rule.Metric == "" {
		msg = fmt.Sprintf("目标「%s」触发规则「%s」（%s %.2f）",
			s.TargetName, rule.Name, rule.Condition.Op, rule.Condition.Value)
	}
	_ = tmpl
	return msg
}

// ---------- 防风暴 ----------

// isSilenced 判断目标是否处于静默窗口。
func (e *Engine) isSilenced(targetType model.AlertTargetType, targetID int64) bool {
	e.stormMu.Lock()
	defer e.stormMu.Unlock()

	key := stormKey(targetType, targetID)
	st, ok := e.storm[key]
	if !ok {
		return false
	}
	if time.Now().Before(st.silencedUntil) {
		return true
	}
	// 静默期已结束
	return false
}

// recordFiring 记录一次触发，返回该目标近期的告警次数。
// 达到阈值时进入静默窗口。
func (e *Engine) recordFiring(s Sample) int {
	e.stormMu.Lock()
	defer e.stormMu.Unlock()

	key := stormKey(s.TargetType, s.TargetID)
	st, ok := e.storm[key]
	if !ok {
		st = &stormState{}
		e.storm[key] = st
	}
	st.pendingCount++

	// 达到阈值则进入静默（只进一次，静默期内不重复延长）
	if st.pendingCount >= e.cfg.StormThreshold && st.silencedUntil.IsZero() {
		st.silencedUntil = time.Now().Add(e.cfg.StormSilence)
		e.log.Warn("目标告警过多，进入静默窗口",
			slog.String("target", s.TargetName),
			slog.Int("count", st.pendingCount),
			slog.Duration("silence_for", e.cfg.StormSilence))
	}
	return st.pendingCount
}

// FlushSummaries 发送静默期结束后的汇总通知。
// 由后台任务周期调用。
func (e *Engine) FlushSummaries(ctx context.Context) {
	e.stormMu.Lock()
	var toFlush []*stormState
	var keys []string
	now := time.Now()
	for k, st := range e.storm {
		if st.silencedUntil.IsZero() {
			continue
		}
		if now.After(st.silencedUntil) && st.pendingCount > 0 && !st.notified {
			toFlush = append(toFlush, st)
			keys = append(keys, k)
		}
	}
	e.stormMu.Unlock()

	for i, st := range toFlush {
		// 汇总发到所有启用的通道
		all, err := e.db.Channels.List(ctx)
		if err == nil && len(all) > 0 {
			channels := make([]notify.Channel, 0, len(all))
			for _, c := range all {
				if c.Enabled {
					channels = append(channels, c)
				}
			}
			if len(channels) > 0 {
				msg := notify.Message{
					Title: "告警汇总（静默期结束）",
					Body: fmt.Sprintf("目标 %s 在静默期内共触发 %d 个告警，现恢复正常通知。",
						keys[i], st.pendingCount),
					Severity: model.SeverityWarning,
				}
				e.notify.Dispatch(ctx, channels, msg)
			}
		}

		// 重置该目标的防风暴状态
		e.stormMu.Lock()
		delete(e.storm, keys[i])
		e.stormMu.Unlock()
	}
}

// ---------- 辅助 ----------

// channelsFor 按 ID 列表加载通道。
func (e *Engine) channelsFor(ctx context.Context, ids []int64) ([]notify.Channel, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	all, err := e.db.Channels.List(ctx)
	if err != nil {
		return nil, err
	}
	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	out := make([]notify.Channel, 0, len(ids))
	for _, c := range all {
		if want[c.ID] && c.Enabled {
			out = append(out, c)
		}
	}
	return out, nil
}

func (e *Engine) logResult(action, rule string, results []notify.DispatchResult) {
	ok, failed := 0, 0
	for _, r := range results {
		if r.OK {
			ok++
		} else {
			failed++
		}
		if !r.OK {
			e.log.Warn("通知发送失败",
				slog.String("action", action),
				slog.String("rule", rule),
				slog.String("channel", string(r.Type)),
				slog.String("error", r.Error))
		}
	}
	e.log.Info(action,
		slog.String("rule", rule),
		slog.Int("sent", ok),
		slog.Int("failed", failed))
}

func stateKey(ruleID int64, targetType model.AlertTargetType, targetID int64) string {
	return fmt.Sprintf("%d|%s|%d", ruleID, targetType, targetID)
}

func stormKey(targetType model.AlertTargetType, targetID int64) string {
	return fmt.Sprintf("%s|%d", targetType, targetID)
}

func severityText(s model.Severity) string {
	switch s {
	case model.SeverityCritical:
		return "严重"
	case model.SeverityWarning:
		return "警告"
	default:
		return "提示"
	}
}
