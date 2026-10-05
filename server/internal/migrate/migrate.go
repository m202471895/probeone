// Package migrate 实现数据库迁移。
//
// 为什么不引golang-migrate（PRD 附录 A 的取向）：
//   - PRD 明确"不用 ORM，手写 SQL"，引一个重量级迁移框架与之风格不一致
//   - 我们的迁移需求很单纯：顺序执行、记录版本、支持回滚
//   - 约 200 行即可覆盖，零依赖省掉一个供应链攻击面
//
// 使用方式：
//
//	m, err := migrate.New(embedFS, "migrations", db)
//	err = m.Up(ctx)       // 应用所有未执行的迁移
//	err = m.Version()     // 查询当前版本
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/m202471895/probeone/server/migrations"
)

// Migration 是一个迁移文件。
type Migration struct {
	Version int64
	Name    string
	SQL     string
	// Checksum 用于检测已执行迁移被篡改的情况
	Checksum uint32
}

// DownSQL 是可选的向下迁移。不提供则回滚报错。
type DownSQL struct {
	Version int64
	SQL     string
}

// Builtin 返回内置迁移集（server/migrations 下的 SQL）。
// 服务端二进制内嵌这些文件，部署时无需额外拷贝。
//
// embed 必须在 migrations 包内完成——go:embed 只能嵌入当前包目录树内的文件，
// 本包在 internal/migrate 下，够不到同级的 server/migrations/。
// 因此实际 embed 在 migrations 包，这里只做转发。
func Builtin() fs.FS { return migrations.FS() }

// Dialect 表示数据库方言，决定选用哪套迁移文件。
type Dialect string

const (
	DialectSQLite   Dialect = "sqlite"
	DialectPostgres Dialect = "postgres"
)

// Migrator 执行迁移。
type Migrator struct {
	db        *sql.DB
	fsys      fs.FS
	dir       string
	dialect   Dialect
	tableName string
	logf      func(format string, args ...any)
}

// Option 是构造选项。
type Option func(*Migrator)

// WithLogger 设置日志输出。
func WithLogger(fn func(format string, args ...any)) Option {
	return func(m *Migrator) { m.logf = fn }
}

// New 创建迁移器（SQLite 方言）。
// dir 是 fs.FS 中的目录名，builtin 模式下传 "."。
func New(dbm *sql.DB, fsys fs.FS, dir string, opts ...Option) *Migrator {
	return NewWithDialect(dbm, fsys, dir, DialectSQLite, opts...)
}

// NewWithDialect 创建指定方言的迁移器。
//
// 文件选择规则（见 migrations/ 目录说明）：
//   - SQLite   → 优先取 0001_init.sql；若不存在再退回无后缀的版本
//   - PostgreSQL → 取 0001_postgres_init.sql
//
// 两套文件版本号必须一致，schema 变更时同步修改。
func NewWithDialect(dbm *sql.DB, fsys fs.FS, dir string, d Dialect, opts ...Option) *Migrator {
	m := &Migrator{
		db:        dbm,
		fsys:      fsys,
		dir:       dir,
		dialect:   d,
		tableName: "schema_migrations",
		logf:      func(string, ...any) {},
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// ensureTable 创建版本记录表。
//
// 注意：这里刻意用 CREATE TABLE IF NOT EXISTS 而非迁移文件建表——
// 版本表必须在任何迁移执行前就存在。
func (m *Migrator) ensureTable(ctx context.Context) error {
	dml := `CREATE TABLE IF NOT EXISTS ` + m.tableName + ` (
		version    BIGINT PRIMARY KEY,
		name       VARCHAR(255) NOT NULL,
		checksum   BIGINT      NOT NULL DEFAULT 0,
		applied_at TIMESTAMPTZ NOT NULL,
		exec_ms    BIGINT      NOT NULL DEFAULT 0
	)`
	if _, err := m.db.ExecContext(ctx, dml); err != nil {
		return fmt.Errorf("创建迁移版本表失败: %w", err)
	}
	return nil
}

// join 拼接迁移目录与文件名。
//
// 用 path.Join 而非字符串拼接 + "/"，因为 io/fs 的路径语义是
// 无前导 "./"、无重复分隔符。直接拼 "migrations/" + "/x.sql"
// 在 dir 传 "." 时会产生 "./x.sql"，而 fs.ReadFile 拒绝这种路径。
func (m *Migrator) join(name string) string {
	if m.dir == "" || m.dir == "." {
		return name
	}
	return path.Join(m.dir, name)
}

// parseName 解析 "0001_init.sql" → (1, "init")。
// 版本号取前导数字，文件名剩下部分作为描述。
func parseName(filename string) (int64, string, error) {
	if !strings.HasSuffix(filename, ".sql") {
		return 0, "", fmt.Errorf("非法迁移文件名 %q：必须以 .sql 结尾", filename)
	}
	base := strings.TrimSuffix(filename, ".sql")
	// 也支持 "0001_init.down.sql"
	base = strings.TrimSuffix(base, ".down")
	idx := strings.Index(base, "_")
	if idx <= 0 {
		return 0, "", fmt.Errorf("非法迁移文件名 %q：格式应为 0001_name.sql", filename)
	}
	ver, err := strconv.ParseInt(base[:idx], 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("非法迁移版本号 %q：%w", base[:idx], err)
	}
	return ver, base[idx+1:], nil
}

// Load 读取全部迁移文件，按版本升序。
func (m *Migrator) Load(ctx context.Context) ([]Migration, error) {
	entries, err := fs.ReadDir(m.fsys, m.dir)
	if err != nil {
		return nil, fmt.Errorf("读取迁移目录失败: %w", err)
	}

	var out []Migration
	seen := make(map[int64]string)

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		// 跳过 down 文件，它们走 Rollback 路径
		if strings.HasSuffix(name, ".down.sql") {
			continue
		}
		// 按方言过滤：PostgreSQL 专用文件在 SQLite 下不能用，反之亦然
		isPG := strings.Contains(name, "_postgres_")
		if (m.dialect == DialectPostgres) != isPG {
			continue
		}

		ver, desc, err := parseName(strings.Replace(name, "_postgres_", "_", 1))
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[ver]; dup {
			return nil, fmt.Errorf("迁移版本冲突：%s 与 %s 的版本都是 %d", prev, name, ver)
		}
		seen[ver] = name

		content, err := fs.ReadFile(m.fsys, m.join(name))
		if err != nil {
			return nil, fmt.Errorf("读取迁移文件 %s 失败: %w", name, err)
		}
		out = append(out, Migration{
			Version:  ver,
			Name:     desc,
			SQL:      string(content),
			Checksum: checksum(content),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Up 应用所有未执行的迁移。
// 每个迁移在独立事务中执行，失败只回滚该次，不影响已完成的。
func (m *Migrator) Up(ctx context.Context) error {
	if err := m.ensureTable(ctx); err != nil {
		return err
	}
	migrations, err := m.Load(ctx)
	if err != nil {
		return err
	}
	applied, err := m.appliedVersions(ctx)
	if err != nil {
		return err
	}

	for _, mg := range migrations {
		if _, done := applied[mg.Version]; done {
			continue
		}
		if err := m.applyOne(ctx, mg); err != nil {
			return err
		}
	}
	return nil
}

// applyOne 执行单个迁移。
func (m *Migrator) applyOne(ctx context.Context, mg Migration) error {
	start := time.Now()

	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("迁移 %d_%s：开启事务失败: %w", mg.Version, mg.Name, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, mg.SQL); err != nil {
		return fmt.Errorf("迁移 %d_%s 执行失败（已回滚）: %w", mg.Version, mg.Name, err)
	}

	ins := fmt.Sprintf(
		`INSERT INTO %s (version, name, checksum, applied_at, exec_ms) VALUES (?, ?, ?, ?, ?)`,
		m.tableName)
	if _, err := tx.ExecContext(ctx, ins,
		mg.Version, mg.Name, int64(mg.Checksum),
		m.formatTime(time.Now().UTC()), time.Since(start).Milliseconds()); err != nil {
		return fmt.Errorf("迁移 %d_%s：记录版本失败: %w", mg.Version, mg.Name, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("迁移 %d_%s：提交失败: %w", mg.Version, mg.Name, err)
	}

	m.logf("已应用迁移 %d_%s（耗时 %dms）", mg.Version, mg.Name, time.Since(start).Milliseconds())
	return nil
}

// Status 是单个迁移的状态。
type Status struct {
	Version   int64
	Name      string
	Applied   bool
	AppliedAt *time.Time
	// Mismatch 为 true 表示文件已被修改但已执行过——
	// 这通常意味着有人改了历史迁移，破坏可重现性。
	Mismatch bool
	ExecMS   int64
}

// Status 列出全部迁移的执行状态。
func (m *Migrator) Status(ctx context.Context) ([]Status, error) {
	if err := m.ensureTable(ctx); err != nil {
		return nil, err
	}
	migrations, err := m.Load(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := m.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT version, name, checksum, applied_at, exec_ms FROM %s`, m.tableName))
	if err != nil {
		return nil, fmt.Errorf("查询迁移状态失败: %w", err)
	}
	defer rows.Close()

	type rec struct {
		name         string
		checksum     int64
		appliedAt    time.Time
		appliedAtRaw any
		execMS       int64
	}
	recs := make(map[int64]rec)
	for rows.Next() {
		var (
			version int64
			r       rec
		)
		if err := rows.Scan(&version, &r.name, &r.checksum, &r.appliedAtRaw, &r.execMS); err != nil {
			return nil, err
		}
		t, perr := m.parseTime(r.appliedAtRaw)
		if perr != nil {
			return nil, fmt.Errorf("迁移版本 %d 的 applied_at 无法解析: %w", version, perr)
		}
		r.appliedAt = t
		recs[version] = r
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]Status, 0, len(migrations))
	for _, mg := range migrations {
		s := Status{Version: mg.Version, Name: mg.Name}
		if r, ok := recs[mg.Version]; ok {
			s.Applied = true
			t := r.appliedAt
			s.AppliedAt = &t
			s.ExecMS = r.execMS
			if int64(mg.Checksum) != r.checksum {
				s.Mismatch = true
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// Version 返回当前已应用的最大版本号，未应用任何迁移时返回 0。
func (m *Migrator) Version(ctx context.Context) (int64, error) {
	if err := m.ensureTable(ctx); err != nil {
		return 0, err
	}
	var v sql.NullInt64
	err := m.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT MAX(version) FROM %s`, m.tableName)).Scan(&v)
	if err != nil {
		return 0, err
	}
	return v.Int64, nil
}

// Rollback 回退到指定版本。
// 需要对应的 .down.sql 文件，缺失时报错而不是静默跳过。
func (m *Migrator) Rollback(ctx context.Context, target int64) error {
	current, err := m.Version(ctx)
	if err != nil {
		return err
	}
	if target >= current {
		return fmt.Errorf("回滚目标版本 %d 必须小于当前版本 %d", target, current)
	}

	entries, err := fs.ReadDir(m.fsys, m.dir)
	if err != nil {
		return err
	}
	downs := make(map[int64]string)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".down.sql") {
			continue
		}
		ver, _, err := parseName(e.Name())
		if err != nil {
			return err
		}
		downs[ver] = e.Name()
	}

	for v := current; v > target; v-- {
		downFile, ok := downs[v]
		if !ok {
			return fmt.Errorf("缺少版本 %d 的回滚文件（%06d_xxx.down.sql）", v, v)
		}
		content, err := fs.ReadFile(m.fsys, m.join(downFile))
		if err != nil {
			return err
		}

		tx, err := m.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(content)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("回滚版本 %d 失败（已回滚该步）: %w", v, err)
		}
		del := fmt.Sprintf(`DELETE FROM %s WHERE version = ?`, m.tableName)
		if _, err := tx.ExecContext(ctx, del, v); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			return err
		}
		m.logf("已回滚版本 %d", v)
	}
	return nil
}

// Reset 回到空库（执行所有 down 文件）。仅用于本地开发与测试。
func (m *Migrator) Reset(ctx context.Context) error {
	cur, err := m.Version(ctx)
	if err != nil {
		return err
	}
	if cur == 0 {
		return nil
	}
	return m.Rollback(ctx, 0)
}

// appliedVersions 返回已应用版本集合。
func (m *Migrator) appliedVersions(ctx context.Context) (map[int64]time.Time, error) {
	rows, err := m.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT version, applied_at FROM %s`, m.tableName))
	if err != nil {
		return nil, fmt.Errorf("查询已应用迁移失败: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]time.Time)
	for rows.Next() {
		var (
			v   int64
			raw any
		)
		if err := rows.Scan(&v, &raw); err != nil {
			return nil, err
		}
		t, perr := m.parseTime(raw)
		if perr != nil {
			return nil, fmt.Errorf("迁移版本 %d 的 applied_at 无法解析: %w", v, perr)
		}
		out[v] = t
	}
	return out, rows.Err()
}

// formatTime 按方言格式化时间。
// SQLite 侧统一用 RFC3339（可排序、可读、跨版本稳定）。
func (m *Migrator) formatTime(t time.Time) any {
	if m.dialect == DialectSQLite {
		return t.UTC().Format(time.RFC3339)
	}
	return t.UTC()
}

// parseTime 解析 applied_at，兼容字符串与原生时间两种返回。
func (m *Migrator) parseTime(v any) (time.Time, error) {
	switch t := v.(type) {
	case time.Time:
		return t.UTC(), nil
	case []byte:
		return time.Parse(time.RFC3339, string(t))
	case string:
		if t == "" {
			return time.Time{}, fmt.Errorf("空值")
		}
		return time.Parse(time.RFC3339, t)
	default:
		return time.Time{}, fmt.Errorf("不支持的类型 %T", v)
	}
}

// checksum 是 FNV-1a 32 位。仅用于检测历史迁移被篡改，
// 不需要密码学强度。
func checksum(b []byte) uint32 {
	const (
		offset32 = uint32(2166136261)
		prime32  = uint32(16777619)
	)
	h := offset32
	for _, c := range b {
		h ^= uint32(c)
		h *= prime32
	}
	return h
}
