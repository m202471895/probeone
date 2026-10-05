package grpcsvc

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	agentv1 "github.com/m202471895/probeone/api/agent/v1"
	"github.com/m202471895/probeone/server/internal/config"
	"github.com/m202471895/probeone/server/internal/migrate"
	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/store"
	"github.com/m202471895/probeone/server/internal/store/sqlite"
)

// setup 建一个迁移到最新版的测试库，返回已建好的节点与 Ingestor。
func setupIngestor(t *testing.T) (*store.DB, *Ingestor, int64) {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/test.db"

	handle, err := sqlite.Open(&config.DatabaseConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })

	m := migrate.New(handle, migrate.Builtin(), ".")
	if err := m.Up(context.Background()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	db := store.NewWithDialect(handle, "sqlite")
	// 关闭测试日志噪音，只在失败时输出
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	nodeID, err := db.Nodes.Create(context.Background(), store.CreateNodeInput{
		UID: "uid-test", Name: "test-node", SecretHash: "hash",
	})
	if err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	return db, NewIngestor(db, log), nodeID
}

// mkMetrics 构造一条上报的指标。
func mkMetrics(fp string, cores int32, memGB int64) *agentv1.ReportMetrics {
	return &agentv1.ReportMetrics{
		CollectedAt: time.Now().Unix(),
		Seq:         1,
		HardwareFp:  fp,
		Cpu: &agentv1.CpuStat{
			Usage:         12.5,
			CoresLogical:  cores,
			CoresPhysical: cores / 2,
			Model:         "Test CPU",
			PerCore:       []float32{10, 15},
		},
		Mem: &agentv1.MemStat{
			Total: memGB * 1024 * 1024 * 1024,
			Used:  memGB * 1024 * 1024 * 1024 / 2,
			Usage: 50,
		},
		Disks: []*agentv1.DiskStat{
			{Mount: "/", Device: "/dev/sda1", Fstype: "ext4", Total: 100 << 30, Usage: 45},
		},
		Nets: []*agentv1.NetStat{
			{Iface: "eth0", RxBps: 1000, TxBps: 2000},
		},
	}
}

func Test升配检测_首次上报不告警(t *testing.T) {
	db, ing, nodeID := setupIngestor(t)
	ctx := context.Background()

	// 首次上报：只记录，不产生审计与告警
	if err := ing.IngestMetrics(ctx, nodeID, mkMetrics("fp-initial", 4, 8)); err != nil {
		t.Fatal(err)
	}

	node, _ := db.Nodes.GetByID(ctx, nodeID)
	if node.HardwareFP != "fp-initial" {
		t.Errorf("指纹未记录: %q", node.HardwareFP)
	}
	if node.CPUCores != 4 {
		t.Errorf("核心数未记录: %d", node.CPUCores)
	}
	if node.HardwareChangedAt != nil {
		t.Error("首次上报不应标记 hardware_changed_at")
	}

	// 不应有审计与告警
	_, total, _ := db.Alerts.ListEvents(ctx, store.AlertListFilter{})
	if total != 0 {
		t.Errorf("首次上报不应产生告警，实际 = %d", total)
	}
}

func Test升配检测_单轮抖动不误报(t *testing.T) {
	// 云主机的 vCPU 会因宿主机调度出现 ±1 抖动，
	// 单轮变化不应触发变更（PRD 8.4 验收项）
	db, ing, nodeID := setupIngestor(t)
	ctx := context.Background()

	// 建立基线
	ing.IngestMetrics(ctx, nodeID, mkMetrics("fp-base", 4, 8))

	// 单轮变化：4核 → 5核
	ing.IngestMetrics(ctx, nodeID, mkMetrics("fp-jitter", 5, 8))

	// 此时不应有任何告警
	_, total, _ := db.Alerts.ListEvents(ctx, store.AlertListFilter{})
	if total != 0 {
		t.Errorf("单轮抖动不应告警，实际 = %d 个", total)
	}
	// 规格也不应被更新（还在确认中）
	node, _ := db.Nodes.GetByID(ctx, nodeID)
	if node.CPUCores != 4 {
		t.Errorf("待确认期间不应更新规格，核心数 = %d", node.CPUCores)
	}
	if node.HardwareFP != "fp-base" {
		t.Errorf("待确认期间不应更新指纹，实际 = %q", node.HardwareFP)
	}
}

func Test升配检测_连续三轮确认变更(t *testing.T) {
	db, ing, nodeID := setupIngestor(t)
	ctx := context.Background()

	// 基线
	ing.IngestMetrics(ctx, nodeID, mkMetrics("fp-base", 4, 8))

	// 第 1 轮新指纹：开始确认
	ing.IngestMetrics(ctx, nodeID, mkMetrics("fp-new", 8, 16))
	// 第 2 轮：仍在确认
	ing.IngestMetrics(ctx, nodeID, mkMetrics("fp-new", 8, 16))
	// 前两轮都还不该变更
	node, _ := db.Nodes.GetByID(ctx, nodeID)
	if node.CPUCores != 4 {
		t.Errorf("第 2 轮后仍未到确认阈值，核心数 = %d（应为 4）", node.CPUCores)
	}

	// 第 3 轮：达到确认阈值，触发变更
	ing.IngestMetrics(ctx, nodeID, mkMetrics("fp-new", 8, 16))

	node, _ = db.Nodes.GetByID(ctx, nodeID)
	if node.CPUCores != 8 {
		t.Errorf("确认后核心数 = %d，期望 8", node.CPUCores)
	}
	if node.MemTotal != 16*1024*1024*1024 {
		t.Errorf("确认后内存 = %d，期望 16GB", node.MemTotal)
	}
	if node.HardwareFP != "fp-new" {
		t.Errorf("确认后指纹 = %q，期望 fp-new", node.HardwareFP)
	}
	if node.HardwareChangedAt == nil {
		t.Error("确认后应记录 hardware_changed_at")
	}

	// 应产生一条 info 级告警
	evs, total, err := db.Alerts.ListEvents(ctx, store.AlertListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("应产生 1 条告警，实际 = %d", total)
	}
	ev := evs[0]
	if ev.Severity != model.SeverityInfo {
		t.Errorf("告警级别 = %s，应为 info（配置变更是正常业务事件，不该打扰值班）", ev.Severity)
	}
	if ev.TargetType != model.TargetNode {
		t.Errorf("告警目标类型 = %s", ev.TargetType)
	}
	// 告警内容应含前后对比
	if ev.Payload == nil || ev.Payload["before"] == nil || ev.Payload["after"] == nil {
		t.Errorf("告警应含before/after 对比，实际 = %+v", ev.Payload)
	}
}

func Test升配检测_写审计日志(t *testing.T) {
	db, ing, nodeID := setupIngestor(t)
	ctx := context.Background()

	ing.IngestMetrics(ctx, nodeID, mkMetrics("fp-base", 4, 8))
	for i := 0; i < 3; i++ {
		ing.IngestMetrics(ctx, nodeID, mkMetrics("fp-upgraded", 16, 32))
	}

	logs, total, err := db.Audit.List(ctx, nil, "node.hardware_changed", "", "",
		time.Time{}, time.Time{}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("应写 1 条硬件变更审计，实际 = %d", total)
	}
	l := logs[0]
	if l.TargetID != "uid-test" {
		t.Errorf("审计目标 ID = %s，应为 uid-test", l.TargetID)
	}
	if l.Detail["before"] == nil || l.Detail["after"] == nil {
		t.Errorf("审计应含前后对比，实际 = %+v", l.Detail)
	}
}

func Test升配检测_指纹回退则清除待确认(t *testing.T) {
	// 抖动场景：A→B→A，B 只出现一轮就消失，不该被确认
	db, ing, nodeID := setupIngestor(t)
	ctx := context.Background()

	ing.IngestMetrics(ctx, nodeID, mkMetrics("fp-A", 4, 8))
	// B 出现 1 轮
	ing.IngestMetrics(ctx, nodeID, mkMetrics("fp-B", 5, 8))
	// 回到 A
	ing.IngestMetrics(ctx, nodeID, mkMetrics("fp-A", 4, 8))
	// B 又出现 1 轮
	ing.IngestMetrics(ctx, nodeID, mkMetrics("fp-B", 5, 8))
	// 回A
	ing.IngestMetrics(ctx, nodeID, mkMetrics("fp-A", 4, 8))

	_, total, _ := db.Alerts.ListEvents(ctx, store.AlertListFilter{})
	if total != 0 {
		t.Errorf("抖动的 B 不该被确认告警，实际 = %d 个", total)
	}
	node, _ := db.Nodes.GetByID(ctx, nodeID)
	if node.CPUCores != 4 {
		t.Errorf("规格不应变化，核心数 = %d", node.CPUCores)
	}
}

func Test升配检测_并发安全(t *testing.T) {
	// 多个上报并发到达时指纹状态不能错乱（-race 会检测）
	_, ing, nodeID := setupIngestor(t)
	ctx := context.Background()

	ing.IngestMetrics(ctx, nodeID, mkMetrics("fp-base", 4, 8))

	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func(g int) {
			for i := 0; i < 20; i++ {
				fp := "fp-a"
				if (g+i)%2 == 0 {
					fp = "fp-b"
				}
				ing.IngestMetrics(ctx, nodeID, mkMetrics(fp, 4, 8))
			}
			done <- struct{}{}
		}(g)
	}
	for g := 0; g < 8; g++ {
		<-done
	}
	// 不 panic 即通过；-race 会捕获数据竞争
}

func TestIngest_指标落库(t *testing.T) {
	db, ing, nodeID := setupIngestor(t)
	ctx := context.Background()

	ing.IngestMetrics(ctx, nodeID, mkMetrics("fp-1", 4, 8))

	latest, err := db.Metrics.Latest(ctx, []int64{nodeID})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := latest[nodeID]
	if !ok {
		t.Fatal("未查到刚落库的指标")
	}
	if m.CPUUsage != 12.5 {
		t.Errorf("CPU 使用率 = %v，期望 12.5", m.CPUUsage)
	}
	if len(m.Disks) != 1 || m.Disks[0].Mount != "/" {
		t.Errorf("磁盘未正确落库: %+v", m.Disks)
	}
	if len(m.NetIO) != 1 || m.NetIO[0].Iface != "eth0" {
		t.Errorf("网络未正确落库: %+v", m.NetIO)
	}

	// 节点应变为在线
	node, _ := db.Nodes.GetByID(ctx, nodeID)
	if node.Status != model.NodeOnline {
		t.Errorf("上报后节点状态 = %s，应为 online", node.Status)
	}
	if node.LastReportAt == nil {
		t.Error("上报时间未记录")
	}
}

func TestIngest_异常值被裁剪(t *testing.T) {
	// Agent 不可信：上报 -5% 或 105% 的使用率时必须夹到 0-100，
	// 否则图表会画出越界曲线
	db, ing, nodeID := setupIngestor(t)
	ctx := context.Background()

	m := mkMetrics("fp-1", 4, 8)
	m.Cpu.Usage = 105
	m.Mem.Usage = -5
	m.Disks[0].Usage = 200
	ing.IngestMetrics(ctx, nodeID, m)

	latest, _ := db.Metrics.Latest(ctx, []int64{nodeID})
	got := latest[nodeID]
	if got.CPUUsage != 100 {
		t.Errorf("CPU 105 应裁剪为 100，实际 = %v", got.CPUUsage)
	}
	if got.MemUsage != 0 {
		t.Errorf("内存 -5 应裁剪为 0，实际 = %v", got.MemUsage)
	}
	if got.Disks[0].Usage != 100 {
		t.Errorf("磁盘 200 应裁剪为 100，实际 = %v", got.Disks[0].Usage)
	}
}

func TestIngest_时钟异常被纠正(t *testing.T) {
	db, ing, nodeID := setupIngestor(t)
	ctx := context.Background()

	// Agent 时钟超前 1 小时
	m := mkMetrics("fp-1", 4, 8)
	m.CollectedAt = time.Now().Add(time.Hour).Unix()
	ing.IngestMetrics(ctx, nodeID, m)

	latest, _ := db.Metrics.Latest(ctx, []int64{nodeID})
	got := latest[nodeID]
	// 应被服务端时间兜底，而不是写入 1 小时后的时间
	if got.CollectedAt.After(time.Now().Add(2 * time.Minute)) {
		t.Errorf("超前的时间戳未被纠正: %v", got.CollectedAt)
	}
}

func TestClampPercent(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{-1, 0}, {0, 0}, {50.5, 50.5}, {100, 100}, {101, 100}, {1000, 100},
	}
	for _, c := range cases {
		if got := clampPercent(c.in); got != c.want {
			t.Errorf("clampPercent(%v) = %v，期望 %v", c.in, got, c.want)
		}
	}
}

func TestHardwareSnapshot(t *testing.T) {
	// 描述应包含关键规格，用于告警消息
	h := hardwareSnapshot{Cores: 8, MemGB: 16, Disks: 2, Model: "AMD EPYC 7543"}
	d := h.Describe()
	if !contains(d, "8核") || !contains(d, "16GB") || !contains(d, "2块盘") {
		t.Errorf("描述 = %q，应含核心数/内存/磁盘数", d)
	}
	// 超长模型名应被截断
	long := hardwareSnapshot{Cores: 4, MemGB: 8, Model: "very-long-cpu-model-name-that-exceeds-limit"}
	if len(long.Describe()) > 80 {
		t.Errorf("超长描述未被截断: %s", long.Describe())
	}
	// 相同规格应相等
	a := hardwareSnapshot{Cores: 4, MemGB: 8, Disks: 1, Model: "X"}
	b := hardwareSnapshot{Cores: 4, MemGB: 8, Disks: 1, Model: "X"}
	if !a.Equal(b) {
		t.Error("相同规格应相等")
	}
	// 不同规格不应相等
	c := hardwareSnapshot{Cores: 8, MemGB: 8, Disks: 1, Model: "X"}
	if a.Equal(c) {
		t.Error("不同规格不应相等")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
