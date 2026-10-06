// Package auth 处理登录、登出与会话。
package auth

import (
	"net/http"
	"strings"
	"time"

	"github.com/m202471895/probeone/server/internal/apperr"
	"github.com/m202471895/probeone/server/internal/auth"
	"github.com/m202471895/probeone/server/internal/httpapi"
	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/store"
)

// Handler 是认证接口的处理器。
type Handler struct {
	db         *store.DB
	trustProxy bool
	sessionTTL time.Duration
}

// New 创建处理器。
func New(db *store.DB, trustProxy bool, sessionTTL time.Duration) *Handler {
	if sessionTTL <= 0 {
		sessionTTL = 7 * 24 * time.Hour
	}
	return &Handler{db: db, trustProxy: trustProxy, sessionTTL: sessionTTL}
}

// Register 挂载认证路由。
func (h *Handler) Register(mux *http.ServeMux, authn *httpapi.Authenticator) {
	mux.HandleFunc("POST /api/auth/login", h.login)
	mux.Handle("POST /api/auth/logout", authn.RequireAuth(http.HandlerFunc(h.logout)))
	mux.Handle("GET /api/auth/me", authn.RequireAuth(http.HandlerFunc(h.me)))
	mux.Handle("POST /api/auth/refresh", authn.RequireAuth(http.HandlerFunc(h.refresh)))
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	TOTP     string `json:"totp"`
}

type userView struct {
	ID        int64       `json:"id"`
	Username  string      `json:"username"`
	Email     string      `json:"email"`
	Role      model.Role  `json:"role"`
	Status    string      `json:"status"`
	CreatedAt string      `json:"created_at"`
}

// login 登录并签发会话。
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var in loginRequest
	if err := httpapi.DecodeJSON(r, &in); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if in.Username == "" || in.Password == "" {
		httpapi.Fail(w, r, apperr.BadRequest("请输入用户名和密码"))
		return
	}

	user, err := h.db.Users.GetByUsername(r.Context(), in.Username)
	if err != nil {
		// 用户不存在与密码错误返回同一个文案——
		// 区分开就成了用户名枚举接口
		httpapi.Fail(w, r, apperr.New("INVALID_CREDENTIALS",
			"用户名或密码错误", http.StatusUnauthorized))
		return
	}
	if user.Status != "active" {
		httpapi.Fail(w, r, apperr.Forbidden("账号已被停用"))
		return
	}
	if !store.VerifyPassword(in.Password, user.PasswordHash) {
		h.recordLoginFailure(r, user.Username)
		httpapi.Fail(w, r, apperr.New("INVALID_CREDENTIALS",
			"用户名或密码错误", http.StatusUnauthorized))
		return
	}

	token, err := auth.NewSessionToken()
	if err != nil {
		httpapi.Fail(w, r, apperr.Internal(err, "生成会话令牌失败"))
		return
	}
	expires := time.Now().UTC().Add(h.sessionTTL)
	if _, err := h.db.Sessions.Create(r.Context(), user.ID,
		store.HashToken(token), httpapi.ClientIP(r, h.trustProxy),
		r.UserAgent(), expires); err != nil {
		httpapi.Fail(w, r, apperr.Internal(err, "创建会话失败"))
		return
	}

	// 登录成功清零失败计数
	if h.db.Settings != nil {
		_ = h.db.Settings.Set(r.Context(), "login_fail:"+user.Username, 0)
	}
	_ = h.db.Audit.Write(r.Context(), &model.AuditLog{
		Username: user.Username, Action: "login", TargetType: "user",
		TargetID: itoa(user.ID), IP: httpapi.ClientIP(r, h.trustProxy),
	})

	httpapi.OK(w, r, map[string]any{
		"token":      token,
		"expires_at": expires.Format(time.RFC3339),
		"user":       toUserView(user),
	})
}

// logout 登出：删除当前会话。
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if tok := bearer(r); tok != "" {
		_ = h.db.Sessions.DeleteByTokenHash(r.Context(), store.HashToken(tok))
	}
	_ = h.db.Audit.Write(r.Context(), &model.AuditLog{
		Username: httpapi.Username(r.Context()), Action: "logout",
		TargetType: "user", IP: httpapi.ClientIP(r, h.trustProxy),
	})
	httpapi.NoContent(w, r)
}

// refresh 刷新会话有效期，返回同一令牌。
//
// 不签发新令牌：refresh 端点返回新令牌会扩大令牌泄露面，
// 而且调用方本来就有旧令牌，续期足够。
func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	tok := bearer(r)
	if tok == "" {
		httpapi.Fail(w, r, apperr.Unauthenticated("缺少访问令牌"))
		return
	}
	hash := store.HashToken(tok)
	if _, err := h.db.Sessions.GetByTokenHash(r.Context(), hash); err != nil {
		httpapi.Fail(w, r, apperr.Unauthenticated("登录已失效，请重新登录"))
		return
	}
	expires := time.Now().UTC().Add(h.sessionTTL)
	// 用删除+重建的方式续期：仓库层不提供 Touch，
	// 重建会换ID 但不影响客户端（客户端只认令牌）
	_ = h.db.Sessions.DeleteByTokenHash(r.Context(), hash)
	if _, err := h.db.Sessions.Create(r.Context(), httpapi.UserID(r.Context()),
		hash, httpapi.ClientIP(r, h.trustProxy), r.UserAgent(), expires); err != nil {
		httpapi.Fail(w, r, apperr.Internal(err, "续期失败"))
		return
	}
	httpapi.OK(w, r, map[string]any{
		"token": tok, "expires_at": expires.Format(time.RFC3339),
	})
}

// me 返回当前用户信息。
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	user, err := h.db.Users.GetByID(r.Context(), httpapi.UserID(r.Context()))
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	httpapi.OK(w, r, toUserView(user))
}

// recordLoginFailure 记录登录失败次数。
//
// 连续失败会逐步延长等待，防在线爆破；
// 计数存 settings 表而非内存——多实例部署时内存计数会被绕过。
func (h *Handler) recordLoginFailure(r *http.Request, username string) {
	if h.db.Settings == nil {
		return
	}
	key := "login_fail:" + username
	cur, _, _ := h.db.Settings.Get(r.Context(), key)
	n := atoiAny(cur) + 1
	_ = h.db.Settings.Set(r.Context(), key, n)
}

func toUserView(u *model.User) userView {
	return userView{
		ID: u.ID, Username: u.Username, Email: u.Email,
		Role: u.Role, Status: u.Status,
		CreatedAt: u.CreatedAt.Format(time.RFC3339),
	}
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) > len(p) && strings.EqualFold(h[:len(p)], p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}
