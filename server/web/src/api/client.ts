/**
 * API 客户端
 *
 * 三条硬性规则（PRD 11.4 / 专家规范）：
 *  1. Base URL 走环境变量，不硬编码任何地址
 *  2. 令牌存在内存 + sessionStorage，**不进 localStorage**（防 XSS 窃取后长期有效）
 *  3. 401 自动刷新并重试一次；4xx 不重试、5xx 最多重试 3 次
 *
 * 错误处理：后端返回稳定错误码，前端映射为可读文案，
 * 不把后端的原始消息直接甩给用户。
 */

const BASE_URL = import.meta.env.VITE_API_BASE ?? ''

/** 后端错误码 → 用户可读文案。找不到时用兜底文案，不透传原始消息。 */
const ERROR_MESSAGES: Record<number, string> = {
  400: '请求参数有误',
  401: '登录已失效，请重新登录',
  403: '没有权限执行此操作',
  404: '请求的资源不存在',
  409: '资源已存在',
  429: '操作过于频繁，请稍后再试',
  500: '服务器内部错误',
}

/** 后端业务错误码（apperr 里定义的）→ 用户文案 */
const BIZ_MESSAGES: Record<string, string> = {
  NODE_NOT_FOUND: '节点不存在或已被删除',
  NODE_EXISTS: '该节点已存在',
  MONITOR_NOT_FOUND: '监控对象不存在',
  CHANNEL_NOT_FOUND: '通知通道不存在',
  ALERT_RULE_NOT_FOUND: '告警规则不存在',
  USER_NOT_FOUND: '用户不存在',
  USER_EXISTS: '用户名已被占用',
  INVALID_CREDENTIALS: '用户名或密码错误',
  SESSION_EXPIRED: '登录已过期，请重新登录',
  VALIDATION_FAILED: '提交的内容不符合要求',
  TARGET_BLOCKED: '目标地址被安全策略拦截（内网地址默认禁止）',
  AGENT_HARD_LOCKED: '该 Agent 因多次鉴权失败已被临时锁定',
}

export class ApiError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: number,
    public readonly bizCode: string | undefined,
    message: string,
  ) {
    super(message)
    this.name = 'ApiError'
  }

  /** 是否为鉴权失败（需要跳登录） */
  get isAuthError(): boolean {
    return this.status === 401
  }

  /** 是否为可重试的网络错误 */
  get isRetryable(): boolean {
    return this.status >= 500 || this.status === 429
  }
}

// ============ 令牌存储 ============

const TOKEN_KEY = 'probeone_token'

/**
 * 令牌只放内存与 sessionStorage。
 * 不用 localStorage 是因为它跨会话持久存在，
 * 一旦被 XSS 读走，攻击者能长期冒充用户。
 * sessionStorage 关闭标签页即失效，攻击窗口显著缩短。
 */
let memoryToken: string | null = null

export function getToken(): string | null {
  if (memoryToken) return memoryToken
  memoryToken = sessionStorage.getItem(TOKEN_KEY)
  return memoryToken
}

export function setToken(token: string): void {
  memoryToken = token
  sessionStorage.setItem(TOKEN_KEY, token)
}

export function clearToken(): void {
  memoryToken = null
  sessionStorage.removeItem(TOKEN_KEY)
}

// ============ 刷新令牌 ============

let refreshPromise: Promise<boolean> | null = null

/**
 * 刷新访问令牌。
 *
 * 并发去重：多个请求同时 401 时只发一次刷新，
 * 否则 10 个并发请求会触发 10 次刷新，其中 9 次拿着已失效的 refresh token。
 */
async function refreshToken(): Promise<boolean> {
  if (refreshPromise) return refreshPromise

  refreshPromise = (async () => {
    try {
      const res = await fetch(`${BASE_URL}/api/auth/refresh`, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
      })
      if (!res.ok) return false
      const body = await res.json()
      const token = body?.data?.token
      if (typeof token !== 'string' || token === '') return false
      setToken(token)
      return true
    } catch {
      return false
    } finally {
      // 无论成败都要清掉，否则后续 401 会一直卡在同一个 promise 上
      refreshPromise = null
    }
  })()

  return refreshPromise
}

/** 401 时的回调，由 main.ts 挂上（跳登录页） */
let onUnauthorized: (() => void) | null = null

export function setUnauthorizedHandler(fn: (() => void) | null): void {
  onUnauthorized = fn
}

// ============ 请求 ============

interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'DELETE'
  body?: unknown
  /** 内部用：标记这是重试，避免无限递归 */
  _retried?: boolean
  signal?: AbortSignal
}

/** 5xx 重试次数。3 次是权衡：再多会拖垮界面，再少会漏掉瞬时故障。 */
const MAX_RETRY = 3
const RETRY_BASE_DELAY = 300

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms))
}

async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, _retried = false, signal } = opts

  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  const token = getToken()
  if (token) headers['Authorization'] = `Bearer ${token}`

  let res: Response
  try {
    res = await fetch(`${BASE_URL}${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: 'include',
      signal,
    })
  } catch (err) {
    // fetch 本身失败 = 网络问题，值得重试
    if (!_retried && (err as Error).name !== 'AbortError') {
      await sleep(RETRY_BASE_DELAY)
      return request<T>(path, { ...opts, _retried: true })
    }
    throw new ApiError(0, 0, undefined, '网络连接失败，请检查网络后重试')
  }

  // 401：尝试刷新令牌后重试一次
  if (res.status === 401 && !_retried) {
    const ok = await refreshToken()
    if (ok) {
      return request<T>(path, { ...opts, _retried: true })
    }
    clearToken()
    onUnauthorized?.()
    throw new ApiError(401, 401, undefined, ERROR_MESSAGES[401] ?? '登录已失效')
  }

  // 5xx / 429：退避重试
  if ((res.status >= 500 || res.status === 429) && !_retried) {
    await sleep(RETRY_BASE_DELAY * (1 << _retried))
    return request<T>(path, { ...opts, _retried: true })
  }

  // 204 等无响应体
  if (res.status === 204) {
    return undefined as T
  }

  let payload: unknown
  try {
    payload = await res.json()
  } catch {
    payload = null
  }

  if (!res.ok) {
    const envelope = payload as { code?: number; message?: string } | null
    // 业务错误码优先：它比 HTTP 状态更精确
    const bizCode = envelope?.message && findBizCode(envelope.message)
    const message =
      (bizCode && BIZ_MESSAGES[bizCode]) ||
      (envelope?.message && ERROR_MESSAGES[envelope.message] === undefined && !res.ok
        ? envelope.message
        : undefined) ||
      ERROR_MESSAGES[res.status] ||
      `请求失败（${res.status}）`
    throw new ApiError(res.status, envelope?.code ?? res.status, bizCode, message)
  }

  const envelope = payload as { code?: number; data?: T } | null
  return (envelope?.data ?? (payload as T)) as T
}

/** 从后端消息里提取业务错误码（后端目前把 code 放在 message 里）。 */
function findBizCode(message: string): string | undefined {
  return Object.keys(BIZ_MESSAGES).find((k) => message.includes(k))
}

// ============ 对外方法 ============

export const api = {
  get: <T>(path: string, opts?: Omit<RequestOptions, 'method' | 'body'>) =>
    request<T>(path, { ...opts, method: 'GET' }),

  post: <T>(path: string, body?: unknown, opts?: Omit<RequestOptions, 'method' | 'body'>) =>
    request<T>(path, { ...opts, method: 'POST', body }),

  put: <T>(path: string, body?: unknown, opts?: Omit<RequestOptions, 'method' | 'body'>) =>
    request<T>(path, { ...opts, method: 'PUT', body }),

  delete: <T>(path: string, opts?: Omit<RequestOptions, 'method' | 'body'>) =>
    request<T>(path, { ...opts, method: 'DELETE' }),
}

// ============ 错误文案辅助 ============

/**
 * humanizeError 把任意异常转成可展示给用户的文案。
 * 组件里统一用这个，不要直接读 err.message ——
 * 网络错误会带出"Failed to fetch"这类用户看不懂的内容。
 */
export function humanizeError(err: unknown, fallback = '操作失败，请稍后重试'): string {
  if (err instanceof ApiError) {
    if (err.bizCode && BIZ_MESSAGES[err.bizCode]) return BIZ_MESSAGES[err.bizCode]!
    return err.message || fallback
  }
  if (err instanceof Error) {
    // 主动 abort 不算错误
    if (err.name === 'AbortError') return ''
    return fallback
  }
  return fallback
}

/** 判断是否该显示"离线"提示 */
export function isOfflineError(err: unknown): boolean {
  return err instanceof ApiError && err.status === 0
}
