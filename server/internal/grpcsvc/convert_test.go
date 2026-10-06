package grpcsvc

import (
	"context"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	agentv1 "github.com/m202471895/probeone/api/agent/v1"
	"github.com/m202471895/probeone/server/internal/config"
)

// quietLogger 造一个只输出 Error 的 logger。
// 单元测试里那些"预期失败"的路径会打Warn/Info，全量输出会把
// 真正的失败信息淹掉——测试日志的可见性本身就是一种资源。
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// metadataOf 造一个带 incoming metadata 的 context。
// 用 NewIncomingContext 而非 AppendToOutgoingContext：
// credentialsFromContext 读的是 FromIncomingContext，
// 两者方向相反，混用会得到"没有 metadata"的假象。
func metadataOf(kv map[string]string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.New(kv))
}

// ---------- convert.go ----------

func TestDisksFromHostProto_过滤无效磁盘并保留容量(t *testing.T) {
	// 这个过滤是刻意的：mount 为空或 total 为 0 的条目进了库，
	// 会让"磁盘数量"这个规格字段虚高——而磁盘数参与硬件指纹计算，
	// 一个坏条目就能让指纹每轮都变，进而触发假的"规格已变更"告警。
	cases := []struct {
		name  string
		disks []*agentv1.DiskInfo
		want  int
	}{
		{"全部有效", []*agentv1.DiskInfo{
			{Device: "/dev/sda1", Mount: "/", Fstype: "ext4", Total: 100 << 30},
			{Device: "/dev/sdb1", Mount: "/data", Fstype: "xfs", Total: 500 << 30},
		}, 2},
		{"无 mount 的被丢弃", []*agentv1.DiskInfo{
			{Device: "/dev/sda1", Fstype: "ext4", Total: 100 << 30},
		}, 0},
		{"total 为 0 的被丢弃", []*agentv1.DiskInfo{
			{Mount: "/", Fstype: "ext4", Total: 0},
		}, 0},
		{"空列表", []*agentv1.DiskInfo{}, 0},
		{"nil 列表", nil, 0},
		{"有效与无效混合", []*agentv1.DiskInfo{
			{Device: "/dev/sda1", Mount: "/", Fstype: "ext4", Total: 100 << 30},
			{Device: "/dev/loop0", Fstype: "squashfs", Total: 50 << 30},
			{Mount: "/boot", Total: 0},
		}, 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := disksFromHostProto(c.disks)
			if len(got) != c.want {
				t.Errorf("磁盘数 = %d，期望 %d，详情 = %+v", len(got), c.want, got)
			}
		})
	}
}

func TestDisksFromHostProto_字段完整搬运(t *testing.T) {
	// device 必须搬运：它是"这块盘是哪块盘"的唯一标识，
	// 丢了就没法在面板上把磁盘与设备对应起来
	in := []*agentv1.DiskInfo{
		{Device: "/dev/nvme0n1p2", Mount: "/", Fstype: "ext4", Total: 100 << 30},
	}
	got := disksFromHostProto(in)
	if len(got) != 1 {
		t.Fatalf("磁盘数 = %d，期望 1", len(got))
	}
	d := got[0]
	if d.Device != "/dev/nvme0n1p2" || d.Mount != "/" || d.FSType != "ext4" || d.Total != 100<<30 {
		t.Errorf("字段搬运不完整: %+v", d)
	}
}

func TestDisksFromProto_与主机信息版本行为一致(t *testing.T) {
	// 两个函数逻辑相同但类型不同。写这个测试是为了让"它们必须保持一致"
	// 成为显式约束：哪天只改了一个（比如给其中一个加了 label 字段），
	// 这里会失败并指出该同步。
	in := []*agentv1.DiskStat{
		{Mount: "/", Device: "/dev/sda1", Fstype: "ext4", Total: 100 << 30},
		{Mount: "", Total: 100 << 30},
		{Mount: "/x", Total: 0},
	}
	a := disksFromProto(in)
	b := disksFromHostProto([]*agentv1.DiskInfo{
		{Mount: "/", Device: "/dev/sda1", Fstype: "ext4", Total: 100 << 30},
		{Mount: "", Total: 100 << 30},
		{Mount: "/x", Total: 0},
	})
	if len(a) != len(b) {
		t.Fatalf("两个转换函数过滤结果不一致: %d vs %d", len(a), len(b))
	}
	if len(a) != 1 {
		t.Fatalf("磁盘数 = %d，期望 1", len(a))
	}
}

func TestToModelMetric_数值搬运与裁剪(t *testing.T) {
	// proto 用 float32、模型用 float64，转换点必须显式。
	// 这里同时验证裁剪：Agent 不可信，-5% / 105% 若直接入库，
	// 图表会画出越界曲线。
	at := time.Unix(1700000000, 0).UTC()
	m := &agentv1.ReportMetrics{
		CollectedAt:  1700000000,
		Uptime:       86400,
		TcpConnCount: 42,
		Seq:          1,
		Cpu: &agentv1.CpuStat{
			Usage:   105, //越界，应裁到 100
			PerCore: []float32{10, 20.5, 30},
		},
		Mem: &agentv1.MemStat{
			Total: 8 << 30, Used: 4 << 30, Available: 4 << 30,
			Usage:     -5, // 负值，应裁到 0
			SwapTotal: 2 << 30, SwapUsed: 1 << 30,
		},
		Load: &agentv1.LoadStat{Load1: 1.5, Load5: 1.2, Load15: 0.9},
		Disks: []*agentv1.DiskStat{
			{Mount: "/", Fstype: "ext4", Total: 100 << 30, Used: 50 << 30,
				Usage: 200, ReadBps: 111, WriteBps: 222},
		},
		Nets: []*agentv1.NetStat{
			{Iface: "eth0", RxBps: 1000, TxBps: 2000, RxTotal: 10, TxTotal: 20},
		},
		Sensors: []*agentv1.SensorStat{
			{Name: "cpu_temp", Kind: "temperature", Value: 55.5, Unit: "C"},
		},
	}

	got := toModelMetric(7, at, m)

	if got.NodeID != 7 {
		t.Errorf("NodeID = %d，期望 7", got.NodeID)
	}
	if !got.CollectedAt.Equal(at) {
		t.Errorf("CollectedAt = %v，期望 %v（时间由调用方决定，转换只搬运）", got.CollectedAt, at)
	}
	if got.Uptime != 86400 || got.TCPConnCount != 42 {
		t.Errorf("uptime/tcp = %d/%d，期望 86400/42", got.Uptime, got.TCPConnCount)
	}
	if got.CPUUsage != 100 {
		t.Errorf("CPU 105 应裁为 100，实际 = %v", got.CPUUsage)
	}
	// per-core 是数组，len==0 时必须保持 nil 而不是空切片：
	// 空切片会被序列化成 "[]" 存进 JSON 列，与NULL 语义不同
	if len(got.CPUCores) != 3 || got.CPUCores[1] != 20.5 {
		t.Errorf("per-core = %v，期望 [10 20.5 30]", got.CPUCores)
	}
	if got.MemUsage != 0 {
		t.Errorf("内存 -5 应裁为 0，实际 = %v", got.MemUsage)
	}
	if got.MemTotal != 8<<30 || got.SwapTotal != 2<<30 || got.SwapUsed != 1<<30 {
		t.Errorf("内存字段搬运错误: %+v", got)
	}
	// 负载必须用容差比较，不能用 !=：proto 里是 float32，
	// 1.2 在 float32 里并不精确等于 1.2，转成 float64 后是 1.2000000476837158。
	// float32 → float64 转换本身是无损的（float32 总是 float64 的子集），
	// 精度损失发生在 Agent 写入 float32 的那一刻，不是服务端的锅。
	if d := got.Load1 - 1.5; d > 1e-6 || d < -1e-6 {
		t.Errorf("load1 = %v，与 1.5 偏差过大", got.Load1)
	}
	if d := got.Load5 - 1.2; d > 1e-6 || d < -1e-6 {
		t.Errorf("load5 = %v，与 1.2 偏差过大", got.Load5)
	}
	if d := got.Load15 - 0.9; d > 1e-6 || d < -1e-6 {
		t.Errorf("load15 = %v，与 0.9 偏差过大", got.Load15)
	}
	if len(got.Disks) != 1 {
		t.Fatalf("磁盘数 = %d，期望 1", len(got.Disks))
	}
	d := got.Disks[0]
	if d.Usage != 100 || d.Mount != "/" || d.ReadBps != 111 || d.WriteBps != 222 {
		t.Errorf("磁盘字段错误: %+v", d)
	}
	if len(got.NetIO) != 1 || got.NetIO[0].Iface != "eth0" || got.NetIO[0].RxBps != 1000 {
		t.Errorf("网络字段错误: %+v", got.NetIO)
	}
	if len(got.Sensors) != 1 || got.Sensors[0].Name != "cpu_temp" || got.Sensors[0].Value != 55.5 {
		t.Errorf("传感器字段错误: %+v", got.Sensors)
	}
}

func TestToModelMetric_子消息缺失不panic(t *testing.T) {
	// Agent 是不可信输入：可以只发一个空的 ReportMetrics。
	// 任何 nil 子消息都走 GetXxx() 的零值分支，不能 panic——
	// panic 会让整个 gRPC 服务崩掉，等于一次空上报就能DoS。
	cases := []struct {
		name string
		m    *agentv1.ReportMetrics
	}{
		{"完全空消息", &agentv1.ReportMetrics{}},
		{"只有 cpu", &agentv1.ReportMetrics{Cpu: &agentv1.CpuStat{}}},
		{"只有 mem", &agentv1.ReportMetrics{Mem: &agentv1.MemStat{}}},
		{"空切片", &agentv1.ReportMetrics{
			Disks: []*agentv1.DiskStat{}, Nets: []*agentv1.NetStat{}, Sensors: []*agentv1.SensorStat{},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := toModelMetric(1, time.Now(), c.m)
			if got.CPUCores != nil {
				t.Errorf("无 per-core 时CPUCores 应为 nil（NULL 语义），实际 = %v", got.CPUCores)
			}
		})
	}
}

// ---------- server.go 辅助函数 ----------

func TestMaskUUID_短UUID完全隐藏(t *testing.T) {
	//日志脱敏：UUID 是凭据定位符，日志里保留过多等于帮攻击者缩小范围。
	//短于等于 8 位时无法安全截断（截断后剩余熵太小），必须全掩。
	cases := []struct {
		in   string
		want string
	}{
		{"", "***"},
		{"short", "***"},
		{"12345678", "***"},          // 正好 8 位
		{"123456789", "12345678***"}, // 9 位：留前 8
		{"550e8400-e29b-41d4-a716-446655440000", "550e8400***"},
	}
	for _, c := range cases {
		if got := maskUUID(c.in); got != c.want {
			t.Errorf("maskUUID(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestIPFromAddr_提取纯IP不带端口(t *testing.T) {
	// IP 会进审计日志与失败计数表。带端口会导致
	// (uuid, ip) 计数维度错位：同一客户端换个端口就绕过限流。
	cases := []struct {
		name string
		addr net.Addr
		want string
	}{
		{"TCP IPv4", &net.TCPAddr{IP: net.ParseIP("1.2.3.4"), Port: 5555}, "1.2.3.4"},
		{"TCP IPv6", &net.TCPAddr{IP: net.ParseIP("::1"), Port: 5555}, "::1"},
		// 非 TCPAddr 走 SplitHostPort 兜底：bufconn 就是这种
		{"非 TCP 地址", fakeAddr{s: "10.0.0.5:1234"}, "10.0.0.5"},
		{"非 TCP 无端口", fakeAddr{s: "10.0.0.5"}, "10.0.0.5"},
		// 无法解析时原样返回：宁可返回原始串，
		// 也不要返回空串——空 IP 会让所有客户端的失败计数挤在一起
		{"不可解析", fakeAddr{s: "weird"}, "weird"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ipFromAddr(c.addr); got != c.want {
				t.Errorf("ipFromAddr = %q，期望 %q", got, c.want)
			}
		})
	}
}

type fakeAddr struct{ s string }

func (f fakeAddr) Network() string { return "fake" }
func (f fakeAddr) String() string  { return f.s }

func TestDefaultEnabledMetrics_不含任何可执行语义(t *testing.T) {
	// PRD 1.1 红线：下发给 Agent 的东西只能是采集配置。
	// 这份清单是 ConfigSync 的唯一数据来源，
	// 一旦有人往里加"重启""升级"之类的东西，就等于开了后门。
	got := (&Service{}).defaultEnabledMetrics()
	want := map[string]bool{
		"cpu": true, "mem": true, "disk": true,
		"net": true, "load": true, "uptime": true,
	}
	if len(got) != len(want) {
		t.Fatalf("指标清单 = %v，期望 %d 项", got, len(want))
	}
	for _, m := range got {
		if !want[m] {
			t.Errorf("指标清单含非采集项 %q——下发给 Agent 的内容必须全是采集配置", m)
		}
	}
}

func TestTimezone_固定为上海(t *testing.T) {
	// Agent 用它算本地时间的采集点，错了会让曲线整体偏移。
	if timezone() != "Asia/Shanghai" {
		t.Errorf("时区 = %q，期望 Asia/Shanghai", timezone())
	}
}

func TestCredentialsFromContext_逐项校验(t *testing.T) {
	// 已由 TestHandshake_缺少凭据被拒 端到端覆盖，
	// 这里只做纯函数的边界补充：多值 metadata 只取第一个。
	cases := []struct {
		name    string
		ctx     context.Context
		wantErr bool
	}{
		{"无 metadata", context.Background(), true},
		{"空 metadata", metadataOf(map[string]string{}), true},
		{"两个键都在", metadataOf(map[string]string{mdClientUUID: "u", mdClientSecret: "s"}), false},
		{"缺 secret", metadataOf(map[string]string{mdClientUUID: "u"}), true},
		{"缺 uuid", metadataOf(map[string]string{mdClientSecret: "s"}), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, s, err := credentialsFromContext(c.ctx)
			if c.wantErr {
				if err == nil {
					t.Errorf("应报错，却返回 uuid=%q secret=%q", u, s)
				}
				return
			}
			if err != nil {
				t.Errorf("不应报错: %v", err)
			}
			if u != "u" || s != "s" {
				t.Errorf("提取结果 = %q/%q，期望 u/s", u, s)
			}
		})
	}
}

func TestAuthorizeSession_空会话直接拒(t *testing.T) {
	// 空 session_id 走的是最前面那个分支，不查库——
	// 无效输入不该消耗一次数据库往返
	db, _, _ := setupIngestor(t)
	svc := New(&config.Config{}, db, quietLogger())

	_, err := svc.authorizeSession(context.Background(), "")
	if err == nil {
		t.Fatal("空 session_id 应被拒")
	}
	if !strings.Contains(err.Error(), "会话无效") {
		t.Errorf("错误信息 = %q，期望提到会话无效", err.Error())
	}
}
