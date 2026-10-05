/**
 * 主题 Store
 *
 * 深浅色双主题，跟随系统 + 手动切换。
 * 关键点：**切换主题不能丢失当前页面状态**——
 * 只改 data-theme 属性，组件不重建，因此路由与数据都保持不变。
 */

import { defineStore } from 'pinia'
import { ref, watch } from 'vue'

export type ThemeMode = 'light' | 'dark' | 'system'

const STORAGE_KEY = 'probeone_theme'

/** 读系统偏好 */
function systemPrefersDark(): boolean {
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false
}

export const useThemeStore = defineStore('theme', () => {
  const mode = ref<ThemeMode>('system')
  const resolved = ref<'light' | 'dark'>('light')

  function apply(): void {
    const dark = mode.value === 'system' ? systemPrefersDark() : mode.value === 'dark'
    resolved.value = dark ? 'dark' : 'light'
    document.documentElement.setAttribute('data-theme', resolved.value)
  }

  function setMode(next: ThemeMode): void {
    mode.value = next
    localStorage.setItem(STORAGE_KEY, next)
    apply()
  }

  /** 在浅色/深色间切换（跟随系统时按当前解析结果取反） */
  function toggle(): void {
    setMode(resolved.value === 'dark' ? 'light' : 'dark')
  }

  function init(): void {
    const saved = localStorage.getItem(STORAGE_KEY)
    if (saved === 'light' || saved === 'dark' || saved === 'system') {
      mode.value = saved
    }
    apply()

    // 跟随系统时，用户在系统层面切换深浅色要能实时反映
    window.matchMedia?.('(prefers-color-scheme: dark)').addEventListener('change', () => {
      if (mode.value === 'system') apply()
    })
  }

  // 组件卸载后仍需响应模式变化
  watch(mode, apply)

  return { mode, resolved, setMode, toggle, init }
})
