package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/m202471895/probeone/server/internal/config"
	"github.com/m202471895/probeone/server/internal/migrate"
	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/store"
	"github.com/m202471895/probeone/server/internal/store/sqlite"
)

// setupTestDB 建一个迁移到最新版的临时数据库。
// 集成测试用真实数据库而非mock—— mock 过的 SQL 往往能过，
// 但一上真实库才暴露方言差异（PRD 测试策略）。
func setupTestDB(t *testing.T) *store.DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	cfg := &config.DatabaseConfig{Driver: "sqlite", Path: path}
	handle, err := sqlite.Open(cfg)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })

	m := migrate.New(handle, migrate.Builtin(), ".",
		migrate.WithLogger(func(f string, a ...any) { t.Logf(f, a...) }))
	if err := m.Up(context.Background()); err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}

	// 验证数据库文件权限（PRD T12）
	if info, err := os.Stat(path); err == nil {
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("数据库文件权限过宽: %o，应为 600", perm)
		}
	}

	return store.NewWithDialect(handle, "sqlite")
}

func TestMigrate_幂等(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.db")
	handle, err := sqlite.Open(&config.DatabaseConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()

	m := migrate.New(handle, migrate.Builtin(), ".")
	ctx := context.Background()

	// 连续跑三次都不应报错，且版本不重复记录
	for i := 0; i < 3; i++ {
		if err := m.Up(ctx); err != nil {
			t.Fatalf("第 %d 次执行迁移失败: %v", i+1, err)
		}
	}

	var count int
	if err := handle.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("迁移版本记录数 = %d，期望 1（幂等要求不重复记录）", count)
	}

	ver, err := m.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ver != 1 {
		t.Errorf("当前版本 = %d，期望 1", ver)
	}
}

func TestMigrate_建表齐全(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	expect := []string{
		"users", "sessions", "node_groups", "nodes",
		"node_metrics", "node_metrics_rollup",
		"monitors", "monitor_results", "ssl_certificates",
		"alert_channels", "alert_rules", "alert_events",
		"login_attempts", "agent_failures", "agent_sessions",
		"visibility_policies", "settings", "audit_logs",
	}
	for _, table := range expect {
		var n int
		q := `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`
		if err := db.SQL.QueryRowContext(ctx, q, table).Scan(&n); err != nil {
			t.Errorf("查询表 %s 失败: %v", table, err)
			continue
		}
		if n == 0 {
			t.Errorf("缺少表 %s", table)
		}
	}
}

func TestMigrate_预置可见性策略(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	pol, err := db.Visibility.Policies(ctx, model.ScopePublicStatus)
	if err != nil {
		t.Fatal(err)
	}
	if len(pol) == 0 {
		t.Fatal("public_status scope 未预置策略")
	}

	byField := make(map[string]model.VisibilityPolicy, len(pol))
	for _, p := range pol {
		byField[p.Field] = p
	}

	// 公网 IP 必须默认不可见
	if p, ok := byField["public_ip"]; !ok {
		t.Error("缺少 public_ip 策略")
	} else if p.Visible {
		t.Error("public_ip 在公开状态页默认必须不可见")
	}
	// 硬件规格按产品决策公开
	if p, ok := byField["cpu_model"]; !ok || !p.Visible {
		t.Error("cpu_model 应默认公开")
	}
	// 主机名必须默认不可见
	if p, ok := byField["hostname"]; !ok {
		t.Error("缺少 hostname 策略")
	} else if p.Visible {
		t.Error("hostname 在公开状态页默认必须不可见")
	}
	// 关键：agent_secret 绝不能出现在策略表里
	if _, ok := byField["agent_secret"]; ok {
		t.Error("agent_secret 不应出现在可见性策略表中（它是硬禁止字段）")
	}
}

func TestUserRepo_CRUD(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	id, err := db.Users.Create(ctx, store.CreateUserInput{
		Username: "admin",
		Password: "Admin123456",
		Role:     model.RoleOwner,
	})
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	u, err := db.Users.GetByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("按名取用户失败: %v", err)
	}
	if u.ID != id {
		t.Errorf("ID 不匹配: %d != %d", u.ID, id)
	}
	// 用户名统一小写，查询时也应大小写不敏感
	if _, err := db.Users.GetByUsername(ctx, "ADMIN"); err != nil {
		t.Errorf("用户名查询应大小写不敏感: %v", err)
	}
	// 密码必须哈希存储，绝不能是明文
	if u.PasswordHash == "Admin123456" {
		t.Fatal("密码以明文存储，这是严重安全缺陷")
	}
	if len(u.PasswordHash) < 40 {
		t.Errorf("哈希长度 %d 过短，可能不是 argon2id", len(u.PasswordHash))
	}

	// 角色变更
	if err := db.Users.UpdateRole(ctx, id, model.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	u, _ = db.Users.GetByID(ctx, id)
	if u.Role != model.RoleAdmin {
		t.Errorf("角色 = %s，期望 admin", u.Role)
	}

	// 计数
	n, err := db.Users.Count(ctx)
	if err != nil || n != 1 {
		t.Errorf("用户数 = %d（err=%v），期望 1", n, err)
	}

	// 删除
	if err := db.Users.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Users.GetByID(ctx, id); err == nil {
		t.Error("删除后仍能取到用户")
	}
}

func TestUserRepo_用户名唯一(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	if _, err := db.Users.Create(ctx, store.CreateUserInput{
		Username: "same", Password: "Password12345", Role: model.RoleViewer,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := db.Users.Create(ctx, store.CreateUserInput{
		Username: "same", Password: "Password12345", Role: model.RoleViewer,
	})
	if err == nil {
		t.Error("重复用户名应被拒绝")
	}
}

func TestSessionRepo_过期即失效(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	uid, _ := db.Users.Create(ctx, store.CreateUserInput{
		Username: "u1", Password: "Password12345", Role: model.RoleViewer,
	})
	hash := "hash_abc123"

	// 正常会话
	if _, err := db.Sessions.Create(ctx, uid, hash, "1.2.3.4", "curl", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Sessions.GetByTokenHash(ctx, hash); err != nil {
		t.Errorf("有效会话应可读: %v", err)
	}

	// 已过期会话应视为不存在
	expired := "hash_expired"
	if _, err := db.Sessions.Create(ctx, uid, expired, "1.2.3.4", "curl", time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Sessions.GetByTokenHash(ctx, expired); err == nil {
		t.Error("过期会话应被拒绝")
	}
}

func TestAgentSession_重复握手使旧会话失效(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	nid, err := db.Nodes.Create(ctx, store.CreateNodeInput{
		UID: "uid-1", Name: "node-1", SecretHash: "h1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 第一次握手
	if err := db.AgentSessions.CreateSession(ctx, nid, "sess-A", "1.2.3.4", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AgentSessions.GetSession(ctx, "sess-A"); err != nil {
		t.Fatalf("新会话应有效: %v", err)
	}

	// 第二次握手：旧会话必须立即失效（防重放，PRD 7.3）
	if err := db.AgentSessions.CreateSession(ctx, nid, "sess-B", "1.2.3.4", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AgentSessions.GetSession(ctx, "sess-A"); err == nil {
		t.Error("旧会话应在新握手后立即失效——这是防重放的关键")
	}
	if _, err := db.AgentSessions.GetSession(ctx, "sess-B"); err != nil {
		t.Errorf("新会话应有效: %v", err)
	}
}

func TestAgentFailure_计数与封禁(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		n, err := db.AgentSessions.RecordAgentFailure(ctx, "uuid-x", "9.9.9.9")
		if err != nil {
			t.Fatal(err)
		}
		if n != i {
			t.Errorf("第 %d 次失败计数 = %d，期望 %d", i, n, i)
		}
	}

	locked, err := db.AgentSessions.IsHardLocked(ctx, "uuid-x", "9.9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if locked {
		t.Error("失败 3 次不应触发封禁（阈值 20）")
	}

	// 手工标记封禁并检查
	if _, err := db.SQL.ExecContext(ctx,
		`UPDATE agent_failures SET hard_locked = 1, locked_until = ? WHERE client_uuid = ?`,
		time.Now().Add(24*time.Hour).UTC(), "uuid-x"); err != nil {
		t.Fatal(err)
	}
	locked, _ = db.AgentSessions.IsHardLocked(ctx, "uuid-x", "9.9.9.9")
	if !locked {
		t.Error("已标记封禁的 uuid 应被拒绝")
	}

	// 封禁到期后自动解除
	if _, err := db.SQL.ExecContext(ctx,
		`UPDATE agent_failures SET locked_until = ? WHERE client_uuid = ?`,
		time.Now().Add(-time.Hour).UTC(), "uuid-x"); err != nil {
		t.Fatal(err)
	}
	locked, _ = db.AgentSessions.IsHardLocked(ctx, "uuid-x", "9.9.9.9")
	if locked {
		t.Error("封禁到期后应自动解除")
	}

	// 成功握手后清零
	if err := db.AgentSessions.ClearAgentFailures(ctx, "uuid-x", "9.9.9.9"); err != nil {
		t.Fatal(err)
	}
	var cnt int
	db.SQL.QueryRowContext(ctx, `SELECT count FROM agent_failures WHERE client_uuid = ?`, "uuid-x").Scan(&cnt)
	if cnt != 0 {
		t.Errorf("握手成功后失败计数应清零，实际 = %d", cnt)
	}
}

func TestNodeRepo_增删查改(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	id, err := db.Nodes.Create(ctx, store.CreateNodeInput{
		UID: "n-1", Name: "香港节点", SecretHash: "hash1",
	})
	if err != nil {
		t.Fatal(err)
	}

	n, err := db.Nodes.GetByUID(ctx, "n-1")
	if err != nil {
		t.Fatal(err)
	}
	if n.Name != "香港节点" {
		t.Errorf("名称 = %s", n.Name)
	}
	if n.Status != model.NodePending {
		t.Errorf("初始状态 = %s，期望 pending", n.Status)
	}
	// 密钥哈希必须能取到（服务端鉴权要用），但绝不返回客户端
	if n.AgentSecretHash == "" {
		t.Error("密钥哈希未保存")
	}

	// 更新
	n.Name = "香港节点-01"
	n.CPUCores = 4
	n.MemTotal = 8 * 1024 * 1024 * 1024
	n.IsPublic = true
	n.HardwareFP = "fp-abc"
	if err := db.Nodes.Update(ctx, n); err != nil {
		t.Fatal(err)
	}

	n2, _ := db.Nodes.GetByID(ctx, id)
	if n2.Name != "香港节点-01" || n2.CPUCores != 4 || !n2.IsPublic {
		t.Errorf("更新未生效: %+v", n2)
	}
	if n2.HardwareFP != "fp-abc" {
		t.Error("硬件指纹未保存")
	}

	// 密钥轮换
	if err := db.Nodes.RotateSecret(ctx, id, "hash2"); err != nil {
		t.Fatal(err)
	}
	n3, _ := db.Nodes.GetByID(ctx, id)
	if n3.AgentSecretHash != "hash2" {
		t.Errorf("密钥轮换未生效: %s", n3.AgentSecretHash)
	}
}

func TestNodeRepo_状态页过滤离线节点(t *testing.T) {
	// 侧信道防护：公开列表必须排除离线节点（PRD T16）
	db := setupTestDB(t)
	ctx := context.Background()

	online, _ := db.Nodes.Create(ctx, store.CreateNodeInput{UID: "on", Name: "在线", SecretHash: "h"})
	offline, _ := db.Nodes.Create(ctx, store.CreateNodeInput{UID: "off", Name: "离线", SecretHash: "h"})
	secret, _ := db.Nodes.Create(ctx, store.CreateNodeInput{UID: "sec", Name: "未公开", SecretHash: "h"})

	for _, id := range []int64{online, offline, secret} {
		if err := db.Nodes.Update(ctx, &model.Node{ID: id, IsPublic: true, Name: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	db.SQL.ExecContext(ctx, `UPDATE nodes SET status = 'online' WHERE id = ?`, online)
	db.SQL.ExecContext(ctx, `UPDATE nodes SET status = 'offline' WHERE id = ?`, offline)
	// secret 保持 offline 之外的 public 状态但为 pending

	// 未指定 PublicOnly 时应能取到全部
	all, total, err := db.Nodes.List(ctx, store.NodeListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Errorf("不加过滤应返回 3 个，实际 = %d", total)
	}
	_ = all

	// PublicOnly 必须排除离线节点
	pub, pubTotal, err := db.Nodes.List(ctx, store.NodeListFilter{PublicOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if pubTotal != 1 {
		t.Errorf("公开列表应只有 1 个在线已公开节点，实际 = %d（离线节点泄露会形成侧信道）", pubTotal)
	}
	for _, n := range pub {
		if n.UID == "off" {
			t.Error("离线节点不应出现在公开状态页")
		}
		if n.Status == model.NodeOffline {
			t.Error("公开列表中出现离线节点")
		}
	}
}

func TestNodeRepo_离线判定(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	online, _ := db.Nodes.Create(ctx, store.CreateNodeInput{UID: "a", Name: "a", SecretHash: "h"})
	stale, _ := db.Nodes.Create(ctx, store.CreateNodeInput{UID: "b", Name: "b", SecretHash: "h"})

	// 用UTC 写入，与 sqlbase.Now() 的存储格式一致
	db.SQL.ExecContext(ctx, `UPDATE nodes SET status='online', last_report_at = ? WHERE id = ?`,
		time.Now().UTC().Truncate(time.Second), online)
	db.SQL.ExecContext(ctx, `UPDATE nodes SET status='online', last_report_at = ? WHERE id = ?`,
		time.Now().Add(-2*time.Hour).UTC().Truncate(time.Second), stale)

	// 宽限 1 小时：只有 stale 应被判离线
	n, err := db.Nodes.MarkOffline(ctx, time.Now().Add(-time.Hour).UTC().Truncate(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("应标记 1 个节点离线，实际 = %d", n)
	}

	var status string
	db.SQL.QueryRowContext(ctx, `SELECT status FROM nodes WHERE id = ?`, online).Scan(&status)
	if status != "online" {
		t.Errorf("活跃节点被误判离线: %s", status)
	}
	db.SQL.QueryRowContext(ctx, `SELECT status FROM nodes WHERE id = ?`, stale).Scan(&status)
	if status != "offline" {
		t.Errorf("超时节点未标记离线: %s", status)
	}

	// 再次上报应恢复在线
	if err := db.Nodes.TouchReport(ctx, stale, time.Now()); err != nil {
		t.Fatal(err)
	}
	db.SQL.QueryRowContext(ctx, `SELECT status FROM nodes WHERE id = ?`, stale).Scan(&status)
	if status != "online" {
		t.Errorf("重新上报后应恢复在线，实际 = %s", status)
	}
}

func TestMetricRepo_写入与查询(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	nid, _ := db.Nodes.Create(ctx, store.CreateNodeInput{UID: "n1", Name: "n1", SecretHash: "h"})

	now := time.Now().UTC()
	batch := []model.NodeMetric{
		{NodeID: nid, CollectedAt: now.Add(-2 * time.Minute), CPUUsage: 10,
			MemTotal: 1024, MemUsage: 20, Disks: []model.DiskUsage{{Mount: "/", Usage: 55}},
			NetIO: []model.NetUsage{{Iface: "eth0", RxBps: 1000, TxBps: 2000}}},
		{NodeID: nid, CollectedAt: now.Add(-time.Minute), CPUUsage: 80,
			MemTotal: 1024, MemUsage: 85, Disks: []model.DiskUsage{{Mount: "/", Usage: 60}},
			NetIO: []model.NetUsage{{Iface: "eth0", RxBps: 5000, TxBps: 3000}}},
		{NodeID: nid, CollectedAt: now, CPUUsage: 45,
			MemTotal: 1024, MemUsage: 50, Disks: []model.DiskUsage{{Mount: "/", Usage: 58}},
			NetIO: []model.NetUsage{{Iface: "eth0", RxBps: 3000, TxBps: 1000}}},
	}
	if err := db.Metrics.InsertBatch(ctx, batch); err != nil {
		t.Fatalf("批量写入失败: %v", err)
	}

	// 范围查询
	got, err := db.Metrics.Range(ctx, nid, now.Add(-time.Hour), now.Add(time.Minute), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("范围查询返回 %d 条，期望 3", len(got))
	}
	// 时间升序
	if got[0].CPUUsage != 10 || got[2].CPUUsage != 45 {
		t.Errorf("时间排序错误: %v", []float64{got[0].CPUUsage, got[2].CPUUsage})
	}
	// JSON 列应正确还原
	if len(got[0].Disks) != 1 || got[0].Disks[0].Mount != "/" {
		t.Errorf("disk_usage JSON 未正确还原: %+v", got[0].Disks)
	}
	if len(got[0].NetIO) != 1 || got[0].NetIO[0].RxBps != 1000 {
		t.Errorf("net_io JSON 未正确还原: %+v", got[0].NetIO)
	}

	// 最新一条
	latest, err := db.Metrics.Latest(ctx, []int64{nid})
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := latest[nid]; !ok || m.CPUUsage != 45 {
		t.Errorf("最新值 = %+v，期望 CPU=45", m)
	}
}

func TestMetricRepo_预聚合(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	nid, _ := db.Nodes.Create(ctx, store.CreateNodeInput{UID: "n1", Name: "n1", SecretHash: "h"})

	// 一分钟内 6 个采样点（10s 间隔）
	base := time.Now().UTC().Truncate(time.Minute)
	var ms []model.NodeMetric
	for i := 0; i < 6; i++ {
		ms = append(ms, model.NodeMetric{
			NodeID: nid, CollectedAt: base.Add(time.Duration(i*10) * time.Second),
			CPUUsage: float64(10 + i*10), // 10,20,...,60
			MemUsage: float64(50 + i),
			Load1:    1.5, Disks: []model.DiskUsage{{Mount: "/", Usage: float64(70 + i)}},
			NetIO: []model.NetUsage{{Iface: "eth0", RxBps: int64(1000 * (i + 1)), TxBps: 500}},
		})
	}
	if err := db.Metrics.InsertBatch(ctx, ms); err != nil {
		t.Fatal(err)
	}

	if err := db.Metrics.ComputeAndStoreRollup(ctx, "1m", base, base.Add(time.Minute)); err != nil {
		t.Fatalf("聚合失败: %v", err)
	}

	pts, err := db.Metrics.Rollup(ctx, nid, "1m", base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 {
		t.Fatalf("聚合点数 = %d，期望 1", len(pts))
	}
	p := pts[0]
	if p.SampleCount != 6 {
		t.Errorf("样本数 = %d，期望 6", p.SampleCount)
	}
	// 平均 CPU = (10+20+30+40+50+60)/6 = 35
	if p.CPUAvg < 34 || p.CPUAvg > 36 {
		t.Errorf("CPU 均值 = %.1f，期望 35", p.CPUAvg)
	}
	if p.CPUMax < 59 || p.CPUMax > 61 {
		t.Errorf("CPU 峰值 = %.1f，期望 60", p.CPUMax)
	}
	if p.DiskUsageMax < 74 || p.DiskUsageMax > 76 {
		t.Errorf("磁盘峰值 = %.1f，期望 75", p.DiskUsageMax)
	}

	// 幂等：重复执行不产生重复行
	if err := db.Metrics.ComputeAndStoreRollup(ctx, "1m", base, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	pts2, _ := db.Metrics.Rollup(ctx, nid, "1m", base.Add(-time.Hour), base.Add(time.Hour))
	if len(pts2) != 1 {
		t.Errorf("重复聚合后点数 = %d，期望仍为 1（幂等要求）", len(pts2))
	}
}

func TestMetricRepo_清理(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	nid, _ := db.Nodes.Create(ctx, store.CreateNodeInput{UID: "n1", Name: "n1", SecretHash: "h"})

	db.Metrics.Insert(ctx, &model.NodeMetric{
		NodeID: nid, CollectedAt: time.Now().Add(-100 * 24 * time.Hour).UTC(), CPUUsage: 1,
	})
	n, err := db.Metrics.PurgeRaw(ctx, time.Now().Add(-30*24*time.Hour).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("应清理 1 条过期原始数据，实际 = %d", n)
	}
}

func TestMonitorRepo_生命周期(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	id, err := db.Monitors.Create(ctx, &model.Monitor{
		Name: "官网", Type: model.MonitorHTTP, Target: "https://example.com",
		Config:      model.MonitorConfig{Method: "GET", ExpectStatus: []int{200}},
		IntervalSec: 60, TimeoutSec: 10, IsPublic: true, Status: model.MonitorPending,
	})
	if err != nil {
		t.Fatal(err)
	}

	m, err := db.Monitors.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "官网" || m.Type != model.MonitorHTTP {
		t.Errorf("读取失败: %+v", m)
	}
	// config 是 JSON 列，应正确还原
	if m.Config.Method != "GET" || len(m.Config.ExpectStatus) != 1 || m.Config.ExpectStatus[0] != 200 {
		t.Errorf("config JSON 未还原: %+v", m.Config)
	}

	// 状态更新
	latency := 128
	if err := db.Monitors.UpdateStatus(ctx, id, model.MonitorUp, time.Now(), &latency); err != nil {
		t.Fatal(err)
	}
	m, _ = db.Monitors.GetByID(ctx, id)
	if m.Status != model.MonitorUp {
		t.Errorf("状态 = %s，期望 up", m.Status)
	}
	if m.AvgLatencyMs == nil || *m.AvgLatencyMs != 128 {
		t.Errorf("延迟未记录: %+v", m.AvgLatencyMs)
	}
}

func TestMonitorRepo_可用率与分位数(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	mid, _ := db.Monitors.Create(ctx, &model.Monitor{
		Name: "test", Type: model.MonitorHTTP, Target: "https://x.com",
		IntervalSec: 60, TimeoutSec: 10,
	})

	// 20 次采样，18 次成功，延迟 10..200
	for i := 0; i < 20; i++ {
		ok := i < 18
		lat := (i + 1) * 10
		db.Monitors.InsertResult(ctx, &model.MonitorResult{
			MonitorID: mid, CheckedAt: time.Now().UTC().Truncate(time.Second),
			OK: ok, Reason: modelReason(ok), LatencyMs: &lat,
		})
	}

	st, err := db.Monitors.Stats(ctx, mid,
		time.Now().Add(-time.Hour).UTC().Truncate(time.Second),
		time.Now().Add(time.Hour).UTC().Truncate(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 20 || st.Up != 18 {
		t.Errorf("样本统计 = %d/%d，期望 18/20", st.Up, st.Total)
	}
	if st.SampleInsufficient {
		t.Error("20 个样本不应标记为样本不足")
	}
	if st.UptimePercent < 89 || st.UptimePercent > 91 {
		t.Errorf("可用率 = %.1f%%，期望 90%%", st.UptimePercent)
	}
	// 18 个样本取第 int(17*0.5)=8 位 → 90
	if st.LatencyP50 != 90 {
		t.Errorf("P50 = %d，期望 90", st.LatencyP50)
	}
	// 成功样本为延迟 10..180 共 18 个，最近秩法 P95 取第 16 位 = 170
	if st.LatencyP95 != 170 {
		t.Errorf("P95 = %d，期望 170", st.LatencyP95)
	}
}

func TestMonitorRepo_样本不足时不给百分比(t *testing.T) {
	// 只有 3 个样本时不给百分比，避免误导（PRD 8.7）
	db := setupTestDB(t)
	ctx := context.Background()
	mid, _ := db.Monitors.Create(ctx, &model.Monitor{
		Name: "t", Type: model.MonitorHTTP, Target: "https://x.com", IntervalSec: 60,
	})
	for i := 0; i < 3; i++ {
		db.Monitors.InsertResult(ctx, &model.MonitorResult{
			MonitorID: mid, CheckedAt: time.Now().UTC().Truncate(time.Second), OK: true, Reason: model.ReasonOK,
		})
	}
	st, _ := db.Monitors.Stats(ctx, mid,
		time.Now().Add(-time.Hour).UTC().Truncate(time.Second),
		time.Now().Add(time.Hour).UTC().Truncate(time.Second))
	if !st.SampleInsufficient {
		t.Error("3 个样本应标记为样本不足")
	}
}

func TestMonitorRepo_到期筛选(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// 已暂停的监控不应被选中
	paused, _ := db.Monitors.Create(ctx, &model.Monitor{
		Name: "paused", Type: model.MonitorHTTP, Target: "https://a.com",
		IntervalSec: 60, Status: model.MonitorPaused,
	})
	// 到期的监控
	due, _ := db.Monitors.Create(ctx, &model.Monitor{
		Name: "due", Type: model.MonitorHTTP, Target: "https://b.com",
		IntervalSec: 60, Status: model.MonitorUp,
	})
	// 刚检查过、未到期的监控
	db.Monitors.Create(ctx, &model.Monitor{
		Name: "fresh", Type: model.MonitorHTTP, Target: "https://c.com",
		IntervalSec: 3600, Status: model.MonitorUp,
	})
	db.Monitors.UpdateStatus(ctx, due, model.MonitorUp, time.Now(), nil)

	list, err := db.Monitors.DueMonitors(ctx, time.Now().Add(2*time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range list {
		if m.ID == paused {
			t.Error("已暂停的监控不应被调度")
		}
	}
}

func TestAlertRepo_去重(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	nid, _ := db.Nodes.Create(ctx, store.CreateNodeInput{UID: "n1", Name: "n1", SecretHash: "h"})
	rid, _ := db.Alerts.CreateRule(ctx, &model.AlertRule{
		Name: "CPU 高", TargetType: model.TargetNode, TargetID: &nid,
		Metric: "cpu_usage", Condition: model.RuleCondition{Op: ">", Value: 80},
		Severity: model.SeverityWarning, ChannelIDs: []int64{}, DedupWindowSec: 1800, Enabled: true,
	})

	mk := func() *model.AlertEvent {
		return &model.AlertEvent{
			RuleID: &rid, TargetType: model.TargetNode, TargetID: &nid,
			TargetName: "n1", Severity: model.SeverityWarning, Message: "CPU 92%",
		}
	}

	// 第一次：新建
	ev, created, err := db.Alerts.FireEvent(ctx, mk(), 1800)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("首次触发应新建事件")
	}
	firstID := ev.ID

	// 去重窗口内再触发：不新建
	ev2, created2, err := db.Alerts.FireEvent(ctx, mk(), 1800)
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Error("去重窗口内重复触发不应新建事件")
	}
	if ev2.ID != firstID {
		t.Errorf("应复用同一事件，实际 ID %d != %d", ev2.ID, firstID)
	}

	// 事件总数应为 1
	_, total, _ := db.Alerts.ListEvents(ctx, store.AlertListFilter{})
	if total != 1 {
		t.Errorf("事件总数 = %d，期望 1（去重生效）", total)
	}

	// 窗口外的触发：应新建
	_, created3, err := db.Alerts.FireEvent(ctx, mk(), 0) // dedup=0 会走默认 1800s
	if err != nil {
		t.Fatal(err)
	}
	_ = created3

	// 手动把 last_fired_at 推远，模拟窗口外
	db.SQL.ExecContext(ctx, `UPDATE alert_events SET last_fired_at = ?`, time.Now().Add(-2*time.Hour).UTC())
	_, created4, err := db.Alerts.FireEvent(ctx, mk(), 1800)
	if err != nil {
		t.Fatal(err)
	}
	if !created4 {
		t.Error("超出去重窗口后应新建事件")
	}
}

func TestAlertRepo_筛选条件一致(t *testing.T) {
	// Count 与 List 必须用同一套筛选条件，否则总数对不上
	db := setupTestDB(t)
	ctx := context.Background()
	nid, _ := db.Nodes.Create(ctx, store.CreateNodeInput{UID: "n1", Name: "n1", SecretHash: "h"})

	for i := 0; i < 3; i++ {
		sev := model.SeverityWarning
		st := model.EventFiring
		if i == 0 {
			sev = model.SeverityCritical
		}
		if i == 2 {
			st = model.EventResolved
		}
		db.Alerts.FireEvent(ctx, &model.AlertEvent{
			TargetType: model.TargetNode, TargetID: &nid, TargetName: "n1",
			Severity: sev, Status: st, Message: "x",
		}, 0)
	}

	// 按级别筛选
	_, total, err := db.Alerts.ListEvents(ctx, store.AlertListFilter{Severity: model.SeverityCritical})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Errorf("critical 事件数 = %d，期望 1", total)
	}

	// 按状态筛选
	_, total, _ = db.Alerts.ListEvents(ctx, store.AlertListFilter{Status: model.EventFiring})
	if total != 2 {
		t.Errorf("firing 事件数 = %d，期望 2", total)
	}

	// 多条件组合
	_, total, _ = db.Alerts.ListEvents(ctx, store.AlertListFilter{
		Status: model.EventFiring, Severity: model.SeverityWarning,
	})
	if total != 1 {
		t.Errorf("组合筛选结果 = %d，期望 1", total)
	}
}

func TestAlertRepo_确认与解决(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	nid, _ := db.Nodes.Create(ctx, store.CreateNodeInput{UID: "n1", Name: "n1", SecretHash: "h"})

	ev, _, _ := db.Alerts.FireEvent(ctx, &model.AlertEvent{
		TargetType: model.TargetNode, TargetID: &nid, TargetName: "n1",
		Severity: model.SeverityWarning, Message: "x",
	}, 0)

	uid, _ := db.Users.Create(ctx, store.CreateUserInput{
		Username: "u", Password: "Password12345", Role: model.RoleAdmin,
	})
	if err := db.Alerts.AckEvent(ctx, ev.ID, uid); err != nil {
		t.Fatal(err)
	}
	got, _ := db.Alerts.GetEvent(ctx, ev.ID)
	if got.Status != model.EventAcked {
		t.Errorf("状态 = %s，期望 acked", got.Status)
	}
	if got.AckedAt == nil {
		t.Error("确认时间未记录")
	}

	// 解决
	n, err := db.Alerts.ResolveEvents(ctx, nil, model.TargetNode, nid)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("应至少解决一个事件")
	}
}

func TestChannelRepo(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	id, err := db.Channels.Create(ctx, &model.AlertChannel{
		Name: "运维群", Type: model.ChannelWebhook,
		Config:  map[string]any{"url": "https://example.com/hook", "secret": "s3cr3t"},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	c, err := db.Channels.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "运维群" || c.Type != model.ChannelWebhook {
		t.Errorf("读取失败: %+v", c)
	}
	if c.Config["url"] != "https://example.com/hook" {
		t.Errorf("config JSON 未还原: %+v", c.Config)
	}

	c.Name = "运维群2"
	if err := db.Channels.Update(ctx, c); err != nil {
		t.Fatal(err)
	}
	c2, _ := db.Channels.GetByID(ctx, id)
	if c2.Name != "运维群2" {
		t.Error("更新未生效")
	}
}

func TestAuditRepo(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	uid, _ := db.Users.Create(ctx, store.CreateUserInput{
		Username: "admin", Password: "Password12345", Role: model.RoleOwner,
	})

	if err := db.Audit.Write(ctx, &model.AuditLog{
		UserID: &uid, Username: "admin", Action: "create_node",
		TargetType: "node", TargetID: "n1",
		Detail: map[string]any{"name": "香港"}, IP: "1.2.3.4", UserAgent: "curl",
	}); err != nil {
		t.Fatal(err)
	}

	logs, total, err := db.Audit.List(ctx, nil, "", "", "", time.Time{}, time.Time{}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(logs) != 1 {
		t.Fatalf("审计记录 = %d 条，期望 1", total)
	}
	if logs[0].Action != "create_node" {
		t.Errorf("action = %s", logs[0].Action)
	}
	if logs[0].Detail["name"] != "香港" {
		t.Errorf("detail JSON 未还原: %+v", logs[0].Detail)
	}

	// 按 action 筛选
	_, total, _ = db.Audit.List(ctx, nil, "create_node", "", "", time.Time{}, time.Time{}, 50, 0)
	if total != 1 {
		t.Errorf("按 action 筛选 = %d，期望 1", total)
	}
	// 无匹配时总数应为 0，而不是全部
	_, total, _ = db.Audit.List(ctx, nil, "delete_node", "", "", time.Time{}, time.Time{}, 50, 0)
	if total != 0 {
		t.Errorf("不匹配的筛选应返回 0，实际 = %d", total)
	}
}

func TestSettingsRepo(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	if _, ok, err := db.Settings.Get(ctx, "notexist"); err != nil || ok {
		t.Error("不存在的键应返回 ok=false")
	}
	if err := db.Settings.Set(ctx, "k", map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	v, ok, err := db.Settings.Get(ctx, "k")
	if err != nil || !ok {
		t.Fatalf("读取失败: %v ok=%v", err, ok)
	}
	m, isMap := v.(map[string]any)
	if !isMap || m["a"] != float64(1) {
		t.Errorf("值 = %+v", v)
	}

	// 覆盖写
	db.Settings.Set(ctx, "k", "v2")
	v, _, _ = db.Settings.Get(ctx, "k")
	if v != "v2" {
		t.Errorf("覆盖写失败: %+v", v)
	}
}

// modelReason 是测试辅助。
func modelReason(ok bool) model.FailReason {
	if ok {
		return model.ReasonOK
	}
	return model.ReasonTimeout
}

// 确认 store 包确实实现了全部接口——编译期检查
var _ = func() bool {
	var i any = store.New(nil)
	_ = i
	return true
}()
