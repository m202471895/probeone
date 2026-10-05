package migrate

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	_ "modernc.org/sqlite"
)

// newDB 开一个内存库。
func newDB(t *testing.T) *sql.DB {
	t.Helper()
	h, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

// 最小可用 schema，够验证版本表逻辑。
const miniSchema = `
CREATE TABLE IF NOT EXISTS things (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT);
INSERT INTO things (name) VALUES ('a'),('b');
`

func TestParseName(t *testing.T) {
	cases := []struct {
		in      string
		wantVer int64
		wantNm  string
		wantErr bool
	}{
		{"0001_init.sql", 1, "init", false},
		{"0002_add_users.sql", 2, "add_users", false},
		{"10_ten.sql", 10, "ten", false},
		// 注意：parseName 本身不剥离 _postgres_，那是 Load 的职责。
		// 这里直接调用 parseName 时 "postgres_init" 就是描述名的一部分。
		{"0001_postgres_init.sql", 1, "postgres_init", false},
		{"0001_init.txt", 0, "", true},
		{"nover_init.sql", 0, "", true},
		{"_nounderscore.sql", 0, "", true},
		{"init.sql", 0, "", true},
		{"abc_init.sql", 0, "", true},
	}
	for _, c := range cases {
		ver, nm, err := parseName(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseName(%q) 应报错", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseName(%q) 报错: %v", c.in, err)
			continue
		}
		if ver != c.wantVer || nm != c.wantNm {
			t.Errorf("parseName(%q) = (%d, %q)，期望 (%d, %q)", c.in, ver, nm, c.wantVer, c.wantNm)
		}
	}
}

func TestLoad_按版本排序(t *testing.T) {
	fsys := fstest.MapFS{
		"0002_second.sql": {Data: []byte("SELECT 1")},
		"0001_first.sql":  {Data: []byte("SELECT 1")},
		"0010_tenth.sql":  {Data: []byte("SELECT 1")},
	}
	m := New(newDB(t), fsys, ".")
	list, err := m.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("加载到 %d 个迁移，期望 3", len(list))
	}
	if list[0].Version != 1 || list[1].Version != 2 || list[2].Version != 10 {
		t.Errorf("排序错误: %d, %d, %d", list[0].Version, list[1].Version, list[2].Version)
	}
}

func TestLoad_跳过down文件(t *testing.T) {
	fsys := fstest.MapFS{
		"0001_init.sql":      {Data: []byte("SELECT 1")},
		"0001_init.down.sql": {Data: []byte("SELECT 2")},
		"0002_next.sql":      {Data: []byte("SELECT 3")},
		"0002_next.down.sql": {Data: []byte("SELECT 4")},
	}
	m := New(newDB(t), fsys, ".")
	list, err := m.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Errorf("应只加载 2 个 up 迁移，实际 = %d", len(list))
	}
}

func TestLoad_版本冲突报错(t *testing.T) {
	fsys := fstest.MapFS{
		"0001_a.sql": {Data: []byte("SELECT 1")},
		"0001_b.sql": {Data: []byte("SELECT 1")},
	}
	m := New(newDB(t), fsys, ".")
	if _, err := m.Load(context.Background()); err == nil {
		t.Error("重复版本号应报错")
	}
}

func TestLoad_按方言过滤(t *testing.T) {
	fsys := fstest.MapFS{
		"0001_init.sql":          {Data: []byte("CREATE TABLE a (id INTEGER)")},
		"0001_postgres_init.sql": {Data: []byte("CREATE TABLE a (id BIGSERIAL)")},
	}
	ctx := context.Background()

	// SQLite 模式只应加载无后缀那份
	sq := NewWithDialect(newDB(t), fsys, ".", DialectSQLite)
	list, err := sq.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("SQLite 应加载 1 个， 实际 = %d", len(list))
	}
	if !strings.Contains(list[0].SQL, "INTEGER") {
		t.Errorf("SQLite 加载了错误的文件: %s", list[0].SQL)
	}

	// PostgreSQL 模式只应加载带后缀那份
	pg := NewWithDialect(newDB(t), fsys, ".", DialectPostgres)
	list, err = pg.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("PostgreSQL 应加载 1 个，实际 = %d", len(list))
	}
	if !strings.Contains(list[0].SQL, "BIGSERIAL") {
		t.Errorf("PostgreSQL 加载了错误的文件: %s", list[0].SQL)
	}
	// 版本号应被还原为 1（去掉 _postgres_ 前缀段）
	if list[0].Version != 1 {
		t.Errorf("版本号 = %d，期望 1", list[0].Version)
	}
}

func TestUp_幂等(t *testing.T) {
	h := newDB(t)
	fsys := fstest.MapFS{
		"0001_init.sql": {Data: []byte(miniSchema)},
	}
	m := New(h, fsys, ".")
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := m.Up(ctx); err != nil {
			t.Fatalf("第 %d 次 Up 失败: %v", i+1, err)
		}
	}
	var n int
	h.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n)
	if n != 1 {
		t.Errorf("版本记录数 = %d，期望 1", n)
	}
	// 数据不应被重复插入
	var things int
	h.QueryRowContext(ctx, `SELECT COUNT(*) FROM things`).Scan(&things)
	if things != 2 {
		t.Errorf("数据行数 = %d，期望 2（不应重复插入）", things)
	}
}

func TestUp_失败时回滚(t *testing.T) {
	h := newDB(t)
	fsys := fstest.MapFS{
		"0001_ok.sql":  {Data: []byte("CREATE TABLE ok1 (id INTEGER)")},
		"0002_bad.sql": {Data: []byte("CREATE TABLE ok2 (id INTEGER); THIS IS NOT SQL;")},
	}
	m := New(h, fsys, ".")
	ctx := context.Background()

	err := m.Up(ctx)
	if err == nil {
		t.Fatal("第 2 个迁移含语法错误，应失败")
	}
	// 第 1 个应已生效
	var n int
	h.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='ok1'`).Scan(&n)
	if n != 1 {
		t.Error("第 1 个迁移应已成功执行")
	}
	// 第 2 个应被回滚
	h.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='ok2'`).Scan(&n)
	if n != 0 {
		t.Error("第 2 个迁移应被回滚，表不应存在")
	}
	// 版本表不应记录失败的那个
	var v int
	h.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = 2`).Scan(&v)
	if v != 0 {
		t.Error("失败的迁移不应被记录版本")
	}
}

func TestVersion_空库返回0(t *testing.T) {
	m := New(newDB(t), fstest.MapFS{}, ".")
	v, err := m.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != 0 {
		t.Errorf("空库版本 = %d，期望 0", v)
	}
}

func TestStatus_检测篡改(t *testing.T) {
	h := newDB(t)
	fsys := fstest.MapFS{"0001_init.sql": {Data: []byte(miniSchema)}}
	m := New(h, fsys, ".")
	ctx := context.Background()
	if err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}

	st, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(st) != 1 || !st[0].Applied {
		t.Fatalf("状态异常: %+v", st)
	}
	if st[0].Mismatch {
		t.Error("刚执行的迁移不应标记为不匹配")
	}

	// 篡改已执行的迁移内容
	h.ExecContext(ctx, `UPDATE schema_migrations SET checksum = 12345 WHERE version = 1`)
	st, _ = m.Status(ctx)
	if !st[0].Mismatch {
		t.Error("内容被篡改后应标记 Mismatch")
	}
}

func TestJoin(t *testing.T) {
	m := New(newDB(t), fstest.MapFS{}, "migrations")
	if got := m.join("0001_init.sql"); got != "migrations/0001_init.sql" {
		t.Errorf("join = %q", got)
	}
	m2 := New(newDB(t), fstest.MapFS{}, ".")
	if got := m2.join("0001_init.sql"); got != "0001_init.sql" {
		t.Errorf("dir=. 时 join 应去掉前缀，实际 = %q", got)
	}
	m3 := New(newDB(t), fstest.MapFS{}, "")
	if got := m3.join("0001_init.sql"); got != "0001_init.sql" {
		t.Errorf("dir 为空时 join 应返回文件名，实际 = %q", got)
	}
}

func TestRollback_缺down文件报错(t *testing.T) {
	h := newDB(t)
	fsys := fstest.MapFS{"0001_init.sql": {Data: []byte(miniSchema)}}
	m := New(h, fsys, ".")
	ctx := context.Background()
	if err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	// 没有 .down.sql，应报错而不是静默跳过
	if err := m.Rollback(ctx, 0); err == nil {
		t.Error("缺少回滚文件时应报错")
	}
}

func TestRollback_正常回滚(t *testing.T) {
	h := newDB(t)
	fsys := fstest.MapFS{
		"0001_init.sql":      {Data: []byte("CREATE TABLE t1 (id INTEGER)")},
		"0001_init.down.sql": {Data: []byte("DROP TABLE t1;")},
	}
	m := New(h, fsys, ".")
	ctx := context.Background()
	if err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Rollback(ctx, 0); err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	var n int
	h.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n)
	if n != 0 {
		t.Errorf("回滚后版本记录应清空，实际 = %d", n)
	}
	h.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='t1'`).Scan(&n)
	if n != 0 {
		t.Error("回滚后表应被删除")
	}
}

func TestRollback_目标大于当前报错(t *testing.T) {
	h := newDB(t)
	fsys := fstest.MapFS{
		"0001_init.sql":      {Data: []byte("CREATE TABLE t1 (id INTEGER)")},
		"0001_init.down.sql": {Data: []byte("DROP TABLE t1;")},
	}
	m := New(h, fsys, ".")
	ctx := context.Background()
	m.Up(ctx)
	if err := m.Rollback(ctx, 99); err == nil {
		t.Error("回滚到更高版本应报错")
	}
}

func TestBuiltin_可用(t *testing.T) {
	// 内置迁移集必须能被加载（生产环境就靠它）
	f := Builtin()
	entries, err := fs.ReadDir(f, ".")
	if err != nil {
		t.Fatalf("读取内置迁移失败: %v", err)
	}
	found := false
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			found = true
		}
	}
	if !found {
		t.Error("内置迁移集中没有 SQL 文件")
	}

	// 实跑一遍，确认内嵌的 DDL 真能在 SQLite 上执行
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := New(db, f, ".")
	if err := m.Up(context.Background()); err != nil {
		t.Fatalf("内置迁移执行失败: %v", err)
	}
}

func TestTimeRoundTrip(t *testing.T) {
	m := NewWithDialect(newDB(t), fstest.MapFS{}, ".", DialectSQLite)
	orig := timeDate(2026, 10, 5, 12, 30, 45)

	formatted := m.formatTime(orig)
	back, err := m.parseTime(formatted)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !back.Equal(orig) {
		t.Errorf("往返不一致: %v != %v", back, orig)
	}

	// 也应接受 time.Time 形式
	if _, err := m.parseTime(orig); err != nil {
		t.Errorf("不应拒绝 time.Time: %v", err)
	}
	// 非法输入应报错
	if _, err := m.parseTime("not-a-time"); err == nil {
		t.Error("非法时间字符串应报错")
	}
	if _, err := m.parseTime(nil); err == nil {
		t.Error("nil 应报错")
	}
}

// timeDate 构造 UTC 时间，供时间往返测试用。
func timeDate(y int, mo int, d, h, mi, sec int) time.Time {
	return time.Date(y, time.Month(mo), d, h, mi, sec, 0, time.UTC)
}
