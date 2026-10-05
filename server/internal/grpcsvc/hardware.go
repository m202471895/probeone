package grpcsvc

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	agentv1 "github.com/m202471895/probeone/api/agent/v1"
	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/store"
)

// confirmRounds 是确认硬件变更所需的连续轮数。
//
// 为什么需要多轮确认：云主机的 vCPU 数量会因宿主机调度出现 ±1 的抖动
// （如"弹性伸缩"或"超卖"场景）。单轮变化就报"规格已变更"会产生
// 大量假告警，值班人员很快就会忽略这类通知——比不报更糟。
const confirmRounds = 3

// pendingFP 记录待确认的指纹及其连续出现次数。
type pendingFP struct {
	fp     string
	rounds int
}

// Ingestor 负责把 Agent 上报的数据落库，并在其中完成升配检测。
type Ingestor struct {
	db  *store.DB
	log *slog.Logger

	// pending 待确认的指纹变更：node_id → 待确认状态。
	// 刻意放内存而非落库：这个状态只活几秒（3 轮 × 10 秒），
	// 服务重启丢失它没有任何影响——重启后重新观察 3 轮即可。
	// 用完即删，不会无限增长。
	pendingMu sync.Mutex
	pending   map[int64]*pendingFP
}

// NewIngestor 创建数据处理器。
func NewIngestor(db *store.DB, log *slog.Logger) *Ingestor {
	return &Ingestor{
		db:      db,
		log:     log,
		pending: make(map[int64]*pendingFP),
	}
}

// IngestMetrics 处理一批指标。
func (i *Ingestor) IngestMetrics(ctx context.Context, nodeID int64, m *agentv1.ReportMetrics) error {
	if m == nil {
		return nil
	}

	// 采集时间以 Agent 本地时钟为准，但要做合理性检查：
	// 时间戳明显异常（未来太远或过于久远）会让曲线错乱。
	collectedAt := time.Unix(m.GetCollectedAt(), 0).UTC()
	now := time.Now().UTC()
	if collectedAt.After(now.Add(5 * time.Minute)) {
		// Agent 时钟超前，用服务端时间兜底
		collectedAt = now
	} else if collectedAt.Before(now.Add(-30 * 24 * time.Hour)) {
		// 过于久远，大概率是时钟错误
		collectedAt = now
	}

	metric := toModelMetric(nodeID, collectedAt, m)

	// 写入指标
	if _, err := i.db.Metrics.Insert(ctx, &metric); err != nil {
		return fmt.Errorf("写入指标失败: %w", err)
	}

	// 标记节点在线（顺便更新最后上报时间）
	if err := i.db.Nodes.TouchReport(ctx, nodeID, now); err != nil {
		i.log.Warn("更新节点上报时间失败",
			slog.Int64("node_id", nodeID), slog.String("error", err.Error()))
	}

	// 硬件变更检测（PRD 3.1.2 / 8.4）
	if m.GetHardwareFp() != "" {
		if err := i.checkHardwareChange(ctx, nodeID, m); err != nil {
			i.log.Warn("硬件变更检测失败",
				slog.Int64("node_id", nodeID), slog.String("error", err.Error()))
		}
	}

	return nil
}

// checkHardwareChange 比对硬件指纹，判断是否发生升配。
//
// 流程（PRD 8.4）：
//  1. 指纹为空 → 首次上报，直接记录
//  2. 指纹相同 → 快速返回（最常见路径，不做任何写操作）
//  3. 指纹不同 → 记为待确认，连续 confirmRounds 轮相同才确认变更
//  4. 确认后：UPSERT + 写审计 + 发 info 级告警
func (i *Ingestor) checkHardwareChange(ctx context.Context, nodeID int64, m *agentv1.ReportMetrics) error {
	node, err := i.db.Nodes.GetByID(ctx, nodeID)
	if err != nil {
		return err
	}

	incoming := m.GetHardwareFp()

	// Case A：首次上报（stored为空）
	if node.HardwareFP == "" {
		return i.applyHardware(ctx, node, m, incoming, nil)
	}

	// Case B：无变化——最常见，直接返回
	if incoming == node.HardwareFP {
		i.clearPending(nodeID)
		return nil
	}

	// Case C：疑似变更 —— 需连续确认
	pending, isNew := i.bumpPending(nodeID, incoming)
	if isNew {
		// 换了新的指纹，重新计数
		i.log.Debug("检测到硬件指纹变化，等待确认",
			slog.Int64("node_id", nodeID),
			slog.String("old", node.HardwareFP),
			slog.String("new", incoming),
			slog.Int("round", 1))
		return nil
	}

	if pending.rounds < confirmRounds {
		i.log.Debug("硬件指纹变化确认中",
			slog.Int64("node_id", nodeID),
			slog.Int("round", pending.rounds),
			slog.Int("required", confirmRounds))
		return nil
	}

	// 达到确认轮数，执行变更
	before := snapshotHardware(node)
	if err := i.applyHardware(ctx, node, m, incoming, before); err != nil {
		return err
	}
	i.clearPending(nodeID)
	return nil
}

// bumpPending 递增待确认计数，返回快照与是否是新指纹。
//
// 返回**值拷贝**而不是内部指针：调用方在锁外读 rounds，
// 若返回内部指针就构成数据竞争（-race 能抓到，-race 关闭时是静默的错值）。
func (i *Ingestor) bumpPending(nodeID int64, fp string) (pendingFP, bool) {
	i.pendingMu.Lock()
	defer i.pendingMu.Unlock()

	p, ok := i.pending[nodeID]
	if !ok || p.fp != fp {
		i.pending[nodeID] = &pendingFP{fp: fp, rounds: 1}
		return pendingFP{fp: fp, rounds: 1}, true
	}
	p.rounds++
	return *p, false
}

// applyHardware 写入新的硬件规格并记录变更。
// before 为 nil 表示首次上报，只记录不告警。
func (i *Ingestor) applyHardware(ctx context.Context, node *model.Node, m *agentv1.ReportMetrics, fp string, before *hardwareSnapshot) error {
	// 变更前的规格（由调用方传入的 before 也应是这个值，
	// 这里再取一次是为了不依赖调用方的快照时机）
	prevState := snapshotHardware(node)

	node.HardwareFP = fp
	node.CPUCores = int(m.GetCpu().GetCoresLogical())
	node.MemTotal = int64(m.GetMem().GetTotal())
	if cpuModel := m.GetCpu().GetModel(); cpuModel != "" {
		node.CPUModel = cpuModel
	}
	node.DiskInfo = disksFromProto(m.GetDisks())
	// hardware_changed_at 只在"确实发生变更"时设置。
	// 首次上报是建立基线，不是变更——标上时间会让界面误显示"最近升配过"。
	var changedAt *time.Time
	if before != nil {
		now := time.Now().UTC()
		changedAt = &now
	}
	node.HardwareChangedAt = changedAt

	if err := i.db.Nodes.Update(ctx, node); err != nil {
		return fmt.Errorf("更新硬件规格失败: %w", err)
	}

	// 变更后的规格
	changed := snapshotHardware(node)

	// 仅在"确实变了"时写审计与告警。
	// 比较对象是「调用方传入的变更前快照」与「当前规格」——
	// 不能拿两个都是变更前的快照互相比较，那样恒相等，条件永远为真。
	if before != nil && !before.Equal(*changed) {
		detail := map[string]any{
			"before": prevState.toMap(),
			"after":  changed.toMap(),
		}
		// 写审计日志
		if err := i.db.Audit.Write(ctx, &model.AuditLog{
			Action:     "node.hardware_changed",
			TargetType: "node",
			TargetID:   node.UID,
			Detail:     detail,
		}); err != nil {
			i.log.Warn("写硬件变更审计失败", slog.String("error", err.Error()))
		}

		// 发 info 级告警事件（PRD 8.4）
		msg := fmt.Sprintf("节点规格变更：%s → %s", prevState.Describe(), changed.Describe())
		ev := &model.AlertEvent{
			TargetType: model.TargetNode,
			TargetID:   &node.ID,
			TargetName: node.Name,
			Severity:   model.SeverityInfo,
			Message:    msg,
			Payload:    detail,
		}
		// dedup=0 走默认 1800s 去重窗口，避免抖动期间重复发通知
		if _, _, err := i.db.Alerts.FireEvent(ctx, ev, 0); err != nil {
			i.log.Warn("创建硬件变更告警失败", slog.String("error", err.Error()))
		}

		i.log.Info("检测到硬件规格变更",
			slog.String("node", node.Name),
			slog.String("uid", node.UID),
			slog.String("before", prevState.Describe()),
			slog.String("after", changed.Describe()))
	}

	return nil
}

func (i *Ingestor) clearPending(nodeID int64) {
	i.pendingMu.Lock()
	defer i.pendingMu.Unlock()
	if _, ok := i.pending[nodeID]; ok {
		delete(i.pending, nodeID)
	}
}

// IngestHostInfo 处理主机静态信息（A 类 + B 类兜底）。
func (i *Ingestor) IngestHostInfo(ctx context.Context, nodeID int64, h *agentv1.ReportHostInfo) error {
	if h == nil {
		return nil
	}
	node, err := i.db.Nodes.GetByID(ctx, nodeID)
	if err != nil {
		return err
	}

	// A 类：不可变信息，仅在有值时更新
	if h.GetHostname() != "" {
		node.Hostname = h.GetHostname()
	}
	// FQDN 暂不单独入库：model.Node 未设该字段，且它与 hostname 高度重合。
	// 若将来需要区分，应新增字段而不是塞进 Hostname——那会让"主机名"这个
	// 字段在不同机器上有不同的含义。
	if h.GetOsType() != "" {
		node.OSType = h.GetOsType()
	}
	if h.GetOsVersion() != "" {
		node.OSVersion = h.GetOsVersion()
	}
	if h.GetArch() != "" {
		node.Arch = h.GetArch()
	}
	if h.GetAgentVersion() != "" {
		node.AgentVersion = h.GetAgentVersion()
	}

	// B 类：硬件规格（ReportMetrics 优先，这里是兜底）
	if h.GetCpuModel() != "" && node.CPUModel == "" {
		node.CPUModel = h.GetCpuModel()
	}
	if h.GetMemTotal() > 0 && node.MemTotal == 0 {
		node.MemTotal = int64(h.GetMemTotal())
	}
	if len(node.DiskInfo) == 0 && len(h.GetDisks()) > 0 {
		node.DiskInfo = disksFromHostProto(h.GetDisks())
	}

	// C 类：运行时
	if h.GetBootTime() > 0 {
		t := time.Unix(h.GetBootTime(), 0).UTC()
		node.BootTime = &t
	}
	if h.GetPublicIp() != "" && h.GetPublicIp() != node.PublicIP {
		node.PublicIP = h.GetPublicIp()
	}

	return i.db.Nodes.Update(ctx, node)
}

// hardwareSnapshot 是硬件规格的快照，用于比较变更。
type hardwareSnapshot struct {
	Cores int
	MemGB int64
	Disks int
	Model string
}

func snapshotHardware(n *model.Node) *hardwareSnapshot {
	return &hardwareSnapshot{
		Cores: n.CPUCores,
		MemGB: n.MemTotal / 1024 / 1024 / 1024,
		Disks: len(n.DiskInfo),
		Model: n.CPUModel,
	}
}

func (h hardwareSnapshot) Equal(o hardwareSnapshot) bool {
	return h.Cores == o.Cores && h.MemGB == o.MemGB &&
		h.Disks == o.Disks && h.Model == o.Model
}

func (h hardwareSnapshot) Describe() string {
	disks := ""
	if h.Disks > 0 {
		disks = fmt.Sprintf(", %d块盘", h.Disks)
	}
	modelName := h.Model
	if len(modelName) > 24 {
		modelName = modelName[:24] + "..."
	}
	return fmt.Sprintf("%d核 %dGB%s (%s)", h.Cores, h.MemGB, disks, modelName)
}

func (h hardwareSnapshot) toMap() map[string]any {
	return map[string]any{
		"cores":     h.Cores,
		"mem_gb":    h.MemGB,
		"disks":     h.Disks,
		"cpu_model": h.Model,
	}
}
