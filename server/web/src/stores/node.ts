/**
 * 节点Store
 *
 * 承担：列表、筛选、实时指标合并。
 * 实时推送来的指标合并进latest，而不是替换整个节点对象——
 * 否则 WebSocket 频率（秒级）会触发列表的完全重渲染。
 */

import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api } from '@/api/client'
import type { MetricSnapshot, Node, NodeGroup, NodeStatus } from '@/api/types'

export type NodeStatusFilter = NodeStatus | 'all'

export const useNodeStore = defineStore('nodes', () => {
  const nodes = ref<Node[]>([])
  const groups = ref<NodeGroup[]>([])
  const total = ref(0)
  const loading = ref(false)
  const error = ref('')

  // 筛选
  const statusFilter = ref<NodeStatusFilter>('all')
  const groupFilter = ref<number | null>(null)
  const keyword = ref('')
  const page = ref(1)
  const size = ref(50)

  /** 最新指标：node_id → 快照。与节点列表分离，避免列表重渲染。 */
  const latest = ref<Record<number, MetricSnapshot>>({})

  const onlineCount = computed(() => nodes.value.filter((n) => n.status === 'online').length)
  const offlineCount = computed(() => nodes.value.filter((n) => n.status === 'offline').length)
  const totalMem = computed(() =>
    nodes.value.reduce((sum, n) => sum + (n.mem_total ?? 0), 0),
  )
  const totalDisk = computed(() =>
    nodes.value.reduce(
      (sum, n) => sum + (n.disk_info ?? []).reduce((s, d) => s + (d.total ?? 0), 0),
      0,
    ),
  )

  function setFilter(f: { status?: NodeStatusFilter; group?: number | null; kw?: string }): void {
    if (f.status !== undefined) statusFilter.value = f.status
    if (f.group !== undefined) groupFilter.value = f.group
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
      if (statusFilter.value !== 'all') params.set('status', statusFilter.value)
      if (groupFilter.value !== null) params.set('group_id', String(groupFilter.value))
      if (keyword.value) params.set('q', keyword.value)
      params.set('page', String(page.value))
      params.set('size', String(size.value))

      const res = await api.get<{ items: Node[]; total: number }>(`/api/nodes?${params}`)
      total.value = res.total
      nodes.value = append ? [...nodes.value, ...res.items] : res.items
    } catch (err) {
      error.value = (err as Error).message || '节点加载失败'
    } finally {
      loading.value = false
    }
  }

  async function fetchGroups(): Promise<void> {
    try {
      groups.value = await api.get<NodeGroup[]>('/api/groups')
    } catch {
      groups.value = []
    }
  }

  async function fetchLatestAll(): Promise<void> {
    try {
      const res = await api.get<{ items: Array<{ node_id: number } & MetricSnapshot> }>(
        '/api/nodes/metrics/latest',
      )
      const map: Record<number, MetricSnapshot> = {}
      for (const row of res.items) {
        const { node_id: id, ...snapshot } = row
        map[id] = snapshot as MetricSnapshot
      }
      latest.value = map
    } catch {
      // 最新指标拉不到不阻塞列表展示
    }
  }

  /** 合并单条实时指标。不动nodes 数组，只改 latest。 */
  function mergeLatest(nodeId: number, snapshot: MetricSnapshot): void {
    latest.value = { ...latest.value, [nodeId]: snapshot }
  }

  function mergeStatus(nodeId: number, status: NodeStatus): void {
    const idx = nodes.value.findIndex((n) => n.id === nodeId)
    if (idx >= 0 && nodes.value[idx]?.status !== status) {
      const copy = [...nodes.value]
      copy[idx] = { ...copy[idx]!, status }
      nodes.value = copy
    }
  }

  function getByUid(uid: string): Node | undefined {
    return nodes.value.find((n) => n.uid === uid)
  }

  function latestOf(nodeId: number): MetricSnapshot | undefined {
    return latest.value[nodeId]
  }

  return {
    nodes,
    groups,
    total,
    loading,
    error,
    statusFilter,
    groupFilter,
    keyword,
    page,
    size,
    latest,
    onlineCount,
    offlineCount,
    totalMem,
    totalDisk,
    setFilter,
    fetch,
    fetchGroups,
    fetchLatestAll,
    mergeLatest,
    mergeStatus,
    getByUid,
    latestOf,
  }
})
