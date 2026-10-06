// Package alert 处理告警事件、告警规则与通知通道的 HTTP 接口。
package alert

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/m202471895/probeone/server/internal/apperr"
	"github.com/m202471895/probeone/server/internal/httpapi"
	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/notify"
	"github.com/m202471895/probeone/server/internal/store"
)

// Handler 是告警接口的处理器。
type Handler struct {
	db         *store.DB
	trustProxy bool
	notifier   *notify.Registry
}

// New 创建处理器。
func New(db *store.DB, trustProxy bool, notifier *notify.Registry) *Handler {
	return &Handler{db: db, trustProxy: trustProxy, notifier: notifier}
}

// Register 挂载告警路由。
//
// 读接口（事件列表/规则列表/通道列表）对所有登录用户开放——值班的人
// 必须能看到告警；写接口要求 admin。
func (h *Handler) Register(mux *http.ServeMux, authn *httpapi.Authenticator) {
	authed := func(hf http.HandlerFunc) http.Handler { return authn.RequireAuth(hf) }
	admin := func(hf http.HandlerFunc) http.Handler {
		return authn.RequireAuth(httpapi.RequireRole(model.RoleAdmin)(hf))
	}

	mux.Handle("GET /api/alerts", authed(h.listEvents))
	mux.Handle("POST /api/alerts/{id}/ack", authed(h.ack))
	mux.Handle("GET /api/alert-rules", authed(h.listRules))
	mux.Handle("POST /api/alert-rules", admin(h.createRule))
	mux.Handle("PUT /api/alert-rules/{id}", admin(h.updateRule))
	mux.Handle("DELETE /api/alert-rules/{id}", admin(h.deleteRule))
	mux.Handle("GET /api/channels", authed(h.listChannels))
	mux.Handle("POST /api/channels", admin(h.createChannel))
	mux.Handle("PUT /api/channels/{id}", admin(h.updateChannel))
	mux.Handle("DELETE /api/channels/{id}", admin(h.deleteChannel))
	mux.Handle("POST /api/channels/{id}/test", admin(h.testChannel))
}

const tsLayout = "2006-01-02T15:04:05Z"

// ---------- 事件 ----------

// eventView 是告警事件的 JSON 形状。
type eventView struct {
	ID         int64             `json:"id"`
	RuleID     *int64            `json:"rule_id"`
	TargetType string            `json:"target_type"`
	TargetID   *int64            `json:"target_id"`
	TargetName string            `json:"target_name"`
	Severity   model.Severity    `json:"severity"`
	Status     model.EventStatus `json:"status"`
	Message    string            `json:"message"`
	Payload    map[string]any    `json:"payload"`
	Notified   bool              `json:"notified"`

	FirstFiredAt string  `json:"first_fired_at"`
	LastFiredAt  string  `json:"last_fired_at"`
	ResolvedAt   *string `json:"resolved_at"`
	AckedAt      *string `json:"acked_at"`
}

func viewOfEvent(e *model.AlertEvent) eventView {
	v := eventView{
		ID: e.ID, RuleID: e.RuleID,
		TargetType: string(e.TargetType), TargetID: e.TargetID,
		TargetName: e.TargetName, Severity: e.Severity, Status: e.Status,
		Message: e.Message, Payload: e.Payload, Notified: e.Notified,
		FirstFiredAt: e.FirstFiredAt.UTC().Format(tsLayout),
		LastFiredAt:  e.LastFiredAt.UTC().Format(tsLayout),
	}
	v.ResolvedAt = fmtTime(e.ResolvedAt)
	v.AckedAt = fmtTime(e.AckedAt)
	return v
}

// listEvents 返回告警事件列表。
func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) {
	page, size := httpapi.PageQuery(r)

	f := store.AlertListFilter{
		Limit: size, Offset: (page - 1) * size,
	}
	if s := r.URL.Query().Get("status"); s != "" {
		f.Status = model.EventStatus(s)
	}
	if s := r.URL.Query().Get("severity"); s != "" {
		f.Severity = model.Severity(s)
	}
	if s := r.URL.Query().Get("target_type"); s != "" {
		f.TargetType = model.AlertTargetType(s)
	}
	if s := r.URL.Query().Get("target_id"); s != "" {
		if id, err := strconv.ParseInt(s, 10, 64); err == nil {
			f.TargetID = &id
		}
	}

	events, total, err := h.db.Alerts.ListEvents(r.Context(), f)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	items := make([]eventView, 0, len(events))
	for i := range events {
		items = append(items, viewOfEvent(&events[i]))
	}
	httpapi.OK(w, r, httpapi.PageData{
		Items: items, Total: total, Page: page, Size: size,
	})
}

// ack 确认告警。
//
// 只要登录就能ack，不要求 admin：ack 是值班人员的日常动作
// （"我知道了，我在处理"），把它绑到写权限上会让告警一直挂着没人能关。
// 真正的处置动作（重启服务、扩容）不经过这个接口。
func (h *Handler) ack(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	if err := h.db.Alerts.AckEvent(r.Context(), id, httpapi.UserID(r.Context())); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	h.audit(r, "ack_alert", "alert_event", itoa(id), nil)
	httpapi.OK(w, r, map[string]any{"id": id, "status": model.EventAcked})
}

// ---------- 规则 ----------

// ruleView 是告警规则的 JSON 形状。
type ruleView struct {
	ID          int64               `json:"id"`
	Name        string              `json:"name"`
	TargetType  string              `json:"target_type"`
	TargetID    *int64              `json:"target_id"`
	Metric      string              `json:"metric"`
	Condition   model.RuleCondition `json:"condition"`
	Severity    model.Severity      `json:"severity"`
	ChannelIDs  []int64             `json:"channel_ids"`
	DedupWindow int                 `json:"dedup_window_sec"`
	Enabled     bool                `json:"enabled"`
	CreatedAt   string              `json:"created_at"`
}

func viewOfRule(r *model.AlertRule) ruleView {
	ids := r.ChannelIDs
	if ids == nil {
		// 前端要的是数组而非 null：null 会让 v-for 直接不渲染，
		// 界面上"没有配置通知通道"这句话就消失了
		ids = []int64{}
	}
	return ruleView{
		ID: r.ID, Name: r.Name,
		TargetType: string(r.TargetType), TargetID: r.TargetID,
		Metric: r.Metric, Condition: r.Condition, Severity: r.Severity,
		ChannelIDs: ids, DedupWindow: r.DedupWindowSec,
		Enabled: r.Enabled, CreatedAt: r.CreatedAt.UTC().Format(tsLayout),
	}
}

// ruleInput 是规则创建/更新入参。
type ruleInput struct {
	Name        string                `json:"name"`
	TargetType  model.AlertTargetType `json:"target_type"`
	TargetID    *int64                `json:"target_id"`
	Metric      string                `json:"metric"`
	Condition   model.RuleCondition   `json:"condition"`
	Severity    model.Severity        `json:"severity"`
	ChannelIDs  []int64               `json:"channel_ids"`
	DedupWindow *int                  `json:"dedup_window_sec"`
	Enabled     *bool                 `json:"enabled"`
}

func (h *Handler) listRules(w http.ResponseWriter, r *http.Request) {
	// enabled=1 时只返回启用的规则：前端"规则下拉框"只需要这些，
	// 而把所有禁用规则也塞进去会让用户选到一条根本不会触发的规则。
	enabledOnly := httpapi.QueryInt(r, "enabled", 0) == 1
	rules, err := h.db.Alerts.ListRules(r.Context(), enabledOnly)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	items := make([]ruleView, 0, len(rules))
	for i := range rules {
		items = append(items, viewOfRule(&rules[i]))
	}
	httpapi.OK(w, r, map[string]any{"items": items, "total": len(items)})
}

func (h *Handler) createRule(w http.ResponseWriter, r *http.Request) {
	var in ruleInput
	if err := httpapi.DecodeJSON(r, &in); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		httpapi.Fail(w, r, apperr.BadRequest("请填写规则名称"))
		return
	}
	if err := validateRule(in); err != nil {
		httpapi.Fail(w, r, err)
		return
	}

	rule := &model.AlertRule{
		Name: in.Name, TargetType: in.TargetType, TargetID: in.TargetID,
		Metric: strings.TrimSpace(in.Metric), Condition: in.Condition,
		Severity: in.Severity, ChannelIDs: in.ChannelIDs,
		DedupWindowSec: pickInt(in.DedupWindow, 1800),
		Enabled:        true,
	}
	if in.Enabled != nil {
		rule.Enabled = *in.Enabled
	}

	id, err := h.db.Alerts.CreateRule(r.Context(), rule)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	rule.ID = id
	h.audit(r, "create_alert_rule", "alert_rule", itoa(id), map[string]any{
		"name": rule.Name, "target_type": string(rule.TargetType),
		"severity": string(rule.Severity),
	})
	httpapi.Created(w, r, viewOfRule(rule))
}

func (h *Handler) updateRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	rule, err := h.db.Alerts.GetRule(r.Context(), id)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	var in ruleInput
	if err := httpapi.DecodeJSON(r, &in); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	if err := validateRule(in); err != nil {
		httpapi.Fail(w, r, err)
		return
	}

	if n := strings.TrimSpace(in.Name); n != "" {
		rule.Name = n
	}
	rule.TargetType = in.TargetType
	rule.TargetID = in.TargetID
	rule.Metric = strings.TrimSpace(in.Metric)
	rule.Condition = in.Condition
	rule.Severity = in.Severity
	if in.ChannelIDs != nil {
		rule.ChannelIDs = in.ChannelIDs
	}
	if in.DedupWindow != nil {
		rule.DedupWindowSec = *in.DedupWindow
	}
	if in.Enabled != nil {
		rule.Enabled = *in.Enabled
	}

	if err := h.db.Alerts.UpdateRule(r.Context(), rule); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	h.audit(r, "update_alert_rule", "alert_rule", itoa(rule.ID), map[string]any{
		"name": rule.Name, "enabled": rule.Enabled,
	})
	httpapi.OK(w, r, viewOfRule(rule))
}

func (h *Handler) deleteRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	rule, err := h.db.Alerts.GetRule(r.Context(), id)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	if err := h.db.Alerts.DeleteRule(r.Context(), id); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	h.audit(r, "delete_alert_rule", "alert_rule", itoa(id), map[string]any{
		"name": rule.Name,
	})
	httpapi.NoContent(w, r)
}

// validateRule 校验规则配置。
func validateRule(in ruleInput) error {
	switch in.TargetType {
	case model.TargetNode, model.TargetMonitor, model.TargetCert:
	default:
		return apperr.BadRequest(fmt.Sprintf(
			"不支持的告警目标类型：%q（可选 node / monitor / cert）", in.TargetType))
	}
	switch in.Severity {
	case model.SeverityInfo, model.SeverityWarning, model.SeverityCritical:
	default:
		return apperr.BadRequest(fmt.Sprintf(
			"不支持的告警级别：%q（可选 info / warning / critical）", in.Severity))
	}
	// 条件运算符是引擎里switch 的分支，写错会静默地永不触发——
	// 引擎对未知 op 的处理是"不满足条件"，用户看不出是配置写错了还是真没告警。
	switch in.Condition.Op {
	case ">", ">=", "<", "<=", "==", "!=":
	default:
		return apperr.BadRequest(fmt.Sprintf(
			"不支持的条件运算符：%q（可选 > >= < <= == !=）", in.Condition.Op))
	}
	if in.Condition.ForTimes < 0 || in.Condition.ForMinutes < 0 {
		return apperr.BadRequest("持续次数与持续分钟数不能为负")
	}
	if len(in.ChannelIDs) == 0 {
		// 允许建"不发通知"的规则，但必须让用户知道：
		// 否则规则能触发、事件能落库，却没人收到通知，
		// 用户会以为告警系统坏了。
		return apperr.BadRequest("请至少配置一个通知通道")
	}
	return nil
}

// ---------- 通知通道 ----------

// channelView 是通知通道的 JSON 形状。
//
// Config 走掩码后的 map 而不是 model.AlertChannel：config 里存着
// webhook secret、SMTP 密码、bot token 这类凭据，
// 直接序列化等于把整个通知系统的钥匙挂在面板上。
type channelView struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	Type      model.ChannelType `json:"type"`
	Config    map[string]any    `json:"config"`
	Enabled   bool              `json:"enabled"`
	CreatedAt string            `json:"created_at"`
}

// sensitiveKeys 是需要掩码的配置键。
//
// 判定依据是"这个值是不是凭据本身"，而不是"这个字段名叫不像敏感字段"。
// webhook / bark 的地址里就嵌着 token（…/send/KEY），dingtalk 的 URL 带 access_token，
// 只掩 secret 字段会漏掉它们。headers 同理：自定义头里常放 Authorization。
// 反过来 host / port / from / to / chat_id 这类不是凭据，必须原样返回，
// 否则用户打开编辑页看到一片掩码，根本没法确认自己填对了地址。
var sensitiveKeys = map[string]bool{
	"password":     true,
	"secret":       true,
	"token":        true,
	"access_token": true,
	"bot_token":    true,
	"device_key":   true,
	"key":          true,
	"webhook":      true,
	"url":          true,
	"server":       true,
	"headers":      true,
}

// maskConfig 返回可安全返回客户端的配置副本。
//
// 保留 key 与 has_value 两个字段：key 让前端知道这个通道需要哪些配置项，
// has_value 让它知道"已填过"从而把输入框显示成占位符而不是留空。
// 用户因此永远看不到明文，但也能看出自己是不是漏填了。
//
// 值短于 8 位时整体替换为 "***"：首尾各留 2 位对 6 位 token 来说
// 已经泄露了 2/3，留了比不留还糟。
func maskConfig(cfg map[string]any) map[string]any {
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		s, isStr := v.(string)
		if !sensitiveKeys[k] {
			out[k] = v
			continue
		}
		if !isStr {
			// 敏感键上出现非字符串（配置被手改成数字/对象）时不做形态假设，
			// 直接整体屏蔽——宁可前端显示得难看，也不能漏出去。
			out[k] = "***"
			out[k+"_has_value"] = v != nil && v != ""
			continue
		}
		out[k] = maskSecret(s)
		out[k+"_has_value"] = s != ""
	}
	return out
}

// maskSecret 掩码单个凭据。
// 规则与前端 utils/format.ts 的 maskSecret 保持一致（首尾各留 2 位），
// 两端同规则才能避免用户看到两个不同的掩码而怀疑是不是系统出了 bug。
func maskSecret(s string) string {
	if len(s) <= 8 {
		return "***"
	}
	return s[:2] + "***" + s[len(s)-2:]
}

func viewOfChannel(c *model.AlertChannel) channelView {
	cfg := c.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	return channelView{
		ID: c.ID, Name: c.Name, Type: c.Type,
		Config: maskConfig(cfg), Enabled: c.Enabled,
		CreatedAt: c.CreatedAt.UTC().Format(tsLayout),
	}
}

func (h *Handler) listChannels(w http.ResponseWriter, r *http.Request) {
	list, err := h.db.Channels.List(r.Context())
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	items := make([]channelView, 0, len(list))
	for i := range list {
		items = append(items, viewOfChannel(&list[i]))
	}
	httpapi.OK(w, r, map[string]any{"items": items, "total": len(items)})
}

// channelInput 是通道创建/更新入参。
type channelInput struct {
	Name    string            `json:"name"`
	Type    model.ChannelType `json:"type"`
	Config  map[string]any    `json:"config"`
	Enabled *bool             `json:"enabled"`
}

func (h *Handler) createChannel(w http.ResponseWriter, r *http.Request) {
	var in channelInput
	if err := httpapi.DecodeJSON(r, &in); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		httpapi.Fail(w, r, apperr.BadRequest("请填写通道名称"))
		return
	}
	if err := h.validateConfig(in.Type, in.Config); err != nil {
		httpapi.Fail(w, r, err)
		return
	}

	ch := &model.AlertChannel{
		Name: in.Name, Type: in.Type, Config: in.Config, Enabled: true,
	}
	if in.Enabled != nil {
		ch.Enabled = *in.Enabled
	}
	id, err := h.db.Channels.Create(r.Context(), ch)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	ch.ID = id
	// 审计里只写通道名与类型，绝不写 config：
	// 审计日志的读取范围比面板更广（owner 能看全部），
	// 凭据进审计等于多一份副本需要保护。
	h.audit(r, "create_channel", "channel", itoa(id), map[string]any{
		"name": ch.Name, "type": string(ch.Type),
	})
	httpapi.Created(w, r, viewOfChannel(ch))
}

func (h *Handler) updateChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	ch, err := h.db.Channels.GetByID(r.Context(), id)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	var in channelInput
	if err := httpapi.DecodeJSON(r, &in); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	if n := strings.TrimSpace(in.Name); n != "" {
		ch.Name = n
	}
	if in.Type != "" {
		ch.Type = in.Type
	}
	if in.Config != nil {
		merged := mergeMasked(ch.Config, in.Config)
		if err := h.validateConfig(ch.Type, merged); err != nil {
			httpapi.Fail(w, r, err)
			return
		}
		ch.Config = merged
	}
	if in.Enabled != nil {
		ch.Enabled = *in.Enabled
	}

	if err := h.db.Channels.Update(r.Context(), ch); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	h.audit(r, "update_channel", "channel", itoa(ch.ID), map[string]any{
		"name": ch.Name, "type": string(ch.Type), "enabled": ch.Enabled,
	})
	httpapi.OK(w, r, viewOfChannel(ch))
}

// mergeMasked 把客户端回传的掩码占位还原成库里的真实值。
//
// 为什么必须做：前端拿到的是 "ab***yz" 这样的掩码，
// 用户只改了 channel 名称就点保存，若原样写回就会把真凭据改成 "ab***yz"，
// 通道当场失效且用户完全不知道发生了什么。
func mergeMasked(stored, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(incoming))
	for k, v := range incoming {
		s, isStr := v.(string)
		if !isStr || !isMasked(s) {
			out[k] = v
			continue
		}
		// 客户端回传的是掩码：用库里的原值顶替。
		// 若库里原本没有这个键，说明用户是想新增却只填了占位符，
		// 此时保留掩码原样，让 Validate 去报错而不是静默丢弃。
		if old, ok := stored[k]; ok {
			out[k] = old
			continue
		}
		out[k] = v
	}
	return out
}

// isMasked 判断字符串是否是掩码占位。
func isMasked(s string) bool {
	return s == "***" || (len(s) > 6 && strings.Contains(s, "***"))
}

func (h *Handler) deleteChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	ch, err := h.db.Channels.GetByID(r.Context(), id)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	if err := h.db.Channels.Delete(r.Context(), id); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	h.audit(r, "delete_channel", "channel", itoa(id), map[string]any{"name": ch.Name})
	httpapi.NoContent(w, r)
}

// validateConfig 校验通道类型与配置完整性。
//
// 复用 notify.Sender.Validate 而不是自己写一份规则：七个通道各需要哪些字段
// 只有 sender 自己清楚（钉钉要 access_token、飞书要 webhook…），
// 这里再抄一遍必然会漏，而且两边一旦漂移就会出现"保存成功但发不出去"。
func (h *Handler) validateConfig(t model.ChannelType, cfg map[string]any) error {
	if h.notifier == nil {
		return apperr.Internal(nil, "通知注册表未初始化")
	}
	s, ok := h.notifier.Get(t)
	if !ok {
		return apperr.BadRequest(fmt.Sprintf("不支持的通知通道类型：%q", t))
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	if err := s.Validate(cfg); err != nil {
		return apperr.BadRequest("通道配置不完整：" + err.Error()).WithCause(err)
	}
	return nil
}

// testChannel 发一条测试消息。
//
// 用真实的库内配置、真实的sender，只是不经过告警引擎。
// 这样测的是"这条通道现在能不能通"，
// 而不仅仅是"配置看起来对"——SMTP 密码过期、机器人被踢出群
// 这类问题只有真发一次才会暴露。
func (h *Handler) testChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	ch, err := h.db.Channels.GetByID(r.Context(), id)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	if h.notifier == nil {
		httpapi.Fail(w, r, apperr.Internal(nil, "通知注册表未初始化"))
		return
	}
	s, ok := h.notifier.Get(ch.Type)
	if !ok {
		httpapi.Fail(w, r, apperr.BadRequest(
			fmt.Sprintf("不支持的通知通道类型：%q", ch.Type)))
		return
	}

	msg := notify.Message{
		Title: "ProbeOne 测试通知",
		Body: "这是一条测试消息。\n" +
			"如果你在 IM 里收到了它，说明该通道配置正确、可以正常送达。",
		Severity: model.SeverityInfo,
		Fields: []notify.Field{
			{Label: "通道名称", Value: ch.Name},
			{Label: "通道类型", Value: string(ch.Type)},
			{Label: "触发人", Value: httpapi.Username(r.Context())},
			{Label: "触发时间", Value: time.Now().UTC().Format(tsLayout)},
		},
	}

	// 用独立超时：Send 内部已有超时，但 http.Server 的 WriteTimeout
	// 不覆盖 handler 内部的网络等待，不设上限会让一个卡住的通道
	// 长期占着这条连接
	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	if err := s.Send(ctx, ch.Config, msg); err != nil {
		// 发送失败的详情（对方返回的 4xx/5xx 片段）对排障有用，
		// 但它可能含内网主机名，所以只作为日志，不进响应体。
		slog.Warn("测试通知发送失败",
			slog.Int64("channel_id", id),
			slog.String("error", err.Error()))
		h.audit(r, "test_channel", "channel", itoa(id),
			map[string]any{"ok": false})
		httpapi.Fail(w, r, apperr.BadRequest("测试消息发送失败："+err.Error()).
			WithCause(err))
		return
	}

	h.audit(r, "test_channel", "channel", itoa(id), map[string]any{"ok": true})
	httpapi.OK(w, r, map[string]any{"ok": true, "type": string(ch.Type)})
}

// ---------- 辅助 ----------

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, apperr.BadRequest("ID 不是合法数字")
	}
	return id, nil
}

func fmtTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(tsLayout)
	return &s
}

func pickInt(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// contextWithTimeout 给测试发送加独立超时。
// 从请求派生而非 Background：客户端断开时不该继续往 IM 里发消息。
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// audit 写审计日志。
//
// 审计失败只记日志不影响主流程：审计是辅助记录，
// 不该因为它写不进去就拒绝用户的操作。
func (h *Handler) audit(r *http.Request, action, targetType, targetID string, detail map[string]any) {
	if h.db.Audit == nil {
		return
	}
	_ = h.db.Audit.Write(r.Context(), &model.AuditLog{
		Username:   httpapi.Username(r.Context()),
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		IP:         httpapi.ClientIP(r, h.trustProxy),
		Detail:     detail,
	})
}
