/*
 * convert.go —— collect.Metrics 到 protobuf 的转换。
 *
 * 为什么单独一个文件：collect 包不依赖 protobuf，
 * 这样采集逻辑可以单独测试（不需要起 gRPC），
 * 也不会因为协议改动而牵动采集代码。
 */
package transport

import (
	"github.com/m202471895/probeone/agent/internal/collect"
	agentv1 "github.com/m202471895/probeone/api/agent/v1"
)

// MetricsToProto 把采集结果转成协议消息。
func MetricsToProto(m *collect.Metrics, seq int32) *agentv1.ReportMetrics {
	if m == nil {
		return nil
	}
	out := &agentv1.ReportMetrics{
		CollectedAt:  m.CollectedAt,
		Cpu:          cpuToProto(m.CPU),
		Mem:          memToProto(m.Mem),
		Load:         loadToProto(m.Load),
		Uptime:       int64(m.Uptime),
		TcpConnCount: int32(m.TCPConns),
		HardwareFp:   m.HardwareFP,
	}
	out.Disks = make([]*agentv1.DiskStat, 0, len(m.Disks))
	for i := range m.Disks {
		out.Disks = append(out.Disks, diskToProto(&m.Disks[i]))
	}
	out.Nets = make([]*agentv1.NetStat, 0, len(m.Nets))
	for i := range m.Nets {
		out.Nets = append(out.Nets, netToProto(&m.Nets[i]))
	}
	out.Sensors = make([]*agentv1.SensorStat, 0, len(m.Sensors))
	for i := range m.Sensors {
		out.Sensors = append(out.Sensors, sensorToProto(&m.Sensors[i]))
	}
	return out
}

func cpuToProto(c *collect.CpuStat) *agentv1.CpuStat {
	if c == nil {
		return nil
	}
	return &agentv1.CpuStat{
		Usage:         float32(c.Usage),
		PerCore:       toF32Slice(c.PerCore),
		Model:         c.Model,
		CoresPhysical: int32(c.CoresPhysical),
		CoresLogical:  int32(c.CoresLogical),
	}
}

func memToProto(m *collect.MemStat) *agentv1.MemStat {
	if m == nil {
		return nil
	}
	return &agentv1.MemStat{
		Total:     int64(m.Total),
		Used:      int64(m.Used),
		Available: int64(m.Available),
		Usage:     float32(m.Usage),
		SwapTotal: int64(m.SwapTotal),
		SwapUsed:  int64(m.SwapUsed),
	}
}

func diskToProto(d *collect.DiskStat) *agentv1.DiskStat {
	return &agentv1.DiskStat{
		Mount:    d.Mount,
		Device:   d.Device,
		Fstype:   d.FSType,
		Total:    int64(d.Total),
		Used:     int64(d.Used),
		Usage:    float32(d.Usage),
		ReadBps:  int64(d.ReadBps),
		WriteBps: int64(d.WriteBps),
	}
}

func netToProto(n *collect.NetStat) *agentv1.NetStat {
	return &agentv1.NetStat{
		Iface:   n.Iface,
		RxBps:   int64(n.RxBps),
		TxBps:   int64(n.TxBps),
		RxTotal: int64(n.RxTotal),
		TxTotal: int64(n.TxTotal),
	}
}

func loadToProto(l *collect.LoadStat) *agentv1.LoadStat {
	if l == nil {
		return nil
	}
	return &agentv1.LoadStat{
		Load1:  float32(l.Load1),
		Load5:  float32(l.Load5),
		Load15: float32(l.Load15),
	}
}

func sensorToProto(s *collect.Sensor) *agentv1.SensorStat {
	return &agentv1.SensorStat{
		Name:  s.Name,
		Kind:  s.Kind,
		Value: float32(s.Value),
		Unit:  s.Unit,
	}
}

// HostInfoToProto 把主机信息转成协议消息。
func HostInfoToProto(h *collect.HostInfo, seq int32) *agentv1.ReportHostInfo {
	if h == nil {
		return nil
	}
	out := &agentv1.ReportHostInfo{
		Hostname:       h.Hostname,
		Fqdn:           h.FQDN,
		OsType:         h.OSType,
		OsVersion:      h.OSVersion,
		Arch:           h.Arch,
		AgentVersion:   h.AgentVersion,
		CpuModel:       h.CPUModel,
		MemTotal:       int64(h.MemTotal),
		HardwareFp:     h.HardwareFP,
		BootTime:       h.BootTime,
		PublicIp:       h.PublicIP,
		AgentStartedAt: h.AgentStartedAt,
	}
	out.Disks = make([]*agentv1.DiskInfo, 0, len(h.Disks))
	for i := range h.Disks {
		out.Disks = append(out.Disks, &agentv1.DiskInfo{
			Mount:  h.Disks[i].Mount,
			Device: h.Disks[i].Device,
			Fstype: h.Disks[i].FSType,
			Total:  int64(h.Disks[i].Total),
		})
	}
	return out
}

// toF32Slice 把 []float64 转成 protobuf 需要的 []float32。
//
// protobuf 的 float 是 32 位，内存占用是 float64 的一半。
// 指标精度用不到 64 位（0.01% 的差异在面板上不可见），
// 但每个节点每 10 秒发几十条，流量差别是实打实的。
func toF32Slice(in []float64) []float32 {
	if len(in) == 0 {
		return nil
	}
	out := make([]float32, len(in))
	for i, v := range in {
		out[i] = float32(v)
	}
	return out
}
