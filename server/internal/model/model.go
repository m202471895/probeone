// Package model 定义领域模型。
//
// 这些结构是存储层与业务层之间的契约。**注意**：model 中的 Node 含敏感字段
// （agent_secret、public_ip、internal_ip 等），因此 model 结构体**绝不可直接**
// 序列化给客户端。所有对外响应必须经 DTO 组装 + visibility 脱敏。
// 详见 PRD 3.6.4。
package model

import "time"

// Role 是用户角色。v1.0 内置三种固定角色，不做自定义角色（PRD 3.5）。
type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleViewer Role = "viewer"
)

// Valid 判断角色是否合法。
func (r Role) Valid() bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleViewer:
		return true
	}
	return false
}

// CanWrite 判断是否有写权限。
func (r Role) CanWrite() bool { return r == RoleOwner || r == RoleAdmin }

// CanManageSystem 判断是否可管理系统级设置。
func (r Role) CanManageSystem() bool { return r == RoleOwner }

// User 是用户。
type User struct {
	ID           int64
	Username     string
	Email        string
	PasswordHash string // argon2id 编码串，绝不返回客户端
	Role         Role
	Status       string // active / disabled
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Session 是登录会话。
// TokenHash 只存 SHA256 哈希，明文仅在登录响应中返回一次（PRD 9.4）。
type Session struct {
	ID        int64
	UserID    int64
	TokenHash string
	IP        string
	UserAgent string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// NodeGroup 是节点分组。
type NodeGroup struct {
	ID   int64
	Name string
	Sort int
}

// NodeStatus 是节点在线状态。
type NodeStatus string

const (
	NodePending NodeStatus = "pending"
	NodeOnline  NodeStatus = "online"
	NodeOffline NodeStatus = "offline"
)

// Node 是被监控的节点。
//
// 字段按PRD 3.1.2 的 A/B/C 三类组织：
//   A 类身份信息  不可变
//   B 类硬件规格  可随升配变化，每轮UPSERT
//   C 类运行时    每轮覆盖
type Node struct {
	ID     int64
	UID    string // 对外暴露的 ID
	Name   string
	GroupID *int64

	// A 类·身份信息
	Hostname     string
	OSType       string
	OSVersion    string
	Arch         string
	AgentVersion string

	// B 类·硬件规格
	CPUModel           string
	CPUCores           int
	MemTotal           int64
	DiskInfo           []DiskInfo // 内部结构含 device，公开时必须剔除
	HardwareFP         string
	HardwareChangedAt  *time.Time

	// C 类·运行时环境
	BootTime    *time.Time
	PublicIP    string
	GeoCountry  string
	GeoCity     string
	LastSeenAt  *time.Time
	LastReportAt *time.Time

	// 管理字段
	AgentSecretHash string // argon2id，绝不返回客户端
	Remark          string
	IsPublic        bool
	Status          NodeStatus
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// DiskInfo 是磁盘信息。
// Device/Label/UUID 在公开响应中必须被剔除（PRD 3.6.2）。
type DiskInfo struct {
	Device string `json:"device"`
	Mount  string `json:"mount"`
	FSType string `json:"fstype"`
	Total  int64  `json:"total"`
	Used   int64  `json:"used"`
	Usage  float64 `json:"usage"`
}

// MonitorType 是网站监控探针类型（PRD 3.2）。
type MonitorType string

const (
	MonitorHTTP MonitorType = "http"
	MonitorTCP  MonitorType = "tcp"
	MonitorPing MonitorType = "ping"
	MonitorDNS  MonitorType = "dns"
	MonitorSSL  MonitorType = "ssl"
)

// MonitorStatus 是监控对象状态。
type MonitorStatus string

const (
	MonitorPending MonitorStatus = "pending"
	MonitorUp      MonitorStatus = "up"
	MonitorDown    MonitorStatus = "down"
	MonitorPaused  MonitorStatus = "paused"
)

// FailReason 是探测失败的具��原因枚举（PRD 3.2）。
// 不允许只存bool，必须能区分失败原因。
type FailReason string

const (
	ReasonOK              FailReason = "ok"
	ReasonTimeout         FailReason = "timeout"
	ReasonConnRefused     FailReason = "conn_refused"
	ReasonDNSError        FailReason = "dns_error"
	ReasonTLSError        FailReason = "tls_error"
	ReasonStatusMismatch  FailReason = "status_mismatch"
	ReasonContentMismatch FailReason = "content_mismatch"
	ReasonSlow            FailReason = "slow"
	ReasonUnknown         FailReason = "unknown"
)

// Monitor 是网站监控对象。
type Monitor struct {
	ID            int64
	Name          string
	Type          MonitorType
	Target        string
	Config        MonitorConfig
	IntervalSec   int
	TimeoutSec    int
	GroupID       *int64
	Status        MonitorStatus
	IsPublic      bool
	Sort          int
	LastCheckedAt *time.Time
	Uptime30d     *float64
	AvgLatencyMs  *int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// MonitorConfig 是各探针类型的私有配置。
type MonitorConfig struct {
	// HTTP
	Method         string   `json:"method,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	ExpectStatus   []int    `json:"expect_status,omitempty"`
	ExpectKeywords []string `json:"expect_keywords,omitempty"`
	// TCP / DNS
	Port     int    `json:"port,omitempty"`
	Record   string `json:"record,omitempty"`// dns 记录类型
	Resolver string `json:"resolver,omitempty"`
	// 阈值
	MaxLatencyMs int `json:"max_latency_ms,omitempty"`
	// SSL
	WarnDaysLeft int `json:"warn_days_left,omitempty"`
}

// MonitorResult 是一次探测结果。
type MonitorResult struct {
	ID          int64
	MonitorID   int64
	CheckedAt   time.Time
	OK          bool
	Reason      FailReason
	StatusCode  *int
	LatencyMs   *int
	DNSMs       *int
	TCPMs       *int
	TLSMs       *int
	TTFBMs      *int
	ErrorDetail string
}

// SSLCertificate 是证书信息。
type SSLCertificate struct {
	MonitorID      int64
	Subject        string
	Issuer         string
	Serial         string
	NotBefore      *time.Time
	NotAfter       *time.Time
	DaysLeft       *int
	Fingerprint    string
	LastCheckedAt  *time.Time
}

// ChannelType 是通知通道类型（PRD 3.3）。
type ChannelType string

const (
	ChannelWebhook  ChannelType = "webhook"
	ChannelEmail    ChannelType = "email"
	ChannelWeCom    ChannelType = "wecom"
	ChannelDingTalk ChannelType = "dingtalk"
	ChannelFeishu   ChannelType = "feishu"
	ChannelTelegram ChannelType = "telegram"
	ChannelBark     ChannelType = "bark"
)

// AlertChannel 是通知通道。
// Config 中的密钥类字段落库前必须加密（PRD T12）。
type AlertChannel struct {
	ID        int64
	Name      string
	Type      ChannelType
	Config    map[string]any
	Enabled   bool
	CreatedAt time.Time
}

// AlertTargetType 是告警目标类型。
type AlertTargetType string

const (
	TargetNode    AlertTargetType = "node"
	TargetMonitor AlertTargetType = "monitor"
	TargetCert    AlertTargetType = "cert"
)

// Severity 是告警级别。
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// AlertRule 是告警规则。
type AlertRule struct {
	ID              int64
	Name            string
	TargetType      AlertTargetType
	TargetID        *int64 // nil 表示匹配全部
	Metric          string
	Condition       RuleCondition
	Severity        Severity
	ChannelIDs      []int64
	DedupWindowSec  int
	Enabled         bool
	CreatedAt       time.Time
}

// RuleCondition 是规则条件。
type RuleCondition struct {
	Op          string  `json:"op"`           // > >= < <= == !=
	Value       float64 `json:"value"`
	ForTimes    int     `json:"for_times"`    // 连续满足次数
	ForMinutes  int     `json:"for_minutes"`  // 持续分钟数
}

// EventStatus 是告警事件状态。
type EventStatus string

const (
	EventFiring   EventStatus = "firing"
	EventResolved EventStatus = "resolved"
	EventAcked    EventStatus = "acked"
)

// AlertEvent 是告警事件。
type AlertEvent struct {
	ID           int64
	RuleID       *int64
	TargetType   AlertTargetType
	TargetID     *int64
	TargetName   string
	Severity     Severity
	Status       EventStatus
	Message      string
	Payload      map[string]any
	FirstFiredAt time.Time
	LastFiredAt  time.Time
	ResolvedAt   *time.Time
	AckedAt      *time.Time
	AckedBy      *int64
}

// VisibilityScope 是脱敏范围（PRD 3.6.1）。
type VisibilityScope string

const (
	ScopePublicStatus VisibilityScope = "public_status"
	ScopeAPIUnauth    VisibilityScope = "api_unauth"
	ScopeExport       VisibilityScope = "export"
)

// MaskMode 是掩码方式。
type MaskMode string

const (
	MaskHide    MaskMode = "hide"
	MaskPartial MaskMode = "partial"
	MaskFull    MaskMode = "full"
)

// VisibilityPolicy 是字段可见性策略。
type VisibilityPolicy struct {
	ID       int64
	Scope    VisibilityScope
	Field    string
	Visible  bool
	MaskMode MaskMode
	MaskRule string
}

// AuditLog 是审计日志。写操作必须记录。
type AuditLog struct {
	ID         int64
	UserID     *int64
	Username   string
	Action     string
	TargetType string
	TargetID   string
	Detail     map[string]any
	IP         string
	UserAgent  string
	CreatedAt  time.Time
}

// NodeMetric 是一条时序指标（原始表）。
type NodeMetric struct {
	ID           int64
	NodeID       int64
	CollectedAt  time.Time
	CPUUsage     float64
	CPUCores     []float64
	MemTotal     int64
	MemUsed      int64
	MemAvailable int64
	MemUsage     float64
	SwapTotal    int64
	SwapUsed     int64
	Load1        float64
	Load5        float64
	Load15       float64
	Uptime       int64
	TCPConnCount int
	Disks        []DiskUsage
	NetIO        []NetUsage
	Sensors      []Sensor
}

// DiskUsage 是磁盘占用。
type DiskUsage struct {
	Mount    string  `json:"mount"`
	FSType   string  `json:"fstype"`
	Total    int64   `json:"total"`
	Used     int64   `json:"used"`
	Usage    float64 `json:"usage"`
	ReadBps  int64   `json:"read_bps"`
	WriteBps int64   `json:"write_bps"`
}

// NetUsage 是网络吞吐。
type NetUsage struct {
	Iface   string `json:"iface"`
	RxBps   int64  `json:"rx_bps"`
	TxBps   int64  `json:"tx_bps"`
	RxTotal int64  `json:"rx_total"`
	TxTotal int64  `json:"tx_total"`
}

// Sensor 是温度/风扇传感器。
type Sensor struct {
	Name  string  `json:"name"`
	Kind  string  `json:"kind"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}
