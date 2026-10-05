// Package sqlbase 提供跨数据库通用的辅助函数。
//
// 这些函数屏蔽 SQLite 与 PostgreSQL 的方言差异，让上层仓储实现
// 可以用同一套代码在两种数据库上工作。
package sqlbase

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Dialect 表示数据库方言。
type Dialect string

const (
	DialectSQLite   Dialect = "sqlite"
	DialectPostgres Dialect = "postgres"
)

// JSONPlaceholder 返回第 n 个JSON 列参数占位符。
// SQLite 用 ?，PostgreSQL 用 $n。
func (d Dialect) Placeholder(n int) string {
	if d == DialectPostgres {
		return "$" + strconv.Itoa(n)
	}
	return "?"
}

// Rebind 把SQL 里的 ? 全部转成对应方言的占位符。
// 用法：以 ? 写SQL（SQLite 风格），调用 Rebind 转成 PostgreSQL 风格。
func (d Dialect) Rebind(query string) string {
	if d == DialectSQLite {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	for i := 0; i < len(query); i++ {
		if query[i] != '?' {
			b.WriteByte(query[i])
			continue
		}
		// 判断是否为字符串字面量里的问号（如 '?'），是则原样保留
		if inStringLiteral(query, i) {
			b.WriteByte(query[i])
			continue
		}
		n++
		b.WriteString("$")
		b.WriteString(strconv.Itoa(n))
	}
	return b.String()
}

func inStringLiteral(s string, pos int) bool {
	inStr := false
	for i := 0; i < pos; i++ {
		switch s[i] {
		case '\'':
			inStr = !inStr
		case '\\':
			// 转义字符，跳过下一个
			i++
		}
	}
	return inStr
}

// Now 返回数据库可接受的当前时间。
// 参数化查询里禁止直接用 time.Now()，统一走这里便于测试替换。
func Now() time.Time { return time.Now().UTC().Truncate(time.Second) }

// ---------- JSON 辅助 ----------

// MarshalJSON 把任意结构序列化为JSON 字符串，失败返回空串。
// 指标表里的 disk_usage / net_io / sensors 都是 JSON 列。
func MarshalJSON(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// NullString 把字符串转为可空值。
func NullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// NullInt64 把 int64 转为可空值。
func NullInt64(v int64) any {
	return v
}

// NullInt 把 int 转为可空值。
func NullInt(v int) any {
	return v
}

// NullFloat 把 float64 转为可空值。
func NullFloat(v float64) any {
	return v
}

// NullTime 把 time.Time 转为可空值。
// 零值 time 转为 NULL，而不是写入 0001-01-01。
func NullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// NullTimePtr 把 *time.Time 转为可空值。
func NullTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

// TimePtr 把可空时间列转成 *time.Time。
func TimePtr(nt sql.NullTime) *time.Time {
	if !nt.Valid {
		return nil
	}
	t := nt.Time
	return &t
}

// ---------- 扫描辅助 ----------

// ScanJSON 扫描 JSON 列到目标结构。
// 数据损坏时记录警告但不中断——采集数据不该因为一条坏记录让整个面板挂掉。
func ScanJSON(raw sql.NullString, dst any) bool {
	if !raw.Valid || raw.String == "" {
		return false
	}
	if err := json.Unmarshal([]byte(raw.String), dst); err != nil {
		return false
	}
	return true
}

// LimitClause 生成分页 LIMIT/OFFSET 子句。
// 负数或超限值会被夹到安全区间，防止恶意分页参数拖垮数据库。
func LimitClause(limit, offset int, maxLimit int) (string, []any) {
	if maxLimit <= 0 {
		maxLimit = 200
	}
	if limit <= 0 || limit > maxLimit {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset), nil
}

// InClause 生成 IN 子句与对应参数。
// 空列表返回 "1=0"（匹配不到任何行）而不是 "IN ()"（语法错误）。
func InClause(column string, values []int64) (string, []any) {
	if len(values) == 0 {
		return "1=0", nil
	}
	parts := make([]string, len(values))
	args := make([]any, len(values))
	for i, v := range values {
		parts[i] = "?"
		args[i] = v
	}
	return "(" + strings.Join(parts, ",") + ")", args
}
