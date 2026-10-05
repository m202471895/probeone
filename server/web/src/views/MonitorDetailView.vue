<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { useRoute } from 'vue-router'
import CardBox from '@/components/CardBox.vue'
import StatusDot from '@/components/StatusDot.vue'
import { baseAxis, baseGrid, baseTooltip, bindResize, createChart, onThemeChange, themeColors } from '@/charts/echarts'
import type { EChartsInstance, EChartsOption } from '@/charts/echarts'
import { api, humanizeError } from '@/api/client'
import type { Monitor, MonitorResult, MonitorStats, SSLCertificate } from '@/api/types'
import { formatDateTime, formatMs, formatPercent, formatRelative, failReasonText, monitorTypeText } from '@/utils/format'

const route = useRoute()
const monitor = ref<Monitor | null>(null)
const results = ref<MonitorResult[]>([])
const stats = ref<MonitorStats | null>(null)
const cert = ref<SSLCertificate | null>(null)
const range = ref<'24h' | '7d' | '30d'>('24h')
const checking = ref(false)
const error = ref('')

const chartRef = ref<HTMLDivElement | null>(null)
const chart = shallowRef<EChartsInstance | null>(null)
let unbindResize: (() => void) | null = null
let unbindTheme: (() => void) | null = null

const RANGE_SEC: Record<string, number> = { '24h': 86400, '7d': 604800, '30d': 2592000 }

const recent = computed(() => results.value.slice(-20).reverse())

/** 证书剩余天数配色。与后端告警阈值呼应：>7 天正常，≤7 天警告，≤3 天或已过期为严重。 */
const daysTone = computed(() => {
  const d = cert.value?.days_left
  if (d === null || d === undefined) return ''
  if (d <= 3) return 'crit'
  if (d <= 7) return 'warn'
  return ''
})

async function loadAll(): Promise<void> {
  const id = route.params.id
  if (!id) return
  try {
    monitor.value = await api.get<Monitor>(`/api/monitors/${id}`)
    await Promise.all([loadResults(), loadStats(), loadCert()])
  } catch (err) {
    error.value = humanizeError(err, '加载失败')
  }
}

async function loadResults(): Promise<void> {
  const id = route.params.id
  if (!id) return
  const from = Math.floor(Date.now() / 1000) - RANGE_SEC[range.value]!
  try {
    const res = await api.get<{ items: MonitorResult[] }>(
      `/api/monitors/${id}/results?range=${range.value}&from=${from}`)
    results.value = res.items
    render()
  } catch { results.value = [] }
}

async function loadStats(): Promise<void> {
  const id = route.params.id
  if (!id) return
  try {
    stats.value = await api.get<MonitorStats>(`/api/monitors/${id}/stats`)
  } catch { stats.value = null }
}

async function loadCert(): Promise<void> {
  const id = route.params.id
  if (!id) return
  try {
    cert.value = await api.get<SSLCertificate>(`/api/monitors/${id}/certificate`)
  } catch { cert.value = null }
}

async function checkNow(): Promise<void> {
  const id = route.params.id
  if (!id) return
  checking.value = true
  try {
    await api.post(`/api/monitors/${id}/check`)
    await loadAll()
  } catch (err) {
    error.value = humanizeError(err, '立即检查失败')
  } finally {
    checking.value = false
  }
}

function render(): void {
  if (!chart.value || results.value.length === 0) return
  const c = themeColors()
  // 把不通过的采样点单独画成红点，一眼看出什么时候挂的
  const okData: Array<[number, number]> = []
  const failData: Array<[number, number]> = []
  for (const r of results.value) {
    const t = new Date(r.checked_at).getTime()
    if (r.ok) okData.push([t, r.latency_ms ?? 0])
    else failData.push([t, 0])
  }
  const opt: EChartsOption = {
    grid: baseGrid({ top: 14, right: 12, bottom: 18, left: 8 }),
    tooltip: { ...baseTooltip(), valueFormatter: (v) => formatMs(Number(v)) },
    xAxis: { type: 'time', ...baseAxis() },
    yAxis: { type: 'value', ...baseAxis(), axisLabel: { ...baseAxis().axisLabel, formatter: (v: string) => formatMs(Number(v)) } },
    series: [
      { type: 'line', name: '响应时间', data: okData, showSymbol: false,
        smooth: 0.2, smoothMonotone: 'x', lineStyle: { width: 1.5, color: c.palette[0] },
        areaStyle: { color: c.palette[0], opacity: 0.08 } },
      { type: 'scatter', name: '失败', data: failData, symbolSize: 6,
        itemStyle: { color: c.critical } },
    ],
  }
  chart.value.setOption(opt, false)
}

onMounted(async () => {
  await loadAll()
  if (chartRef.value) {
    chart.value = await createChart(chartRef.value)
    unbindResize = bindResize(chart.value, chartRef.value)
  }
  unbindTheme = onThemeChange(render)
  render()
})

onBeforeUnmount(() => {
  unbindResize?.(); unbindTheme?.(); chart.value?.dispose()
})

watch(range, () => void loadResults())
</script>

<template>
  <div class="detail">
    <p v-if="error" class="error">{{ error }}</p>

    <template v-if="monitor">
      <CardBox>
        <div class="head">
          <StatusDot :status="monitor.status" :size="10" />
          <div class="head-main">
            <h2 class="head-title">{{ monitor.name }}</h2>
            <div class="head-meta">
              <span class="type-tag">{{ monitorTypeText(monitor.type) }}</span>
              <span class="font-mono truncate">{{ monitor.target }}</span>
              <span class="text-tertiary">最后检查 {{ formatRelative(monitor.last_checked_at) }}</span>
            </div>
          </div>
          <button class="btn-ghost" :disabled="checking" @click="checkNow">
            {{ checking ? '检查中…' : '立即检查' }}
          </button>
        </div>
      </CardBox>

      <div v-if="stats" class="stats">
        <div class="stat">
          <span class="stat-val font-num">
            {{ stats.sample_insufficient ? '—' : formatPercent(stats.uptime_percent, 2) }}
          </span>
          <span class="stat-label">{{ stats.sample_insufficient ? '样本不足' : '可用率' }}</span>
        </div>
        <div class="stat">
          <span class="stat-val font-num">{{ formatMs(stats.latency_p50) }}</span>
          <span class="stat-label">P50</span>
        </div>
        <div class="stat">
          <span class="stat-val font-num">{{ formatMs(stats.latency_p95) }}</span>
          <span class="stat-label">P95</span>
        </div>
        <div class="stat">
          <span class="stat-val font-num">{{ formatMs(stats.latency_p99) }}</span>
          <span class="stat-label">P99</span>
        </div>
      </div>

      <CardBox v-if="cert" title="SSL 证书">
        <div class="cert">
          <div class="cert-row">
            <span class="cert-label">颁发者</span>
            <span class="cert-val">{{ cert.issuer || '—' }}</span>
          </div>
          <div class="cert-row">
            <span class="cert-label">到期时间</span>
            <span class="cert-val">{{ formatDateTime(cert.not_after) }}</span>
          </div>
          <div class="cert-row">
            <span class="cert-label">剩余</span>
            <span class="cert-val font-num" :class="daysTone">
              {{ cert.days_left !== null && cert.days_left !== undefined ? `${cert.days_left} 天` : '—' }}
            </span>
          </div>
        </div>
      </CardBox>

      <CardBox :padded="false">
        <template #actions>
          <div class="ranges">
            <button v-for="r in ['24h','7d','30d'] as const" :key="r" class="range-btn"
              :class="{ active: range === r }" @click="range = r">{{ r }}</button>
          </div>
        </template>
        <div class="chart-block">
          <span class="chart-title">响应时间</span>
          <div v-if="results.length === 0" class="no-data">暂无数据</div>
          <div v-else ref="chartRef" class="chart" />
        </div>
      </CardBox>

      <CardBox v-if="recent.length > 0" title="最近检查" :padded="false">
        <ul class="checks">
          <li v-for="r in recent" :key="r.id" class="check" :class="{ fail: !r.ok }">
            <StatusDot :status="r.ok ? 'up' : 'down'" :size="6" />
            <span class="check-time">{{ formatDateTime(r.checked_at) }}</span>
            <span class="check-reason" :class="r.ok ? 'ok' : 'bad'">
              {{ r.ok ? '正常' : failReasonText(r.reason) }}
            </span>
            <span v-if="r.status_code" class="check-code font-num">{{ r.status_code }}</span>
            <span v-if="r.latency_ms" class="check-latency font-num">{{ formatMs(r.latency_ms) }}</span>
            <span v-if="r.error_detail" class="check-detail truncate" :title="r.error_detail">
              {{ r.error_detail }}
            </span>
          </li>
        </ul>
      </CardBox>
    </template>
  </div>
</template>

<style scoped>
.detail { display: flex; flex-direction: column; gap: var(--space-4); }
.error { padding: var(--space-3); background: var(--critical-subtle);
  color: var(--critical); border-radius: var(--radius-md); font-size: var(--font-sm); }

.head { display: flex; align-items: flex-start; gap: var(--space-3); }
.head-main { flex: 1; min-width: 0; }
.head-title { font-size: var(--font-lg); margin-bottom: var(--space-1); }
.head-meta { display: flex; align-items: center; gap: var(--space-2); flex-wrap: wrap;
  font-size: var(--font-xs); color: var(--text-tertiary); }
.type-tag { padding: 2px 6px; border-radius: var(--radius-sm);
  background: var(--bg-hover); color: var(--text-secondary); }

.btn-ghost { flex-shrink: 0; height: 30px; padding: 0 var(--space-3);
  border: 1px solid var(--line-color); border-radius: var(--radius-md);
  background: transparent; color: var(--text-secondary);
  font-size: var(--font-sm); font-family: inherit; cursor: pointer; }
.btn-ghost:hover:not(:disabled) { background: var(--bg-hover); }
.btn-ghost:disabled { opacity: 0.6; cursor: not-allowed; }

.stats { display: grid; grid-template-columns: repeat(4, 1fr); gap: var(--space-3); }
.stat { display: flex; flex-direction: column; gap: 2px; padding: var(--space-4);
  background: var(--bg-surface); border: 1px solid var(--line-color);
  border-radius: var(--radius-lg); }
.stat-val { font-size: var(--font-lg); font-weight: 600; }
.stat-label { font-size: var(--font-xs); color: var(--text-tertiary); }

.cert { display: flex; flex-direction: column; gap: var(--space-3); }
.cert-row { display: flex; gap: var(--space-4); font-size: var(--font-sm); }
.cert-label { width: 70px; flex-shrink: 0; color: var(--text-tertiary); font-size: var(--font-xs); }
.cert-val { color: var(--text-primary); min-width: 0; overflow: hidden;
  text-overflow: ellipsis; white-space: nowrap; }
.daysTone { color: var(--down); }
.daysTone.warn { color: var(--warning); }
.daysTone.crit { color: var(--critical); }

.ranges { display: flex; gap: 2px; background: var(--bg-hover);
  padding: 2px; border-radius: var(--radius-md); }
.range-btn { border: none; background: transparent; color: var(--text-tertiary);
  font-size: var(--font-xs); font-family: inherit; padding: 3px 8px;
  border-radius: var(--radius-sm); cursor: pointer; }
.range-btn.active { background: var(--bg-surface); color: var(--text-primary); }

.chart-block { padding: var(--space-4) var(--space-5); }
.chart-title { display: block; font-size: var(--font-xs);
  color: var(--text-tertiary); margin-bottom: var(--space-2); }
.chart { height: 200px; width: 100%; }
.no-data { padding: var(--space-7); text-align: center;
  color: var(--text-tertiary); font-size: var(--font-sm); }

.checks { list-style: none; margin: 0; padding: 0; }
.check { display: flex; align-items: center; gap: var(--space-3);
  padding: var(--space-2) var(--space-5); border-bottom: 1px solid var(--line-color);
  font-size: var(--font-sm); }
.check:last-child { border-bottom: none; }
.check-time { color: var(--text-tertiary); font-size: var(--font-xs);
  width: 150px; flex-shrink: 0; }
.check-reason { font-size: var(--font-xs); width: 90px; flex-shrink: 0; }
.check-reason.ok { color: var(--down); }
.check-reason.bad { color: var(--critical); }
.check-code { font-size: var(--font-xs); color: var(--text-tertiary); width: 40px; }
.check-latency { font-size: var(--font-xs); color: var(--text-secondary); width: 60px; }
.check-detail { font-size: var(--font-xs); color: var(--text-tertiary);
  flex: 1; min-width: 0; }

@media (max-width: 767px) {
  .stats { grid-template-columns: repeat(2, 1fr); }
  .check-time { width: auto; }
  .check-code, .check-latency { display: none; }
  .check { padding: var(--space-2) var(--space-3); }
}
</style>
