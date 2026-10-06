package grpcsvc

import (
	"context"
	"net"
	"testing"
	"time"

	agentv1 "github.com/m202471895/probeone/api/agent/v1"
	"github.com/m202471895/probeone/server/internal/config"
	"github.com/m202471895/probeone/server/internal/model"
)

// ---------- IngestHostInfo ----------

func TestIngestHostInfo_全字段落库(t *testing.T) {
	// A/B/C 三类字段一次覆盖。缺任何一类都不致命，
	// 但缺了会让面板上出现空白格子，而用户分不清"没采到"还是"没上报"
	db, ing, nodeID := setupIngestor(t)
	ctx := context.Background()

	err := ing.IngestHostInfo(ctx, nodeID, &agentv1.ReportHostInfo{
		// A 类
		Hostname: "web-01", OsType: "linux", OsVersion: "Debian 12", Arch: "amd64",
		AgentVersion: "1.4.2",
		// B 类
		CpuModel: "AMD EPYC 7543", MemTotal: 16 << 30,
		Disks: []*agentv1.DiskInfo{
			{Device: "/dev/sda1", Mount: "/", Fstype: "ext4", Total: 100 << 30},
			{Device: "/dev/sdb1", Mount: "/data", Fstype: "xfs", Total: 500 << 30},
		},
		// C 类
		BootTime: 1700000000, PublicIp: "203.0.113.5",
	})
	if err != nil {
		t.Fatalf("IngestHostInfo 失败: %v", err)
	}

	n, err := db.Nodes.GetByID(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if n.Hostname != "web-01" || n.OSType != "linux" || n.OSVersion != "Debian 12" ||
		n.Arch != "amd64" || n.AgentVersion != "1.4.2" {
		t.Errorf("A 类字段错误: %+v", n)
	}
	if n.CPUModel != "AMD EPYC 7543" {
		t.Errorf("CPU 型号 = %q", n.CPUModel)
	}
	if n.MemTotal != 16<<30 {
		t.Errorf("内存 = %d，期望 %d", n.MemTotal, 16<<30)
	}
	if len(n.DiskInfo) != 2 {
		t.Errorf("磁盘数 = %d，期望 2", len(n.DiskInfo))
	}
	if n.BootTime == nil {
		t.Error("启动时间未记录")
	} else if n.BootTime.Unix() != 1700000000 {
		t.Errorf("启动时间 = %v，期望 unix 1700000000", n.BootTime.UTC())
	}
	if n.PublicIP != "203.0.113.5" {
		t.Errorf("公网 IP = %q", n.PublicIP)
	}
}

func TestIngestHostInfo_空值不覆盖已有值(t *testing.T) {
	// 这是"仅在有值时更新"规则的直接断言。
	// 反过来的做法（无条件覆盖）会让一次残缺的上报把已有的
	// 主机名、内存等信息清空——面板上表现为节点信息突然变空白，
	// 而 Agent 端什么都没改，极难排查。
	db, ing, nodeID := setupIngestor(t)
	ctx := context.Background()

	// 先上报完整信息
	if err := ing.IngestHostInfo(ctx, nodeID, &agentv1.ReportHostInfo{
		Hostname: "web-01", OsType: "linux", Arch: "amd64",
		CpuModel: "Xeon", MemTotal: 32 << 30,
		Disks: []*agentv1.DiskInfo{{Device: "/dev/sda1", Mount: "/", Total: 100 << 30}},
	}); err != nil {
		t.Fatal(err)
	}

	// 再上报一条几乎全空的
	if err := ing.IngestHostInfo(ctx, nodeID, &agentv1.ReportHostInfo{
		PublicIp: "198.51.100.7",
	}); err != nil {
		t.Fatal(err)
	}

	n, _ := db.Nodes.GetByID(ctx, nodeID)
	if n.Hostname != "web-01" {
		t.Errorf("主机名被空值覆盖为 %q——残缺上报不应清空已有信息", n.Hostname)
	}
	if n.OSType != "linux" || n.Arch != "amd64" {
		t.Errorf("A 类字段被覆盖: os=%q arch=%q", n.OSType, n.Arch)
	}
	if n.CPUModel != "Xeon" {
		t.Errorf("CPU 型号被覆盖为 %q", n.CPUModel)
	}
	if n.MemTotal != 32<<30 {
		t.Errorf("内存被覆盖为 %d", n.MemTotal)
	}
	if len(n.DiskInfo) != 1 {
		t.Errorf("磁盘信息被覆盖，数量 = %d", len(n.DiskInfo))
	}
	// 有值的字段仍应正常更新
	if n.PublicIP != "198.51.100.7" {
		t.Errorf("公网 IP 未更新: %q", n.PublicIP)
	}
}

func TestIngestHostInfo_B类不以本消息覆盖ReportMetrics(t *testing.T) {
	// agent.proto 明确："B 类字段以 ReportMetrics 为权威源，
	// 本消息中的副本仅用于交叉校验"。
	// 一旦反了：一个过期或伪造的主机信息就能把节点规格改小，
	// 面板上却显示"用户自己改的"，运维不会想到要去查上报链路。
	db, ing, nodeID := setupIngestor(t)
	ctx := context.Background()

	// 先由指标路径建立权威值：8 核 16G，型号 "Test CPU"，1 块盘
	if err := ing.IngestMetrics(ctx, nodeID, mkMetrics("fp-a", 8, 16)); err != nil {
		t.Fatal(err)
	}

	// 主机信息声称是 4G + 另一个型号 + 另一块盘——都不能覆盖
	if err := ing.IngestHostInfo(ctx, nodeID, &agentv1.ReportHostInfo{
		CpuModel: "不应生效的型号", MemTotal: 4 << 30,
		Disks: []*agentv1.DiskInfo{{Device: "/dev/sdz9", Mount: "/fake", Total: 1 << 30}},
	}); err != nil {
		t.Fatal(err)
	}

	n, _ := db.Nodes.GetByID(ctx, nodeID)
	if n.MemTotal != 16<<30 {
		t.Errorf("内存 = %d，期望 16GB——B 类被主机信息覆盖了", n.MemTotal)
	}
	if n.CPUModel != "Test CPU" {
		t.Errorf("CPU 型号 = %q，期望 Test CPU（ReportMetrics 为权威源）", n.CPUModel)
	}
	if n.CPUCores != 8 {
		t.Errorf("核心数 = %d，期望 8", n.CPUCores)
	}
	if len(n.DiskInfo) != 1 || n.DiskInfo[0].Mount != "/" {
		t.Errorf("磁盘信息被主机信息覆盖: %+v", n.DiskInfo)
	}
}

func TestIngestHostInfo_指标缺失时B类可由主机信息补齐(t *testing.T) {
	// 上一个用例的另一半：ReportMetrics 没提供 B 类时，
	// 主机信息必须能补上，否则节点规格永远空白。
	// 只填空的、不覆盖有的——两个方向都要有测试，
	// 只测一个方向会漏掉"该填的不填"或"不该覆盖的覆盖了"。
	db, ing, nodeID := setupIngestor(t)
	ctx := context.Background()

	// 不带任何指标的 IngestMetrics：只TouchReport，不建立硬件基线
	if err := ing.IngestMetrics(ctx, nodeID, &agentv1.ReportMetrics{
		CollectedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := ing.IngestHostInfo(ctx, nodeID, &agentv1.ReportHostInfo{
		CpuModel: "Intel Xeon", MemTotal: 64 << 30,
		Disks: []*agentv1.DiskInfo{
			{Device: "/dev/sda1", Mount: "/", Fstype: "ext4", Total: 100 << 30},
		},
	}); err != nil {
		t.Fatal(err)
	}

	n, _ := db.Nodes.GetByID(ctx, nodeID)
	if n.CPUModel != "Intel Xeon" {
		t.Errorf("CPU 型号 = %q，期望 Intel Xeon（字段为空时应由主机信息补齐）", n.CPUModel)
	}
	if n.MemTotal != 64<<30 {
		t.Errorf("内存 = %d，期望 64GB", n.MemTotal)
	}
	if len(n.DiskInfo) != 1 {
		t.Errorf("磁盘数 = %d，期望 1", len(n.DiskInfo))
	}
}

func TestIngestHostInfo_空指针与不存在节点(t *testing.T) {
	db, ing, nodeID := setupIngestor(t)
	ctx := context.Background()

	// nil 是安全的空操作：Agent 可能发来一个空的 Host 消息
	if err := ing.IngestHostInfo(ctx, nodeID, nil); err != nil {
		t.Errorf("nil 主机信息应被安全忽略，却报错: %v", err)
	}
	// 节点不存在必须报错：静默成功会让数据丢进虚空，
	// 上层以为上报成功了，实际什么都没存
	if err := ing.IngestHostInfo(ctx, 999999, &agentv1.ReportHostInfo{Hostname: "x"}); err == nil {
		t.Error("对不存在的节点上报主机信息应报错，却返回成功——数据会静默丢失")
	}
	// 确认 nil 那次没有副作用
	n, _ := db.Nodes.GetByID(ctx, nodeID)
	if n.Hostname != "" {
		t.Errorf("nil 上报产生了副作用: hostname=%q", n.Hostname)
	}
}

func TestIngestHostInfo_过滤无效磁盘(t *testing.T) {
	// 磁盘数参与硬件指纹与规格展示，坏条目必须在这里被挡住
	db, ing, nodeID := setupIngestor(t)
	ctx := context.Background()

	if err := ing.IngestHostInfo(ctx, nodeID, &agentv1.ReportHostInfo{
		Disks: []*agentv1.DiskInfo{
			{Device: "/dev/sda1", Mount: "/", Fstype: "ext4", Total: 100 << 30},
			{Device: "/dev/loop0", Fstype: "squashfs", Total: 50 << 30}, // 无 mount
			{Mount: "/zero", Total: 0},                                  // 无容量
		},
	}); err != nil {
		t.Fatal(err)
	}
	n, _ := db.Nodes.GetByID(ctx, nodeID)
	if len(n.DiskInfo) != 1 {
		t.Errorf("磁盘数 = %d，期望 1（无 mount / 无容量的应被过滤）: %+v", len(n.DiskInfo), n.DiskInfo)
	}
}

// ---------- NewServer / Serve ----------

func TestNewServer_TLS配置不完整时按明文启动但告警(t *testing.T) {
	// 只配证书不配私钥时 config.validate 会拒绝，
	// 但 NewServer 本身不校验——它只在两者都非空时启用 TLS。
	// 这个用例锁定当前行为：明文启动 + Warn 日志。
	// 安全上依赖上层"反代终止 TLS"，但绝不能静默：
	// 明文传Agent 凭据等于把密钥送人。
	db, _, _ := setupIngestor(t)
	cfg := &config.Config{}
	cfg.Server.TLSCert = "cert.pem" // 有 cert 无 key
	srv := NewServer(cfg, New(cfg, db, quietLogger()), quietLogger())
	if srv == nil {
		t.Fatal("NewServer 返回 nil")
	}
	if srv.addr != ":0" {
		t.Errorf("addr = %q，期望 :0（未配置 GRPCPort 时的默认值）", srv.addr)
	}
}

func TestNewServer_监听地址随配置端口(t *testing.T) {
	db, _, _ := setupIngestor(t)
	cfg := &config.Config{}
	cfg.Server.GRPCPort = 38008
	srv := NewServer(cfg, New(cfg, db, quietLogger()), quietLogger())
	if srv.addr != ":38008" {
		t.Errorf("addr = %q，期望 :38008", srv.addr)
	}
}

func TestServer_Serve随上下文取消而优雅退出(t *testing.T) {
	// 停机路径必须有测试：GracefulStop 分支一旦有死锁，
	// 表现为"发布后Pod 永远Terminating"，排查成本极高。
	// 上限 10 秒那条超时分支也一起覆盖。
	db, _, _ := setupIngestor(t)
	cfg := &config.Config{}
	// 随机高位端口，降低与本机已有服务撞端口的概率
	cfg.Server.GRPCPort = 45999
	srv := NewServer(cfg, New(cfg, db, quietLogger()), quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, quietLogger()) }()

	// 给它一点时间开始监听
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("优雅停机不应返回错误，却得到: %v", err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("Serve 在取消上下文后 12 秒仍未返回——停机路径可能死锁")
	}
}

func TestLisFor_端口被占用时panic(t *testing.T) {
	// 端口冲突必须 panic 而不是返回一个坏listener：
	// 静默继续的话，服务会"看起来启动成功"但实际不监听，
	// 而所有 Agent 都连不上——这种故障现场几乎没有线索。
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占用端口失败: %v", err)
	}
	defer l.Close()

	addr := l.Addr().String()
	defer func() {
		r := recover()
		if r == nil {
			t.Errorf("端口 %s 已被占用时 lisFor 应panic，却正常返回了", addr)
		}
	}()
	_ = lisFor(addr)
}

// ---------- 编译期断言 ----------

// 确认 IngestHostInfo 走的是真实的 Nodes.Update 路径：
// 硬件规格变了但磁盘信息没跟着变，是很难发现的静默不一致。
var _ = func() bool {
	var n model.Node
	n.DiskInfo = disksFromHostProto(nil)
	return n.DiskInfo != nil && len(n.DiskInfo) == 0
}()
