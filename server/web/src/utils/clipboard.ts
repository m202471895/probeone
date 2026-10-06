/**
 * 剪贴板工具。
 *
 * 为什么不能直接用 navigator.clipboard：
 * 该API 只在**安全上下文**下可用——HTTPS，或 localhost。
 * 部署在 http://43.250.175.188:8000 这类明文 HTTP 的非本机地址上，
 * navigator.clipboard 是 undefined，调用直接抛错。
 * 用户看到的现象就是"点了复制没反应"。
 *
 * 降级方案：execCommand('copy') + 临时 textarea。
 * 虽然已被标记为废弃，但支持面广且在非安全上下文里是唯一可行的办法。
 * 保留是因为"能用比好看重要"——生产环境用明文 HTTP 的场景很常见。
 */

/** 复制结果，供调用方给出反馈。 */
export type CopyResult = 'ok' | 'fallback' | 'failed'

/**
 * 复制文本到剪贴板。
 *
 * 返回 'fallback' 表示用了降级方案（成功但走了老API），
 * 调用方可以据此提示用户，不必要求。
 */
export async function copyToClipboard(text: string): Promise<CopyResult> {
  const value = text ?? ''

  // 路径 1：标准 API（需要安全上下文）
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(value)
      return 'ok'
    } catch {
      // 有API 但被拒（权限、用户手势缺失等），继续走降级
    }
  }

  // 路径 2：降级（明文 HTTP 环境下唯一可行的办法）
  return legacyCopy(value)
}

/**
 * 传统复制方式。
 *
 * 细节说明：
 * - textarea 必须挂到文档上，display:none 的元素无法被选中
 * - 用 readonly 而非 disabled，disabled 元素不能被 select
 * - 移到视口外而不是 left:-9999，避免页面横向滚动条出现
 * - 复制后立刻移除，保留会污染用户的复制历史
 */
function legacyCopy(value: string): CopyResult {
  if (typeof document === 'undefined') return 'failed'

  const ta = document.createElement('textarea')
  ta.value = value
  ta.setAttribute('readonly', '')
  ta.style.position = 'fixed'
  ta.style.top = '0'
  ta.style.left = '-9999px'
  ta.style.opacity = '0'

  document.body.appendChild(ta)

  let ok = false
  try {
    ta.focus()
    ta.select()
    // setSelectionRange 在部分移动端浏览器上是 select 的必要前置
    ta.setSelectionRange(0, ta.value.length)
    ok = document.execCommand('copy')
  } catch {
    ok = false
  } finally {
    document.body.removeChild(ta)
  }

  return ok ? 'fallback' : 'failed'
}
