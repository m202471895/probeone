package visibility

import (
	"testing"

	"github.com/m202471895/probeone/server/internal/model"
)

// fakeStore 是测试用的策略源。
type fakeStore struct {
	policies map[model.VisibilityScope][]model.VisibilityPolicy
}

func (f *fakeStore) Policies(scope model.VisibilityScope) ([]model.VisibilityPolicy, error) {
	return f.policies[scope], nil
}

// newTestEngine 构造一个带默认策略的引擎，模拟迁移内置数据。
func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	store := &fakeStore{
		policies: map[model.VisibilityScope][]model.VisibilityPolicy{
			model.ScopePublicStatus: {
				{Field: "name", Visible: true, MaskMode: model.MaskFull},
				{Field: "public_ip", Visible: false, MaskMode: model.MaskPartial, MaskRule: "country_only"},
				{Field: "cpu_model", Visible: true, MaskMode: model.MaskFull},
				{Field: "mem_total", Visible: true, MaskMode: model.MaskFull},
				{Field: "hostname", Visible: false, MaskMode: model.MaskHide},
			},
			model.ScopeAPIUnauth: {
				{Field: "name", Visible: true, MaskMode: model.MaskFull},
				{Field: "cpu_model", Visible: false, MaskMode: model.MaskHide},
			},
			model.ScopeExport: {
				{Field: "name", Visible: true, MaskMode: model.MaskFull},
				{Field: "public_ip", Visible: true, MaskMode: model.MaskPartial, MaskRule: "first_two_octets"},
			},
		},
	}
	e, err := New(store)
	if err != nil {
		t.Fatalf("创建脱敏引擎失败: %v", err)
	}
	return e
}

func TestApply_PublicStatus(t *testing.T) {
	e := newTestEngine(t)

	in := map[string]any{
		"name":      "hk-node-01",
		"public_ip": "203.0.113.42",
		"cpu_model": "AMD EPYC 7543",
		"mem_total": int64(8589934592),
		"hostname":  "prod-web-01",
		"unknown_f": "无策略字段",
	}

	got := e.Apply(model.ScopePublicStatus, in).Fields

	// 应保留
	if got["name"] != "hk-node-01" {
		t.Errorf("name 应保留，实际 = %v", got["name"])
	}
	if got["cpu_model"] != "AMD EPYC 7543" {
		t.Errorf("cpu_model 是公开字段，应保留，实际 = %v", got["cpu_model"])
	}
	if got["mem_total"] == nil {
		t.Error("mem_total 是公开字段，应保留")
	}

	// 应隐藏
	if _, ok := got["public_ip"]; ok {
		t.Error("public_ip 在 public_status scope 下应隐藏")
	}
	if _, ok := got["hostname"]; ok {
		t.Error("hostname 在 public_status scope 下应隐藏")
	}
	// 策略缺失 → 保守丢弃
	if _, ok := got["unknown_f"]; ok {
		t.Error("无策略的字段应被丢弃（fail-closed）")
	}
}

func TestApply_HardDenyBeatsPolicy(t *testing.T) {
	// 构造一个"把agent_secret 设为可见"的策略表。
	// 正确实现下，硬禁止字段仍必须被丢弃——这是PRD 3.6.2 的核心约束。
	store := &fakeStore{
		policies: map[model.VisibilityScope][]model.VisibilityPolicy{
			model.ScopePublicStatus: {
				{Field: "name", Visible: true, MaskMode: model.MaskFull},
				{Field: "agent_secret", Visible: true, MaskMode: model.MaskFull},
				{Field: "internal_ip", Visible: true, MaskMode: model.MaskFull},
				{Field: "mac_address", Visible: true, MaskMode: model.MaskFull},
			},
		},
	}
	e, err := New(store)
	if err != nil {
		t.Fatalf("创建引擎失败: %v", err)
	}

	in := map[string]any{
		"name":         "n1",
		"agent_secret": "s3cr3t",
		"internal_ip":  "10.0.0.5",
		"mac_address":  "aa:bb:cc:dd:ee:ff",
	}
	got := e.Apply(model.ScopePublicStatus, in).Fields

	for _, k := range []string{"agent_secret", "internal_ip", "mac_address"} {
		if v, ok := got[k]; ok {
			t.Errorf("硬禁止字段 %s 必须无条件丢弃，但返回值 = %v", k, v)
		}
	}
	if got["name"] != "n1" {
		t.Error("非敏感字段应正常保留")
	}
}

func TestIsHardDenied(t *testing.T) {
	cases := []struct {
		field string
		want  bool
		why   string
	}{
		{"agent_secret", true, "精确匹配"},
		{"client_secret", true, "精确匹配"},
		{"password_hash", true, "精确匹配"},
		{"token_hash", true, "精确匹配"},
		{"internal_ip", true, "精确匹配"},
		{"mac_address", true, "精确匹配"},
		{"smtp_secret", true, "_secret 后缀"},
		{"user_password", true, "_password 后缀"},
		{"csrf_token", true, "_token 后缀"},
		{"sessionTokenHash", true, "驼峰归一化后 _hash 后缀"},
		{"AgentSecret", true, "驼峰归一化后精确匹配"},
		{"name", false, "普通字段"},
		{"public_ip", false, "普通字段"},
		{"cpu_model", false, "公开字段"},
		{"tokenizer", false, "后缀不匹配，不应误杀"},
		{"hashed", false, "不含 _hash 下划线形式"},
		{"", false, "空字符串"},
	}
	for _, c := range cases {
		if got := IsHardDenied(c.field); got != c.want {
			t.Errorf("IsHardDenied(%q) = %v，期望 %v（%s）", c.field, got, c.want, c.why)
		}
	}
}

func TestMaskRules(t *testing.T) {
	t.Run("first_two_octets", func(t *testing.T) {
		store := &fakeStore{policies: map[model.VisibilityScope][]model.VisibilityPolicy{
			model.ScopeExport: {{Field: "public_ip", Visible: true, MaskMode: model.MaskPartial, MaskRule: "first_two_octets"}},
		}}
		e, _ := New(store)
		got := e.Apply(model.ScopeExport, map[string]any{"public_ip": "203.0.113.42"}).Fields
		if got["public_ip"] != "203.0.x.x" {
			t.Errorf("first_two_octets 掩码结果 = %v，期望 203.0.x.x", got["public_ip"])
		}
	})

	t.Run("first_three_octets", func(t *testing.T) {
		store := &fakeStore{policies: map[model.VisibilityScope][]model.VisibilityPolicy{
			model.ScopeExport: {{Field: "public_ip", Visible: true, MaskMode: model.MaskPartial, MaskRule: "first_three_octets"}},
		}}
		e, _ := New(store)
		got := e.Apply(model.ScopeExport, map[string]any{"public_ip": "203.0.113.42"}).Fields
		if got["public_ip"] != "203.0.113.x" {
			t.Errorf("first_three_octets 掩码结果 = %v，期望 203.0.113.x", got["public_ip"])
		}
	})

	t.Run("非法IP输入不泄露原值", func(t *testing.T) {
		store := &fakeStore{policies: map[model.VisibilityScope][]model.VisibilityPolicy{
			model.ScopeExport: {{Field: "public_ip", Visible: true, MaskMode: model.MaskPartial, MaskRule: "first_two_octets"}},
		}}
		e, _ := New(store)
		// 输入不是四段 IP → 掩码失败 → 字段被丢弃，绝不返回原值
		got := e.Apply(model.ScopeExport, map[string]any{"public_ip": "not-an-ip"}).Fields
		if v, ok := got["public_ip"]; ok {
			t.Errorf("非法 IP 应被丢弃，但返回了 %v", v)
		}
	})

	t.Run("country_only 丢弃裸IP", func(t *testing.T) {
		store := &fakeStore{policies: map[model.VisibilityScope][]model.VisibilityPolicy{
			model.ScopePublicStatus: {{Field: "public_ip", Visible: true, MaskMode: model.MaskPartial, MaskRule: "country_only"}},
		}}
		e, _ := New(store)
		// 裸 IP（无国家后缀）应被丢弃
		got := e.Apply(model.ScopePublicStatus, map[string]any{"public_ip": "203.0.113.42"}).Fields
		if v, ok := got["public_ip"]; ok {
			t.Errorf("country_only 下裸 IP 应被丢弃，但返回了 %v", v)
		}
	})

	t.Run("未知掩码规则保守丢弃", func(t *testing.T) {
		store := &fakeStore{policies: map[model.VisibilityScope][]model.VisibilityPolicy{
			model.ScopeExport: {{Field: "public_ip", Visible: true, MaskMode: model.MaskPartial, MaskRule: "no_such_rule"}},
		}}
		e, _ := New(store)
		// fail-closed：规则不认识就不能返回原值
		got := e.Apply(model.ScopeExport, map[string]any{"public_ip": "203.0.113.42"}).Fields
		if v, ok := got["public_ip"]; ok {
			t.Errorf("未知掩码规则应丢弃字段，但返回了 %v", v)
		}
	})
}

func TestSanitizeJSON(t *testing.T) {
	// 模拟 handler 漏写脱敏，直接返回了含敏感字段的 JSON。
	// 兜底扫描必须拦住（PRD 3.6.4 第 3 条）。
	raw := []byte(`{"name":"n1","internal_ip":"10.0.0.5","agent_secret":"abc","nested":{"password_hash":"x"},"list":[{"mac_address":"aa:bb"}]}`)

	out, changed := sanitizeJSON(raw, nil)
	if !changed {
		t.Fatal("兜底扫描应检出敏感字段并修改内容")
	}
	s := string(out)
	for _, bad := range []string{"10.0.0.5", "internal_ip", "agent_secret", "password_hash", "mac_address"} {
		if contains(s, bad) {
			t.Errorf("清洗后仍含 %s：%s", bad, s)
		}
	}
	if !contains(s, `"name":"n1"`) {
		t.Errorf("清洗不应影响正常字段：%s", s)
	}
}

func TestSanitizeJSON_ValidJSONUnchanged(t *testing.T) {
	raw := []byte(`{"name":"n1","cpu_model":"EPYC","mem_total":1024}`)
	out, changed := sanitizeJSON(raw, nil)
	if changed {
		t.Errorf("无敏感字段时不应修改内容，实际 = %s", string(out))
	}
}

func TestSanitizeJSON_InvalidJSON(t *testing.T) {
	// 非 JSON 内容原样返回，不应 panic
	raw := []byte(`plain text response`)
	out, changed := sanitizeJSON(raw, nil)
	if changed || string(out) != string(raw) {
		t.Error("非 JSON 响应应原样透传")
	}
}

func TestScopeIsolation(t *testing.T) {
	e := newTestEngine(t)
	in := map[string]any{"name": "n1", "cpu_model": "EPYC"}

	// 状态页可见 cpu_model
	pub := e.Apply(model.ScopePublicStatus, in).Fields
	if _, ok := pub["cpu_model"]; !ok {
		t.Error("public_status scope 下 cpu_model 应可见")
	}
	// 未鉴权 API 不可见
	api := e.Apply(model.ScopeAPIUnauth, in).Fields
	if _, ok := api["cpu_model"]; ok {
		t.Error("api_unauth scope 下 cpu_model 应隐藏")
	}
	if api["name"] != "n1" {
		t.Error("name 在两个 scope 下都应可见")
	}
}

func TestReloadIncrementsVersion(t *testing.T) {
	e := newTestEngine(t)
	v1 := e.Version()
	if err := e.Reload(); err != nil {
		t.Fatalf("Reload 失败: %v", err)
	}
	if e.Version() <= v1 {
		t.Error("Reload 后版本号应递增，用于观测策略热更新")
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		indexOf(haystack, needle) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
