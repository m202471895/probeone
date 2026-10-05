package collect

import (
	"context"
	"math"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 采集器必须能在真实系统上跑出合理数值——
// mock 出来的指标恒等于 0，永远发现不了"全是 0"这类问题。

func TestCollect_真实数据合理(t *testing.T) {
	c := New()
	c.Prime()
	// 差值法需要至少一个周期的间隔
	time.Sleep(150 * time.Millisecond)

	m, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}

	// CPU：必须存在且在 0-100
	if m.CPU == nil {
		t.Fatal("CPU 统计为空")
	}
	if m.CPU.Usage < 0 || m.CPU.Usage > 100 {
		t.Errorf("CPU 使用率 = %.2f，超出 0-100 范围", m.CPU.Usage)
	}
	// 逐核数量应等于逻辑核数
	if len(m.CPU.PerCore) > 0 && len(m.CPU.PerCore) != runtime.NumCPU() {
		t.Errorf("逐核数 = %d，期望 = %d", len(m.CPU.PerCore), runtime.NumCPU())
	}

	// 内存：总量必须 > 0
	if m.Mem == nil {
		t.Fatal("内存统计为空")
	}
	if m.Mem.Total == 0 {
		t.Error("内存总量为 0")
	}
	if m.Mem.Usage < 0 || m.Mem.Usage > 100 {
		t.Errorf("内存使用率 = %.2f，超出范围", m.Mem.Usage)
	}
	// used 不应超过 total
	if m.Mem.Used > m.Mem.Total {
		t.Errorf("已用内存 %d > 总量 %d", m.Mem.Used, m.Mem.Total)
	}

	// 磁盘：根分区必须能采到
	if len(m.Disks) == 0 {
		t.Error("未采集到任何磁盘分区")
	}
	foundRoot := false
	for _, d := range m.Disks {
		if d.Mount == "/" {
			foundRoot = true
			if d.Total == 0 {
				t.Error("根分区容量为 0")
			}
			if d.Usage < 0 || d.Usage > 100 {
				t.Errorf("分区 %s 使用率 = %.2f，超出范围", d.Mount, d.Usage)
			}
		}
	}
	if !foundRoot {
		t.Error("未采集到根分区")
	}

	// 运行时长
	if m.Uptime == 0 {
		t.Error("运行时长为 0")
	}
}

func TestCollect_虚拟文件系统被过滤(t *testing.T) {
	c := New()
	c.Prime()
	m, _ := c.Collect(context.Background())

	for _, d := range m.Disks {
		switch d.Mount {
		case "/proc", "/sys", "/dev", "/run":
			t.Errorf("虚拟文件系统 %s 不应被采集", d.Mount)
		}
		if strings.HasPrefix(d.Mount, "/dev/loop") {
			t.Errorf("loop 设备 %s 不应被采集", d.Mount)
		}
		if strings.HasPrefix(d.Mount, "/proc/") || strings.HasPrefix(d.Mount, "/sys/") {
			t.Errorf("伪挂载 %s 不应被采集", d.Mount)
		}
	}
}

func TestIsRealDisk(t *testing.T) {
	cases := []struct {
		mount string
		want  bool
	}{
		{"/", true},
		{"/home", true},
		{"/var", true},
		{"/data", true},
		{"", false},
		{"/proc", false},
		{"/sys", false},
		{"/dev", false},
		{"/run", false},
		{"/snap/core/123", false},
		{"/var/lib/docker/overlay2/x", false},
		{"/var/lib/kubelet/pods/y", false},
		{"/dev/loop0", false},
		{"/dev/loop12", false},
		{"/System/Volumes/VM", false},
		{"/proc/sys/net", false},
	}
	for _, c := range cases {
		if got := isRealDisk(c.mount); got != c.want {
			t.Errorf("isRealDisk(%q) = %v，期望 %v", c.mount, got, c.want)
		}
	}
}

func TestHostInfo_静态信息只采一次(t *testing.T) {
	c := New()
	ctx := context.Background()

	hi1 := c.HostInfo(ctx, "v1.0.0")
	if hi1 == nil {
		t.Fatal("HostInfo 为 nil")
	}
	if hi1.Hostname == "" {
		t.Error("主机名为空")
	}
	if hi1.CPUCoresLog <= 0 {
		t.Error("逻辑核数应 > 0")
	}
	if hi1.MemTotal == 0 {
		t.Error("内存总量为 0")
	}
	if hi1.HardwareFP == "" || len(hi1.HardwareFP) != 16 {
		t.Errorf("硬件指纹 = %q，应为 16 位十六进制", hi1.HardwareFP)
	}
	if hi1.AgentStartedAt == 0 {
		t.Error("Agent 启动时间未记录")
	}

	// 第二次应返回缓存（同一指针）
	hi2 := c.HostInfo(ctx, "v1.0.0")
	if hi1 != hi2 {
		t.Error("静态信息应被缓存，不应重复采集")
	}
}

func TestComputeFingerprint_稳定性(t *testing.T) {
	base := FingerprintInput{
		CoresLogical:  4,
		CoresPhysical: 2,
		MemTotal:      8589934592,
		Disks: []DiskInfo{
			{Device: "/dev/sda1", Mount: "/", FSType: "ext4", Total: 85899345920},
			{Device: "/dev/sda2", Mount: "/data", FSType: "xfs", Total: 214748364800},
		},
		CPUModel: "Intel(R) Xeon(R) CPU E5-2680 v4 @ 2.40GHz",
	}

	fp1 := ComputeFingerprint(base)
	if len(fp1) != 16 {
		t.Errorf("指纹长度 = %d，应为 16", len(fp1))
	}
	// 同样输入 → 同样输出
	if fp2 := ComputeFingerprint(base); fp1 != fp2 {
		t.Error("相同输入应产生相同指纹")
	}

	// 磁盘顺序打乱 → 指纹不变（防"挂载顺序变化造成假变更"）
	shuffled := base
	shuffled.Disks = []DiskInfo{base.Disks[1], base.Disks[0]}
	if ComputeFingerprint(shuffled) != fp1 {
		t.Error("磁盘顺序变化不应改变指纹（会造成假变更告警）")
	}

	// CPU 型号大小写/空格差异 → 指纹不变
	varied := base
	varied.CPUModel = "  intel(r)  xeon(r) cpu   e5-2680 v4 @ 2.40ghz  "
	if ComputeFingerprint(varied) != fp1 {
		t.Error("CPU 型号的空格/大小写差异不应改变指纹")
	}

	// 内存字节级抖动 → 指纹不变（各平台 reserved 区差异）
	noisy := base
	noisy.MemTotal = 8589934592 + 4096 // 差 4KB
	if ComputeFingerprint(noisy) != fp1 {
		t.Error("内存的字节级抖动不应改变指纹（应取整到 MB）")
	}
	// 但 MB 级变化必须改变
	jumped := base
	jumped.MemTotal = 8589934592 * 2
	if ComputeFingerprint(jumped) == fp1 {
		t.Error("内存翻倍必须改变指纹")
	}
}

func TestComputeFingerprint_升配可识别(t *testing.T) {
	base := FingerprintInput{
		CoresLogical: 4,
		MemTotal:     8589934592,
		Disks:        []DiskInfo{{Mount: "/", FSType: "ext4", Total: 85899345920}},
		CPUModel:     "EPYC 7543",
	}
	fp1 := ComputeFingerprint(base)

	// 场景1：vCPU 4→8（典型云主机升配）
	upCPU := base
	upCPU.CoresLogical = 8
	if ComputeFingerprint(upCPU) == fp1 {
		t.Error("核心数变化必须被识别")
	}

	// 场景2：内存 8GB→16GB
	upMem := base
	upMem.MemTotal = 8589934592 * 2
	if ComputeFingerprint(upMem) == fp1 {
		t.Error("内存变化必须被识别")
	}

	// 场景3：加挂数据盘
	addDisk := base
	addDisk.Disks = append(append([]DiskInfo{}, base.Disks...),
		DiskInfo{Mount: "/data2", FSType: "xfs", Total: 107374182400})
	if ComputeFingerprint(addDisk) == fp1 {
		t.Error("新增磁盘必须被识别")
	}
}

func TestRound2(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{0, 0},
		{45.123, 45.12},
		{45.125, 45.13},
		{99.999, 100},
		{-1.5, -1.5}, // 负数不该被算成 0
	}
	for _, c := range cases {
		if got := round2(c.in); math.Abs(got-c.want) > 0.001 {
			t.Errorf("round2(%v) = %v，期望 %v", c.in, got, c.want)
		}
	}
}

func TestItoa(t *testing.T) {
	cases := map[int]string{
		0: "0", 1: "1", 10: "10", -1: "-1", -42: "-42",
		8589934592: "8589934592",
	}
	for in, want := range cases {
		if got := itoa(in); got != want {
			t.Errorf("itoa(%d) = %s，期望 %s", in, got, want)
		}
	}
}

func TestCollectSensors_不panic(t *testing.T) {
	// 非 Linux 上应返回空而不是 panic
	s := collectSensors()
	for _, sensor := range s {
		if sensor.Value <= 0 {
			t.Errorf("传感器 %s 报了非正数值 %v", sensor.Name, sensor.Value)
		}
		if sensor.Kind != "temperature" && sensor.Kind != "fan" {
			t.Errorf("未知的传感器类型: %s", sensor.Kind)
		}
	}
}

func TestDiskRates_首次为零(t *testing.T) {
	// 首次采集没有"上一次"，速率必须为 0 而不是巨大数字
	c := New()
	c.Prime()
	m, _ := c.Collect(context.Background())
	for _, d := range m.Disks {
		if d.ReadBps > 1<<40 || d.WriteBps > 1<<40 {
			t.Errorf("设备 %s 的首次速率异常: r=%d w=%d", d.Device, d.ReadBps, d.WriteBps)
		}
	}
}

func TestNetRates_无负值(t *testing.T) {
	c := New()
	c.Prime()
	for i := 0; i < 3; i++ {
		m, _ := c.Collect(context.Background())
		for _, n := range m.Nets {
			// uint64 不会是负数，但网卡重置导致的巨值要能识别
			if n.RxBps > 1<<40 {
				t.Errorf("网卡 %s 的接收速率异常: %d", n.Iface, n.RxBps)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
