<script setup lang="ts">
/**
 * 公开状态页（免鉴权）
 *
 * 关键：这里展示的字段全部经过服务端脱敏层过滤，
 * 前端**不能**自行判断该不该显示什么——那是服务端的职责（PRD 3.6.4）。
 */
import { onMounted, ref } from 'vue'
import { api } from '@/api/client'
import { useThemeStore } from '@/stores/theme'
import { formatRelative, formatPercent } from '@/utils/format'

interface PublicMonitor {
  id: number
  name: string
  status: string
  uptime_30d?: number | null
  avg_latency_ms?: number | null
  last_checked_at?: string | null
}

const theme = useThemeStore()
const monitors = ref<PublicMonitor[]>([])
const loading = ref(true)
const error = ref('')

const upCount = () => monitors.value.filter((m) => m.status === 'up').length
const downCount = () => monitors.value.filter((m) => m.status === 'down').length

const overall = () => {
  if (monitors.value.length === 0) return { tone: 'unknown', text: '暂无数据' }
  if (downCount() > 0) return { tone: 'down', text: '部分服务异常' }
  return { tone: 'up', text: '全部正常' }
}

onMounted(async () => {
  theme.init()
  try {
    monitors.value = await api.get<PublicMonitor[]>('/api/status/monitors')
  } catch {
    error.value = '无法获取状态信息'
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div class="status-page">
    <header class="head">
      <div class="head-inner">
        <div class="brand">
          <svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor"
            stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M3 12h4l3-7 4 14 3-7h4" />
          </svg>
          <span>服务状态</span>
        </div>
        <button class="theme-btn" @click="theme.toggle()" aria-label="切换主题">
          {{ theme.resolved === 'dark' ? '浅色' : '深色' }}
        </button>
      </div>
    </header>

    <main class="body">
      <div class="overall" :class="`tone-${overall().tone}`">
        <span class="overall-dot" />
        <div>
          <p class="overall-text">{{ overall().text }}</p>
          <p v-if="monitors.length > 0" class="overall-sub">
            {{ upCount() }} / {{ monitors.length }} 项正常
          </p>
        </div>
      </div>

      <p v-if="error" class="error">{{ error }}</p>
      <p v-else-if="loading" class="loading">加载中…</p>
      <p v-else-if="monitors.length === 0" class="loading">暂无可展示的监控项</p>

      <ul v-else class="list">
        <li v-for="m in monitors" :key="m.id" class="item">
          <span class="dot" :class="`is-${m.status}`" />
          <div class="item-info">
            <span class="item-name">{{ m.name }}</span>
            <span class="item-time">最后检查 {{ formatRelative(m.last_checked_at) }}</span>
          </div>
          <div class="item-metrics">
            <span v-if="m.avg_latency_ms" class="metric font-num">{{ m.avg_latency_ms }}ms</span>
            <span
              v-if="m.uptime_30d !== null && m.uptime_30d !== undefined"
              class="metric font-num" :class="m.uptime_30d >= 99.9 ? 'ok' : 'warn'"
            >
              {{ formatPercent(m.uptime_30d, 2) }}
            </span>
          </div>
        </li>
      </ul>
    </main>

    <footer class="foot">
      <span>由 ProbeOne 驱动</span>
    </footer>
  </div>
</template>

<style scoped>
.status-page { min-height: 100vh; display: flex; flex-direction: column; background: var(--bg-body); }
.head { border-bottom: 1px solid var(--line-color); background: var(--bg-surface); }
.head-inner { max-width: 720px; margin: 0 auto; padding: var(--space-4) var(--space-5);
  display: flex; align-items: center; justify-content: space-between; }
.brand { display: flex; align-items: center; gap: var(--space-2);
  font-size: var(--font-md); font-weight: 600; color: var(--accent); }
.theme-btn { border: 1px solid var(--line-color); border-radius: var(--radius-md);
  background: transparent; color: var(--text-secondary); font-size: var(--font-xs);
  font-family: inherit; padding: 4px 10px; cursor: pointer; }
.theme-btn:hover { background: var(--bg-hover); }

.body { flex: 1; max-width: 720px; width: 100%; margin: 0 auto;
  padding: var(--space-6) var(--space-5); }

.overall { display: flex; align-items: center; gap: var(--space-4);
  padding: var(--space-5); border: 1px solid var(--line-color);
  border-radius: var(--radius-lg); background: var(--bg-surface); margin-bottom: var(--space-5); }
.overall-dot { width: 10px; height: 10px; border-radius: 50%; background: var(--neutral); flex-shrink: 0; }
.tone-up .overall-dot { background: var(--down); }
.tone-down .overall-dot { background: var(--critical); }
.overall-text { font-size: var(--font-md); font-weight: 600; }
.tone-down .overall-text { color: var(--critical); }
.overall-sub { font-size: var(--font-xs); color: var(--text-tertiary); margin-top: 2px; }

.loading, .error { text-align: center; padding: var(--space-7); color: var(--text-tertiary);
  font-size: var(--font-sm); }
.error { color: var(--critical); }

.list { list-style: none; margin: 0; padding: 0; background: var(--bg-surface);
  border: 1px solid var(--line-color); border-radius: var(--radius-lg); overflow: hidden; }
.item { display: flex; align-items: center; gap: var(--space-3);
  padding: var(--space-4) var(--space-5); border-bottom: 1px solid var(--line-color); }
.item:last-child { border-bottom: none; }
.dot { width: 8px; height: 8px; border-radius: 50%; background: var(--neutral); flex-shrink: 0; }
.dot.is-up { background: var(--down); }
.dot.is-down { background: var(--critical); }
.item-info { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 1px; }
.item-name { font-size: var(--font-sm); font-weight: 500; }
.item-time { font-size: var(--font-xs); color: var(--text-tertiary); }
.item-metrics { display: flex; gap: var(--space-4); flex-shrink: 0; }
.metric { font-size: var(--font-sm); color: var(--text-secondary); }
.metric.ok { color: var(--down); }
.metric.warn { color: var(--warning); }

.foot { text-align: center; padding: var(--space-5);
  font-size: var(--font-xs); color: var(--text-tertiary); border-top: 1px solid var(--line-color); }

@media (max-width: 767px) {
  .body { padding: var(--space-4) var(--space-3); }
  .head-inner { padding: var(--space-3); }
  .item { padding: var(--space-3); }
  .item-metrics { gap: var(--space-3); }
}
</style>
