package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/m202471895/probeone/server/internal/apperr"
	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/store"
)

type userCtxKey struct{}

// User 是挂在请求上下文里的当前用户。
type User struct {
	ID       int64
	Username string
	Role     model.Role
}

// Role 返回当前用户角色，未登录时返回空。
func Role(ctx context.Context) model.Role {
	u, _ := ctx.Value(userCtxKey{}).(*User)
	if u == nil {
		return ""
	}
	return u.Role
}

// UserID 返回当前用户 ID，未登录时返回 0。
func UserID(ctx context.Context) int64 {
	u, _ := ctx.Value(userCtxKey{}).(*User)
	if u == nil {
		return 0
	}
	return u.ID
}

// Username 返回当前用户名。
func Username(ctx context.Context) string {
	u, _ := ctx.Value(userCtxKey{}).(*User)
	if u == nil {
		return ""
	}
	return u.Username
}

func withUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, userCtxKey{}, u)
}

// Authenticator 负责会话校验。
type Authenticator struct {
	db *store.DB
}

// NewAuthenticator 创建鉴权器。
func NewAuthenticator(db *store.DB) *Authenticator {
	return &Authenticator{db: db}
}

// bearerToken 从 Authorization 头取 Bearer 令牌。
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// RequireAuth 强制登录。
func (a *Authenticator) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			fail(w, r, apperr.Unauthenticated("缺少访问令牌"))
			return
		}
		u, err := a.authenticate(r, token)
		if err != nil {
			fail(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(withUser(r.Context(), u)))
	})
}

// OptionalAuth 可选登录：有令牌就解析，没有也放行。
// 用于公开接口在登录后返回更多信息（如公开状态页）。
func (a *Authenticator) OptionalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		u, err := a.authenticate(r, token)
		if err != nil {
			// 令牌无效就当未登录，不报错——公开接口不该因坏令牌而 401
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(withUser(r.Context(), u)))
	})
}

// authenticate 校验令牌并返回用户。
func (a *Authenticator) authenticate(r *http.Request, token string) (*User, error) {
	// 令牌以哈希形式存储：库里被拖走也拿不到可用凭据
	sess, err := a.db.Sessions.GetByTokenHash(r.Context(), store.HashToken(token))
	if err != nil {
		if apperr.Is(err, "SESSION_NOT_FOUND") || apperr.Is(err, "SESSION_EXPIRED") {
			return nil, apperr.Unauthenticated("登录已失效，请重新登录")
		}
		return nil, err
	}

	// 用户信息不在会话里（避免角色变更后旧会话仍持有旧角色），
	// 每次请求查一次 users —— 有索引且行数极小，代价可接受。
	user, err := a.db.Users.GetByID(r.Context(), sess.UserID)
	if err != nil {
		if apperr.Is(err, "USER_NOT_FOUND") {
			// 用户已被删除但会话还在：清掉会话并要求重新登录
			_ = a.db.Sessions.DeleteByTokenHash(r.Context(), store.HashToken(token))
			return nil, apperr.Unauthenticated("账号已不存在")
		}
		return nil, err
	}
	if user.Status != "active" {
		return nil, apperr.Forbidden("账号已被停用")
	}

	return &User{ID: user.ID, Username: user.Username, Role: user.Role}, nil
}

// RequireRole 要求最低角色。
//
// 角色分阶owner > admin > viewer，
// 用"至少达到"而非"精确匹配"，避免 admin 被 owner 拒绝这种反直觉行为。
func RequireRole(min model.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := Role(r.Context())
			if role == "" {
				fail(w, r, apperr.Unauthenticated("请先登录"))
				return
			}
			if !roleAtLeast(role, min) {
				fail(w, r, apperr.Forbidden("权限不足：需要 " + string(min) + " 及以上角色"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// roleAtLeast 判断角色是否达到最低要求。
func roleAtLeast(have, need model.Role) bool {
	return roleRank(have) >= roleRank(need)
}

// roleRank 给角色定权重。
func roleRank(r model.Role) int {
	switch r {
	case model.RoleOwner:
		return 3
	case model.RoleAdmin:
		return 2
	case model.RoleViewer:
		return 1
	default:
		return 0
	}
}
