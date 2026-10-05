/** 格式化工具。集中一处，避免各页面各写一套导致口径不一。 */

import dayjs from 'dayjs'
import relativeTime from 'dayjs/plugin/relativeTime'
import 'dayjs/locale/zh-cn'

dayjs.extend(relativeTime)
dayjs.locale('zh-cn')

/** 字节 → 人类可读。1024 进制。 */
export function formatBytes(bytes: number | undefined | null, digits = 1): string {
  if (bytes === undefined || bytes === null || Number.isNaN(bytes)) return '-'
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const k = 1024
  const i = Math.min(Math.floor(Math.log(Math.abs(bytes)) / Math.log(k)), units.length - 1)
  return `${(bytes / Math.pow(k, i)).toFixed(i === 0 ? 0 : digits)} ${units[i]}`
}

/** 比特率 → 人类可读。监控场景习惯用 bit 而非 byte。 */
export function formatBits(bps: number | undefined | null, digits = 1): string {
  if (bps === undefined || bps === null || Number.isNaN(bps) || bps === 0) return '0 bps'
  const units = ['bps', 'Kbps', 'Mbps', 'Gbps', 'Tbps']
  const k = 1000
  const i = Math.min(Math.floor(Math.log(bps) / Math.log(k)), units.length - 1)
  return `${(bps / Math.pow(k, i)).toFixed(i === 0 ? 0 : digits)} ${units[i]}`
}

/** 毫秒 → 人类可读 */
export function formatMs(ms: number | undefined | null): string {
  if (ms === undefined || ms === null || Number.isNaN(ms)) return '-'
  if (ms < 1000) return `${Math.round(ms)}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)}s`
  return `${Math.floor(ms / 60_000)}m${Math.round((ms % 60_000) / 1000)}s`
}

/** 秒 → 运行时长 */
export function formatUptime(seconds: number | undefined | null): string {
  if (!seconds || seconds < 0) return '-'
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d > 0) return `${d} 天 ${h} 小时`
  if (h > 0) return `${h} 小时 ${m} 分`
  return `${m} 分钟`
}

/** 百分比。null/样本不足时返回占位符而非 0。 */
export function formatPercent(v: number | undefined | null, digits = 1): string {
  if (v === undefined || v === null || Number.isNaN(v)) return '-'
  return `${v.toFixed(digits)}%`
}

/** 相对时间。"3 分钟前" */
export function formatRelative(input: string | number | undefined | null): string {
  if (!input) return '-'
  const d = dayjs(input)
  if (!d.isValid()) return '-'
  return d.fromNow()
}

/** 绝对时间 */
export function formatDateTime(input: string | number | undefined | null): string {
  if (!input) return '-'
  const d = dayjs(input)
  if (!d.isValid()) return '-'
  return d.format('YYYY-MM-DD HH:mm:ss')
}

/** 只到分钟的绝对时间。列表里用这个，秒级精度是噪音。 */
export function formatDateTimeShort(input: string | number | undefined | null): string {
  if (!input) return '-'
  const d = dayjs(input)
  if (!d.isValid()) return '-'
  return d.format('MM-DD HH:mm')
}

/** 时间戳 → HH:mm:ss，图表 X 轴用 */
export function formatClock(ts: number): string {
  return dayjs(ts * 1000).format('HH:mm:ss')
}

/** 时间戳 → MM-DD HH:mm，跨天图表用 */
export function formatDateClock(ts: number): string {
  return dayjs(ts * 1000).format('MM-DD HH:mm')
}

/** 掩码敏感串：只留首尾各2 位。用于公网 IP 等的模糊展示。 */
export function maskSecret(s: string | undefined | null): string {
  if (!s) return '-'
  if (s.length <= 8) return '***'
  return `${s.slice(0, 2)}***${s.slice(-2)}`
}

/** 掩码邮箱 */
export function maskEmail(s: string | undefined | null): string {
  if (!s) return '-'
  const at = s.indexOf('@')
  if (at <= 0) return '***'
  const name = s.slice(0, at)
  const domain = s.slice(at)
  return name.length <= 2 ? `**${domain}` : `${name.slice(0, 2)}***${domain}`
}

/** 掩码 URL：去掉路径与查询，保留协议+域名。避免把 token 打到日志或页面上。 */
export function maskUrl(s: string | undefined | null): string {
  if (!s) return '-'
  try {
    const u = new URL(s)
    return `${u.protocol}//${u.host}`
  } catch {
    return s
  }
}

/** 告警级别 → 中文 */
export function severityText(s: string): string {
  switch (s) {
    case 'critical':
      return '严重'
    case 'warning':
      return '警告'
    default:
      return '提示'
  }
}

/** 失败原因 → 中文。用户看到枚举值会懵。 */
export function failReasonText(r: string): string {
  const map: Record<string, string> = {
    ok: '正常',
    timeout: '请求超时',
    conn_refused: '连接被拒绝',
    dns_error: 'DNS 解析失败',
    tls_error: 'TLS 握手失败',
    status_mismatch: '状态码不符',
    content_mismatch: '内容不匹配',
    slow: '响应过慢',
    unknown: '未知错误',
    blocked: '被安全策略拦截',
    bad_request: '配置有误',
    tls_expired: '证书已过期',
    cert_expiring: '证书即将过期',
  }
  return map[r] ?? r
}

/** 监控类型 → 中文 */
export function monitorTypeText(t: string): string {
  const map: Record<string, string> = {
    http: 'HTTP',
    tcp: 'TCP 端口',
    ping: 'Ping',
    dns: 'DNS',
    ssl: 'SSL 证书',
  }
  return map[t] ?? t
}
