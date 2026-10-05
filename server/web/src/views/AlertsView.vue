<script setup lang="ts">
/** 告警中心 */
import { onMounted, ref } from 'vue'
import CardBox from '@/components/CardBox.vue'
import StatusDot from '@/components/StatusDot.vue'
import EmptyState from '@/components/EmptyState.vue'
import { useAlertStore } from '@/stores/alert'
import { humanizeError } from '@/api/client'
import { formatDateTime, formatRelative, severityText } from '@/utils/format'

const store = useAlertStore()
const acking = ref<number | null>(null)

async function ack(id: number): Promise<void> {
  acking.value = id
  try {
    await store.ack(id)
  } catch (err) {
    window.alert(humanizeError(err, '确认失败'))
  } finally {
    acking.value = null
  }
}

onMounted(() => void store.fetchEvents())
</script>

<template>
  <CardBox :padded="false">
    <template #actions>
      <div class="filters">
        <button
          class="chip" :class="{ active: store.statusFilter === 'firing' }"
          @click="store.setFilter({ status: 'firing' }); store.fetchEvents()"
        >未处理</button>
        <button
          class="chip" :class="{ active: store.statusFilter === 'resolved' }"
          @click="store.setFilter({ status: 'resolved' }); store.fetchEvents()"
        >已恢复</button>
        <button
          class="chip" :class="{ active: store.statusFilter === 'acked' }"
          @click="store.setFilter({ status: 'acked' }); store.fetchEvents()"
        >已确认</button>
        <button
          class="chip" :class="{ active: store.statusFilter === 'all' }"
          @click="store.setFilter({ status: 'all' }); store.fetchEvents()"
        >全部</button>
      </div>
    </template>

    <EmptyState
      v-if="store.events.length === 0"
      icon="alerts"
      :title="store.statusFilter === 'firing' ? '当前没有未处理的告警' : '没有告警记录'"
      description="告警触发时会在这里显示，并按规则推送到配置的通知通道"
    />

    <ul v-else class="list">
      <li v-for="ev in store.events" :key="ev.id" class="item" :class="`sev-${ev.severity}`">
        <span class="sev-bar" />
        <div class="item-main">
          <div class="item-head">
            <span class="sev-tag">{{ severityText(ev.severity) }}</span>
            <span class="item-target truncate">{{ ev.target_name }}</span>
            <span class="item-status" :class="`st-${ev.status}`">
              {{ ev.status === 'firing' ? '未处理' : ev.status === 'acked' ? '已确认' : '已恢复' }}
            </span>
          </div>
          <p v-if="ev.message" class="item-msg">{{ ev.message }}</p>
          <div class="item-meta">
            <span>{{ formatDateTime(ev.first_fired_at) }}</span>
            <span class="text-tertiary">{{ formatRelative(ev.first_fired_at) }}</span>
            <span v-if="!ev.notified" class="silenced-tag">静默中</span>
          </div>
        </div>
        <button
          v-if="ev.status === 'firing'"
          class="ack-btn"
          :disabled="acking === ev.id"
          @click="ack(ev.id)"
        >
          {{ acking === ev.id ? '处理中' : '确认' }}
        </button>
      </li>
    </ul>
  </CardBox>
</template>

<style scoped>
.filters { display: flex; gap: 2px; background: var(--bg-hover); padding: 2px; border-radius: var(--radius-md); }
.chip { border: none; background: transparent; color: var(--text-tertiary); font-size: var(--font-xs);
  font-family: inherit; padding: 3px 10px; border-radius: var(--radius-sm); cursor: pointer; }
.chip.active { background: var(--bg-surface); color: var(--text-primary); }

.list { list-style: none; margin: 0; padding: 0; }
.item { display: flex; align-items: flex-start; gap: var(--space-3);
  padding: var(--space-4) var(--space-5); border-bottom: 1px solid var(--line-color); }
.item:last-child { border-bottom: none; }
.item:hover { background: var(--bg-hover); }

.sev-bar { width: 2px; align-self: stretch; border-radius: 1px; flex-shrink: 0; background: var(--neutral); }
.sev-warning .sev-bar { background: var(--warning); }
.sev-critical .sev-bar { background: var(--critical); }

.item-main { flex: 1; min-width: 0; }
.item-head { display: flex; align-items: center; gap: var(--space-2); flex-wrap: wrap; }
.sev-tag { font-size: 10px; padding: 2px 6px; border-radius: var(--radius-sm);
  background: var(--neutral-subtle); color: var(--text-secondary); }
.sev-warning .sev-tag { background: var(--warning-subtle); color: var(--warning); }
.sev-critical .sev-tag { background: var(--critical-subtle); color: var(--critical); }

.item-target { font-size: var(--font-sm); font-weight: 500; }
.item-status { font-size: var(--font-xs); color: var(--text-tertiary); }
.st-firing { color: var(--critical); }
.st-resolved { color: var(--down); }

.item-msg { margin-top: var(--space-1); font-size: var(--font-sm); color: var(--text-secondary); line-height: 1.5; }
.item-meta { display: flex; gap: var(--space-3); margin-top: var(--space-1);
  font-size: var(--font-xs); color: var(--text-tertiary); flex-wrap: wrap; }
.silenced-tag { color: var(--warning); }

.ack-btn { flex-shrink: 0; height: 26px; padding: 0 var(--space-3);
  border: 1px solid var(--line-color); border-radius: var(--radius-md);
  background: transparent; color: var(--text-secondary); font-size: var(--font-xs);
  font-family: inherit; cursor: pointer; }
.ack-btn:hover:not(:disabled) { background: var(--bg-hover); color: var(--text-primary); }
.ack-btn:disabled { opacity: 0.5; cursor: not-allowed; }

@media (max-width: 767px) {
  .item { padding: var(--space-3); }
  .filters { flex-wrap: wrap; }
}
</style>
