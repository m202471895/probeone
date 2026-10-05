package grpcsvc

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"time"

	"google.golang.org/grpc/credentials"

	agentv1 "github.com/m202471895/probeone/api/agent/v1"
	"github.com/m202471895/probeone/server/internal/model"
)

// 本文件放转换函数与小工具，与主服务逻辑分开，
// 让 server.go 与 hardware.go 专注于流程控制。

// toModelMetric 把 proto 消息转成存储层的模型。
//
// 转换点归一化：
//   - 时间由调用方算好，这里只搬运
//   - 数值裁剪到合理范围，Agent 不可信，异常值不该污染曲线
func toModelMetric(nodeID int64, collectedAt time.Time, m *agentv1.ReportMetrics) model.NodeMetric {
	out := model.NodeMetric{
		NodeID:       nodeID,
		CollectedAt:  collectedAt,
		Uptime:       int64(m.GetUptime()),
		TCPConnCount: int(m.GetTcpConnCount()),
	}

	// proto 用 float32（省带宽），模型用 float64。
	// 转换是显式的：protobuf 出于兼容性不会改字段类型，
	// 而 float32 → float64 的隐式转换会在编译期报错，正好提醒我们这里有精度选择要做。
	// 选 float64：内存指标本来就该用双精度，且转换是无损的（float32 总是 float64 的子集）。
	if c := m.GetCpu(); c != nil {
		out.CPUUsage = clampPercent(float64(c.GetUsage()))
		per := c.GetPerCore()
		if len(per) > 0 {
			out.CPUCores = make([]float64, 0, len(per))
			for _, v := range per {
				out.CPUCores = append(out.CPUCores, float64(v))
			}
		}
	}
	if mem := m.GetMem(); mem != nil {
		out.MemTotal = mem.GetTotal()
		out.MemUsed = mem.GetUsed()
		out.MemAvailable = mem.GetAvailable()
		out.MemUsage = clampPercent(float64(mem.GetUsage()))
		out.SwapTotal = mem.GetSwapTotal()
		out.SwapUsed = mem.GetSwapUsed()
	}
	if l := m.GetLoad(); l != nil {
		out.Load1 = float64(l.GetLoad1())
		out.Load5 = float64(l.GetLoad5())
		out.Load15 = float64(l.GetLoad15())
	}

	for _, d := range m.GetDisks() {
		out.Disks = append(out.Disks, model.DiskUsage{
			Mount:    d.GetMount(),
			FSType:   d.GetFstype(),
			Total:    d.GetTotal(),
			Used:     d.GetUsed(),
			Usage:    clampPercent(float64(d.GetUsage())),
			ReadBps:  d.GetReadBps(),
			WriteBps: d.GetWriteBps(),
		})
	}

	for _, n := range m.GetNets() {
		out.NetIO = append(out.NetIO, model.NetUsage{
			Iface:   n.GetIface(),
			RxBps:   n.GetRxBps(),
			TxBps:   n.GetTxBps(),
			RxTotal: n.GetRxTotal(),
			TxTotal: n.GetTxTotal(),
		})
	}

	for _, s := range m.GetSensors() {
		out.Sensors = append(out.Sensors, model.Sensor{
			Name:  s.GetName(),
			Kind:  s.GetKind(),
			Value: float64(s.GetValue()),
			Unit:  s.GetUnit(),
		})
	}

	return out
}

// disksFromProto 从上报的磁盘列表提取设备信息。
// 只取容量维度，device/label 保留但不参与指纹计算。
func disksFromProto(disks []*agentv1.DiskStat) []model.DiskInfo {
	out := make([]model.DiskInfo, 0, len(disks))
	for _, d := range disks {
		if d.GetMount() == "" || d.GetTotal() == 0 {
			continue
		}
		out = append(out, model.DiskInfo{
			Device: d.GetDevice(),
			Mount:  d.GetMount(),
			FSType: d.GetFstype(),
			Total:  d.GetTotal(),
		})
	}
	return out
}

// disksFromHostProto 从主机信息的磁盘列表提取。
func disksFromHostProto(disks []*agentv1.DiskInfo) []model.DiskInfo {
	out := make([]model.DiskInfo, 0, len(disks))
	for _, d := range disks {
		if d.GetMount() == "" || d.GetTotal() == 0 {
			continue
		}
		out = append(out, model.DiskInfo{
			Device: d.GetDevice(),
			Mount:  d.GetMount(),
			FSType: d.GetFstype(),
			Total:  d.GetTotal(),
		})
	}
	return out
}

// clampPercent 把百分比夹到 0-100。
//
// Agent 是不可信输入：一个 -5 或 105 的使用率若直接入库，
// 图表会画出越界的曲线，前端还要额外处理。宁可截断也不要脏数据。
func clampPercent(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 100:
		return 100
	default:
		return v
	}
}

// loadMTLS 构建 mTLS 凭据：服务端要求并校验客户端证书。
func loadMTLS(certFile, keyFile, caFile string) (credentials.TransportCredentials, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("加载服务端证书失败: %w", err)
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("读取客户端 CA 失败: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("客户端 CA 文件内容不是合法的 PEM 证书")
	}
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}), nil
}

// lisFor 创建监听器。抽出来便于测试替换。
func lisFor(addr string) net.Listener {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		// 监听失败属于致命错误，NewServer 阶段已经校验过配置，
		// 走到这里通常是端口被占用，直接 panic 让上层感知
		panic("创建监听器失败: " + err.Error())
	}
	return l
}
