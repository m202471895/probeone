package store

import (
	"context"
	"time"

	"github.com/m202471895/probeone/server/internal/model"
)

// ---------- 用户 ----------

// CreateUserInput 是创建用户的入参。
// 密码传明文，由实现层负责哈希后落库——调用方永远不接触哈希逻辑。
type CreateUserInput struct {
	Username     string
	Email        string
	Password     string
	Role         model.Role
	MustChangePW bool
}

type UserRepository interface {
	Create(ctx context.Context, in CreateUserInput) (int64, error)
	GetByID(ctx context.Context, id int64) (*model.User, error)
	GetByUsername(ctx context.Context, username string) (*model.User, error)
	List(ctx context.Context, limit, offset int) ([]model.User, error)
	UpdateRole(ctx context.Context, id int64, role model.Role) error
	UpdatePassword(ctx context.Context, id int64, newPassword string) error
	Delete(ctx context.Context, id int64) error
	Count(ctx context.Context) (int, error)
	// RecordLoginAttempt 记录一次登录尝试，返回该 identifier+ip 的近期失败次数
	RecordLoginAttempt(ctx context.Context, identifier, ip string, success bool) error
	CountRecentFailures(ctx context.Context, identifier, ip string, since time.Time) (int, error)
	// PurgeLoginAttempts 清理过期记录，由后台任务定期调用
	PurgeLoginAttempts(ctx context.Context, before time.Time) (int64, error)
}

// ---------- 会话 ----------

type SessionRepository interface {
	Create(ctx context.Context, userID int64, tokenHash, ip, ua string, expiresAt time.Time) (int64, error)
	// GetByTokenHash 返回会话。过期会话视为不存在并顺手清理。
	GetByTokenHash(ctx context.Context, tokenHash string) (*model.Session, error)
	DeleteByTokenHash(ctx context.Context, tokenHash string) error
	DeleteAllForUser(ctx context.Context, userID int64) error
	PurgeExpired(ctx context.Context, before time.Time) (int64, error)
}

// ---------- Agent 会话 ----------

type AgentSessionRepository interface {
	// CreateSession 创建会话并使该节点的所有旧会话立即失效（防重放，PRD 7.3）
	CreateSession(ctx context.Context, nodeID int64, sessionID, ip string, ttl time.Duration) error
	GetSession(ctx context.Context, sessionID string) (*model.Session, error)
	// DeleteBySessionID 主动结束会话（如收到该节点的新握手）
	DeleteBySessionID(ctx context.Context, sessionID string) error
	PurgeExpired(ctx context.Context, before time.Time) (int64, error)

	// 握手失败计数。返回当前累计失败次数。
	RecordAgentFailure(ctx context.Context, clientUUID, ip string) (int, error)
	// IsHardLocked 判断是否处于封禁期
	IsHardLocked(ctx context.Context, clientUUID, ip string) (bool, error)
	// ClearAgentFailures 握手成功后清零
	ClearAgentFailures(ctx context.Context, clientUUID, ip string) error
}

// ---------- 节点 ----------

type CreateNodeInput struct {
	UID        string
	Name       string
	GroupID    *int64
	SecretHash string
}

type NodeListFilter struct {
	GroupID *int64
	Status  model.NodeStatus
	Keyword string
	// PublicOnly 为 true 时只返回 is_public 且在线的节点。
	// 用于公开状态页——**必须**同时过滤掉离线节点，否则会通过
	// "某节点是否出现"反推出隐藏资产的存在（PRD T16 侧信道）。
	PublicOnly bool
	Limit      int
	Offset     int
}

type NodeRepository interface {
	Create(ctx context.Context, in CreateNodeInput) (int64, error)
	GetByID(ctx context.Context, id int64) (*model.Node, error)
	GetByUID(ctx context.Context, uid string) (*model.Node, error)
	List(ctx context.Context, f NodeListFilter) ([]model.Node, int, error)
	Update(ctx context.Context, n *model.Node) error
	Delete(ctx context.Context, id int64) error
	RotateSecret(ctx context.Context, id int64, newHash string) error
	Count(ctx context.Context) (int, error)
	// TouchReport 更新最后上报时间并置为在线
	TouchReport(ctx context.Context, id int64, at time.Time) error
	// MarkOffline 把 last_report_at 早于 before 的节点标记为离线，返回受影响行数
	MarkOffline(ctx context.Context, before time.Time) (int64, error)
	// ListGroups 返回全部分组
	ListGroups(ctx context.Context) ([]model.NodeGroup, error)
	CreateGroup(ctx context.Context, name string, sort int) (int64, error)
	UpdateGroup(ctx context.Context, id int64, name string, sort int) error
	DeleteGroup(ctx context.Context, id int64) error
}

// ---------- 指标 ----------

type MetricRepository interface {
	Insert(ctx context.Context, m *model.NodeMetric) (int64, error)
	// InsertBatch 批量插入，单条失败整批回滚
	InsertBatch(ctx context.Context, ms []model.NodeMetric) error
	// Range 按时间范围查询原始指标
	Range(ctx context.Context, nodeID int64, from, to time.Time, limit int) ([]model.NodeMetric, error)
	// Latest 返回指定节点各自的最新一条
	Latest(ctx context.Context, nodeIDs []int64) (map[int64]model.NodeMetric, error)
	// LatestAll 返回全部节点的最新一条，用于总览大盘
	LatestAll(ctx context.Context) (map[int64]model.NodeMetric, error)
	// Rollup 读预聚合数据
	Rollup(ctx context.Context, nodeID int64, bucket string, from, to time.Time) ([]RollupPoint, error)
	// ComputeAndStoreRollup 幂等地把原始数据聚合进预聚合表。
	// 重复执行不产生重复行（依赖 UNIQUE 约束 + upsert）。
	ComputeAndStoreRollup(ctx context.Context, bucket string, bucketStart, bucketEnd time.Time) error
	// PurgeRaw 清理过期原始数据
	PurgeRaw(ctx context.Context, before time.Time) (int64, error)
	// PurgeRollup 清理过期预聚合数据
	PurgeRollup(ctx context.Context, bucket string, before time.Time) (int64, error)
}

// RollupPoint 是一个聚合数据点。
type RollupPoint struct {
	Bucket       string
	BucketAt     time.Time
	SampleCount  int
	CPUAvg       float64
	CPUMax       float64
	MemAvg       float64
	MemMax       float64
	NetRxAvg     int64
	NetRxMax     int64
	NetTxAvg     int64
	NetTxMax     int64
	LoadAvg      float64
	DiskUsageMax float64
}

// ---------- 网站监控 ----------

type MonitorListFilter struct {
	GroupID    *int64
	Type       model.MonitorType
	Status     model.MonitorStatus
	Keyword    string
	PublicOnly bool
	Limit      int
	Offset     int
}

type MonitorRepository interface {
	Create(ctx context.Context, m *model.Monitor) (int64, error)
	GetByID(ctx context.Context, id int64) (*model.Monitor, error)
	List(ctx context.Context, f MonitorListFilter) ([]model.Monitor, int, error)
	Update(ctx context.Context, m *model.Monitor) error
	Delete(ctx context.Context, id int64) error
	UpdateStatus(ctx context.Context, id int64, status model.MonitorStatus, checkedAt time.Time, latencyMs *int) error
	// DueMonitors 返回到期需要探测的监控
	DueMonitors(ctx context.Context, now time.Time, limit int) ([]model.Monitor, error)
	InsertResult(ctx context.Context, r *model.MonitorResult) (int64, error)
	Results(ctx context.Context, monitorID int64, from, to time.Time, limit int) ([]model.MonitorResult, error)
	// Stats 计算可用率与延迟分位数
	Stats(ctx context.Context, monitorID int64, from, to time.Time) (Stats, error)
	// AllPublic 返回所有可公开的监控，用于免鉴权的状态页
	AllPublic(ctx context.Context) ([]model.Monitor, error)
	UpsertCertificate(ctx context.Context, c *model.SSLCertificate) error
	GetCertificate(ctx context.Context, monitorID int64) (*model.SSLCertificate, error)
	// DueCertificates 返回需要检查证书过期的监控
	DueCertificates(ctx context.Context, now time.Time, limit int) ([]model.Monitor, error)
	// MarkCertificateNotified 记录已通知的告警等级，避免重复提醒
	MarkCertificateNotified(ctx context.Context, monitorID int64, level string, at time.Time) error
}

// Stats 是监控统计结果。
type Stats struct {
	Total         int
	Up            int
	LatencyP50    int
	LatencyP95    int
	LatencyP99    int
	UptimePercent float64
	// SampleInsufficient 为 true 时前端应显示"样本不足"而非百分比，
	// 避免用极少样本误导用户（PRD 8.7）
	SampleInsufficient bool
}

// ---------- 告警 ----------

type AlertListFilter struct {
	Status     model.EventStatus
	Severity   model.Severity
	TargetType model.AlertTargetType
	TargetID   *int64
	Limit      int
	Offset     int
}

type AlertRepository interface {
	CreateRule(ctx context.Context, r *model.AlertRule) (int64, error)
	GetRule(ctx context.Context, id int64) (*model.AlertRule, error)
	ListRules(ctx context.Context, enabledOnly bool) ([]model.AlertRule, error)
	UpdateRule(ctx context.Context, r *model.AlertRule) error
	DeleteRule(ctx context.Context, id int64) error

	// FireEvent 创建告警事件。若同 (rule, target) 在去重窗口内已有
	// firing 事件，则只更新 last_fired_at 并返回该事件、第二个返回值为 false
	// 表示"未新建、未通知"（PRD 8.6 告警去重）。
	FireEvent(ctx context.Context, e *model.AlertEvent, dedupWindowSec int) (*model.AlertEvent, bool, error)
	ResolveEvents(ctx context.Context, ruleID *int64, targetType model.AlertTargetType, targetID int64) (int64, error)
	GetEvent(ctx context.Context, id int64) (*model.AlertEvent, error)
	ListEvents(ctx context.Context, f AlertListFilter) ([]model.AlertEvent, int, error)
	AckEvent(ctx context.Context, id, userID int64) error
	// CountRecentForTarget 统计近 N 时间同 target 的告警数，用于防风暴
	CountRecentForTarget(ctx context.Context, targetType model.AlertTargetType, targetID int64, since time.Time) (int, error)
	// RecentFiredForTarget 列出静默期内已创建但未通知的事件，用于发汇总
	RecentFiredForTarget(ctx context.Context, targetType model.AlertTargetType, targetID int64, since, until time.Time) ([]model.AlertEvent, error)
	MarkNotified(ctx context.Context, id int64) error
	PurgeEvents(ctx context.Context, before time.Time) (int64, error)
}

// ---------- 通知通道 ----------

type ChannelRepository interface {
	Create(ctx context.Context, c *model.AlertChannel) (int64, error)
	GetByID(ctx context.Context, id int64) (*model.AlertChannel, error)
	List(ctx context.Context) ([]model.AlertChannel, error)
	Update(ctx context.Context, c *model.AlertChannel) error
	Delete(ctx context.Context, id int64) error
}

// ---------- 可见性策略 ----------

type VisibilityRepository interface {
	// Policies 供 visibility.Engine 启动时载入
	Policies(ctx context.Context, scope model.VisibilityScope) ([]model.VisibilityPolicy, error)
	Update(ctx context.Context, scope model.VisibilityScope, field string, visible bool, mode model.MaskMode, rule string) error
	All(ctx context.Context) ([]model.VisibilityPolicy, error)
}

// ---------- 审计 ----------

type AuditRepository interface {
	Write(ctx context.Context, l *model.AuditLog) error
	List(ctx context.Context, userID *int64, action, targetType, targetID string, from, to time.Time, limit, offset int) ([]model.AuditLog, int, error)
	Purge(ctx context.Context, before time.Time) (int64, error)
}

// ---------- 设置 ----------

type SettingsRepository interface {
	Get(ctx context.Context, key string) (any, bool, error)
	Set(ctx context.Context, key string, value any) error
	All(ctx context.Context) (map[string]any, error)
}
