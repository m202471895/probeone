<script setup lang="ts">
/** 服务器列表 */
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import CardBox from '@/components/CardBox.vue'
import StatusDot from '@/components/StatusDot.vue'
import EmptyState from '@/components/EmptyState.vue'
import { useNodeStore } from '@/stores/node'
import { useAuthStore } from '@/stores/auth'
import { formatBytes, formatBits, formatRelative } from '@/utils/format'

const nodes = useNodeStore()
const auth = useAuthStore()

const search = ref('')

/** 列表按状态排序：离线优先。运维时最关心"谁挂了"。 */
const sorted = computed(() => {
  const order: Record<string, number> = { offline: 0, pending: 1, online: 2 }
  return [...nodes.nodes].sort(
    (a, b) => (order[a.status] ?? 3) - (order[b.status] ?? 3) || a.name.localeCompare(b.name),
  )
})

function netRate(nodeId: number): { rx: number; tx: number } | null {
  const m = nodes.latestOf(nodeId)
  if (!m) return null
  let rx = 0
  let tx = 0
  for (const n of m.net_io ?? []) {
    rx += n.rx_bps
    tx += n.tx_bps
  }
  return { rx, tx }
}

function cpu(nodeId: number): number | null {
  return nodes.latestOf(nodeId)?.cpu_usage ?? null
}

function mem(nodeId: number): number | null {
  return nodes.latestOf(nodeId)?.mem_usage ?? null
}

function onSearch(): void {
  nodes.setFilter({ kw: search.value })
  void nodes.fetch()
}

function onStatusChange(e: Event): void {
  nodes.setFilter({ status: (e.target as HTMLSelectElement).value as never })
  void nodes.fetch()
}

onMounted(async () => {
  await Promise.all([nodes.fetch(), nodes.fetchGroups(), nodes.fetchLatestAll()])
})
</script>

<template>
  <CardBox :padded="false">
    <template #actions>
      <div class="toolbar">
        <input
          v-model="search"
          type="search"
          class="search"
          placeholder="搜索名称、主机名或 IP"
          @keyup.enter="onSearch"
        />
        <select class="filter" :value="nodes.statusFilter" @change="onStatusChange">
          <option value="all">全部状态</option>
          <option value="online">在线</option>
          <option value="offline">离线</option>
          <option value="pending">待接入</option>
        </select>
        <RouterLink v-if="auth.isAdmin" to="/nodes/new" class="btn-primary">添加节点</RouterLink>
      </div>
    </template>

    <EmptyState
      v-if="sorted.length === 0"
      icon="nodes"
      :title="search ? '没有匹配的节点' : '还没有添加节点'"
      :description="
        search ? '换个关键词试试' : '添加节点后安装 Agent，即可看到实时 CPU、内存、网络与磁盘指标'
      "
    >
      <RouterLink v-if="auth.isAdmin && !search" to="/nodes/new" class="btn-primary">
        添加节点
      </RouterLink>
    </EmptyState>

    <div v-else class="table-wrap">
      <table class="table">
        <thead>
          <tr>
            <th class="col-name">名称</th>
            <th class="col-status">状态</th>
            <th class="col-num">CPU</th>
            <th class="col-num">内存</th>
            <th class="col-net">网络</th>
            <th class="col-spec">规格</th>
            <th class="col-time">最后上报</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="node in sorted" :key="node.id" class="row">
            <td class="col-name">
              <RouterLink :to="`/nodes/${node.uid}`" class="node-link">
                <span class="node-title">{{ node.name }}</span>
                <span class="node-sub truncate">
                  {{ node.public_ip || node.hostname || node.os_type || '—' }}
                </span>
              </RouterLink>
            </td>
            <td class="col-status"><StatusDot :status="node.status" /></td>
            <td class="col-num">
              <span v-if="cpu(node.id) !== null" class="font-num">
                {{ cpu(node.id)?.toFixed(1) }}%
              </span>
              <span v-else class="text-tertiary">—</span>
            </td>
            <td class="col-num">
              <span v-if="mem(node.id) !== null" class="font-num">
                {{ mem(node.id)?.toFixed(1) }}%
              </span>
              <span v-else class="text-tertiary">—</span>
              <span v-if="node.mem_total" class="sub-note">{{ formatBytes(node.mem_total) }}</span>
            </td>
            <td class="col-net">
              <template v-if="netRate(node.id)">
                <div class="net-row">
                  <span class="net-arrow">↓</span>
                  <span class="font-num">{{ formatBits(netRate(node.id)!.rx) }}</span>
                </div>
                <div class="net-row">
                  <span class="net-arrow">↑</span>
                  <span class="font-num">{{ formatBits(netRate(node.id)!.tx) }}</span>
                </div>
              </template>
              <span v-else class="text-tertiary">—</span>
            </td>
            <td class="col-spec">
              <span v-if="node.cpu_cores" class="font-num">{{ node.cpu_cores }} 核</span>
              <span v-else class="text-tertiary">—</span>
              <span v-if="node.cpu_model" class="sub-note truncate" :title="node.cpu_model">
                {{ node.cpu_model }}
              </span>
            </td>
            <td class="col-time">
              <span class="text-tertiary">{{ formatRelative(node.last_report_at) }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </CardBox>
</template>

<style scoped>
.toolbar {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}

.search {
  height: 30px;
  width: 200px;
  padding: 0 var(--space-3);
  border: 1px solid var(--line-color);
  border-radius: var(--radius-md);
  background: var(--bg-body);
  color: var(--text-primary);
  font-size: var(--font-sm);
  font-family: inherit;
}

.search:focus {
  outline: none;
  border-color: var(--accent);
}

.filter {
  height: 30px;
  padding: 0 var(--space-2);
  border: 1px solid var(--line-color);
  border-radius: var(--radius-md);
  background: var(--bg-body);
  color: var(--text-secondary);
  font-size: var(--font-sm);
  font-family: inherit;
  cursor: pointer;
}

.btn-primary {
  display: inline-flex;
  align-items: center;
  height: 30px;
  padding: 0 var(--space-3);
  border-radius: var(--radius-md);
  background: var(--accent);
  color: #fff;
  font-size: var(--font-sm);
  font-weight: 500;
  white-space: nowrap;
}

.btn-primary:hover {
  background: var(--accent-hover);
  color: #fff;
}

.table-wrap {
  overflow-x: auto;
}

.table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--font-sm);
}

.table th {
  text-align: left;
  font-size: var(--font-xs);
  font-weight: 500;
  color: var(--text-tertiary);
  padding: var(--space-2) var(--space-4);
  border-bottom: 1px solid var(--line-color);
  white-space: nowrap;
}

/*
 * 首列与末列单独加横向内边距。
 * 给 .table-wrap 加 padding 无效：表格是 width:100% 的块级元素，
 * 会把容器 padding 顶开并填满，等于什么都没加。
 * 直接在单元格上动手才有效。
 */
.table th:first-child,
.table td:first-child {
  padding-left: var(--space-4);
}

.table th:last-child,
.table td:last-child {
  padding-right: var(--space-4);
}

.table td {
  padding: var(--space-3) var(--space-4);
  border-bottom: 1px solid var(--line-color);
  vertical-align: middle;
}

.row:last-child td {
  border-bottom: none;
}

.row {
  transition: background-color var(--duration-fast) var(--ease-out);
}

.row:hover {
  background: var(--bg-hover);
}

.col-name { min-width: 180px; }
.col-status { width: 60px; }
.col-num { width: 100px; text-align: right; }
.col-net { width: 120px; }
.col-spec { max-width: 200px; }
.col-time { width: 120px; white-space: nowrap; }

.node-link {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
  color: var(--text-primary);
}

.node-link:hover .node-title {
  color: var(--accent);
}

.node-title {
  font-weight: 500;
  transition: color var(--duration-fast) var(--ease-out);
}

.node-sub {
  font-size: var(--font-xs);
  color: var(--text-tertiary);
  max-width: 220px;
}

.sub-note {
  display: block;
  font-size: 10px;
  color: var(--text-tertiary);
}

.net-row {
  display: flex;
  align-items: center;
  gap: 3px;
  font-size: var(--font-xs);
  line-height: 1.4;
}

.net-arrow {
  color: var(--text-tertiary);
  width: 10px;
}

@media (max-width: 1023px) {
  .search { width: 150px; }
  .col-spec, .col-time { display: none; }
}

@media (max-width: 767px) {
  .toolbar { flex-wrap: wrap; }
  .search { width: 100%; }
  .col-net, .col-spec, .col-time { display: none; }
  .table th, .table td { padding: var(--space-2) var(--space-3); }
  .node-sub { max-width: 140px; }
}
</style>
