package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m202471895/probeone/server/internal/apperr"
	"github.com/m202471895/probeone/server/internal/config"
	"github.com/m202471895/probeone/server/internal/migrate"
	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/store"
	"github.com/m202471895/probeone/server/internal/store/sqlite"
)

// setupAuthDB 建一个带迁移的测试库。
// 鉴权测试必须用真实库：RequireAuth 的分支判断依赖
// apperr.Is(err, "SESSION_NOT_FOUND")，而这个错误是
// store 层在 SQL 返回 sql.ErrNoRows 时构造的——
// mock 出来的假 store 不会产生这个错误，测试就测不到真正的分支。
func setupAuthDB(t *testing.T) *store.DB {
	t.Helper()
	handle, err := sqlite.Open(&config.DatabaseConfig{
		Driver: "sqlite",
		Path:   filepath.Join(t.TempDir(), "auth.db"),
	})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })

	if err := migrate.New(handle, migrate.Builtin(), ".").Up(context.Background()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return store.NewWithDialect(handle, "sqlite")
}

// ---------- bearerToken ----------

func TestBearerToken_各种畸形输入(t *testing.T) {
	// 关键安全属性：只有严格的 "Bearer <token>" 形态才算数。
	// 任何宽松解析（如按空格 split 后取最后一段）都会让
	// "Basic abc" 或 "Bearer" 这类头部被误认成有效凭据。
	cases := []struct {
		name   string
		header string
		want   string
		why    string
	}{
		{"标准形态", "Bearer abc123", "abc123", "正常用法"},
		// RFC 7235 规定 auth-scheme 大小写不敏感，
		// 现实中 curl / 某些 SDK 会发 "bearer"
		{"scheme 小写", "bearer abc123", "abc123", "auth-scheme 大小写不敏感是协议要求"},
		{"scheme 大写", "BEARER abc123", "abc123", "同上"},
		{"scheme 混合大小写", "BeArEr abc123", "abc123", "同上"},
		{"令牌前后有空格", "Bearer   abc123  ", "abc123", "空白应被裁掉"},
		{"缺 scheme", "abc123", "", "没有 Bearer 前缀一律不认"},
		{"scheme 拼错", "Bearers abc123", "", "前缀必须完整匹配"},
		{"用 Basic 代替", "Basic abc123", "", "其他认证方案不被接受"},
		{"只有 scheme 无令牌", "Bearer ", "", "空令牌等同于没带凭据"},
		{"只有 scheme 无空格无令牌", "Bearer", "", "长度不足"},
		{"空头", "", "", "无Authorization 头"},
		{"令牌里含 Bearer", "Bearer Bearer x", "Bearer x", "只剥一层前缀"},
		{"多个空格分隔的段", "Bearer abc def", "abc def", "令牌本身可能含空格，不做二次切分"},
		{"Token 方案", "Token abc123", "", "只支持 Bearer"},
		{"分号分隔", "Bearer;abc123", "", "分隔符必须是空格"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/x", nil)
			if c.header != "" {
				r.Header.Set("Authorization", c.header)
			}
			if got := bearerToken(r); got != c.want {
				t.Errorf("bearerToken(%q) = %q，期望 %q。%s", c.header, got, c.want, c.why)
			}
		})
	}
}

func TestBearerToken_不以令牌内容做判断(t *testing.T) {
	// 反向断言：解析只认头部形态，与令牌长什么样无关。
	// 若实现里加了"长度校验"或"字符集校验"这类逻辑，
	// 就会把某些合法令牌判为无效——那属于凭空制造的问题。
	valid := []string{
		"Bearer " + strings.Repeat("a", 8),   // 短令牌
		"Bearer " + strings.Repeat("b", 512), // 长令牌
		"Bearer 12345678-1234-1234-1234-123456789012",
		"Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig",
		"Bearer " + strings.Repeat("=", 32), // base64 padding
	}
	for _, h := range valid {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.Header.Set("Authorization", h)
		if bearerToken(r) == "" {
			t.Errorf("合法令牌被误判为无效: Authorization=%q", h)
		}
	}
}

// ---------- roleAtLeast ----------

func TestRoleAtLeast_三档角色矩阵(t *testing.T) {
	// 角色是分阶的：owner > admin > viewer。
	// 用"至少达到"而非"精确匹配"，是为了避免 admin 被 owner-only
	// 的接口拒绝这种反直觉行为——但前提是矩阵本身正确，
	// 任何一格填错就是越权或误拒。
	type row struct {
		have model.Role
		want map[model.Role]bool
	}
	cases := []row{
		{have: model.RoleOwner, want: map[model.Role]bool{
			model.RoleOwner: true, model.RoleAdmin: true, model.RoleViewer: true,
		}},
		{have: model.RoleAdmin, want: map[model.Role]bool{
			model.RoleOwner: false, model.RoleAdmin: true, model.RoleViewer: true,
		}},
		{have: model.RoleViewer, want: map[model.Role]bool{
			model.RoleOwner: false, model.RoleAdmin: false, model.RoleViewer: true,
		}},
		// 未登录/ 角色被清空：任何门槛都不该通过。
		// 这是最重要的一格——它为0 意味着 RequireRole 会先拦下空角色，
		// 但 roleAtLeast 本身也必须守住，不能依赖上层。
		{have: "", want: map[model.Role]bool{
			model.RoleOwner: false, model.RoleAdmin: false, model.RoleViewer: false,
		}},
		// 未知角色：按最低权限处理，绝不放行
		{have: model.Role("superuser"), want: map[model.Role]bool{
			model.RoleOwner: false, model.RoleAdmin: false, model.RoleViewer: false,
		}},
		{have: model.Role("admin "), want: map[model.Role]bool{ // 带空格，不应匹配 admin
			model.RoleOwner: false, model.RoleAdmin: false, model.RoleViewer: false,
		}},
		{have: model.Role("Admin"), want: map[model.Role]bool{ // 大写，不应匹配 admin
			model.RoleOwner: false, model.RoleAdmin: false, model.RoleViewer: false,
		}},
	}

	for _, c := range cases {
		for need, want := range c.want {
			t.Run(string(c.have)+"_需要"+string(need), func(t *testing.T) {
				got := roleAtLeast(c.have, need)
				if got != want {
					t.Errorf("roleAtLeast(%q, %q) = %v，期望 %v", c.have, need, got, want)
				}
			})
		}
	}
}

func TestRoleRank_权重严格递减(t *testing.T) {
	// 权重必须严格递减，否则"至少"的语义就不成立。
	// 相等会导致 viewer 拿到 admin 权限。
	o, a, v := roleRank(model.RoleOwner), roleRank(model.RoleAdmin), roleRank(model.RoleViewer)
	if !(o > a && a > v) {
		t.Errorf("角色权重应严格递减，实际 owner=%d admin=%d viewer=%d", o, a, v)
	}
	if v < 1 {
		t.Errorf("viewer 权重 = %d，必须 ≥1，否则会被当成未登录", v)
	}
	if roleRank("") != 0 || roleRank("bogus") != 0 {
		t.Error("未知角色权重应为 0")
	}
}

// ---------- RequireAuth ----------

// protectedHandler 是一个"必须登录才能访问"的处理器。
// 到达这里就说明鉴权通过。
var protectedCalled bool

func protectedHandler(w http.ResponseWriter, r *http.Request) {
	protectedCalled = true
	w.WriteHeader(http.StatusOK)
}

func TestRequireAuth_缺令牌返回401(t *testing.T) {
	// 最基本的防线：没带凭据一律401。
	// 关键在于"在进入业务逻辑之前"就返回，
	// 所以这里用一个会记录调用的处理器来验证它没被碰到。
	cases := []struct {
		name   string
		header string
	}{
		{"完全无Authorization", ""},
		{"非Bearer 方案", "Basic YWRtaW46YWRtaW4="},
		{"Bearer 但无令牌", "Bearer "},
		{"仅 Bearer 字样", "Bearer"},
		{"伪造的 scheme", "Bearersome-token"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := setupAuthDB(t)
			authn := NewAuthenticator(db)

			protectedCalled = false
			h := authn.RequireAuth(http.HandlerFunc(protectedHandler))

			r := httptest.NewRequest(http.MethodGet, "/api/nodes", nil)
			if c.header != "" {
				r.Header.Set("Authorization", c.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("状态码 = %d，期望 401", rec.Code)
			}
			if protectedCalled {
				t.Error("业务处理器被调用了——鉴权失败却放行，这是越权")
			}
			// 响应不应泄漏"为什么失败"
			body := rec.Body.String()
			if strings.Contains(body, "sql") || strings.Contains(body, "SELECT") {
				t.Errorf("401 响应泄漏了内部细节: %s", body)
			}
			var env Response
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("响应不是合法 JSON: %v", err)
			}
			if env.Code == "" {
				t.Error("响应缺少 code 字段，前端无法做文案映射")
			}
		})
	}
}

func TestRequireAuth_伪造或过期令牌返回401(t *testing.T) {
	// 无效令牌必须与"没带令牌"一样被拒。
	// 令牌在库里以 SHA256 存储，攻击者拿到库也换不出可用凭据。
	db := setupAuthDB(t)
	authn := NewAuthenticator(db)

	// 造一个真实用户，用于"令牌指向已删用户"等场景
	uid, err := db.Users.Create(context.Background(), store.CreateUserInput{
		Username: "admin", Password: "Admin123456", Role: model.RoleOwner,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 一个真实但已过期的会话
	if _, err := db.Sessions.Create(context.Background(), uid,
		store.HashToken("expired-token"), "1.2.3.4", "test",
		time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		token string
	}{
		{"完全随机的令牌", "totally-made-up-token-value"},
		// 已知用户名但令牌错：不得因为"用户存在"就放行
		{"用户名正确令牌错误", "wrong-token-for-admin"},
		{"已过期的真实令牌", "expired-token"},
		// SHA256 长度但内容不对：攻击者按格式伪造
		{"伪造的十六进制令牌", strings.Repeat("a", 64)},
		{"空令牌", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			protectedCalled = false
			h := authn.RequireAuth(http.HandlerFunc(protectedHandler))

			r := httptest.NewRequest(http.MethodGet, "/api/nodes", nil)
			if c.token != "" {
				r.Header.Set("Authorization", "Bearer "+c.token)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("状态码 = %d，期望 401（令牌 %q 不应被接受）", rec.Code, c.token)
			}
			if protectedCalled {
				t.Error("无效令牌却放行了业务处理器")
			}
			// 关键：不同失败原因不能给出可区分的响应，
			// 否则可用来探测"这个令牌是否对应某个用户"
			if !strings.Contains(rec.Body.String(), "UNAUTHENTICATED") {
				t.Errorf("响应 = %s，期望 code=UNAUTHENTICATED", rec.Body.String())
			}
		})
	}
}

func TestRequireAuth_有效令牌放行并注入用户(t *testing.T) {
	// 正向路径：合法令牌必须能通过，且上下文里能拿到用户信息。
	// 角色每次都从DB 查（不存会话里），这样角色变更后
	// 旧会话不会继续持有旧权限——这条不变量需要一个正向用例来锚定。
	db := setupAuthDB(t)
	authn := NewAuthenticator(db)

	uid, err := db.Users.Create(context.Background(), store.CreateUserInput{
		Username: "viewer1", Password: "Viewer12345", Role: model.RoleViewer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Sessions.Create(context.Background(), uid,
		store.HashToken("good-token"), "1.2.3.4", "test", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	var (
		gotID       int64
		gotUsername string
		gotRole     model.Role
	)
	h := authn.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = UserID(r.Context())
		gotUsername = Username(r.Context())
		gotRole = Role(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodGet, "/api/nodes", nil)
	r.Header.Set("Authorization", "Bearer good-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；响应 = %s", rec.Code, rec.Body.String())
	}
	if gotID != uid {
		t.Errorf("上下文用户 ID = %d，期望 %d", gotID, uid)
	}
	if gotUsername != "viewer1" {
		t.Errorf("上下文用户名 = %q，期望 viewer1", gotUsername)
	}
	if gotRole != model.RoleViewer {
		t.Errorf("上下文角色 = %q，期望 viewer", gotRole)
	}
}

func TestRequireAuth_角色变更后旧会话立即降权(t *testing.T) {
	// 不变式：角色不缓存在会话里。
	// 若哪天为了性能把角色塞进 session，这里会失败——
	// 那意味着被降为viewer 的用户，在会话过期前仍是admin。
	db := setupAuthDB(t)
	authn := NewAuthenticator(db)

	uid, err := db.Users.Create(context.Background(), store.CreateUserInput{
		Username: "u1", Password: "Password12345", Role: model.RoleAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Sessions.Create(context.Background(), uid,
		store.HashToken("role-token"), "1.2.3.4", "test", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	getRole := func() model.Role {
		var role model.Role
		h := authn.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role = Role(r.Context())
		}))
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.Header.Set("Authorization", "Bearer role-token")
		h.ServeHTTP(httptest.NewRecorder(), r)
		return role
	}

	if got := getRole(); got != model.RoleAdmin {
		t.Fatalf("初始角色 = %q，期望 admin", got)
	}
	// 降级为 viewer，会话本身不换
	if err := db.Users.UpdateRole(context.Background(), uid, model.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if got := getRole(); got != model.RoleViewer {
		t.Errorf("降级后旧会话仍持有角色 %q，应立即变为 viewer——"+
			"角色变更对已登录用户不生效是严重越权", got)
	}
}

func TestRequireAuth_已停用账号返回403(t *testing.T) {
	// 停用与无效是两回事：401 让人重新登录（没用），
	// 403 明确告诉他"账号被停用"，前端才能给出正确提示。
	db := setupAuthDB(t)
	authn := NewAuthenticator(db)

	uid, err := db.Users.Create(context.Background(), store.CreateUserInput{
		Username: "disabled", Password: "Password12345", Role: model.RoleAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Sessions.Create(context.Background(), uid,
		store.HashToken("disabled-token"), "1.2.3.4", "test", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.ExecContext(context.Background(),
		`UPDATE users SET status = 'disabled' WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}

	protectedCalled = false
	h := authn.RequireAuth(http.HandlerFunc(protectedHandler))
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Header.Set("Authorization", "Bearer disabled-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusForbidden {
		t.Errorf("状态码 = %d，期望 403（停用账号应明确告知而非让用户重新登录）", rec.Code)
	}
	if protectedCalled {
		t.Error("已停用账号竟被放行")
	}
}

func TestRequireAuth_用户已删除时会话被清理(t *testing.T) {
	// 用户被删但会话还在（数据库没有外键级联）：
	// 此时必须清掉会话并要求重新登录，
	// 否则删号后那个令牌还能用最长 7 天（sessionTTL）。
	db := setupAuthDB(t)
	authn := NewAuthenticator(db)

	uid, err := db.Users.Create(context.Background(), store.CreateUserInput{
		Username: "ghost", Password: "Password12345", Role: model.RoleOwner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Sessions.Create(context.Background(), uid,
		store.HashToken("ghost-token"), "1.2.3.4", "test", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// 直接删用户，绕过任何"同时删会话"的业务逻辑
	if _, err := db.SQL.ExecContext(context.Background(),
		`DELETE FROM users WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}

	h := authn.RequireAuth(http.HandlerFunc(protectedHandler))
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Header.Set("Authorization", "Bearer ghost-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("状态码 = %d，期望 401", rec.Code)
	}
	var n int
	db.SQL.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM sessions WHERE token_hash = ?`,
		store.HashToken("ghost-token")).Scan(&n)
	if n != 0 {
		t.Error("用户已删除后其会话未被清理——孤儿会话可继续使用直到过期")
	}
}

// ---------- OptionalAuth ----------

func TestOptionalAuth_三种放行策略(t *testing.T) {
	// 公开接口不该因坏令牌而 401：
	// 用户浏览器里有个过期令牌是常态，
	// 让他看到"登录已失效"而不是公开内容，体验与安全都更差。
	db := setupAuthDB(t)
	authn := NewAuthenticator(db)

	uid, err := db.Users.Create(context.Background(), store.CreateUserInput{
		Username: "opt", Password: "Password12345", Role: model.RoleAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Sessions.Create(context.Background(), uid,
		store.HashToken("opt-token"), "1.2.3.4", "test", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		header   string
		wantCode int
		wantRole model.Role
	}{
		{"无令牌按游客放行", "", http.StatusOK, ""},
		{"坏令牌也按游客放行", "Bearer garbage-token-value", http.StatusOK, ""},
		{"有效令牌带上用户", "Bearer opt-token", http.StatusOK, model.RoleAdmin},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotRole model.Role
			h := authn.OptionalAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotRole = Role(r.Context())
				w.WriteHeader(http.StatusOK)
			}))
			r := httptest.NewRequest(http.MethodGet, "/public/status", nil)
			if c.header != "" {
				r.Header.Set("Authorization", c.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)

			if rec.Code != c.wantCode {
				t.Errorf("状态码 = %d，期望 %d", rec.Code, c.wantCode)
			}
			if gotRole != c.wantRole {
				t.Errorf("角色 = %q，期望 %q", gotRole, c.wantRole)
			}
		})
	}
}

// ---------- RequireRole ----------

func TestRequireRole_按角色门槛放行或拒绝(t *testing.T) {
	// RequireRole 依赖上下文里的角色，所以这里直接构造带角色的 context，
	// 不走完整鉴权链——本用例只考察门槛判定本身。
	cases := []struct {
		name     string
		role     model.Role
		min      model.Role
		wantCode int
	}{
		{"owner 访问 owner 接口", model.RoleOwner, model.RoleOwner, http.StatusOK},
		{"admin 访问 owner 接口", model.RoleAdmin, model.RoleOwner, http.StatusForbidden},
		{"viewer 访问 owner 接口", model.RoleViewer, model.RoleOwner, http.StatusForbidden},
		{"admin 访问 admin 接口", model.RoleAdmin, model.RoleAdmin, http.StatusOK},
		{"viewer 访问 admin 接口", model.RoleViewer, model.RoleAdmin, http.StatusForbidden},
		{"viewer 访问 viewer 接口", model.RoleViewer, model.RoleViewer, http.StatusOK},
		// 分阶的核心价值：高角色能访问低权限接口，
		// 避免 admin 被只读接口拒绝这种反直觉行为
		{"owner 访问 viewer 接口", model.RoleOwner, model.RoleViewer, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := RequireRole(c.min)(http.HandlerFunc(protectedHandler))
			r := httptest.NewRequest(http.MethodGet, "/x", nil)
			r = r.WithContext(withUser(r.Context(), &User{ID: 1, Role: c.role}))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != c.wantCode {
				t.Errorf("状态码 = %d，期望 %d", rec.Code, c.wantCode)
			}
		})
	}
}

func TestRequireRole_未登录返回401而非403(t *testing.T) {
	// 401 与 403 必须区分：401 = "去登录"，
	// 403 = "登录了也没用"。返回 403 会让用户反复点登录按钮。
	// 真正的未登录是空角色——只有它该走 401。
	h := RequireRole(model.RoleAdmin)(http.HandlerFunc(protectedHandler))

	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r = r.WithContext(withUser(r.Context(), &User{ID: 0, Role: ""}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("空角色时状态码 = %d，期望 401", rec.Code)
	}

	// 完全不带用户信息（连 User 都没有）也应视作未登录
	r2 := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec2 := httptest.NewRecorder()
	RequireRole(model.RoleAdmin)(http.HandlerFunc(protectedHandler)).ServeHTTP(rec2, r2)
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("上下文无用户时状态码 = %d，期望 401", rec2.Code)
	}
}

func TestRequireRole_非法角色被拒为403(t *testing.T) {
	// 库里出现非法角色时（如被直接改库、或将来加了自定义角色），
	// 必须 fail-closed：拒绝访问，而不是因为角色"非空"就放行。
	//
	// 403 而非 401 在此是对的：用户确实已登录（上下文里有 User），
	// 只是角色无法识别。返回 401 会诱导客户端反复重新登录。
	// 真正的防线在写入侧——repo_user.UpdateRole 与 user handler
	// 都调了 Role.Valid()，所以这条路在正常流程下走不到；
	// 本用例守的是"万一有值漏进来了"的情况。
	h := RequireRole(model.RoleViewer)(http.HandlerFunc(protectedHandler))
	for _, role := range []model.Role{"bogus", "Admin", "admin ", "OWNER", "root"} {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		r = r.WithContext(withUser(r.Context(), &User{ID: 1, Role: role}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Errorf("非法角色 %q 时状态码 = %d，期望 403（必须 fail-closed）", role, rec.Code)
		}
	}
}

func TestRequireRole_拒绝时提示所需角色(t *testing.T) {
	// 错误信息里带上所需角色，前端可直接展示，
	// 管理员能立刻知道该找谁要权限——省掉一轮工单往返。
	h := RequireRole(model.RoleOwner)(http.HandlerFunc(protectedHandler))
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r = r.WithContext(withUser(r.Context(), &User{ID: 1, Role: model.RoleViewer}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	body := rec.Body.String()
	if !strings.Contains(body, string(model.RoleOwner)) {
		t.Errorf("响应 = %s，未说明所需角色 %q", body, model.RoleOwner)
	}
	if !strings.Contains(body, "FORBIDDEN") {
		t.Errorf("响应 = %s，期望 code=FORBIDDEN", body)
	}
}

// ---------- 上下文辅助 ----------

func TestContextAccessors_未登录时返回零值(t *testing.T) {
	// 未登录时返回零值而不是 panic：
	// 这些函数被公开接口大量调用，那边没有鉴权中间件。
	ctx := context.Background()
	if got := Role(ctx); got != "" {
		t.Errorf("Role = %q，期望空串", got)
	}
	if got := UserID(ctx); got != 0 {
		t.Errorf("UserID = %d，期望 0", got)
	}
	if got := Username(ctx); got != "" {
		t.Errorf("Username = %q，期望空串", got)
	}
}

func TestContextAccessors_类型不匹配时不panic(t *testing.T) {
	// 上下文里塞了别的类型时应安全回落。
	// 用 panic 恢复来验证：这类问题在生产里表现为 goroutine 崩溃，
	// 一个请求就能打挂整个服务。
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("上下文类型不匹配导致 panic: %v", r)
		}
	}()
	// 故意用错误的 key 类型
	ctx := context.WithValue(context.Background(), userCtxKey{}, "not-a-user-pointer")
	_ = Role(ctx)
	_ = UserID(ctx)
	_ = Username(ctx)
}

func TestRequestIDFrom_透传与缺省(t *testing.T) {
	if got := RequestIDFrom(context.Background()); got != "" {
		t.Errorf("无 request_id 时 = %q，期望空串", got)
	}
	ctx := withRequestID(context.Background(), "req-abc123")
	if got := RequestIDFrom(ctx); got != "req-abc123" {
		t.Errorf("RequestIDFrom = %q，期望 req-abc123", got)
	}
}

// ---------- apperr 契约 ----------

func TestFail_区分5xx与4xx的日志级别(t *testing.T) {
	// 5xx 记完整错误（含内部 cause），4xx 只记摘要。
	// 这条区分很重要：4xx 是用户的错，5xx 是我们的错，
	// 混在一起会让告警噪声淹没真正的故障。
	// 这里只断言对外行为一致（状态码与文案），日志级别不直接断言。
	cases := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"未分类错误", errString("boom"), http.StatusInternalServerError},
		{"参数错误", apperr.BadRequest("x"), http.StatusBadRequest},
		{"未认证", apperr.Unauthenticated("y"), http.StatusUnauthorized},
		{"无权限", apperr.Forbidden("z"), http.StatusForbidden},
		{"限流", apperr.TooManyRequests("慢一点"), http.StatusTooManyRequests},
		{"内部错误", apperr.Internal(errString("db down"), "内部错误"), http.StatusInternalServerError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			fail(rec, httptest.NewRequest(http.MethodGet, "/x", nil), c.err)
			if rec.Code != c.wantStatus {
				t.Errorf("状态码 = %d，期望 %d", rec.Code, c.wantStatus)
			}
			// 内部错误的 cause 绝不能出现在响应里
			if strings.Contains(rec.Body.String(), "db down") {
				t.Errorf("响应泄漏了内部 cause: %s", rec.Body.String())
			}
		})
	}
}
