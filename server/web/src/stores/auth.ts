/**
 * 认证 Store
 *
 * 权限判定集中在这里，组件不要自己写 `role === 'admin' || role === 'owner'`。
 * 权限规则散落到各处是越权漏洞的常见来源。
 */

import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api, clearToken, getToken, setToken } from '@/api/client'
import type { Role, User } from '@/api/types'

export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(null)
  const loading = ref(false)
  /** 是否已尝试过加载用户信息。用于避免路由守卫重复请求。 */
  const initialized = ref(false)

  const isLoggedIn = computed(() => user.value !== null)
  const role = computed<Role | null>(() => user.value?.role ?? null)

  const isOwner = computed(() => role.value === 'owner')
  const isAdmin = computed(() => role.value === 'admin' || role.value === 'owner')
  const isViewer = computed(() => role.value === 'viewer')

  async function login(username: string, password: string, totp?: string): Promise<void> {
    const res = await api.post<{ token: string; user: User }>('/api/auth/login', {
      username,
      password,
      totp: totp || undefined,
    })
    setToken(res.token)
    user.value = res.user
    initialized.value = true
  }

  async function logout(): Promise<void> {
    try {
      await api.post('/api/auth/logout')
    } catch {
      // 登出失败不阻塞本地清理——令牌已失效的话服务端可能已不认识它
    }
    clear()
  }

  /**
   * 加载当前用户。
   * 应用启动时调用一次：令牌可能还在（刷新页面），但用户信息需要重新拿。
   */
  async function loadUser(): Promise<void> {
    if (!getToken()) {
      clear()
      return
    }
    loading.value = true
    try {
      user.value = await api.get<User>('/api/auth/me')
    } catch {
      clear()
    } finally {
      loading.value = false
      initialized.value = true
    }
  }

  function clear(): void {
    clearToken()
    user.value = null
    initialized.value = true
  }

  /** 能否访问某路由。集中判定，避免各处遗漏。 */
  function canAccess(required: 'viewer' | 'admin' | 'owner'): boolean {
    switch (required) {
      case 'owner':
        return isOwner.value
      case 'admin':
        return isAdmin.value
      default:
        return isLoggedIn.value
    }
  }

  return {
    user,
    loading,
    initialized,
    isLoggedIn,
    role,
    isOwner,
    isAdmin,
    isViewer,
    login,
    logout,
    loadUser,
    clear,
    canAccess,
  }
})
