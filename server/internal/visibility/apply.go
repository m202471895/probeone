// Package visibility 实现字段脱敏（PRD 3.6）。
//
// 这是本项目最容易被"新加接口忘了接"攻破的地方，因此设计上强调：
//  1. 硬禁止字段由harddeny.go 唯一定义，无任何 API 可开启
//  2. DTO 层不含硬禁止字段，从类型层面杜绝误序列化
//  3. 兜底扫描中间件（middleware.go）作为最后一道防线
package visibility

import (
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/m202471895/probeone/server/internal/model"
)

// PolicyStore 提供策略查询。实现方可以是数据库或内存缓存。
type PolicyStore interface {
	// Policies 返回指定 scope 的全部策略。
	Policies(scope model.VisibilityScope) ([]model.VisibilityPolicy, error)
}

// Engine 是脱敏引擎。
//
// 全表仅约 30 行，启动时载入内存，变更时热更新。脱敏是纯内存操作，
// 对响应延迟影响 < 0.1ms。
type Engine struct {
	store PolicyStore
	// cache 按 scope 缓存策略，atomic.Value 持有不可变快照，读多写少无需加锁
	cache atomic.Value // map[model.VisibilityScope]map[string]model.VisibilityPolicy
	// version 用于观测策略是否已热更新
	version atomic.Int64
	loaded  atomic.Bool
}

// New 创建引擎并首次加载策略。
func New(store PolicyStore) (*Engine, error) {
	e := &Engine{store: store}
	if err := e.Reload(); err != nil {
		return nil, err
	}
	return e, nil
}

// Reload 重新加载策略。调用方在策略变更后触发，实现热更新（PRD 3.6.5）。
func (e *Engine) Reload() error {
	next := make(map[model.VisibilityScope]map[string]model.VisibilityPolicy)
	scopes := []model.VisibilityScope{
		model.ScopePublicStatus,
		model.ScopeAPIUnauth,
		model.ScopeExport,
	}
	for _, s := range scopes {
		list, err := e.store.Policies(s)
		if err != nil {
			return err
		}
		m := make(map[string]model.VisibilityPolicy, len(list))
		for _, p := range list {
			m[p.Field] = p
		}
		next[s] = m
	}
	e.cache.Store(next)
	e.version.Add(1)
	e.loaded.Store(true)
	return nil
}

// Version 返回当前策略版本号，用于观测热更新是否生效。
func (e *Engine) Version() int64 { return e.version.Load() }

// scopeSnapshot 读取某个 scope 的不可变策略快照。
func (e *Engine) scopeSnapshot(scope model.VisibilityScope) map[string]model.VisibilityPolicy {
	v, ok := e.cache.Load().(map[model.VisibilityScope]map[string]model.VisibilityPolicy)
	if !ok {
		return nil
	}
	return v[scope]
}

// maskFn 是掩码函数。
type maskFn func(value any, rule string) (any, bool)

// Result 是脱敏结果。
// Fields 是保留下来的字段，Dropped 记录被隐藏的字段名（供测试断言与审计）。
type Result struct {
	Fields  map[string]any
	Dropped []string
}

// Apply 对字段集合按 scope 施加脱敏。
//
// 流程固定为：
//  1. 硬禁止字段无条件丢弃（先于策略判断，无法绕过）
//  2. 查策略；策略缺失或 visible=false 则丢弃
//  3. 按 mask_mode 处理：full 原样、partial 走掩码规则、hide 丢弃
//
// 注意：hard deny 必须在策略之前。这样即使有人把某字段的 visible 设为 true，
// 硬禁止字段依然不会返回。
func (e *Engine) Apply(scope model.VisibilityScope, fields map[string]any) Result {
	out := make(map[string]any, len(fields))
	dropped := make([]string, 0, 4)

	policies := e.scopeSnapshot(scope)
	for k, v := range fields {
		// 1. 硬禁止：最优先，无策略可绕过
		if IsHardDenied(k) {
			dropped = append(dropped, k)
			continue
		}
		// 2. 策略缺失 → 保守丢弃（默认不信任，见PRD 3.6.4）
		p, ok := policies[k]
		if !ok {
			dropped = append(dropped, k)
			continue
		}
		// 3. 可见性开关
		if !p.Visible {
			dropped = append(dropped, k)
			continue
		}
		// 4. 掩码方式
		switch p.MaskMode {
		case model.MaskFull:
			out[k] = v
		case model.MaskPartial:
			fn, ok := maskRegistry[p.MaskRule]
			if !ok {
				// 未知掩码规则 → 保守丢弃，绝不返回原值
				dropped = append(dropped, k)
				continue
			}
			if masked, ok := fn(v, p.MaskRule); ok {
				out[k] = masked
			} else {
				dropped = append(dropped, k)
			}
		case model.MaskHide, "":
			dropped = append(dropped, k)
		default:
			dropped = append(dropped, k)
		}
	}

	sort.Strings(dropped)
	return Result{Fields: out, Dropped: dropped}
}

// maskRegistry 是掩码规则表。未知规则不在此表中，
// Apply 会因此丢弃字段而非泄露原值（fail-closed 设计）。
var maskRegistry = map[string]maskFn{
	"first_two_octets":   maskFirstTwoOctets,
	"first_three_octets": maskFirstThreeOctets,
	"country_only":       maskCountryOnly,
	"ip_hash":            maskIPHash,
}

// ---------- 具体掩码实现 ----------

func maskFirstTwoOctets(value any, _ string) (any, bool) {
	s, ok := value.(string)
	if !ok || s == "" {
		return nil, false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return nil, false
	}
	return parts[0] + "." + parts[1] + ".x.x", true
}

func maskFirstThreeOctets(value any, _ string) (any, bool) {
	s, ok := value.(string)
	if !ok || s == "" {
		return nil, false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return nil, false
	}
	return parts[0] + "." + parts[1] + "." + parts[2] + ".x", true
}

// maskCountryOnly 完全丢弃 IP，调用方应配合单独提供国家字段。
// 若传入的是 "IP|国家" 形式，则只返回国家。
func maskCountryOnly(value any, _ string) (any, bool) {
	s, ok := value.(string)
	if !ok {
		return nil, false
	}
	if idx := strings.Index(s, "|"); idx >= 0 {
		return s[idx+1:], true
	}
	return nil, false
}

// maskIPHash 返回 IP 的稳定短哈希，便于关联同一 IP 而不泄露原值。
func maskIPHash(value any, _ string) (any, bool) {
	s, ok := value.(string)
	if !ok || s == "" {
		return nil, false
	}
	return shortHash(s), true
}

func shortHash(s string) string {
	// FNV-1a 32 位，取8 位十六进制。用于脱敏显示，无需密码学强度。
	const (
		offset32 = uint32(2166136261)
		prime32  = uint32(16777619)
	)
	h := offset32
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime32
	}
	const hexdigits = "0123456789abcdef"
	buf := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		buf[i] = hexdigits[h&0xf]
		h >>= 4
	}
	return string(buf)
}

// ReloadEvery 定期重载策略的辅助函数。
// 由于 Reload 是全量替换且不可变快照切换，读侧无锁竞争。
func ReloadEvery(e *Engine, interval time.Duration, stop <-chan struct{}, onErr func(error)) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if err := e.Reload(); err != nil && onErr != nil {
				onErr(err)
			}
		}
	}
}
