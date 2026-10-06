// Package visibility 处理可见性策略管理的 HTTP 接口。
//
// 本包名与 internal/visibility 同名，因此导入后者时统一用别名 viscore。
// 这不是偷懒：两个包都会出现在同一个文件里，用别名能避免
// "这个 Apply 是引擎的还是接口的"这种误读。
//
// 与user 包一样，全部接口仅限 owner。理由：可见性策略是
// **脱敏规则的唯一控制面**，能把某个字段从"隐藏"改成"完整可见"。
// 能改它的人等于能决定哪些资产信息对外可见——这必须比
// "管节点"更高一档，限admin 都不够。
package visibility

import (
	"net/http"
	"strings"

	"github.com/m202471895/probeone/server/internal/apperr"
	"github.com/m202471895/probeone/server/internal/httpapi"
	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/store"
	viscore "github.com/m202471895/probeone/server/internal/visibility"
)

// Handler 是可见性策略处理器。
type Handler struct {
	db         *store.DB
	engine     *viscore.Engine
	trustProxy bool
}

// New 创建处理器。engine 用于策略变更后的热更新，nil 时降级为不热更新
// （仅测试或引擎尚未装配的场景；生产装配必须传入，否则改了策略不生效）。
func New(db *store.DB, engine *viscore.Engine, trustProxy bool) *Handler {
	return &Handler{db: db, engine: engine, trustProxy: trustProxy}
}

// Register 挂载可见性策略路由。
func (h *Handler) Register(mux *http.ServeMux, authn *httpapi.Authenticator) {
	owner := func(hf http.HandlerFunc) http.Handler {
		return authn.RequireAuth(httpapi.RequireRole(model.RoleOwner)(hf))
	}
	mux.Handle("GET /api/visibility", owner(h.list))
	mux.Handle("PUT /api/visibility/{field}", owner(h.update))
}

// policyView 是策略对外的 JSON 形状。
//
// **不含 mask_rule 的内容**，只回 has_mask_rule 布尔值。
// 理由：mask_rule 是脱敏规则的细节（如 first_two_octets 暴露的是
// "IP 保留前两段"这一实现选择）。把它回显给前端没有功能价值——
// 前端只需要知道"这条规则存不存在"，而规则内容一旦流进日志、
// 截图或前端缓存，就等于给攻击者一份"哪些字段被弱化处理"的地图，
// 让他精确挑选绕过成本最低的字段。
type policyView struct {
	Scope    model.VisibilityScope `json:"scope"`
	Field    string                `json:"field"`
	Visible  bool                  `json:"visible"`
	MaskMode model.MaskMode        `json:"mask_mode"`
	// HasMaskRule 让前端能提示"该字段已配置掩码规则"，
	// 而不必把规则本身取回来。
	HasMaskRule bool `json:"has_mask_rule"`
	// HardDenied 标记该字段属于硬禁止集合、本接口永远无法开启。
	// 前端据此把开关置为禁用态并说明原因，而不是让用户点了才报 400。
	HardDenied bool `json:"hard_denied"`
}

func viewOf(p *model.VisibilityPolicy) policyView {
	return policyView{
		Scope:       p.Scope,
		Field:       p.Field,
		Visible:     p.Visible,
		MaskMode:    p.MaskMode,
		HasMaskRule: strings.TrimSpace(p.MaskRule) != "",
		HardDenied:  isHardDenied(p.Field),
	}
}

// list 列出全部可见性策略。
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	policies, err := h.db.Visibility.All(r.Context())
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	items := make([]policyView, 0, len(policies))
	for i := range policies {
		items = append(items, viewOf(&policies[i]))
	}
	// 附带引擎版本号：策略改了之后版本号必须变化，
	// 前端据此确认热更新真的生效了，而不只是收到一个 200。
	// 这是一个可观测性锚点——"改了没生效"是最难排查的一类问题。
	var version int64
	if h.engine != nil {
		version = h.engine.Version()
	}
	httpapi.OK(w, r, map[string]any{
		"items": items, "engine_version": version,
	})
}

// update 修改某个字段在某scope 下的策略。
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	field := r.PathValue("field")
	if err := validateField(field); err != nil {
		httpapi.Fail(w, r, err)
		return
	}

	// 硬禁止字段校验放在最前面，且在任何数据库写入之前。
	//
	// 为什么必须拒绝：这些字段代表凭据（password_hash、token_hash、
	// agent_secret）或可直接用于横向移动的信息（internal_ip、mac_address）。
	// viscore.Apply 里它们会被无条件丢弃，但**只有 DTO 不含它们、
	// 中间件兜底扫描也生效时那才成立**——而那两道防线都可能被
	// 未来的改动绕过（新增接口忘了接脱敏层，是这个项目最容易被攻破的地方，
	// 见 internal/visibility 的包注释）。
	// 在策略层直接拒绝开启，等于加一道与代码正确性无关的、
	// 物理上无法绕过的锁：不存在任何 API、任何请求能把它们设为 visible。
	if isHardDenied(field) {
		httpapi.Fail(w, r, apperr.BadRequest(
			"字段 "+field+" 属于硬禁止字段（凭据或可用于横向移动的信息），不存在任何接口可以开启"))
		return
	}

	var in struct {
		Scope    model.VisibilityScope `json:"scope"`
		Visible  *bool                 `json:"visible"`
		MaskMode model.MaskMode        `json:"mask_mode"`
		MaskRule string                `json:"mask_rule"`
	}
	if err := httpapi.DecodeJSON(r, &in); err != nil {
		httpapi.Fail(w, r, err)
		return
	}

	// scope 用 viscore.ModelScope 归一：它对未知值返回空串，
	// 于是非法 scope 会走到下面的空串分支被拒。用它而不是自己写
	// switch，是为了让"合法 scope 集合"只有 viscore 一处定义。
	scope := viscore.ModelScope(string(in.Scope))
	if scope == "" {
		httpapi.Fail(w, r, apperr.BadRequest(
			"scope 非法（可选 public_status / api_unauth / export）"))
		return
	}
	// visible 用指针：false 是有意义的值（"请把这个字段关掉"），
	// 空指针才是"没传"。用 bool 会让"只想关掉它"无法表达。
	if in.Visible == nil {
		httpapi.Fail(w, r, apperr.BadRequest("请指定 visible"))
		return
	}
	mode, err := normalizeMaskMode(in.MaskMode)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	rule := strings.TrimSpace(in.MaskRule)
	if mode == model.MaskPartial && rule == "" {
		// partial 却没有规则：Apply 会查不到掩码函数从而丢弃字段。
		// 与其让用户以为"已配置脱敏"实则整个字段消失，
		// 不如在写入前要求给出规则。
		httpapi.Fail(w, r, apperr.BadRequest("mask_mode 为 partial 时必须指定 mask_rule"))
		return
	}
	// 非partial 时清掉规则：留着一条陈旧规则没有意义，
	// 且日后切回 partial 会意外生效一条用户早已忘记的规则。
	if mode != model.MaskPartial {
		rule = ""
	}

	if err := h.db.Visibility.Update(r.Context(), scope, field, *in.Visible, mode, rule); err != nil {
		httpapi.Fail(w, r, err)
		return
	}

	// 热更新引擎。**这一步不是可选的优化，是功能正确性的一部分**：
	// 引擎在启动时把策略全量载入内存，之后只读内存快照
	// （脱敏是纯内存操作，不查库）。不调 Reload 的话，
	// 数据库里的新策略要等到 ReloadEvery 周期到达甚至进程重启才会生效——
	// 用户明明刚在界面上打开开关，公开状态页却仍然脱敏，
	// 表现为"设置无效"，且没有任何报错。
	if h.engine != nil {
		if err := h.engine.Reload(); err != nil {
			httpapi.Fail(w, r, apperr.Internal(err, "策略已保存但脱敏引擎热更新失败，请稍后重试"))
			return
		}
	}

	h.audit(r, "update_visibility", "visibility_policy", string(scope)+"/"+field, map[string]any{
		"scope": scope, "field": field,
		"visible": *in.Visible, "mask_mode": mode,
		// 审计里同样不记 mask_rule 内容，与响应保持同一口径
		"has_mask_rule": rule != "",
	})

	httpapi.OK(w, r, policyView{
		Scope:       scope,
		Field:       field,
		Visible:     *in.Visible,
		MaskMode:    mode,
		HasMaskRule: rule != "",
		HardDenied:  false, // 走到这里说明它不是硬禁止字段
	})
}

// hardDeniedFields 是硬禁止字段的精确集合，在包初始化时从
// viscore.HardDeniedFields() 取一次。
//
// 为什么要"取列表"而不是只调IsHardDenied：
//   - HardDeniedFields() 是 viscore 唯一的权威来源，导入它等于
//     让本包与硬禁止表保持同步——表里加了新字段，这里自动生效，
//     不需要改本包。
//   - IsHardDenied 额外做后缀匹配（_secret / _hash / _token / _password），
//     能兜住"没登记但同义"的命名。两者取并集才是完整的拒绝面：
//     少任何一个都等于给绕过留口子。
var hardDeniedFields = func() map[string]bool {
	m := make(map[string]bool)
	for _, f := range viscore.HardDeniedFields() {
		m[f] = true
	}
	return m
}()

// isHardDenied 判断字段是否禁止开启：精确表∪ 后缀匹配。
func isHardDenied(field string) bool {
	return hardDeniedFields[field] || viscore.IsHardDenied(field)
}

// ---------- 校验辅助 ----------

// validMaskModes 是合法的掩码方式集合。
//
// 只接受这三种是因为 viscore.Apply 只认这三种：传入别的值会走到
// Apply 的 default 分支丢弃字段。与其那时静默丢弃，
// 不如在入口就拒绝并说明合法值。
var validMaskModes = map[model.MaskMode]bool{
	model.MaskFull:    true,
	model.MaskPartial: true,
	model.MaskHide:    true,
}

func normalizeMaskMode(m model.MaskMode) (model.MaskMode, error) {
	if m == "" {
		// 缺省按 hide 处理（fail-closed）：
		// "没说清楚要怎么处理"时应当藏起来，而不是原样输出。
		return model.MaskHide, nil
	}
	if !validMaskModes[m] {
		return "", apperr.BadRequest("mask_mode 非法（可选 full / partial / hide）")
	}
	return m, nil
}

// validateField 校验路径参数中的字段名。
//
// 限制长度与字符集是为了让 field 能安全地参与 SQL 与审计日志：
// 字段名最终会进 WHERE 条件（走参数绑定，不会注入），
// 但它同时会进审计 detail，而审计是要长期保存的文本。
func validateField(field string) error {
	if field == "" {
		return apperr.BadRequest("请指定字段名")
	}
	if len(field) > 64 {
		return apperr.BadRequest("字段名过长（上限 64，与表定义一致）")
	}
	if field != strings.ToLower(strings.TrimSpace(field)) {
		return apperr.BadRequest("字段名必须为小写")
	}
	for _, r := range field {
		if r >= 'a' && r <= 'z' {
			continue
		}
		switch r {
		case '_', '.', '-':
			continue
		}
		return apperr.BadRequest("字段名只能包含小写字母、数字与 _ . -")
	}
	return nil
}

// audit 写审计日志。
//
// 可见性策略的每一次变更都记：这是"谁让哪个字段变得可见"的唯一凭据，
// 出了数据泄露时它是溯源起点。
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
