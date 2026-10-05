// Package collect 实现 Agent 的指标采集。
//
// 安全边界（PRD 1.1 / 9.2）：本包**只读**。
//   - 不写任何系统文件
//   - 不执行外部命令
//   - 不读取监控无关的路径
//
// 所有跨平台差异由 gopsutil 承担，这个包只做三件事：
//  1. 编排采集顺序
//  2. 计算差值（CPU/网络/IO 的使用率、速率）
//  3. 过滤与归一化（排除虚拟文件系统、剔除 loop 设备等）
package collect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

// 采集结果的结构与 proto 对应，但放在 Agent 侧独立定义，
// 避免 Agent 依赖 protobuf 生成代码（保持 agent 模块轻量）。

// CpuStat 是 CPU 统计。
type CpuStat struct {
	Usage         float64
	PerCore       []float64
	Model         string
	CoresPhysical int
	CoresLogical  int
}

// MemStat 是内存统计。
type MemStat struct {
	Total     uint64
	Used      uint64
	Available uint64
	Usage     float64
	SwapTotal uint64
	SwapUsed  uint64
}

// DiskStat 是磁盘统计。
type DiskStat struct {
	Mount    string
	Device   string
	FSType   string
	Total    uint64
	Used     uint64
	Usage    float64
	ReadBps  uint64
	WriteBps uint64
}

// NetStat 是网络统计。
type NetStat struct {
	Iface   string
	RxBps   uint64
	TxBps   uint64
	RxTotal uint64
	TxTotal uint64
}

// LoadStat 是负载统计。
type LoadStat struct {
	Load1  float64
	Load5  float64
	Load15 float64
}

// Sensor 是温度/风扇传感器。
type Sensor struct {
	Name  string
	Kind  string
	Value float64
	Unit  string
}

// DiskInfo 是磁盘设备信息（B 类硬件规格的一部分）。
type DiskInfo struct {
	Device string
	Mount  string
	FSType string
	Total  uint64
}

// HostInfo 是主机静态信息。
type HostInfo struct {
	Hostname     string
	FQDN         string
	OSType       string
	OSVersion    string
	Arch         string
	AgentVersion string

	// B 类：硬件规格
	CPUModel     string
	CPUCoresPhys int
	CPUCoresLog  int
	MemTotal     uint64
	Disks        []DiskInfo
	HardwareFP   string

	// C 类：运行时
	BootTime       int64
	PublicIP       string
	AgentStartedAt int64
}

// Metrics 是一轮采集的完整结果。
type Metrics struct {
	CollectedAt int64
	CPU         *CpuStat
	Mem         *MemStat
	Disks       []DiskStat
	Nets        []NetStat
	Load        *LoadStat
	Uptime      uint64
	TCPConns    int
	Sensors     []Sensor
	HardwareFP  string
}

// Collector 是采集器。
//
// 必须先调用 Prime() 预热一个周期，再调用 Collect()——
// CPU、网络、IO 的使用率都是差值法，首次调用没有"上一次"可减。
type Collector struct {
	mu sync.Mutex

	// 上一次的累计值，用于算速率
	prevNet      map[string]net.IOCountersStat
	prevNetTime  time.Time
	prevDisk     map[string]disk.IOCountersStat
	prevDiskTime time.Time

	// 静态信息缓存：这些值不会变，没必要每轮重采
	hostInfo *HostInfo

	// 预热标记：Prime 是否已执行
	primed bool
}

// New 创建采集器。
func New() *Collector {
	return &Collector{
		prevNet:  make(map[string]net.IOCountersStat),
		prevDisk: make(map[string]disk.IOCountersStat),
	}
}

// Prime 预热。
//
// 原理：CPU 使用率 = (total - idle) / total，需要"上一次"的累计值。
// Agent 启动后立即调用一次 Prime 丢掉结果，再等一个周期开始正式采集，
// 否则第一个采集点的 CPU / 网络 / IO 速率全是 0 或错误值。
func (c *Collector) Prime() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 触发一次 CPU 采样以建立内部基线
	_, _ = cpu.Percent(0, false)
	_, _ = cpu.Percent(0, true)

	now := time.Now()
	if counters, err := net.IOCounters(true); err == nil {
		for _, s := range counters {
			c.prevNet[s.Name] = s
		}
		c.prevNetTime = now
	}
	if io, err := disk.IOCounters(); err == nil {
		for _, s := range io {
			c.prevDisk[s.Name] = s
		}
		c.prevDiskTime = now
	}
	c.primed = true
}

// HostInfo 返回静态主机信息（缓存）。
func (c *Collector) HostInfo(ctx context.Context, agentVersion string) *HostInfo {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.hostInfo != nil {
		return c.hostInfo
	}

	hi := &HostInfo{
		OSType:         runtime.GOOS,
		Arch:           runtime.GOARCH,
		AgentVersion:   agentVersion,
		AgentStartedAt: time.Now().Unix(),
	}

	// 主机名与 FQDN
	if h, err := host.Info(); err == nil {
		hi.Hostname = h.Hostname
		hi.OSVersion = h.Platform + " " + h.PlatformVersion
		hi.BootTime = int64(h.BootTime)
	}
	if hi.Hostname == "" {
		hi.Hostname = "unknown"
	}
	// FQDN 通过反向 DNS 解析。多数 VPS 没有配 PTR，解析失败就留空——
	// 编造一个 FQDN 比没有更糟，会让人以为配置有误。
	hi.FQDN = resolveLocalFQDN(hi.Hostname)

	// CPU 型号与核心数（启动时读一次）
	if ci, err := cpu.Info(); err == nil && len(ci) > 0 {
		hi.CPUModel = strings.TrimSpace(ci[0].ModelName)
	}
	if ci, err := cpu.Info(); err == nil {
		seen := make(map[string]bool)
		for _, i := range ci {
			if i.PhysicalID != "" {
				seen[i.PhysicalID] = true
			}
		}
		if len(seen) > 0 {
			hi.CPUCoresPhys = len(seen)
		} else {
			// 没有 physical id 的老平台，退回用逻辑核数
			hi.CPUCoresPhys = 0 // 0 表示未知，服务端会按逻辑核显示
		}
	}
	hi.CPUCoresLog = runtime.NumCPU()

	// 内存总量
	if vm, err := mem.VirtualMemory(); err == nil {
		hi.MemTotal = vm.Total
	}

	// 磁盘设备与容量
	for _, d := range diskPartitions() {
		if !isRealDisk(d.Mountpoint) {
			continue
		}
		usage, err := disk.Usage(d.Mountpoint)
		if err != nil {
			continue
		}
		hi.Disks = append(hi.Disks, DiskInfo{
			Device: d.Device,
			Mount:  d.Mountpoint,
			FSType: d.Fstype,
			Total:  usage.Total,
		})
	}

	// 硬件指纹：服务端据此识别升配（PRD 8.4）
	hi.HardwareFP = ComputeFingerprint(FingerprintInput{
		CoresLogical:  hi.CPUCoresLog,
		CoresPhysical: hi.CPUCoresPhys,
		MemTotal:      hi.MemTotal,
		Disks:         hi.Disks,
		CPUModel:      hi.CPUModel,
	})

	c.hostInfo = hi
	return hi
}

// Collect 执行一轮采集。
func (c *Collector) Collect(ctx context.Context) (*Metrics, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	m := &Metrics{CollectedAt: time.Now().Unix()}

	// ---- CPU ----
	if percents, err := cpu.Percent(0, false); err == nil && len(percents) > 0 {
		m.CPU = &CpuStat{Usage: round2(percents[0])}
	}
	if percents, err := cpu.Percent(0, true); err == nil {
		per := make([]float64, 0, len(percents))
		for _, p := range percents {
			per = append(per, round2(p))
		}
		if m.CPU == nil {
			m.CPU = &CpuStat{}
		}
		m.CPU.PerCore = per
	}
	// 静态 CPU 信息补上（模型名、核心数）
	if c.hostInfo != nil {
		if m.CPU == nil {
			m.CPU = &CpuStat{}
		}
		m.CPU.Model = c.hostInfo.CPUModel
		m.CPU.CoresPhysical = c.hostInfo.CPUCoresPhys
		m.CPU.CoresLogical = c.hostInfo.CPUCoresLog
	}

	// ---- 内存 ----
	if vm, err := mem.VirtualMemory(); err == nil {
		m.Mem = &MemStat{
			Total:     vm.Total,
			Used:      vm.Used,
			Available: vm.Available,
			Usage:     round2(vm.UsedPercent),
		}
	}
	if sm, err := mem.SwapMemory(); err == nil {
		if m.Mem == nil {
			m.Mem = &MemStat{}
		}
		m.Mem.SwapTotal = sm.Total
		m.Mem.SwapUsed = sm.Used
	}

	// ---- 磁盘容量 ----
	now := time.Now()
	readBps, writeBps := c.diskRates(now)
	for _, d := range diskPartitions() {
		if !isRealDisk(d.Mountpoint) {
			continue
		}
		usage, err := disk.Usage(d.Mountpoint)
		if err != nil {
			continue
		}
		// 同一挂载点只保留一次（bind mount 会重复出现）
		ds := DiskStat{
			Mount:  d.Mountpoint,
			Device: d.Device,
			FSType: d.Fstype,
			Total:  usage.Total,
			Used:   usage.Used,
			Usage:  round2(usage.UsedPercent),
		}
		ds.ReadBps, ds.WriteBps = readBps[d.Device], writeBps[d.Device]
		m.Disks = append(m.Disks, ds)
	}

	// ---- 网络 ----
	m.Nets = c.netRates(now)

	// ---- 负载（仅 Linux/Unix 有）----
	if avg, err := load.Avg(); err == nil {
		m.Load = &LoadStat{
			Load1:  round2(avg.Load1),
			Load5:  round2(avg.Load5),
			Load15: round2(avg.Load15),
		}
	}

	// ---- 运行时长 ----
	if h, err := host.Uptime(); err == nil {
		m.Uptime = h
	}

	// ---- 传感器（P2，Linux 走 sysfs）----
	m.Sensors = collectSensors()

	// ---- 硬件指纹 ----
	if c.hostInfo != nil {
		m.HardwareFP = c.hostInfo.HardwareFP
	}

	return m, nil
}

// diskRates 计算各设备读写速率（差值法）。
// 返回的 map key 是设备名，可能为空（部分平台不提供）。
func (c *Collector) diskRates(now time.Time) (map[string]uint64, map[string]uint64) {
	read := make(map[string]uint64)
	write := make(map[string]uint64)

	counters, err := disk.IOCounters()
	if err != nil {
		return read, write
	}
	dt := now.Sub(c.prevDiskTime).Seconds()
	if dt <= 0 {
		return read, write
	}

	for _, cur := range counters {
		prev, ok := c.prevDisk[cur.Name]
		if ok && cur.ReadBytes >= prev.ReadBytes && cur.WriteBytes >= prev.WriteBytes {
			read[cur.Name] = uint64(float64(cur.ReadBytes-prev.ReadBytes) / dt)
			write[cur.Name] = uint64(float64(cur.WriteBytes-prev.WriteBytes) / dt)
		}
		c.prevDisk[cur.Name] = cur
	}
	c.prevDiskTime = now
	return read, write
}

// netRates 计算各网卡收发速率（差值法）。
func (c *Collector) netRates(now time.Time) []NetStat {
	counters, err := net.IOCounters(true)
	if err != nil {
		return nil
	}
	dt := now.Sub(c.prevNetTime).Seconds()
	if dt <= 0 {
		dt = c.prevNetTime.Sub(now.Add(-time.Second)).Seconds()
		if dt <= 0 {
			return nil
		}
	}

	out := make([]NetStat, 0, len(counters))
	for _, cur := range counters {
		ns := NetStat{
			Iface:   cur.Name,
			RxTotal: cur.BytesRecv,
			TxTotal: cur.BytesSent,
		}
		if prev, ok := c.prevNet[cur.Name]; ok {
			// 计数器可能因网卡重置而回退，此时报 0 而不是巨大的负数
			if cur.BytesRecv >= prev.BytesRecv && cur.BytesSent >= prev.BytesSent {
				ns.RxBps = uint64(float64(cur.BytesRecv-prev.BytesRecv) / dt)
				ns.TxBps = uint64(float64(cur.BytesSent-prev.BytesSent) / dt)
			}
		}
		// 跳过全零的虚拟网卡（lo 会全零但至少有流量计数）
		if cur.BytesRecv == 0 && cur.BytesSent == 0 {
			continue
		}
		out = append(out, ns)
		c.prevNet[cur.Name] = cur
	}
	c.prevNetTime = now
	return out
}

// diskPartitions 返回分区列表，失败时返回空。
func diskPartitions() []disk.PartitionStat {
	parts, err := disk.Partitions(false)
	if err != nil {
		return nil
	}
	return parts
}

// isRealDisk 判断是否为需要统计的真实磁盘。
//
// 过滤掉虚拟文件系统，否则面板上会是一堆 tmpfs/overlay，
// 磁盘占用率被这些"占用 0%"的挂载点拉低，失去参考价值。
func isRealDisk(mountpoint string) bool {
	if mountpoint == "" {
		return false
	}
	// 明确排除的系统挂载点
	switch mountpoint {
	case "/proc", "/sys", "/dev", "/run", "/snap", "/var/lib/docker",
		"/var/lib/kubelet", "/System/Volumes/VM", "/private/var/vm":
		return false
	}
	// 前缀排除
	for _, prefix := range []string{
		"/proc/", "/sys/", "/dev/", "/run/", "/snap/",
		"/var/lib/docker/", "/var/lib/containers/", "/var/lib/kubelet/",
		"/System/Volumes/",
	} {
		if strings.HasPrefix(mountpoint, prefix) {
			return false
		}
	}
	// loop 设备不统计（同一块盘的多个挂载点）
	if strings.HasPrefix(mountpoint, "/dev/loop") {
		return false
	}
	// 根挂载点必须保留
	if mountpoint == "/" {
		return true
	}
	// 太浅的路径通常是伪挂载
	return len(strings.Split(strings.Trim(mountpoint, "/"), "/")) >= 1
}

// round2 保留两位小数，避免上报 45.000000001 这类噪声。
//
// 注意负数：Go 的 int64 转换是向零截断，-1.5 会被算成 -1 而非 -2。
// 指标理论上不会为负（速率、占用率都是非负量），
// 但 gopsutil 在部分平台的边界情况下可能返回微小负值，
// 因此这里显式处理符号，不依赖"反正不会出现负数"。
func round2(f float64) float64 {
	if f < 0 {
		return -float64(int64(-f*100+0.5)) / 100
	}
	return float64(int64(f*100+0.5)) / 100
}

// ---------- 硬件指纹 ----------

// FingerprintInput 是指纹的输入项。
type FingerprintInput struct {
	CoresLogical  int
	CoresPhysical int
	MemTotal      uint64
	Disks         []DiskInfo
	CPUModel      string
}

// ComputeFingerprint 计算硬件指纹。
//
// 算法：SHA256(排序后的磁盘列表 + 核心数 + 内存总量 + 归一化的CPU 型号)[:16]
//
// 三个必须做的归一化（PRD 8.4），否则会产生假变更：
//  1. 磁盘列表**先排序再哈希**——挂载顺序变化会造成假变更
//  2. 内存总量**向下取整到 MB**——各平台 reserved 区差几个字节会造成持续误报
//  3. CPU 型号**归一化**——同型号在不同平台字符串可能不同（空格、大小写）
func ComputeFingerprint(in FingerprintInput) string {
	var b strings.Builder

	// 核心数
	b.WriteString("cores=")
	b.WriteString(itoa(in.CoresLogical))
	b.WriteString(",")
	b.WriteString(itoa(in.CoresPhysical))
	b.WriteString(";")

	// 内存：向下取整到 MB，抹掉平台差异带来的字节级抖动
	b.WriteString("mem=")
	b.WriteString(itoa(int(in.MemTotal / 1024 / 1024)))
	b.WriteString(";")

	// CPU 型号：去空格 + 转小写
	b.WriteString("cpu=")
	b.WriteString(normalizeCPUModel(in.CPUModel))
	b.WriteString(";")

	// 磁盘：先按挂载点排序，保证顺序无关
	disks := make([]DiskInfo, len(in.Disks))
	copy(disks, in.Disks)
	sort.Slice(disks, func(i, j int) bool {
		if disks[i].Mount != disks[j].Mount {
			return disks[i].Mount < disks[j].Mount
		}
		return disks[i].Device < disks[j].Device
	})
	for _, d := range disks {
		b.WriteString("disk=")
		b.WriteString(d.Mount)
		b.WriteString(":")
		b.WriteString(d.FSType)
		b.WriteString(":")
		// 容量同样取整到 MB
		b.WriteString(itoa(int(d.Total / 1024 / 1024)))
		b.WriteString(";")
	}

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:16]
}

// normalizeCPUModel 归一化 CPU 型号字符串。
//
// 例："Intel(R) Xeon(R) CPU E5-2680 v4 @ 2.40GHz"
// 与 "intel(r) xeon(r) cpu e5-2680 v4 @ 2.40ghz" 应当视为同一型号。
func normalizeCPUModel(s string) string {
	s = strings.ToLower(s)
	// 折叠所有空白为单个空格并去首尾
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// itoa 是无依赖的整数转字符串。
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
