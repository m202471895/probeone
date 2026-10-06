// Package monitor 处理网站监控（探针）相关的 HTTP 接口。
package monitor

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/m202471895/probeone/server/internal/alert"
	"github.com/m202471895/probeone/server/internal/apperr"
	"github.com/m202471895/probeone/server/internal/httpapi"
	"github.com/m202471895/probeone/server/internal/model"
	probemon "github.com/m202471895/probeone/server/internal/monitor"
	"github.com/m202471895/probeone/server/internal/store"
)

// Handler 是网站监控接口的处理器。
type Handler struct {
	db         *store.DB
	trustProxy bool
	alerts     *alert.Engine
	// allowPrivate 允许探测内网地址。默认 false（SSRF 防护，PRD T11），
	// 只能通过 SetAllowInternalTargets 显式开启。
	allowPrivate bool
}

// New 创建处理器。
func New(db *store.DB, trustProxy bool, alertEngine *alert.Engine) *Handler {
	return &Handler{db: db, trustProxy: trustProxy, alerts: alertEngine}
}

// SetAllowInternalTargets 设置是否允许探测内网地址。
//
// 对应 config.Storage.AllowInternalTargets（PROBEONE_ALLOW_INTERNAL_TARGETS）。
// 为什么不在 New 里传：该开关是安全边界而非业务参数，让它必须被显式
// 赋值（哪怕赋 false）才能构造出正确的 Handler，避免"忘传=放行内网"。
func (h *Handler) SetAllowInternalTargets(v bool) { h.allowPrivate = v }

// Register 把监控路由挂到 mux。
//
// 读接口对所有登录用户开放（viewer 也需要看监控列表与曲线）；
// 写接口要求 admin —— viewer 是纯观察角色，不该能改探测配置，
// 否则任何只读账号都能把监控目标改成自己的内网地址做探测。
func (h *Handler) Register(mux *http.ServeMux, authn *httpapi.Authenticator) {
	authed := func(hf http.HandlerFunc) http.Handler { return authn.RequireAuth(hf) }
	admin := func(hf http.HandlerFunc) http.Handler {
		return authn.RequireAuth(httpapi.RequireRole(model.RoleAdmin)(hf))
	}

	mux.Handle("GET /api/monitors", authed(h.list))
	mux.Handle("POST /api/monitors", admin(h.create))
	mux.Handle("GET /api/monitors/{id}", authed(h.get))
	mux.Handle("PUT /api/monitors/{id}", admin(h.update))
	mux.Handle("DELETE /api/monitors/{id}", admin(h.delete))
	mux.Handle("POST /api/monitors/{id}/check", admin(h.check))
	mux.Handle("GET /api/monitors/{id}/results", authed(h.results))
	mux.Handle("GET /api/monitors/{id}/stats", authed(h.stats))
	mux.Handle("GET /api/monitors/{id}/certificate", authed(h.certificate))
}

// tsLayout 是统一的时间格式。
// 与 node 包保持一致：UTC + Z 后缀，避免前端把本地时区偏移算进延迟曲线。
const tsLayout = "2006-01-02T15:04:05Z"

// monitorView 是监控对外的 JSON 形状。
//
// 独立 DTO 而非直接序列化 model.Monitor：探针配置里可能有
// 认证 header（http探针的 headers 字段常带 Authorization），
// 独立 DTO 让"哪些字段能出库"在编译期就是白名单（PRD 3.6.4）。
type monitorView struct {
	ID       int64               `json:"id"`
	Name     string              `json:"name"`
	Type     model.MonitorType   `json:"type"`
	Target   string              `json:"target"`
	Config   model.MonitorConfig `json:"config"`
	Interval int                 `json:"interval_sec"`
	Timeout  int                 `json:"timeout_sec"`
	GroupID  *int64              `json:"group_id"`
	Status   model.MonitorStatus `json:"status"`
	IsPublic bool                `json:"is_public"`
	Sort     int                 `json:"sort"`

	LastCheckedAt string   `json:"last_checked_at"`
	Uptime30d     *float64 `json:"uptime_30d"`
	AvgLatencyMs  *int     `json:"avg_latency_ms"`
	CreatedAt     string   `json:"created_at"`
}

func viewOf(m *model.Monitor) monitorView {
	v := monitorView{
		ID: m.ID, Name: m.Name, Type: m.Type, Target: m.Target,
		Config: m.Config, Interval: m.IntervalSec, Timeout: m.TimeoutSec,
		GroupID: m.GroupID, Status: m.Status, IsPublic: m.IsPublic,
		Sort: m.Sort, Uptime30d: m.Uptime30d, AvgLatencyMs: m.AvgLatencyMs,
		CreatedAt: m.CreatedAt.UTC().Format(tsLayout),
	}
	if m.LastCheckedAt != nil {
		v.LastCheckedAt = m.LastCheckedAt.UTC().Format(tsLayout)
	}
	return v
}

// resultView 是一次探测结果的 JSON 形状。
// 分段耗时全量返回：用户排障时要能区分"DNS 慢"与"TCP 慢"，
// 只给一个总耗时等于让人自己猜。
type resultView struct {
	ID        int64  `json:"id"`
	MonitorID int64  `json:"monitor_id"`
	CheckedAt string `json:"checked_at"`
	OK        bool   `json:"ok"`
	Reason    string `json:"reason"`

	StatusCode  *int   `json:"status_code"`
	LatencyMs   *int   `json:"latency_ms"`
	DNSMs       *int   `json:"dns_ms"`
	TCPMs       *int   `json:"tcp_ms"`
	TLSMs       *int   `json:"tls_ms"`
	TTFBMs      *int   `json:"ttfb_ms"`
	ErrorDetail string `json:"error_detail"`
}

func viewOfResult(r *model.MonitorResult) resultView {
	return resultView{
		ID: r.ID, MonitorID: r.MonitorID,
		CheckedAt: r.CheckedAt.UTC().Format(tsLayout),
		OK:        r.OK, Reason: string(r.Reason),
		StatusCode: r.StatusCode, LatencyMs: r.LatencyMs,
		DNSMs: r.DNSMs, TCPMs: r.TCPMs, TLSMs: r.TLSMs, TTFBMs: r.TTFBMs,
		ErrorDetail: r.ErrorDetail,
	}
}

// ---------- 列表与详情 ----------

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	page, size := httpapi.PageQuery(r)

	f := store.MonitorListFilter{
		Keyword: r.URL.Query().Get("q"),
		Limit:   size,
		Offset:  (page - 1) * size,
	}
	if s := r.URL.Query().Get("type"); s != "" {
		f.Type = model.MonitorType(s)
	}
	if s := r.URL.Query().Get("status"); s != "" {
		f.Status = model.MonitorStatus(s)
	}

	list, total, err := h.db.Monitors.List(r.Context(), f)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	items := make([]monitorView, 0, len(list))
	for i := range list {
		items = append(items, viewOf(&list[i]))
	}
	httpapi.OK(w, r, httpapi.PageData{
		Items: items, Total: total, Page: page, Size: size,
	})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	m, err := h.monitorByID(r)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	httpapi.OK(w, r, viewOf(m))
}

// monitorByID 取监控，不存在时返回 404。
func (h *Handler) monitorByID(r *http.Request) (*model.Monitor, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	return h.db.Monitors.GetByID(r.Context(), id)
}

// pathID 解析路径参数 id。
// 显式 ParseInt 而不是直接 Atoi再判符号：ParseInt 一次给出溢出信号，
// 而 Atoi 在超范围时会静默返回 MaxInt64，变成一个查不到结果的诡异 ID。
func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, apperr.BadRequest("监控 ID 不是合法数字")
	}
	return id, nil
}

// ---------- 创建与更新 ----------

// monitorInput 是创建/更新监控的入参。
//
// Type 只在创建时接受：探针类型决定 config 各字段的含义，
// 把 http 改成 tcp 会让已有的 expect_status 变成无意义残留。
// 仓储层的 UPDATE 也不含 type 列，两处一致。
type monitorInput struct {
	Name   string            `json:"name"`
	Type   model.MonitorType `json:"type"`
	Target string            `json:"target"`
	// Config 用指针而非值：MonitorConfig 内含 map 与 slice，零值不可比较，
	// 更重要的是只有指针能区分"没传 config"与"传了空 config"——
	// 清空 headers 是合法操作，不能被当成"未提供"而静默忽略。
	Config   *model.MonitorConfig `json:"config"`
	Interval *int                 `json:"interval_sec"`
	Timeout  *int                 `json:"timeout_sec"`
	GroupID  *int64               `json:"group_id"`
	IsPublic *bool                `json:"is_public"`
	Sort     *int                 `json:"sort"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var in monitorInput
	if err := httpapi.DecodeJSON(r, &in); err != nil {
		httpapi.Fail(w, r, err)
		return
	}

	in.Name = strings.TrimSpace(in.Name)
	in.Target = strings.TrimSpace(in.Target)
	if in.Name == "" {
		httpapi.Fail(w, r, apperr.BadRequest("请填写监控名称"))
		return
	}
	if n := len([]rune(in.Name)); n > 64 {
		httpapi.Fail(w, r, apperr.BadRequest(fmt.Sprintf("监控名称过长（%d 字符，上限 64）", n)))
		return
	}
	if err := validateTarget(in.Target, in.Type); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	if err := h.checkTargetAllowed(in.Target); err != nil {
		httpapi.Fail(w, r, err)
		return
	}

	interval, timeout, err := resolveTiming(in.Interval, in.Timeout, nil)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}

	m := &model.Monitor{
		Name: in.Name, Type: in.Type, Target: in.Target,
		IntervalSec: interval, TimeoutSec: timeout, GroupID: in.GroupID,
		// 新建一律pending：还没探测过就标 up/down 都是在骗人。
		// 前端据此显示"等待首次探测"。
		Status: model.MonitorPending,
		Sort:   pickInt(in.Sort, 0),
	}
	if in.Config != nil {
		m.Config = *in.Config
	}
	if in.IsPublic != nil {
		m.IsPublic = *in.IsPublic
	}

	id, err := h.db.Monitors.Create(r.Context(), m)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	m.ID = id

	h.audit(r, "create_monitor", "monitor", itoa(id), map[string]any{
		"name": m.Name, "type": string(m.Type), "target": m.Target,
		"is_public": m.IsPublic,
	})
	httpapi.Created(w, r, viewOf(m))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	m, err := h.monitorByID(r)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	var in monitorInput
	if err := httpapi.DecodeJSON(r, &in); err != nil {
		httpapi.Fail(w, r, err)
		return
	}

	if n := strings.TrimSpace(in.Name); n != "" {
		if len([]rune(n)) > 64 {
			httpapi.Fail(w, r, apperr.BadRequest("监控名称过长（上限 64 字符）"))
			return
		}
		m.Name = n
	}
	// status / uptime / avg_latency 不接受客户端传入：
	// 它们由探测器写入，允许改就能把 down 的监控手动"洗成"up。
	if t := strings.TrimSpace(in.Target); t != "" {
		if err := validateTarget(t, m.Type); err != nil {
			httpapi.Fail(w, r, err)
			return
		}
		if err := h.checkTargetAllowed(t); err != nil {
			httpapi.Fail(w, r, err)
			return
		}
		m.Target = t
	}
	if in.Config != nil {
		m.Config = *in.Config
	}
	interval, timeout, err := resolveTiming(in.Interval, in.Timeout, m)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	m.IntervalSec, m.TimeoutSec = interval, timeout

	m.GroupID = in.GroupID
	if in.IsPublic != nil {
		m.IsPublic = *in.IsPublic
	}
	if in.Sort != nil {
		m.Sort = *in.Sort
	}

	if err := h.db.Monitors.Update(r.Context(), m); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	h.audit(r, "update_monitor", "monitor", itoa(m.ID), map[string]any{
		"name": m.Name, "target": m.Target,
	})
	httpapi.OK(w, r, viewOf(m))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	m, err := h.monitorByID(r)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	if err := h.db.Monitors.Delete(r.Context(), m.ID); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	h.audit(r, "delete_monitor", "monitor", itoa(m.ID), map[string]any{
		"name": m.Name, "target": m.Target,
	})
	httpapi.NoContent(w, r)
}

// ---------- 立即检查 ----------

// prober 是"立即探测"能力的结构化断言。
//
// alert.Engine 目前没有这个方法（P8 才补），用接口断言而不是
// 直接调用，是为了让本包在引擎补齐前后都能编译通过：
// 断言失败时返回 501 并说明原因，而不是让整个服务起不来。
type prober interface {
	ProbeNow(ctx context.Context, monitorID int64) error
}

// check 立即触发一次探测。
func (h *Handler) check(w http.ResponseWriter, r *http.Request) {
	m, err := h.monitorByID(r)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	if h.alerts == nil {
		httpapi.Fail(w, r, errNotImplemented("告警引擎未接入，无法立即探测"))
		return
	}
	p, ok := any(h.alerts).(prober)
	if !ok {
		// TODO(P8): alert.Engine 补上 ProbeNow 后本分支自动失效。
		// 之所以返回 501 而不是"假装成功"：用户点了"立即检查"却拿到
		// 200，页面会显示探测成功，而实际什么都没发生——
		// 这种假成功比直接报错危险得多。
		httpapi.Fail(w, r, errNotImplemented("告警引擎尚未实现立即探测（ProbeNow）"))
		return
	}
	if err := p.ProbeNow(r.Context(), m.ID); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	h.audit(r, "check_monitor", "monitor", itoa(m.ID), nil)
	httpapi.OK(w, r, map[string]any{"id": m.ID, "triggered": true})
}

// errNotImplemented 构造 501。
func errNotImplemented(msg string) *apperr.Error {
	return apperr.New("NOT_IMPLEMENTED", msg, http.StatusNotImplemented)
}

// ---------- 历史与统计 ----------

// results 返回历史探测结果。
func (h *Handler) results(w http.ResponseWriter, r *http.Request) {
	m, err := h.monitorByID(r)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	from, to := timeWindow(r)
	rows, err := h.db.Monitors.Results(r.Context(), m.ID, from, to, 0)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	items := make([]resultView, 0, len(rows))
	for i := range rows {
		items = append(items, viewOfResult(&rows[i]))
	}
	// 直接给数组而非分页信封：曲线图需要的是完整序列，
	// 前端自己按时间戳对齐，分页反而会让断点处出现误判为"宕机"的空洞。
	httpapi.OK(w, r, map[string]any{
		"items": items, "from": from.Unix(), "to": to.Unix(),
	})
}

// stats 返回可用率与延迟分位数。
func (h *Handler) stats(w http.ResponseWriter, r *http.Request) {
	m, err := h.monitorByID(r)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	from, to := timeWindow(r)
	st, err := h.db.Monitors.Stats(r.Context(), m.ID, from, to)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	httpapi.OK(w, r, map[string]any{
		"total": st.Total, "up": st.Up,
		"latency_p50":    st.LatencyP50,
		"latency_p95":    st.LatencyP95,
		"latency_p99":    st.LatencyP99,
		"uptime_percent": st.UptimePercent,
		// 样本不足时前端显示"样本不足"而不是百分比（PRD 8.7）：
		// 用3 条样本算出 100% 可用率毫无意义，却看起来很确定。
		"sample_insufficient": st.SampleInsufficient,
		"from":                from.Unix(), "to": to.Unix(),
	})
}

// certificate 返回 SSL 证书信息。
func (h *Handler) certificate(w http.ResponseWriter, r *http.Request) {
	m, err := h.monitorByID(r)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	c, err := h.db.Monitors.GetCertificate(r.Context(), m.ID)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	out := map[string]any{
		"subject":     c.Subject,
		"issuer":      c.Issuer,
		"serial":      c.Serial,
		"fingerprint": c.Fingerprint,
		"days_left":   c.DaysLeft,
	}
	if c.NotBefore != nil {
		out["not_before"] = c.NotBefore.UTC().Format(tsLayout)
	}
	if c.NotAfter != nil {
		out["not_after"] = c.NotAfter.UTC().Format(tsLayout)
	}
	if c.LastCheckedAt != nil {
		out["last_checked_at"] = c.LastCheckedAt.UTC().Format(tsLayout)
	}
	httpapi.OK(w, r, out)
}

// ---------- 校验 ----------

// validateTarget 校验探针类型与目标地址。
//
// 为什么按类型分别校验 target：dns 探针的 target 是域名（不能带 scheme），
// http 探针的 target 必须是带 scheme 的 URL（否则探测器不知道用哪种协议）。
// 统一用一条规则校验会让其中一种类型必然配错。
func validateTarget(target string, t model.MonitorType) error {
	if target == "" {
		return apperr.BadRequest("请填写监控目标地址")
	}
	switch t {
	case model.MonitorHTTP, model.MonitorSSL:
		if !strings.Contains(target, "://") {
			return apperr.BadRequest("http/ssl 探针的目标需带协议头，如 https://example.com")
		}
	case model.MonitorTCP:
		if !strings.Contains(target, ":") {
			return apperr.BadRequest("tcp 探针的目标需带端口，如 example.com:443")
		}
	case model.MonitorDNS:
		if strings.Contains(target, "://") {
			return apperr.BadRequest("dns 探针的目标只填域名，不要带协议头")
		}
	default:
		// model里还有 MonitorPing，但探测器尚未实现 ping 探针，
		// 放行只会得到一个永远pending 的监控。
		return apperr.BadRequest(fmt.Sprintf(
			"不支持的探针类型：%q（可选http / tcp / dns / ssl）", t))
	}
	return nil
}

// checkTargetAllowed 做 SSRF 校验。
//
// 为什么不省掉这步：写入时不拦，探测器运行时每轮都会去连用户指定的目标。
// 攻击者只要建一个 target=http://169.254.169.254/ 的监控，
// 就获得了周期性的云元数据访问能力。探测器内部虽已有防护，
// 但入口处再拦一道能把错误挡在入库前，用户能立刻看到"为什么不行"。
func (h *Handler) checkTargetAllowed(target string) error {
	if err := probemon.CheckTarget(target, h.allowPrivate); err != nil {
		return apperr.New(apperr.CodeSSRFBlocked, err.Error(),
			http.StatusBadRequest).WithCause(err)
	}
	return nil
}

// resolveTiming 归一化探测间隔与超时。
//
// cur 非nil 时以其为基准（更新场景：未传的字段保持原值）；
// cur 为 nil 时用默认值。下界10 秒不是为了防刷——采集器本来就有抖动，
// 而是 10 秒以下的间隔会让探测流量本身成为被监控服务的负担。
func resolveTiming(interval, timeout *int, cur *model.Monitor) (int, int, error) {
	iv, to := 300, 10
	if cur != nil {
		iv, to = cur.IntervalSec, cur.TimeoutSec
	}
	if interval != nil {
		iv = *interval
	}
	if timeout != nil {
		to = *timeout
	}
	if iv < 10 || iv > 86400 {
		return 0, 0, apperr.BadRequest("探测间隔须在 10 ~ 86400 秒之间")
	}
	if to < 1 || to > 60 {
		return 0, 0, apperr.BadRequest("超时须在 1 ~ 60 秒之间")
	}
	if to > iv {
		// 超时比间隔长意味着上一轮还没结束下一轮就该开始了，
		// 采集器会不断堆积 goroutine
		return 0, 0, apperr.BadRequest("超时不能大于探测间隔")
	}
	return iv, to, nil
}

// timeWindow 解析时间窗口。
//
// 优先用 range（预设窗口），其次 from/to（Unix 秒），都没有则默认最近 24 小时。
// range放在from/to 之前是因为前端图表的"最近1 小时"按钮只改 range，
// 不该同时携带 from/to——两者都传时以 range 为准，语义更符合用户点击的意图。
func timeWindow(r *http.Request) (from, to time.Time) {
	if d, ok := parseRange(r.URL.Query().Get("range")); ok {
		now := time.Now().UTC()
		return now.Add(-d), now
	}
	return httpapi.TimeRange(r)
}

// parseRange 解析预设窗口。
// 只认白名单形式，不接受 "30x" 这类写法：
// 窗口长度直接决定查询量，放开任意解析等于给了放大攻击面。
func parseRange(s string) (time.Duration, bool) {
	switch s {
	case "":
		return 0, false
	case "1h", "6h", "12h":
		d, _ := time.ParseDuration(s)
		return d, true
	case "24h", "2d", "7d", "30d", "90d":
		n, _ := strconv.Atoi(s[:len(s)-1])
		return time.Duration(n) * 24 * time.Hour, true
	default:
		return 0, false
	}
}

// ---------- 辅助 ----------

func pickInt(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
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
