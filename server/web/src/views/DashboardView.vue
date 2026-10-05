<script setup lang="ts">
/**
 * 总览大盘
 *
 * 一屏内只放 4 个统计块 + 节点网格 + 站点状态，
 * 不放细节——细节留给点进去看（PRD 11.3「一屏内核心信息不超过 3 个视觉层级」）。
 */
import { computed, onMounted } from 'vue'
import { RouterLink } from 'vue-router'
import CardBox from '@/components/CardBox.vue'
import StatusDot from '@/components/StatusDot.vue'
import EmptyState from '@/components/EmptyState.vue'
import { useNodeStore } from '@/stores/node'
import { useMonitorStore } from '@/stores/monitor'
import { useAlertStore } from '@/stores/alert'
import { formatBytes, formatPercent, formatRelative, monitorTypeText } from '@/utils/format'

const nodes = useNodeStore()
const monitors = useMonitorStore()
const alerts = useAlertStore()

/** 概览最多展示 12 个节点——再多就该滚屏了，概览的意义在于一眼看全。 */
const previewNodes = computed(() => nodes.nodes.slice(0, 12))
const previewMonitors = computed(() => monitors.monitors.slice(0, 8))

const stats = computed(() => [
  {
    label: '在线节点',
    value: `${nodes.onlineCount}`,
    unit: `/ ${nodes.total}`,
    tone: nodes.offlineCount > 0 ? 'warning' : 'ok',
  },
  {
    label: '监控项目',
    value: `${monitors.upCount}`,
    unit: `/ ${monitors.total}`,
    tone: monitors.downCount > 0 ? 'critical' : 'ok',
  },
  {
    label: '平均可用率',
    value: monitors.avgUptime !== null ? formatPercent(monitors.avgUptime) : '—',
    unit: monitors.avgUptime === null ? '样本不足' : '近 30 天',
    tone: 'ok',
  },
  {
    label: '未处理告警',
    value: `${alerts.unackedCount}`,
    unit: alerts.criticalCount > 0 ? `${alerts.criticalCount} 条严重` : '无严重',
    tone: alerts.criticalCount > 0 ? 'critical' : alerts.unackedCount > 0 ? 'warning' : 'ok',
  },
])

function memUsage(nodeId: number): number | null {
  const m = nodes.latestOf(nodeId)
  return m ? m.mem_usage : null
}

function cpuUsage(nodeId: number): number | null {
  const m = nodes.latestOf(nodeId)
  return m ? m.cpu_usage : null
}

onMounted(async () => {
  await Promise.all([
    nodes.fetch(),
    nodes.fetchGroups(),
    nodes.fetchLatestAll(),
    monitors.fetch(),
    alerts.fetchEvents(),
  ])
})
</script>

<template>
  <div class="dashboard">
    <!-- 统计块 -->
    <div class="stats">
      <div v-for="s in stats" :key="s.label" class="stat" :class="`tone-${s.tone}`">
        <span class="stat-label">{{ s.label }}</span>
        <div class="stat-value-row">
          <span class="stat-value font-num">{{ s.value }}</span>
          <span class="stat-unit">{{ s.unit }}</span>
        </div>
      </div>
    </div>

    <div class="grid">
      <!-- 节点 -->
      <CardBox title="服务器" :padded="false">
        <template #actions>
          <RouterLink to="/nodes" class="more">全部 →</RouterLink>
        </template>

        <EmptyState
          v-if="nodes.nodes.length === 0"
          icon="nodes"
          title="还没有添加节点"
          description="添加节点后安装 Agent，即可在面板看到实时指标"
        >
          <RouterLink to="/nodes/new" class="btn-primary">添加节点</RouterLink>
        </EmptyState>

        <div v-else class="node-grid">
          <RouterLink
            v-for="node in previewNodes"
            :key="node.id"
            :to="`/nodes/${node.uid}`"
            class="node-card"
          >
            <div class="node-head">
              <StatusDot :status="node.status" />
              <span class="node-name truncate">{{ node.name }}</span>
            </div>
            <div class="node-metrics">
              <div class="metric">
                <span class="metric-val font-num">
                  {{ cpuUsage(node.id) !== null ? `${cpuUsage(node.id)?.toFixed(1)}%` : '—' }}
                </span>
                <span class="metric-label">CPU</span>
              </div>
              <div class="metric">
                <span class="metric-val font-num">
                  {{ memUsage(node.id) !== null ? `${memUsage(node.id)?.toFixed(1)}%` : '—' }}
                </span>
                <span class="metric-label">内存</span>
              </div>
              <div class="metric">
                <span class="metric-val font-num">
                  {{ node.cpu_cores ? `${node.cpu_cores}核` : '—' }}
                </span>
                <span class="metric-label">规格</span>
              </div>
            </div>
            <div class="node-foot">
              {{ node.geo_country || node.os_type || '未知地区' }}
              <span v-if="node.last_seen_at" class="text-tertiary">
                · {{ formatRelative(node.last_seen_at) }}
              </span>
            </div>
          </RouterLink>
        </div>
      </CardBox>

      <!-- 站点状态 -->
      <CardBox title="网站监控" :padded="false">
        <template #actions>
          <RouterLink to="/monitors" class="more">全部 →</RouterLink>
        </template>

        <EmptyState
          v-if="monitors.monitors.length === 0"
          icon="monitors"
          title="还没有添加监控"
          description="添加网站后可监控可用性、响应时间与 SSL 证书有效期"
        >
          <RouterLink to="/monitors/new" class="btn-primary">添加监控</RouterLink>
        </EmptyState>

        <ul v-else class="monitor-list">
          <li v-for="m in previewMonitors" :key="m.id" class="monitor-item">
            <StatusDot :status="m.status" />
            <div class="monitor-info">
              <span class="monitor-name truncate">{{ m.name }}</span>
              <span class="monitor-target truncate">{{ m.target }}</span>
            </div>
            <div class="monitor-right">
              <span class="monitor-type">{{ monitorTypeText(m.type) }}</span>
              <span v-if="m.avg_latency_ms" class="monitor-latency font-num">
                {{ m.avg_latency_ms }}ms
              </span>
            </div>
          </li>
        </ul>
      </CardBox>
    </div>

    <!-- 汇总条 -->
    <CardBox v-if="nodes.total > 0 || monitors.total > 0" title="资源汇总">
      <div class="summary">
        <div class="sum-item">
          <span class="sum-label">总内存</span>
          <span class="sum-val font-num">{{ formatBytes(nodes.totalMem) }}</span>
        </div>
        <div class="sum-item">
          <span class="sum-label">总磁盘</span>
          <span class="sum-val font-num">{{ formatBytes(nodes.totalDisk) }}</span>
        </div>
        <div class="sum-item">
          <span class="sum-label">在线率</span>
          <span class="sum-val font-num">
            {{ nodes.total > 0 ? formatPercent((nodes.onlineCount / nodes.total) * 100) : '—' }}
          </span>
        </div>
        <div class="sum-item">
          <span class="sum-label">离线节点</span>
          <span class="sum-val font-num">{{ nodes.offlineCount }}</span>
        </div>
      </div>
    </CardBox>
  </div>
</template>

<style scoped>
.dashboard {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}

/* ---- 统计块 ---- */
.stats {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: var(--space-3);
}

.stat {
  background: var(--bg-surface);
  border: 1px solid var(--line-color);
  border-radius: var(--radius-lg);
  padding: var(--space-4);
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  min-width: 0;
}

.stat-label {
  font-size: var(--font-xs);
  color: var(--text-tertiary);
}

.stat-value-row {
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
  min-width: 0;
}

.stat-value {
  font-size: var(--font-xl);
  font-weight: 600;
  line-height: 1.1;
}

.stat-unit {
  font-size: var(--font-xs);
  color: var(--text-tertiary);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tone-ok .stat-value { color: var(--text-primary); }
.tone-warning .stat-value { color: var(--warning); }
.tone-critical .stat-value { color: var(--critical); }

/* ---- 双栏 ---- */
.grid {
  display: grid;
  grid-template-columns: 1.4fr 1fr;
  gap: var(--space-4);
  align-items: start;
}

.more {
  font-size: var(--font-xs);
  color: var(--text-tertiary);
  white-space: nowrap;
  /*
   * 「全部 →」这类小字链接视觉上要紧凑，但点击区域必须够。
   * padding 撑开热区，负 margin 抵消撑开的视觉占位，
   * 这样既满足 24px 底线，又不会顶开卡片标题的布局。
   */
  display: inline-flex;
  align-items: center;
  padding: var(--space-1) var(--space-2);
  margin: calc(-1 * var(--space-1)) calc(-1 * var(--space-2));
  border-radius: var(--radius-sm);
  transition: color var(--duration-fast) var(--ease-out), background-color var(--duration-fast) var(--ease-out);
}

.more:hover {
  color: var(--accent);
  background: var(--accent-subtle);
}

/* ---- 节点网格 ---- */
.node-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(190px, 1fr));
  gap: var(--space-2);
  padding: var(--space-4);
}

.node-card {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-3);
  border: 1px solid var(--line-color);
  border-radius: var(--radius-md);
  color: var(--text-primary);
  min-width: 0;
  transition:
    border-color var(--duration-fast) var(--ease-out),
    background-color var(--duration-fast) var(--ease-out);
}

.node-card:hover {
  border-color: var(--accent-border);
  background: var(--bg-hover);
  color: var(--text-primary);
}

.node-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
}

.node-name {
  font-size: var(--font-sm);
  font-weight: 500;
  min-width: 0;
}

.node-metrics {
  display: flex;
  gap: var(--space-5);
  flex-wrap: wrap;
}

.metric {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
  /*
   * 不加分隔线：卡片只有 190px 宽，
   * 三个块加竖线后会换行，竖线断在半空反而更乱。
   * 靠加大间距区分即可。
   */
}

.metric-val {
  font-size: var(--font-sm);
  font-weight: 500;
}

.metric-label {
  font-size: 10px;
  color: var(--text-tertiary);
}

.node-foot {
  font-size: 10px;
  color: var(--text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* ---- 监控列表 ---- */
.monitor-list {
  list-style: none;
  margin: 0;
  padding: 0;
}

.monitor-item {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-3) var(--space-4);
  border-bottom: 1px solid var(--line-color);
  min-width: 0;
}

.monitor-item:last-child {
  border-bottom: none;
}

.monitor-info {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.monitor-name {
  font-size: var(--font-sm);
  font-weight: 500;
}

.monitor-target {
  font-size: var(--font-xs);
  color: var(--text-tertiary);
}

.monitor-right {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 1px;
  flex-shrink: 0;
}

.monitor-type {
  font-size: 10px;
  color: var(--text-tertiary);
}

.monitor-latency {
  font-size: var(--font-xs);
  color: var(--text-secondary);
}

/* ---- 汇总 ---- */
.summary {
  display: grid;
  /*
   * 四列等分。
   * 曾试过 flex + gap 让各块按内容紧邻，但在宽屏下会全挤在左侧、
   * 右边留一大片空白（用户反馈"没内边距"的观感来源之一）。
   * 等分铺满整行才是这里想要的：数据少的时候也应该均匀分布。
   */
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: var(--space-4);
}

.sum-item {
  display: flex;
  flex-direction: column;
  /* gap 从 2px 提到 4px：原值让标签与数值像两行独立文字 */
  gap: var(--space-1);
  min-width: 0;
}

.sum-label {
  font-size: var(--font-xs);
  color: var(--text-tertiary);
}

.sum-val {
  font-size: var(--font-md);
  font-weight: 500;
  color: var(--text-primary);
}

.btn-primary {
  display: inline-flex;
  align-items: center;
  height: 30px;
  padding: 0 var(--space-4);
  border-radius: var(--radius-md);
  background: var(--accent);
  color: #fff;
  font-size: var(--font-sm);
  font-weight: 500;
  transition: background-color var(--duration-fast) var(--ease-out);
}

.btn-primary:hover {
  background: var(--accent-hover);
  color: #fff;
}

/* ---- 响应式 ---- */
@media (max-width: 1280px) {
  .grid {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 900px) {
  .stats {
    grid-template-columns: repeat(2, 1fr);
  }
  .summary {
    grid-template-columns: repeat(2, 1fr);
  }
}

@media (max-width: 767px) {
  .node-grid {
    grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
    padding: var(--space-3);
  }
  .stat {
    padding: var(--space-3);
  }
  .stat-value {
    font-size: var(--font-lg);
  }
}
</style>
