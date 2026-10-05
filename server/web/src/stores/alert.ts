/**
 * 告警 Store
 */

import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api } from '@/api/client'
import type { AlertChannel, AlertEvent, AlertRule, EventStatus, Severity } from '@/api/types'

export type SeverityFilter = Severity | 'all'
export type EventStatusFilter = EventStatus | 'all'

export const useAlertStore = defineStore('alerts', () => {
  const events = ref<AlertEvent[]>([])
  const rules = ref<AlertRule[]>([])
  const channels = ref<AlertChannel[]>([])
  const total = ref(0)
  const loading = ref(false)
  const error = ref('')

  const statusFilter = ref<EventStatusFilter>('firing')
  const severityFilter = ref<SeverityFilter>('all')
  const page = ref(1)
  const size = ref(50)

  /** 未处理告警数。侧边栏红点用它。 */
  const unackedCount = computed(
    () => events.value.filter((e) => e.status === 'firing').length,
  )
  const criticalCount = computed(
    () =>
      events.value.filter(
        (e) => e.status === 'firing' && e.severity === 'critical',
      ).length,
  )

  function setFilter(f: { status?: EventStatusFilter; severity?: SeverityFilter }): void {
    if (f.status !== undefined) statusFilter.value = f.status
    if (f.severity !== undefined) severityFilter.value = f.severity
    page.value = 1
  }

  async function fetchEvents(append = false): Promise<void> {
    loading.value = true
    error.value = ''
    try {
      const params = new URLSearchParams()
      if (statusFilter.value !== 'all') params.set('status', statusFilter.value)
      if (severityFilter.value !== 'all') params.set('severity', severityFilter.value)
      params.set('page', String(page.value))
      params.set('size', String(size.value))

      const res = await api.get<{ items: AlertEvent[]; total: number }>(
        `/api/alerts?${params}`,
      )
      total.value = res.total
      events.value = append ? [...events.value, ...res.items] : res.items
    } catch (err) {
      error.value = (err as Error).message || '告警加载失败'
    } finally {
      loading.value = false
    }
  }

  async function fetchRules(): Promise<void> {
    try {
      rules.value = await api.get<AlertRule[]>('/api/alert-rules')
    } catch {
      rules.value = []
    }
  }

  async function fetchChannels(): Promise<void> {
    try {
      channels.value = await api.get<AlertChannel[]>('/api/channels')
    } catch {
      channels.value = []
    }
  }

  async function ack(id: number): Promise<void> {
    await api.post(`/api/alerts/${id}/ack`)
    const idx = events.value.findIndex((e) => e.id === id)
    if (idx >= 0) {
      const copy = [...events.value]
      copy[idx] = { ...copy[idx]!, status: 'acked' }
      events.value = copy
    }
  }

  async function testChannel(id: number): Promise<void> {
    await api.post(`/api/channels/${id}/test`)
  }

  function mergeEvent(ev: AlertEvent): void {
    // 新告警插到最前，同 id 则替换
    const idx = events.value.findIndex((e) => e.id === ev.id)
    if (idx >= 0) {
      const copy = [...events.value]
      copy[idx] = ev
      events.value = copy
    } else {
      events.value = [ev, ...events.value]
      total.value++
    }
  }

  return {
    events,
    rules,
    channels,
    total,
    loading,
    error,
    statusFilter,
    severityFilter,
    page,
    size,
    unackedCount,
    criticalCount,
    setFilter,
    fetchEvents,
    fetchRules,
    fetchChannels,
    ack,
    testChannel,
    mergeEvent,
  }
})
