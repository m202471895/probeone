// Package migrations 内嵌迁移 SQL 文件。
//
// 单独成包的原因：go:embed 只能嵌入**当前包目录及其子目录**的文件。
// 迁移 SQL 放在 server/migrations/（符合 PRD 的目录约定），
// 而迁移执行逻辑在 server/internal/migrate/，后者无法直接 embed 前者。
// 因此由本包负责 embed 并导出 fs.FS，internal/migrate 引用它。
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

// FS 返回内嵌的迁移文件系统，根目录即 migrations/。
// 文件命名规范：0001_init.sql；可选的回滚文件：0001_init.down.sql
func FS() fs.FS { return files }
