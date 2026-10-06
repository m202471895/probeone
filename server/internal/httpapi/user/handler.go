// Package user 处理用户管理的 HTTP 接口。
//
// 全部接口仅限 owner（RequireRole(RoleOwner)）。理由不只是"用户管理很重要"，
// 而是**只有 owner 能改角色**——如果admin 也能改角色，他就能把自己提成
// owner，权限分阶（PRD 3.5）当场失效。所以这不是读接口那种"限制读取范围"，
// 而是限制**权限提升的入口本身**。
package user

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/m202471895/probeone/server/internal/apperr"
	"github.com/m202471895/probeone/server/internal/auth"
	"github.com/m202471895/probeone/server/internal/httpapi"
	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/store"
)

// Handler 是用户管理处理器。
type Handler struct {
	db         *store.DB
	trustProxy bool
}

// New 创建处理器。
func New(db *store.DB, trustProxy bool) *Handler {
	return &Handler{db: db, trustProxy: trustProxy}
}

// Register 挂载用户管理路由。
//
// 全部 owner-only，用与 auditlog 包相同的两层写法（RequireAuth 在外、
// RequireRole 在内）：先确认"是谁"，再确认"能不能"。
func (h *Handler) Register(mux *http.ServeMux, authn *httpapi.Authenticator) {
	owner := func(hf http.HandlerFunc) http.Handler {
		return authn.RequireAuth(httpapi.RequireRole(model.RoleOwner)(hf))
	}
	mux.Handle("GET /api/users", owner(h.list))
	mux.Handle("POST /api/users", owner(h.create))
	mux.Handle("PUT /api/users/{id}", owner(h.update))
	mux.Handle("DELETE /api/users/{id}", owner(h.delete))
}

// userView 是用户对外的 JSON 形状。
//
// 刻意不复用 model.User：model 里有 PasswordHash（argon2id 编码串），
// 独立 DTO 能在编译期保证它漏不出去（PRD 3.6.4 第 2 条）。
// 这与node 包的 nodeView 是同一道防线。
type userView struct {
	ID        int64      `json:"id"`
	Username  string     `json:"username"`
	Email     string     `json:"email"`
	Role      model.Role `json:"role"`
	Status    string     `json:"status"`
	CreatedAt string     `json:"created_at"`
	UpdatedAt string     `json:"updated_at"`
}

const tsLayout = "2006-01-02T15:04:05Z"

func viewOf(u *model.User) userView {
	return userView{
		ID:        u.ID,
		Username:  u.Username,
		Email:     u.Email,
		Role:      u.Role,
		Status:    u.Status,
		CreatedAt: u.CreatedAt.UTC().Format(tsLayout),
		UpdatedAt: u.UpdatedAt.UTC().Format(tsLayout),
	}
}

// list 返回用户列表。
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	page, size := httpapi.PageQuery(r)

	users, err := h.db.Users.List(r.Context(), size, (page-1)*size)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	total, err := h.db.Users.Count(r.Context())
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	items := make([]userView, 0, len(users))
	for i := range users {
		items = append(items, viewOf(&users[i]))
	}
	httpapi.OK(w, r, httpapi.PageData{
		Items: items, Total: total, Page: page, Size: size,
	})
}

// create 创建用户。
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string     `json:"username"`
		Email    string     `json:"email"`
		Password string     `json:"password"`
		Role     model.Role `json:"role"`
	}
	if err := httpapi.DecodeJSON(r, &in); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if err := validateUsername(in.Username); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	in.Email = strings.TrimSpace(in.Email)
	if err := validateEmail(in.Email); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	// 角色缺省给 viewer 而非 owner：新建账号不该默认持有最高权限。
	// 不显式指定就发 owner 意味着"手滑创建了一个超级用户"。
	if in.Role == "" {
		in.Role = model.RoleViewer
	}
	if !in.Role.Valid() {
		httpapi.Fail(w, r, apperr.BadRequest("角色非法（可选 owner / admin / viewer）"))
		return
	}
	if err := validatePassword(in.Password); err != nil {
		httpapi.Fail(w, r, err)
		return
	}

	id, err := h.db.Users.Create(r.Context(), store.CreateUserInput{
		Username: in.Username,
		Email:    in.Email,
		Password: in.Password,
		Role:     in.Role,
	})
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}

	h.audit(r, "create_user", "user", strconv.FormatInt(id, 10), map[string]any{
		"username": in.Username, "role": in.Role,
	})

	// 回读而不是手工拼响应：多返回最新状态，
	// 也保证响应形状只由 viewOf 一处决定。
	createdUser, err := h.db.Users.GetByID(r.Context(), id)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	httpapi.Created(w, r, viewOf(createdUser))
}

// update 更新用户。
//
// 用指针区分"没传这个字段"与"传了零值"：username/status 这类字段
// 空串与不改语义完全不同，用值类型会把"只想改邮箱"的请求
// 变成"把用户名清空"。这也是 node 包 update 用 *bool/*int64 的同一原因。
//
// 密码单独处理：**请求里不带 password 就绝不动密码**。
// PUT 语义上是"更新这个用户的这些字段"，而密码是一个独立的凭据动作，
// 让它搭便车意味着一次"改个邮箱"的请求可能顺手把别人的密码重置了。
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpapi.Fail(w, r, apperr.BadRequest("用户 ID 非法"))
		return
	}
	target, err := h.db.Users.GetByID(r.Context(), id)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	if err := h.denySelf(r, target); err != nil {
		httpapi.Fail(w, r, err)
		return
	}

	var in struct {
		Username *string     `json:"username"`
		Email    *string     `json:"email"`
		Role     *model.Role `json:"role"`
		Status   *string     `json:"status"`
		Password *string     `json:"password"`
	}
	if err := httpapi.DecodeJSON(r, &in); err != nil {
		httpapi.Fail(w, r, err)
		return
	}

	// 角色变更要先校验再落库：非法角色绝不能进到 UpdateRole 里
	if in.Role != nil {
		if !in.Role.Valid() {
			httpapi.Fail(w, r, apperr.BadRequest("角色非法（可选 owner / admin / viewer）"))
			return
		}
		// 降级前的最后一个 owner 检查
		if target.Role == model.RoleOwner && *in.Role != model.RoleOwner {
			if err := h.denyLastOwner(r, target); err != nil {
				httpapi.Fail(w, r, err)
				return
			}
		}
	}
	// 停用 owner 等价于降级：被停用的账号无法登录，
	// 若它是最后一个 owner，系统同样会锁死。
	if in.Status != nil {
		s := strings.TrimSpace(*in.Status)
		if s != "active" && s != "disabled" {
			httpapi.Fail(w, r, apperr.BadRequest("状态非法（可选 active / disabled）"))
			return
		}
		if target.Role == model.RoleOwner && s == "disabled" {
			if err := h.denyLastOwner(r, target); err != nil {
				httpapi.Fail(w, r, err)
				return
			}
		}
	}
	if in.Username != nil {
		if err := validateUsername(strings.TrimSpace(*in.Username)); err != nil {
			httpapi.Fail(w, r, err)
			return
		}
	}
	if in.Email != nil {
		if err := validateEmail(strings.TrimSpace(*in.Email)); err != nil {
			httpapi.Fail(w, r, err)
			return
		}
	}
	if in.Password != nil {
		if err := validatePassword(*in.Password); err != nil {
			httpapi.Fail(w, r, err)
			return
		}
	}

	// changed 记录实际变动的字段名，只进审计 detail、不进响应体。
	// 记字段名而非新旧值：审计日志本身也是资产，写入明文密码或
	// 邮箱等于给攻击者做了一份集中凭据清单。
	changed := make([]string, 0, 5)

	// 仓储层的 Update 接收整个 model.User，白名单校验在调用方完成：
	// 这里先把要改的字段拷进副本，再一次性落库。
	// 用副本而不是就地改 target，是为了让"校验全部通过才落库"
	// 成为结构性保证——中途任何校验失败都不会留下半改状态。
	next := *target
	if in.Username != nil {
		next.Username = strings.TrimSpace(*in.Username)
	}
	if in.Email != nil {
		next.Email = strings.TrimSpace(*in.Email)
	}
	if in.Role != nil {
		next.Role = *in.Role
	}
	if in.Status != nil {
		next.Status = strings.TrimSpace(*in.Status)
	}

	// 只在值真的变了时才落库：无变化的 UPDATE 会白白改掉 updated_at，
	// 让"这个用户最近被改过"这个信号失真。
	profileChanged := next.Username != target.Username || next.Email != target.Email ||
		next.Role != target.Role || next.Status != target.Status
	if profileChanged {
		if err := h.db.Users.Update(r.Context(), &next); err != nil {
			httpapi.Fail(w, r, err)
			return
		}
		if next.Username != target.Username {
			changed = append(changed, "username")
		}
		if next.Email != target.Email {
			changed = append(changed, "email")
		}
		if next.Role != target.Role {
			changed = append(changed, "role")
		}
		if next.Status != target.Status {
			changed = append(changed, "status")
		}
	}

	// 密码走独立方法：它要先做强度校验，且必须能单独触发
	// "踢掉该用户全部会话"这个副作用。混进 Update 里会丢掉两者。
	if in.Password != nil {
		if err := h.db.Users.UpdatePassword(r.Context(), target.ID, *in.Password); err != nil {
			httpapi.Fail(w, r, err)
			return
		}
		changed = append(changed, "password")
		// 改密码后必须踢掉该用户所有会话：否则旧的访问令牌
		// 在有效期内仍能调用接口，"改密码"这个补救手段就形同虚设。
		if err := h.db.Sessions.DeleteAllForUser(r.Context(), target.ID); err != nil {
			httpapi.Fail(w, r, err)
			return
		}
	}

	// 角色/状态变更必须留痕：事后追责时"谁把谁降级了"是关键事实。
	// 密码本身不入审计（detail 里只记 "password" 这个字段名）。
	h.audit(r, "update_user", "user", strconv.FormatInt(target.ID, 10), map[string]any{
		"fields": changed,
	})

	updated, err := h.db.Users.GetByID(r.Context(), target.ID)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	httpapi.OK(w, r, viewOf(updated))
}

// delete 删除用户。
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpapi.Fail(w, r, apperr.BadRequest("用户 ID 非法"))
		return
	}
	target, err := h.db.Users.GetByID(r.Context(), id)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	if err := h.denySelf(r, target); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	if target.Role == model.RoleOwner {
		if err := h.denyLastOwner(r, target); err != nil {
			httpapi.Fail(w, r, err)
			return
		}
	}
	if err := h.db.Users.Delete(r.Context(), target.ID); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	// 冗余清理会话：users→sessions 有 ON DELETE CASCADE，
	// 但显式删除让"会话随用户一起消失"这条不依赖外键行为，
	// 换数据库（外键未强制启用）时也不会漏。
	if err := h.db.Sessions.DeleteAllForUser(r.Context(), target.ID); err != nil {
		httpapi.Fail(w, r, err)
		return
	}

	h.audit(r, "delete_user", "user", strconv.FormatInt(target.ID, 10), map[string]any{
		"username": target.Username, "role": target.Role,
	})
	httpapi.NoContent(w, r)
}

// ---------- 安全约束 ----------

// denySelf 拒绝操作自己。
//
// 理由：owner 能做的操作里，"把owner 降级"和"删掉 owner"是唯二能
// 让系统失去最后一个管理员的路径。如果允许对自己执行，
// 一次误操作（或一个被诱导的误点击）就可能把系统锁死——
// 而此时没有任何人有权把它救回来，只能进数据库手改。
// 自己的账号要改密码请走 /api/auth 下的专用接口，那条路径不经过这里。
func (h *Handler) denySelf(r *http.Request, target *model.User) error {
	if target.ID == httpapi.UserID(r.Context()) {
		return apperr.BadRequest("不能修改或删除自己的账号（否则可能失去唯一的管理员入口）")
	}
	return nil
}

// denyLastOwner 拒绝移除最后一个 owner。
//
// 理由：owner 是唯一能管理用户与可见性策略的角色（ RequireRole(RoleOwner)）。
// 一旦 owner 数量归零且没人能再被提升，系统进入**不可恢复状态**：
// 接口全部 403，且现有接口里没有任何一个能把自己提回 owner。
// 因此"至少保留一个 owner"是不可违反的不变量，不是可选的业务规则。
func (h *Handler) denyLastOwner(r *http.Request, target *model.User) error {
	// 只需要判断"是否还有别的可用 owner"，全表扫描即可：
	// users 表行数极小（管理员数量级），且这条路径只在 owner
	// 变更角色/状态时触发，不是高频操作。
	//
	// List 单页上限 200，用户数超过时会截断。截断方向是**安全**的：
	// 看不到后面的 owner 只会让我们误判"这是最后一个"从而拒绝操作，
	// 而不会误判成"还有别人"从而放行。宁可少删，不可删光。
	users, err := h.db.Users.List(r.Context(), 200, 0)
	if err != nil {
		return err
	}
	for i := range users {
		u := &users[i]
		// 跳过目标本人与已停用的账号：两者都不能承担 owner 职责。
		// 把 disabled 的 owner 计入"还剩一个"是危险的乐观判断——
		// 它永远登不上来，实际等于没有 owner。
		if u.ID == target.ID || u.Status != "active" {
			continue
		}
		if u.Role == model.RoleOwner {
			return nil
		}
	}
	return apperr.BadRequest("系统必须至少保留一个启用状态的 owner，无法删除或降级最后一个 owner")
}

// ---------- 字段校验 ----------

// validatePassword 校验密码强度。
//
// 规则本体在 internal/auth（ValidatePasswordStrength），这里只做错误翻译：
// 强度标准必须全局唯一，否则注册与改密两处标准不一致时弱口令总能钻进来。
func validatePassword(pw string) error {
	if err := auth.ValidatePasswordStrength(pw); err != nil {
		return apperr.BadRequest("密码强度不足：" + err.Error())
	}
	return nil
}

// validateUsername 校验用户名。
//
// 长度上限 64 与 users.username 的列宽一致——在入库前拦下超长值，
// 才能返回"用户名过长"而不是一个含糊的数据库唯一约束冲突。
// 字符集收紧到字母数字与 .-_ ：用户名会出现在审计日志里，
// 允许任意字符等于允许往日志里塞控制字符与终端转义序列。
func validateUsername(name string) error {
	if name == "" {
		return apperr.BadRequest("请填写用户名")
	}
	if n := len([]rune(name)); n > 64 {
		return apperr.BadRequest(fmt.Sprintf("用户名过长（%d 字符，上限 64）", n))
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		switch r {
		case '.', '-', '_':
			continue
		}
		return apperr.BadRequest("用户名只能包含字母、数字与 . - _")
	}
	return nil
}

// validateEmail 校验邮箱。
//
// 刻意只做最浅的检查（单@、有域名、长度 255）。真正的邮箱验证要发确认邮件，
// 那是注册流程的事；这里的职责是拦住明显的垃圾输入并给出可读的错误，
// 重复邮箱由数据库唯一索引兜底并翻译成 409。
func validateEmail(email string) error {
	if email == "" {
		// 邮箱在 schema 里可空，且唯一索引是 partial index（仅非 NULL 参与），
		// 因此空邮箱合法——不能强制必填。
		return nil
	}
	if len(email) > 255 {
		return apperr.BadRequest("邮箱过长（上限 255）")
	}
	at := strings.IndexByte(email, '@')
	if at <= 0 || at == len(email)-1 {
		return apperr.BadRequest("邮箱格式非法")
	}
	if strings.ContainsAny(email, " \t\r\n") {
		return apperr.BadRequest("邮箱格式非法")
	}
	return nil
}

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
