/**
 * WebSocket 实时推送
 *
 * 心跳与重连（PRD 11.4）：
 *  - 每 25s 发一次 ping，60s 收不到任何消息就判定断线
 *  - 指数退避重连：1s → 2s → 4s → 8s → 15s 封顶，带 ±20% 抖动
 *  - 抖动是必须的：服务端重启时所有浏览器会同时重连，
 *    无抖动的重连会把刚恢复的服务端再打垮一次
 *
 * 断线期间不缓存消息——监控场景下"过期的实时数据"没有价值，
 * 重连后拉一次全量比补零散消息更可靠。
 */

import { getToken } from './client'
import type { WsMessage } from './types'

type Handler = (msg: WsMessage) => void
type StatusHandler = (connected: boolean) => void

const PING_INTERVAL = 25_000
const PONG_TIMEOUT = 60_000
const MAX_BACKOFF = 15_000

export class RealtimeClient {
  private ws: WebSocket | null = null
  private handlers = new Set<Handler>()
  private statusHandlers = new Set<StatusHandler>()
  private pingTimer: number | null = null
  private pongTimer: number | null = null
  private retry = 0
  private reconnectTimer: number | null = null
  private manuallyClosed = false

  connect(): void {
    if (this.ws && (this.ws.readyState === WebSocket.OPEN || this.ws.readyState === WebSocket.CONNECTING)) {
      return
    }
    const token = getToken()
    if (!token) return // 未登录不连

    this.manuallyClosed = false

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    // 令牌走 query 参数：WebSocket API 无法自定义 header
    const url = `${proto}//${location.host}/api/v1/ws?token=${encodeURIComponent(token)}`

    try {
      this.ws = new WebSocket(url)
    } catch {
      this.scheduleReconnect()
      return
    }

    this.ws.onopen = () => {
      this.retry = 0
      this.emitStatus(true)
      this.startPing()
      this.schedulePongTimeout()
    }

    this.ws.onmessage = (evt) => {
      this.refreshPongTimeout()
      let msg: WsMessage
      try {
        msg = JSON.parse(evt.data as string) as WsMessage
      } catch {
        return // 忽略无法解析的帧，不影响后续处理
      }
      if (msg.type === 'pong') return
      this.handlers.forEach((h) => {
        try {
          h(msg)
        } catch (err) {
          // 单个处理器的异常不能影响其他订阅者
          console.error('[ws] 消息处理失败', err)
        }
      })
    }

    this.ws.onclose = () => {
      this.cleanup()
      this.emitStatus(false)
      if (!this.manuallyClosed) this.scheduleReconnect()
    }

    this.ws.onerror = () => {
      // onerror 之后必定触发 onclose，重连逻辑统一放在那里
      this.ws?.close()
    }
  }

  disconnect(): void {
    this.manuallyClosed = true
    this.cleanup()
    this.ws?.close()
    this.ws = null
    this.emitStatus(false)
  }

  subscribe(handler: Handler): () => void {
    this.handlers.add(handler)
    return () => this.handlers.delete(handler)
  }

  onStatusChange(handler: StatusHandler): () => void {
    this.statusHandlers.add(handler)
    return () => this.statusHandlers.delete(handler)
  }

  get connected(): boolean {
    return this.ws?.readyState === WebSocket.OPEN
  }

  // ---------- 内部 ----------

  private emitStatus(connected: boolean): void {
    this.statusHandlers.forEach((h) => h(connected))
  }

  private startPing(): void {
    this.stopPing()
    this.pingTimer = window.setInterval(() => {
      this.send({ type: 'ping' })
    }, PING_INTERVAL)
  }

  private stopPing(): void {
    if (this.pingTimer !== null) {
      clearInterval(this.pingTimer)
      this.pingTimer = null
    }
  }

  /**
   * 看门狗：超时未收到任何消息就主动断开并重连。
   * 有些代理会"安静地"吞掉连接（不报错但不再传输），
   * 没有看门狗的话界面会一直显示"已连接"但数据永远不动。
   */
  private schedulePongTimeout(): void {
    if (this.pongTimer !== null) clearTimeout(this.pongTimer)
    this.pongTimer = window.setTimeout(() => {
      this.ws?.close()
    }, PONG_TIMEOUT)
  }

  private refreshPongTimeout(): void {
    this.schedulePongTimeout()
  }

  private send(payload: unknown): void {
    if (this.ws?.readyState !== WebSocket.OPEN) return
    try {
      this.ws.send(JSON.stringify(payload))
    } catch {
      // 发送失败会紧接着触发 onclose，交给那里处理
    }
  }

  private scheduleReconnect(): void {
    if (this.reconnectTimer !== null) clearTimeout(this.reconnectTimer)

    const base = Math.min(1000 * Math.pow(2, this.retry), MAX_BACKOFF)
    // ±20% 抖动，打散惊群
    const delay = base * (0.8 + Math.random() * 0.4)
    this.retry++

    this.reconnectTimer = window.setTimeout(() => this.connect(), delay)
  }

  private cleanup(): void {
    this.stopPing()
    if (this.pongTimer !== null) {
      clearTimeout(this.pongTimer)
      this.pongTimer = null
    }
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
  }
}

/** 全局单例。多个组件共享一条连接比各自建连接省资源得多。 */
export const realtime = new RealtimeClient()
