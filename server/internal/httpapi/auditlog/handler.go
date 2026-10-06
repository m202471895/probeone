// Package auditlog 提供审计日志的只读查询接口。
//
// 独立成包而不是并入 alert：审计日志的**授权模型**与其他包不同——
// 它只对 owner 开放，且不提供任何写接口（写入由各业务包在操作发生时顺带完成）。
// 放进 alert 包会让"告警"这个业务概念被动包含"系统级审计"，
// 读代码的人很容易以为调用 alert 的 Register 就能拿到审计路由。
package auditlog

import (
	"net/http"
	"strconv"
	"time"

	"github.com/m202471895/probeone/server/internal/httpapi"
	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/store"
)

// Handler 是审计日志处理器。
type Handler struct {
	db *store.DB
}

// New 创建处理器。
//
// 不需要 trustProxy：审计里的 IP 是**写入时**记录的（各业务包持有
// trustProxy 配置），读取时再解析一次没有意义。
func New(db *store.DB) *Handler {
	return &Handler{db: db}
}

// Register 挂载审计路由。
//
// 为什么限owner：审计日志会暴露全部人的操作记录——谁在什么时候
// 改了哪个目标、谁的账号登录失败过多少次。它是管理层与事后追责的工具，
// 把它开放给 admin 就等于让管理员能互相监视（PRD 3.5 角色分阶）。
// 也因此本包只有 GET：能读不能改，防止有人清理自己的痕迹。
func (h *Handler) Register(mux *http.ServeMux, authn *httpapi.Authenticator) {
	owner := func(hf http.HandlerFunc) http.Handler {
		return authn.RequireAuth(httpapi.RequireRole(model.RoleOwner)(hf))
	}
	mux.Handle("GET /api/audit-logs", owner(h.list))
}

// auditView 是审计日志的 JSON 形状。
//
// 不含 user_agent：UA 是客户端可控的任意字符串，攻击者可以往里塞
// 几 MB 数据。留着它对追溯来源没有帮助（IP 才是有效线索），
// 却让这张表成了一个可被撑大的存储放大器。
type auditView struct {
	ID         int64          `json:"id"`
	UserID     *int64         `json:"user_id"`
	Username   string         `json:"username"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	Detail     map[string]any `json:"detail"`
	IP         string         `json:"ip"`
	CreatedAt  string         `json:"created_at"`
}

func viewOf(l *model.AuditLog) auditView {
	return auditView{
		ID: l.ID, UserID: l.UserID, Username: l.Username,
		Action: l.Action, TargetType: l.TargetType, TargetID: l.TargetID,
		Detail: l.Detail, IP: l.IP,
		CreatedAt: l.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// list 返回审计日志列表。
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	page, size := httpapi.PageQuery(r)
	from, to := httpapi.TimeRange(r)

	// userID 只允许 owner 传 filter 时按人筛选——owner 本来就能看全部，
	// 这不构成越权。普通用户即使伪造这个参数也进不到这里（路由已限 owner）。
	var userID *int64
	if v := r.URL.Query().Get("user_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			userID = &n
		}
		// 解析失败就当作未传：user_id 只是个筛选条件，
		// 拼错时返回全量比返回 400 更不易让人误以为系统坏了
	}

	logs, total, err := h.db.Audit.List(r.Context(), userID,
		r.URL.Query().Get("action"),
		r.URL.Query().Get("target_type"),
		r.URL.Query().Get("target_id"),
		from, to, size, (page-1)*size)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	items := make([]auditView, 0, len(logs))
	for i := range logs {
		items = append(items, viewOf(&logs[i]))
	}
	httpapi.OK(w, r, httpapi.PageData{
		Items: items, Total: total, Page: page, Size: size,
	})
}
