package store

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// isUniqueViolation 判断错误是否是唯一约束冲突。
//
// 为什么要类型断言优先、字符串匹配兜底：
//
//  1. 优先用pgconn.PgError 的 SQLSTATE。23505 是 unique_violation，
//     由数据库保证准确，不受文案与locale 影响。
//     字符串匹配在 PG 换语言版本时会失效。
//
//  2. 仍保留字符串兜底。当前 SQLite 驱动（modernc.org/sqlite）
//     不导出带类型的错误，只能从文案判断。
//     这是驱动限制，不是偷懒——等换驱动或用 errors.As 拿到底层错误后，
//     这段就可以删。
//
// 3. 错误类型来自哪个驱动不是本包能控制的：仓储可能同时
//     收到两个驱动的错误（迁移期、测试替身、跨驱动集成测试），
//     所以两条路都要走。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}

	// 路径 1：pgx 的结构化错误
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}

	// 路径 2：文案匹配（SQLite 驱动没有类型化错误）
	s := strings.ToLower(err.Error())
	for _, frag := range []string{
		"unique constraint", // SQLite
		"unique violation",  // PG 的标准表述
		"duplicate key",     // PG 实际输出的措辞
	} {
		if strings.Contains(s, frag) {
			return true
		}
	}
	return false
}
