package visibility

// harddeny.go 是硬禁止字段的唯一来源。
//
// 这些字段代表"凭据"或"可直接用于横向移动的信息"，
// 任何 scope 下都不得返回，且**不存在任何 API 可以开启**。
// 这是设计约束而非配置项——见 PRD 3.6.2 与 3.6.5。
//
// 注意：这里只定义"JSON 键名"。存储层的同名字段（如 nodes.agent_secret）
// 永远不应出现在任何 DTO 中；本表是第二道防线，用于兜住手工拼 map 的场景。

// hardDeniedFields 是硬禁止字段集合。
// 加入本表前请确认：泄露后是否会导致凭据泄露或资产被定位。
var hardDeniedFields = map[string]struct{}{
	// 凭据类
	"agent_secret":        {},
	"client_secret":       {},
	"password_hash":       {},
	"token_hash":          {},
	"master_key":          {},
	"session_token":       {},
	"notification_secret": {},
	// 网络与硬件标识类：可用于定位与横向移动
	"internal_ip": {},
	"mac_address": {},
	// 认证凭据，无论何种形式
	"authorization": {},
	"cookie":        {},
}

// HardDeniedFields 返回硬禁止字段的副本（防止调用方误改内部状态）。
func HardDeniedFields() []string {
	out := make([]string, 0, len(hardDeniedFields))
	for k := range hardDeniedFields {
		out = append(out, k)
	}
	return out
}

// IsHardDenied 判断字段是否硬禁止。
//
// 匹配规则：
//   - 精确匹配
//   - 后缀匹配 "_secret" / "_hash" / "_token"（如 smtp_secret、session_token_hash）
//
// 后缀匹配用于覆盖同义命名，避免新加接口时用了个没登记的名字就漏出去。
// 宁可误杀（字段被隐藏）也不可漏出（凭据泄露）。
func IsHardDenied(field string) bool {
	if field == "" {
		return false
	}
	if _, ok := hardDeniedFields[field]; ok {
		return true
	}
	// 归一化：转小写并把驼峰转下划线，避免 AgentSecret 形式绕过
	norm := normalizeFieldName(field)
	if _, ok := hardDeniedFields[norm]; ok {
		return true
	}
	for _, suffix := range []string{"_secret", "_hash", "_token", "_password"} {
		if hasSuffix(norm, suffix) {
			return true
		}
	}
	return false
}

// normalizeFieldName 把驼峰式转成下划线式并转小写。
func normalizeFieldName(s string) string {
	b := make([]byte, 0, len(s)+4)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			// 驼峰边界前插下划线
			if i > 0 && !(s[i-1] >= 'A' && s[i-1] <= 'Z') {
				b = append(b, '_')
			}
			b = append(b, c+('a'-'A'))
		case c == '-':
			b = append(b, '_')
		default:
			b = append(b, c)
		}
	}
	return string(b)
}

func hasSuffix(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
}
