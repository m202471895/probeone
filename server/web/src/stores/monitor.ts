/**
 * 网站监控 Store
 */

import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api } from '@/api/client'
import type { Monitor, MonitorStats, MonitorStatus, MonitorType } from '@/api/types'

export type MonitorTypeFilter = MonitorType | 'all'
export type MonitorStatusFilter = MonitorStatus | 'all'

export const useMonitorStore = defineStore('monitors', () => {
  const monitors = ref<Monitor[]>([])
  const total = ref(0)
  const loading = ref(false)
  const error = ref('')

  const typeFilter = ref<MonitorTypeFilter>('all')
  const statusFilter = ref<MonitorStatusFilter>('all')
  const keyword = ref('')
  const page = ref(1)
  const size = ref(50)

  const upCount = computed(() => monitors.value.filter((m) => m.status === 'up').length)
  const downCount = computed(() => monitors.value.filter((m) => m.status === 'down').length)
  /** 平均可用率。样本不足的监控不参与平均——否则会拉高整体数字。 */
  const avgUptime = computed(() => {
    const valid = monitors.value.filter(
      (m) => m.uptime_30d !== null && m.uptime_30d !== undefined,
    )
    if (valid.length === 0) return null
    return valid.reduce((s, m) => s + (m.uptime_30d ?? 0), 0) / valid.length
  })

  function setFilter(f: { type?: MonitorTypeFilter; status?: MonitorStatusFilter; kw?: string }): void {
    if (f.type !== undefined) typeFilter.value = f.type
    if (f.status !== undefined) statusFilter.value = f.status
    if (f.kw !== undefined) {
      keyword.value = f.kw
      page.value = 1
    }
  }

  async function fetch(append = false): Promise<void> {
    loading.value = true
    error.value = ''
    try {
      const params = new URLSearchParams()
      if (typeFilter.value !== 'all') params.set('type', typeFilter.value)
      if (statusFilter.value !== 'all') params.set('status', statusFilter.value)
      if (keyword.value) params.set('q', keyword.value)
      params.set('page', String(page.value))
      params.set('size', String(size.value))

      const res = await api.get<{ items: Monitor[]; total: number }>(`/api/monitors?${params}`)
      total.value = res.total
      monitors.value = append ? [...monitors.value, ...res.items] : res.items
    } catch (err) {
      error.value = (err as Error).message || '监控加载失败'
    } finally {
      loading.value = false
    }
  }

  async function fetchOne(id: number): Promise<Monitor> {
    return api.get<Monitor>(`/api/monitors/${id}`)
  }

  async function fetchStats(id: number): Promise<MonitorStats> {
    return api.get<MonitorStats>(`/api/monitors/${id}/stats`)
  }

  async function create(payload: Partial<Monitor>): Promise<Monitor> {
    const created = await api.post<Monitor>('/api/monitors', payload)
    monitors.value = [created, ...monitors.value]
    total.value++
    return created
  }

  async function update(id: number, payload: Partial<Monitor>): Promise<void> {
    const updated = await api.put<Monitor>(`/api/monitors/${id}`, payload)
    const idx = monitors.value.findIndex((m) => m.id === id)
    if (idx >= 0) monitors.value.splice(idx, 1, updated)
  }

  async function remove(id: number): Promise<void> {
    await api.delete(`/api/monitors/${id}`)
    monitors.value = monitors.value.filter((m) => m.id !== id)
    total.value = Math.max(0, total.value - 1)
  }

  async function checkNow(id: number): Promise<void> {
    await api.post(`/api/monitors/${id}/check`)
  }

  function mergeStatus(id: number, status: MonitorStatus, latencyMs?: number): void {
    const idx = monitors.value.findIndex((m) => m.id === id)
    if (idx < 0) return
    const cur = monitors.value[idx]
    if (!cur) return
    const copy = [...monitors.value]
    copy[idx] = { ...cur, status, avg_latency_ms: latencyMs ?? cur.avg_latency_ms }
    monitors.value = copy
  }

  return {
    monitors,
    total,
    loading,
    error,
    typeFilter,
    statusFilter,
    keyword,
    page,
    size,
    upCount,
    downCount,
    avgUptime,
    setFilter,
    fetch,
    fetchOne,
    fetchStats,
    create,
    update,
    remove,
    checkNow,
    mergeStatus,
  }
})
