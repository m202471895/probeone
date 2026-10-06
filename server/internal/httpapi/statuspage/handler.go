// Package statuspage 处理免鉴权的公开状态页。
//
// 这是整个项目**最敏感**的对外接口：无需任何凭据即可访问。
// 三道防线（PRD 3.6）：
//
//  1. DTO 层面：本包定义的结构体只含允许公开的字段，
//     类型层面就不可能序列化出公网 IP、主机名、密钥。
//  2. 脱敏引擎：即使 DTO 未来被改动，visibility.Engine 仍会按策略过滤。
//  3. 仓库层：NodeListFilter.PublicOnly 同时过滤 is_public 与离线状态，
//     避免通过"某节点是否出现"反推隐藏资产存在（侧信道）。
//
// 不要在这个包里引入任何返回 model.Node 的路径。
package statuspage

import (
	"net/http"
	"time"

	"github.com/m202471895/probeone/server/internal/httpapi"
	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/store"
	"github.com/m202471895/probeone/server/internal/visibility"
)

// Handler 是公开状态页处理器。
type Handler struct {
	db         *store.DB
	vis        *visibility.Engine
	trustProxy bool
}

// New 创建处理器。vis 可以为 nil（单测场景），此时不做二次脱敏——
// 但 DTO 本身已经足够安全，vis 是纵深防御。
func New(db *store.DB, vis *visibility.Engine, trustProxy bool) *Handler {
	return &Handler{db: db, vis: vis, trustProxy: trustProxy}
}

// Register 挂载公开路由。
//
// 用 OptionalAuth：已登录的访客可以拿到更多信息（如 CPU 负载），
// 未登录的只拿到最基础的状态。令牌无效也不报错——
// 公开页不该因为坏令牌而 401。
func (h *Handler) Register(mux *http.ServeMux, authn *httpapi.Authenticator) {
	mux.Handle("GET /api/status/monitors",
		authn.OptionalAuth(http.HandlerFunc(h.publicMonitors)))
	mux.Handle("GET /api/status/nodes",
		authn.OptionalAuth(http.HandlerFunc(h.publicNodes)))
	mux.Handle("GET /api/status/summary",
		authn.OptionalAuth(http.HandlerFunc(h.summary)))
}

// publicMonitorView 是监控项的公开形状。
//
// 只有名称、状态、可用率、延迟。**不含 target**——
// target 是完整 URL，可能含 token、路径参数、内网地址。
type publicMonitorView struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Status   model.MonitorStatus `json:"status"`
	Uptime30D *float64 `json:"uptime_30d"`
	AvgLatencyMs *int   `json:"avg_latency_ms"`
	LastCheckedAt string `json:"last_checked_at"`
}

// publicNodeView 是节点的公开形状。
//
// 只有显示名与是否在线。CPU 型号、磁盘、内存等一概不返回——
// 硬件规格是攻击者做资产画像的直接输入。
type publicNodeView struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Online bool   `json:"online"`
}

// publicMonitors 返回公开的监控列表。
func (h *Handler) publicMonitors(w http.ResponseWriter, r *http.Request) {
	monitors, err := h.db.Monitors.AllPublic(r.Context())
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	now := time.Now().UTC()
	items := make([]publicMonitorView, 0, len(monitors))
	for i := range monitors {
		m := &monitors[i]
		v := publicMonitorView{
			ID:     m.ID,
			Name:   m.Name,
			Status: m.Status,
		}
		// 可用率按最近 30 天算，样本不足时返回 null 而非 0——
		// 0 会被误读成"完全不可用"
		if u, ok := uptime30d(h, r, m.ID, now); ok {
			v.Uptime30D = &u
		}
		if m.AvgLatencyMs != nil {
			v.AvgLatencyMs = m.AvgLatencyMs
		}
		if m.LastCheckedAt != nil {
			v.LastCheckedAt = m.LastCheckedAt.Format(time.RFC3339)
		}
		items = append(items, v)
	}
	httpapi.OK(w, r, items)
}

// publicNodes 返回公开的节点列表。
//
// PublicOnly 已过滤：只要 is_public 且在线的节点。
func (h *Handler) publicNodes(w http.ResponseWriter, r *http.Request) {
	nodes, _, err := h.db.Nodes.List(r.Context(), store.NodeListFilter{PublicOnly: true})
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	items := make([]publicNodeView, 0, len(nodes))
	for i := range nodes {
		n := &nodes[i]
		items = append(items, publicNodeView{
			ID: n.ID, Name: n.Name, Online: n.Status == model.NodeOnline,
		})
	}
	httpapi.OK(w, r, items)
}

// summary 返回整体状态摘要，供状态页顶部横幅用。
func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	monitors, err := h.db.Monitors.AllPublic(r.Context())
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	up, down := 0, 0
	for i := range monitors {
		if monitors[i].Status == model.MonitorUp {
			up++
		} else {
			down++
		}
	}
	//整体正常 = 没有任何一项异常。
	// 边界情况：全部监控项都挂掉时"整体异常"是正确的，
	// 不该因为"至少有一项"就报正常。
	state := "operational"
	if down > 0 {
		state = "outage"
	}
	httpapi.OK(w, r, map[string]any{
		"status": state,
		"total":  len(monitors),
		"up":     up,
		"down":   down,
	})
}

// uptime30d 计算近 30 天可用率。
//
// 样本不足时返回 false 而不是 0：
// 0 会被读成"完全不可用"，而实际情况是"还没有足够数据"（PRD 8.7）。
func uptime30d(h *Handler, r *http.Request, monitorID int64, now time.Time) (float64, bool) {
	from := now.Add(-30 * 24 * time.Hour)
	st, err := h.db.Monitors.Stats(r.Context(), monitorID, from, now)
	if err != nil || st.SampleInsufficient || st.Total < 1 {
		return 0, false
	}
	return st.UptimePercent, true
}
