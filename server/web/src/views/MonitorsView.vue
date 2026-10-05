<script setup lang="ts">
/** 网站监控列表 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import CardBox from '@/components/CardBox.vue'
import StatusDot from '@/components/StatusDot.vue'
import EmptyState from '@/components/EmptyState.vue'
import Skeleton from '@/components/Skeleton.vue'
import { useMonitorStore } from '@/stores/monitor'
import { useAuthStore } from '@/stores/auth'
import { formatPercent, formatRelative, monitorTypeText, failReasonText } from '@/utils/format'

const monitors = useMonitorStore()
const auth = useAuthStore()
const search = ref('')

/** 异常优先。跟节点列表一个思路。 */
const sorted = computed(() => {
  const order: Record<string, number> = { down: 0, pending: 1, paused: 2, up: 3 }
  return [...monitors.monitors].sort(
    (a, b) => (order[a.status] ?? 4) - (order[b.status] ?? 4) || a.name.localeCompare(b.name),
  )
})

function onSearch(): void {
  monitors.setFilter({ kw: search.value })
  void monitors.fetch()
}

function onTypeChange(e: Event): void {
  monitors.setFilter({ type: (e.target as HTMLSelectElement).value as never })
  void monitors.fetch()
}

function loadAll(): void {
  void monitors.fetch()
}

onMounted(() => {
  loadAll()
  window.addEventListener('probeone:view-activated', loadAll)
})
onBeforeUnmount(() => {
  window.removeEventListener('probeone:view-activated', loadAll)
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
          aria-label="搜索"
          placeholder="搜索名称或目标"
          @keyup.enter="onSearch"
        />
        <select class="filter" :value="monitors.typeFilter" @change="onTypeChange">
          <option value="all">全部类型</option>
          <option value="http">HTTP</option>
          <option value="tcp">TCP 端口</option>
          <option value="dns">DNS</option>
          <option value="ssl">SSL 证书</option>
        </select>
        <RouterLink v-if="auth.isAdmin" to="/monitors/new" class="btn-primary">添加监控</RouterLink>
      </div>
    </template>

    <div v-if="monitors.total > 0" class="summary">
      <div class="sum">
        <span class="sum-val font-num">{{ monitors.upCount }}</span>
        <span class="sum-label">正常</span>
      </div>
      <div class="sum">
        <span class="sum-val font-num" :class="{ 'text-critical': monitors.downCount > 0 }">
          {{ monitors.downCount }}
        </span>
        <span class="sum-label">异常</span>
      </div>
      <div class="sum">
        <span class="sum-val font-num">
          {{ monitors.avgUptime !== null ? formatPercent(monitors.avgUptime) : '—' }}
        </span>
        <span class="sum-label">
          {{ monitors.avgUptime === null ? '样本不足' : '平均可用率' }}
        </span>
      </div>
    </div>

    <Skeleton v-if="monitors.loading && sorted.length === 0" type="table" :cols="5" :table-rows="6" />

    <EmptyState
      v-else-if="sorted.length === 0"
      icon="monitors"
      :title="search ? '没有匹配的监控' : '还没有添加监控'"
      :description="
        search
          ? '换个关键词试试'
          : '支持 HTTP、TCP 端口、DNS 与 SSL 证书四类探针，可监控可用性、响应时间与证书有效期'
      "
    >
      <RouterLink v-if="auth.isAdmin && !search" to="/monitors/new" class="btn-primary">
        添加监控
      </RouterLink>
    </EmptyState>

    <div v-else class="table-wrap">
      <table class="table">
        <thead>
          <tr>
            <th class="col-name">名称</th>
            <th class="col-status">状态</th>
            <th class="col-type">类型</th>
            <th class="col-num">可用率</th>
            <th class="col-num">延迟</th>
            <th class="col-time">最后检查</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="m in sorted" :key="m.id" class="row">
            <td class="col-name">
              <RouterLink :to="`/monitors/${m.id}`" class="mon-link">
                <span class="mon-title">{{ m.name }}</span>
                <span class="mon-target truncate">{{ m.target }}</span>
              </RouterLink>
            </td>
            <td class="col-status">
              <StatusDot :status="m.status" />
            </td>
            <td class="col-type">
              <span class="type-tag">{{ monitorTypeText(m.type) }}</span>
            </td>
            <td class="col-num">
              <template v-if="m.uptime_30d !== null && m.uptime_30d !== undefined">
                <span
                  class="font-num"
                  :class="m.uptime_30d >= 99.9 ? '' : m.uptime_30d >= 95 ? 'text-warning' : 'text-critical'"
                >
                  {{ formatPercent(m.uptime_30d, 2) }}
                </span>
              </template>
              <span v-else class="text-tertiary">样本不足</span>
            </td>
            <td class="col-num">
              <span v-if="m.avg_latency_ms" class="font-num">{{ m.avg_latency_ms }}ms</span>
              <span v-else class="text-tertiary">—</span>
            </td>
            <td class="col-time">
              <span class="text-tertiary">{{ formatRelative(m.last_checked_at) }}</span>
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
  width: 180px;
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

.summary {
  display: flex;
  gap: var(--space-6);
  padding: var(--space-4) var(--space-5);
  border-bottom: 1px solid var(--line-color);
}

.sum {
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.sum-val {
  font-size: var(--font-lg);
  font-weight: 600;
  line-height: 1.2;
}

.sum-label {
  font-size: var(--font-xs);
  color: var(--text-tertiary);
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

.row:hover {
  background: var(--bg-hover);
}

.col-name { min-width: 200px; }
.col-status { width: 60px; }
.col-type { width: 90px; }
.col-num { width: 100px; text-align: right; }
.col-time { width: 120px; white-space: nowrap; }

.mon-link {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
  color: var(--text-primary);
}

.mon-link:hover .mon-title {
  color: var(--accent);
}

.mon-title {
  font-weight: 500;
}

.mon-target {
  font-size: var(--font-xs);
  color: var(--text-tertiary);
  max-width: 280px;
}

.type-tag {
  font-size: 10px;
  padding: 2px 6px;
  border-radius: var(--radius-sm);
  background: var(--bg-hover);
  color: var(--text-secondary);
}

@media (max-width: 1023px) {
  .col-time { display: none; }
}

@media (max-width: 767px) {
  .toolbar { flex-wrap: wrap; }
  .search { width: 100%; }
  .summary { gap: var(--space-4); padding: var(--space-3); }
  .col-type, .col-time { display: none; }
  .table th, .table td { padding: var(--space-2) var(--space-3); }
  .mon-target { max-width: 160px; }
}
</style>
