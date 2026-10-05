/**
 * 与后端对齐的类型定义。
 *
 * 字段名必须与 Go侧的 model / DTO 一致（camelCase 由后端 JSON tag 决定）。
 * 敏感字段（agent_secret / internal_ip）**故意不出现在这里**——
 * 类型层面就不给它们机会被序列化出去（PRD 3.6.4 第2 条）。
 */

// ============ 通用 ============

export type Role = 'owner' | 'admin' | 'viewer'

export interface User {
  id: number
  username: string
  email?: string
  role: Role
  status: string
  created_at: string
}

export interface Paginated<T> {
  items: T[]
  total: number
}

export interface ListQuery {
  page?: number
  size?: number
}

// ============ 节点 ============

export type NodeStatus = 'pending' | 'online' | 'offline'

export interface DiskInfo {
  /** 公开响应中可能没有 device（被脱敏层剔除） */
  device?: string
  mount: string
  fstype?: string
  total: number
  used?: number
  usage?: number
}

export interface Node {
  id: number
  uid: string
  name: string
  group_id?: number | null
  status: NodeStatus
  remark?: string
  is_public: boolean
  created_at: string
  updated_at?: string

  /* A 类：身份信息 */
  hostname?: string
  os_type?: string
  os_version?: string
  arch?: string
  agent_version?: string

  /* B 类：硬件规格（公开可见） */
  cpu_model?: string
  cpu_cores?: number
  mem_total?: number
  disk_info?: DiskInfo[]
  hardware_fp?: string
  hardware_changed_at?: string | null

  /* C 类：运行时环境。public_ip 仅登录后可见 */
  boot_time?: string | null
  public_ip?: string
  geo_country?: string
  geo_city?: string
  last_seen_at?: string | null
  last_report_at?: string | null
}

/** 新建节点的响应：secret 只在这里出现一次，之后不可再取。 */
export interface NodeCreated {
  id: number
  uid: string
  /** 明文密钥，仅此一次。UI 必须明确提示用户立即保存。 */
  secret: string
  install_command: string
}

export interface NodeGroup {
  id: number
  name: string
  sort: number
}

// ============ 指标 ============

export interface DiskUsage {
  mount: string
  fstype?: string
  total: number
  used: number
  usage: number
  read_bps?: number
  write_bps?: number
}

export interface NetUsage {
  iface: string
  rx_bps: number
  tx_bps: number
  rx_total: number
  tx_total: number
}

export interface Sensor {
  name: string
  kind: string
  value: number
  unit: string
}

/** 图表用的时序点 */
export interface MetricPoint {
  t: number
  cpu?: number
  mem?: number
  net_rx?: number
  net_tx?: number
  load?: number
  disk_max?: number
}

export interface MetricSnapshot {
  collected_at: string
  cpu_usage: number
  mem_total: number
  mem_used: number
  mem_usage: number
  swap_total?: number
  swap_used?: number
  load1?: number
  load5?: number
  load15?: number
  uptime: number
  disks: DiskUsage[]
  net_io: NetUsage[]
  sensors?: Sensor[]
}

/** 节点卡片/列表里附带的最新值 */
export interface NodeWithLatest extends Node {
  latest?: MetricSnapshot
}

// ============ 网站监控 ============

export type MonitorType = 'http' | 'tcp' | 'ping' | 'dns' | 'ssl'
export type MonitorStatus = 'up' | 'down' | 'paused' | 'pending'

export type FailReason =
  | 'ok'
  | 'timeout'
  | 'conn_refused'
  | 'dns_error'
  | 'tls_error'
  | 'status_mismatch'
  | 'content_mismatch'
  | 'slow'
  | 'unknown'
  | 'blocked'
  | 'bad_request'
  | 'tls_expired'
  | 'cert_expiring'

export interface MonitorConfig {
  method?: string
  headers?: Record<string, string>
  expect_status?: number[]
  expect_keywords?: string[]
  port?: number
  record?: string
  resolver?: string
  expect_ips?: string[]
  max_latency_ms?: number
  warn_days_left?: number
}

export interface Monitor {
  id: number
  name: string
  type: MonitorType
  target: string
  config: MonitorConfig
  interval_sec: number
  timeout_sec: number
  group_id?: number | null
  status: MonitorStatus
  is_public: boolean
  sort: number
  last_checked_at?: string | null
  /** null 表示样本不足，此时前端应显示"样本不足"而非百分比 */
  uptime_30d?: number | null
  avg_latency_ms?: number | null
  created_at: string
}

export interface MonitorResult {
  id: number
  monitor_id: number
  checked_at: string
  ok: boolean
  reason: FailReason
  status_code?: number | null
  latency_ms?: number | null
  dns_ms?: number | null
  tcp_ms?: number | null
  tls_ms?: number | null
  ttfb_ms?: number | null
  error_detail?: string
}

export interface MonitorStats {
  total: number
  up: number
  latency_p50: number
  latency_p95: number
  latency_p99: number
  uptime_percent: number
  sample_insufficient: boolean
}

export interface SSLCertificate {
  subject?: string
  issuer?: string
  not_after?: string | null
  days_left?: number | null
  fingerprint?: string
  last_checked_at?: string | null
}

// ============ 告警 ============

export type Severity = 'info' | 'warning' | 'critical'
export type AlertTargetType = 'node' | 'monitor' | 'cert'
export type EventStatus = 'firing' | 'resolved' | 'acked'

export type ChannelType =
  | 'webhook'
  | 'email'
  | 'wecom'
  | 'dingtalk'
  | 'feishu'
  | 'telegram'
  | 'bark'

export interface AlertChannel {
  id: number
  name: string
  type: ChannelType
  /** 含凭据，仅 Owner 可见。UI 展示时需掩码。 */
  config: Record<string, unknown>
  enabled: boolean
  created_at: string
}

export interface RuleCondition {
  op: '>' | '>=' | '<' | '<=' | '==' | '!='
  value: number
  for_times: number
  for_minutes: number
}

export interface AlertRule {
  id: number
  name: string
  target_type: AlertTargetType
  target_id?: number | null
  metric?: string
  condition: RuleCondition
  severity: Severity
  channel_ids: number[]
  dedup_window_sec: number
  enabled: boolean
  created_at: string
}

export interface AlertEvent {
  id: number
  rule_id?: number | null
  target_type: AlertTargetType
  target_id?: number | null
  target_name: string
  severity: Severity
  status: EventStatus
  message?: string
  payload?: Record<string, unknown>
  notified?: boolean
  first_fired_at: string
  last_fired_at: string
  resolved_at?: string | null
  acked_at?: string | null
}

// ============ 审计与系统 ============

export interface AuditLog {
  id: number
  username?: string
  action: string
  target_type?: string
  target_id?: string
  detail?: Record<string, unknown>
  ip?: string
  created_at: string
}

export interface SystemVersion {
  version: string
  commit?: string
  build_time?: string
}

// ============ WebSocket 推送 ============

export type WsMessage =
  | { type: 'metrics'; node_id: number; data: MetricSnapshot }
  | { type: 'node_status'; node_id: number; status: NodeStatus }
  | { type: 'monitor_status'; monitor_id: number; status: MonitorStatus; latency_ms?: number }
  | { type: 'alert'; data: AlertEvent }
  | { type: 'pong' }
  | { type: 'error'; message: string }
