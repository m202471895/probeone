<script setup lang="ts">
/** 节点详情：规格 + 多指标图表 */
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import CardBox from '@/components/CardBox.vue'
import StatusDot from '@/components/StatusDot.vue'
import {
  baseAxis,
  baseGrid,
  baseTooltip,
  bindResize,
  createChart,
  onThemeChange,
  themeColors,
} from '@/charts/echarts'
import type { EChartsInstance, EChartsOption } from '@/charts/echarts'
import { api, humanizeError } from '@/api/client'
import type { MetricPoint, Node } from '@/api/types'
import { useNodeStore } from '@/stores/node'
import { useAuthStore } from '@/stores/auth'
import { formatBytes, formatRelative, formatUptime } from '@/utils/format'

const route = useRoute()
const router = useRouter()
const store = useNodeStore()
const auth = useAuthStore()

const node = ref<Node | null>(null)
const points = ref<MetricPoint[]>([])
const range = ref<'1h' | '6h' | '24h' | '7d'>('1h')
const loadError = ref('')

/**
 * 删除节点。
 *
 * 用 prompt 要求输入节点名做二次确认，而不是简单的 confirm：
 * 删除会连带清掉历史指标且不可恢复，节点重新接入后 UID 会变、
 * 历史数据关联不上。误点一次可能丢几周的曲线。
 */
async function onDelete(): Promise<void> {
  if (!node.value) return
  const name = node.value.name
  const input = window.prompt(
    `删除节点「${name}」？\n\n该节点的历史指标会一并删除，且不可恢复。\n` +
    `如需确认，请输入节点名称：`,
  )
  if (input === null) return
  if (input.trim() !== name) {
    window.alert('节点名称不匹配，已取消删除')
    return
  }
  try {
    await store.remove(node.value.uid)
    void router.push('/nodes')
  } catch (err) {
    loadError.value = humanizeError(err, '删除失败')
  }
}

const cpuRef = ref<HTMLDivElement | null>(null)
const memRef = ref<HTMLDivElement | null>(null)
const netRef = ref<HTMLDivElement | null>(null)

const cpuChart = shallowRef<ReturnType<typeof createChart> | null>(null)
const memChart = shallowRef<ReturnType<typeof createChart> | null>(null)
const netChart = shallowRef<ReturnType<typeof createChart> | null>(null)

let unbindResize: (() => void) | null = null
let unbindTheme: (() => void) | null = null

const RANGE_SEC: Record<string, number> = {
  '1h': 3600,
  '6h': 6 * 3600,
  '24h': 24 * 3600,
  '7d': 7 * 24 * 3600,
}

const latest = computed(() => (node.value ? store.latestOf(node.value.id) : undefined))

function formatBps(v: number): string {
  if (!v || v < 1) return '0'
  const units = ['b', 'K', 'M', 'G']
  const i = Math.min(Math.floor(Math.log(v) / Math.log(1000)), units.length - 1)
  return `${(v / Math.pow(1000, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

function lineOption(
  pts: MetricPoint[],
  key: 'cpu' | 'mem',
  name: string,
  color: string,
  fill: boolean,
): EChartsOption {
  const c = themeColors()
  const axisLabel = { ...baseAxis().axisLabel, formatter: '{value}%' }
  return {
    grid: baseGrid({ top: 14, right: 12, bottom: 18, left: 8 }),
    tooltip: { ...baseTooltip(), valueFormatter: (v) => `${Number(v).toFixed(1)}%` },
    xAxis: { type: 'time', ...baseAxis() },
    yAxis: { type: 'value', min: 0, max: 100, ...baseAxis(), axisLabel },
    series: [
      {
        type: 'line',
        name,
        data: pts.map((p) => ({ value: [p.t * 1000, p[key] ?? 0] })),
        showSymbol: false,
        // smoothMonotone:'x' 防过冲——否则曲线会显示不存在的峰值
        smooth: 0.2,
        smoothMonotone: 'x',
        lineStyle: { width: 1.5, color },
        ...(fill ? { areaStyle: { color, opacity: 0.08 } } : {}),
      },
    ],
    textStyle: { color: c.textSecondary },
  }
}

function netOption(pts: MetricPoint[]): EChartsOption {
  const c = themeColors()
  return {
    grid: baseGrid({ top: 26, right: 12, bottom: 18, left: 8 }),
    tooltip: baseTooltip(),
    legend: {
      data: ['下行', '上行'],
      textStyle: { color: c.textSecondary, fontSize: 11 },
      itemWidth: 12,
      itemHeight: 2,
    },
    xAxis: { type: 'time', ...baseAxis() },
    yAxis: {
      type: 'value',
      ...baseAxis(),
      axisLabel: { ...baseAxis().axisLabel, formatter: formatBps },
    },
    series: [
      {
        type: 'line',
        name: '下行',
        // byte/s → bit/s：网络监控习惯用 bit
        data: pts.map((p) => ({ value: [p.t * 1000, (p.net_rx ?? 0) * 8] })),
        showSymbol: false,
        smooth: 0.2,
        lineStyle: { width: 1.5, color: c.palette[0] },
      },
      {
        type: 'line',
        name: '上行',
        data: pts.map((p) => ({ value: [p.t * 1000, (p.net_tx ?? 0) * 8] })),
        showSymbol: false,
        smooth: 0.2,
        lineStyle: { width: 1.5, color: c.palette[1] },
      },
    ],
  }
}

function renderCharts(): void {
  if (!points.value.length) return
  const c = themeColors()
  cpuChart.value?.setOption(lineOption(points.value, 'cpu', 'CPU', c.palette[0]!, true), false)
  memChart.value?.setOption(lineOption(points.value, 'mem', '内存', c.palette[1]!, true), false)
  netChart.value?.setOption(netOption(points.value), false)
}

async function loadNode(): Promise<void> {
  const uid = route.params.uid
  if (typeof uid !== 'string') return
  try {
    node.value = await api.get<Node>(`/api/nodes/${uid}`)
  } catch (err) {
    loadError.value = (err as Error).message || '节点加载失败'
  }
}

async function loadPoints(): Promise<void> {
  if (!node.value) return
  const from = Math.floor(Date.now() / 1000) - RANGE_SEC[range.value]!
  try {
    const res = await api.get<{ items: MetricPoint[] }>(
      `/api/nodes/${node.value.uid}/metrics?range=${range.value}&from=${from}`,
    )
    points.value = res.items
    renderCharts()
  } catch {
    points.value = []
  }
}

onMounted(async () => {
  await loadNode()
  await store.fetchLatestAll()

  if (cpuRef.value) cpuChart.value = createChart(cpuRef.value)
  if (memRef.value) memChart.value = createChart(memRef.value)
  if (netRef.value) netChart.value = createChart(netRef.value)

  if (cpuRef.value && cpuChart.value) {
    unbindResize = bindResize(cpuChart.value, cpuRef.value)
  }
  unbindTheme = onThemeChange(renderCharts)
  await loadPoints()
})

onBeforeUnmount(() => {
  unbindResize?.()
  unbindTheme?.()
  cpuChart.value?.dispose()
  memChart.value?.dispose()
  netChart.value?.dispose()
})

watch(range, () => void loadPoints())
</script>

<template>
  <div class="node-detail">
    <div v-if="loadError" class="error-box">{{ loadError }}</div>

    <template v-else-if="node">
      <CardBox>
        <div class="head">
          <StatusDot :status="node.status" :size="10" />
          <div class="head-main">
            <h2 class="head-title">{{ node.name }}</h2>
            <div class="head-meta">
              <span v-if="node.public_ip" class="font-mono">{{ node.public_ip }}</span>
              <span v-if="node.os_type">{{ node.os_type }} {{ node.os_version }}</span>
              <span v-if="node.agent_version">Agent {{ node.agent_version }}</span>
              <span class="text-tertiary">最后上报 {{ formatRelative(node.last_report_at) }}</span>
            </div>
          </div>
          <div v-if="latest" class="uptime">运行 {{ formatUptime(latest.uptime) }}</div>
        </div>
      </CardBox>

      <CardBox v-if="node.cpu_model || node.disk_info?.length" title="硬件规格">
        <div class="specs">
          <div v-if="node.cpu_model" class="spec">
            <span class="spec-label">CPU</span>
            <span class="spec-val" :title="node.cpu_model">{{ node.cpu_model }}</span>
            <span v-if="node.cpu_cores" class="spec-note">{{ node.cpu_cores }} 核</span>
          </div>
          <div v-if="node.mem_total" class="spec">
            <span class="spec-label">内存</span>
            <span class="spec-val font-num">{{ formatBytes(node.mem_total) }}</span>
          </div>
          <div v-for="(d, i) in node.disk_info ?? []" :key="i" class="spec">
            <span class="spec-label">磁盘 {{ i + 1 }}</span>
            <span class="spec-val font-num">{{ formatBytes(d.total) }}</span>
            <span class="spec-note">{{ d.mount }}{{ d.fstype ? ` · ${d.fstype}` : '' }}</span>
          </div>
        </div>
      </CardBox>

      <CardBox :padded="false">
        <template #actions>
          <div class="actions-row">
            <button
              v-if="auth.isAdmin"
              class="btn-danger-ghost"
              @click="onDelete"
            >
              删除节点
            </button>
          </div>
          <div class="ranges">
            <button
              v-for="r in ['1h', '6h', '24h', '7d'] as const"
              :key="r"
              class="range-btn"
              :class="{ active: range === r }"
              @click="range = r"
            >
              {{ r }}
            </button>
          </div>
        </template>

        <div v-if="points.length === 0" class="no-data">
          {{ node.status === 'online' ? '正在采集数据…' : '节点离线，暂无数据' }}
        </div>

        <div v-else class="charts">
          <div class="chart-block">
            <span class="chart-title">CPU 使用率</span>
            <div ref="cpuRef" class="chart" />
          </div>
          <div class="chart-block">
            <span class="chart-title">内存使用率</span>
            <div ref="memRef" class="chart" />
          </div>
          <div class="chart-block">
            <span class="chart-title">网络吞吐</span>
            <div ref="netRef" class="chart" />
          </div>
        </div>
      </CardBox>
    </template>

    <div v-else class="error-box">加载中…</div>
  </div>
</template>

<style scoped>
.node-detail {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}

.error-box {
  padding: var(--space-4);
  background: var(--critical-subtle);
  color: var(--critical);
  border-radius: var(--radius-md);
  font-size: var(--font-sm);
}

.head {
  display: flex;
  align-items: flex-start;
  gap: var(--space-3);
}

.head-main {
  flex: 1;
  min-width: 0;
}

.head-title {
  font-size: var(--font-lg);
  margin-bottom: var(--space-1);
}

.head-meta {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-1) var(--space-3);
  font-size: var(--font-xs);
  color: var(--text-tertiary);
}

.uptime {
  font-size: var(--font-sm);
  color: var(--text-secondary);
  flex-shrink: 0;
}

.specs {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: var(--space-4);
}

.spec {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.spec-label {
  font-size: var(--font-xs);
  color: var(--text-tertiary);
}

.spec-val {
  font-size: var(--font-sm);
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.spec-note {
  font-size: 10px;
  color: var(--text-tertiary);
}

.ranges {
  display: flex;
  gap: 2px;
  background: var(--bg-hover);
  padding: 2px;
  border-radius: var(--radius-md);
}

.range-btn {
  border: none;
  background: transparent;
  color: var(--text-tertiary);
  font-size: var(--font-xs);
  font-family: inherit;
  padding: 3px 8px;
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition: background-color var(--duration-fast) var(--ease-out);
}

.range-btn.active {
  background: var(--bg-surface);
  color: var(--text-primary);
}

.no-data {
  padding: var(--space-7);
  text-align: center;
  color: var(--text-tertiary);
  font-size: var(--font-sm);
}

.chart-block {
  border-bottom: 1px solid var(--line-color);
  padding: var(--space-4) var(--space-5);
}

.chart-block:last-child {
  border-bottom: none;
}

.chart-title {
  display: block;
  font-size: var(--font-xs);
  color: var(--text-tertiary);
  margin-bottom: var(--space-2);
}

.chart {
  height: 180px;
  width: 100%;
}

@media (max-width: 767px) {
  .chart {
    height: 150px;
  }
  .chart-block {
    padding: var(--space-3);
  }
}
</style>
