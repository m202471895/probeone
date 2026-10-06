package auth

import "strconv"

// atoiAny 把 settings 里取出的 any 转成整数。
// Settings.Get 返回 any（表结构是 JSON 值），不能直接当 string 用。
func atoiAny(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		i, _ := strconv.Atoi(n)
		return i
	default:
		return 0
	}
}

// itoa 转字符串。
func itoa(n int64) string { return strconv.FormatInt(n, 10) }
