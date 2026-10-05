<script setup lang="ts">
import { onMounted, ref } from 'vue'
import CardBox from '@/components/CardBox.vue'
import EmptyState from '@/components/EmptyState.vue'
import { api, humanizeError } from '@/api/client'
import { useAlertStore } from '@/stores/alert'
import { formatDateTime } from '@/utils/format'

const store = useAlertStore()
const error = ref('')
const message = ref('')

const METRICS = [
  { v: 'cpu_usage', label: 'CPU 使用率 (%)' },
  { v: 'mem_usage', label: '内存使用率 (%)' },
  { v: 'load1', label: '系统负载 (1 分钟)' },
  { v: 'disk_max', label: '磁盘最大占用率 (%)' },
  { v: 'net_rx', label: '网络下行 (B/s)' },
  { v: 'net_tx', label: '网络上行 (B/s)' },
]
const OPS = [
  { v: '>', label: '大于' },
  { v: '>=', label: '大于等于' },
  { v: '<', label: '小于' },
  { v: '<=', label: '小于等于' },
  { v: '==', label: '等于' },
  { v: '!=', label: '不等于' },
]
const SEVERITIES = [
  { v: 'info', label: '提示' },
  { v: 'warning', label: '警告' },
  { v: 'critical', label: '严重' },
]

const name = ref('')
const metric = ref('cpu_usage')
const op = ref('>')
const threshold = ref(90)
const forTimes = ref(3)
const forMinutes = ref(2)
const severity = ref('warning')
const dedupSec = ref(1800)
const enabled = ref(true)
const selectedChannels = ref<number[]>([])
const creating = ref(false)

async function create(): Promise<void> {
  if (!name.value.trim()) { error.value = '请填写规则名称'; return }
  creating.value = true
  error.value = ''
  try {
    await api.post('/api/alert-rules', {
      name: name.value.trim(),
      target_type: 'node',
      metric: metric.value,
      condition: {
        op: op.value, value: threshold.value,
        for_times: forTimes.value, for_minutes: forMinutes.value,
      },
      severity: severity.value,
      channel_ids: selectedChannels.value,
      dedup_window_sec: dedupSec.value,
      enabled: enabled.value,
    })
    message.value = `规则「${name.value}」已创建`
    name.value = ''
    await store.fetchRules()
  } catch (err) {
    error.value = humanizeError(err, '创建规则失败')
  } finally {
    creating.value = false
  }
}

async function remove(id: number, ruleName: string): Promise<void> {
  if (!window.confirm(`确定删除规则「${ruleName}」？`)) return
  try {
    await api.delete(`/api/alert-rules/${id}`)
    store.rules = store.rules.filter((r) => r.id !== id)
  } catch (err) {
    error.value = humanizeError(err, '删除失败')
  }
}

function channelNames(ids: number[]): string {
  if (!ids.length) return '未配置'
  return ids.map((id) => store.channels.find((c) => c.id === id)?.name ?? `#${id}`).join('、')
}

onMounted(async () => {
  await Promise.all([store.fetchRules(), store.fetchChannels()])
})
</script>

<template>
  <div class="rules">
    <p v-if="message" class="msg-ok">{{ message }}</p>
    <p v-if="error" class="msg-err">{{ error }}</p>

    <CardBox title="已有规则" :padded="false">
      <EmptyState v-if="store.rules.length === 0" icon="alerts"
        title="还没有告警规则" description="规则决定什么情况下触发通知" />
      <ul v-else class="list">
        <li v-for="r in store.rules" :key="r.id" class="item">
          <div class="item-main">
            <div class="item-head">
              <span class="item-name">{{ r.name }}</span>
              <span class="sev-tag" :class="`sev-${r.severity}`">
                {{ SEVERITIES.find((s) => s.v === r.severity)?.label }}
              </span>
              <span v-if="!r.enabled" class="off-tag">已停用</span>
            </div>
            <p class="item-cond">
              {{ METRICS.find((m) => m.v === r.metric)?.label || r.metric }}
              {{ OPS.find((o) => o.v === r.condition.op)?.label }}
              {{ r.condition.value }}
              <span v-if="r.condition.for_times > 0" class="text-tertiary">
                （连续 {{ r.condition.for_times }} 次 / {{ r.condition.for_minutes }} 分钟）
              </span>
            </p>
            <p class="item-chan text-tertiary">通知到：{{ channelNames(r.channel_ids) }}</p>
          </div>
          <button class="btn-del" @click="remove(r.id, r.name)">删除</button>
        </li>
      </ul>
    </CardBox>

    <CardBox title="新建规则">
      <form class="form" @submit.prevent="create">
        <div class="field">
          <label for="rname">规则名称</label>
          <input id="rname" v-model="name" type="text" placeholder="如：CPU 持续过高" required />
        </div>

        <div class="row">
          <div class="field grow">
            <label for="rmetric">监控指标</label>
            <select id="rmetric" v-model="metric">
              <option v-for="m in METRICS" :key="m.v" :value="m.v">{{ m.label }}</option>
            </select>
          </div>
          <div class="field">
            <label for="rop">条件</label>
            <select id="rop" v-model="op">
              <option v-for="o in OPS" :key="o.v" :value="o.v">{{ o.label }}</option>
            </select>
          </div>
          <div class="field">
            <label for="rth">阈值</label>
            <input id="rth" v-model.number="threshold" type="number" />
          </div>
        </div>

        <div class="row">
          <div class="field grow">
            <label for="rft">连续命中次数</label>
            <input id="rft" v-model.number="forTimes" type="number" min="0" max="100" />
            <span class="hint">0 表示不限制。次数越多越能过滤瞬时抖动</span>
          </div>
          <div class="field grow">
            <label for="rfm">持续分钟数</label>
            <input id="rfm" v-model.number="forMinutes" type="number" min="0" max="1440" />
            <span class="hint">0 表示不限制</span>
          </div>
          <div class="field">
            <label for="rsev">级别</label>
            <select id="rsev" v-model="severity">
              <option v-for="s in SEVERITIES" :key="s.v" :value="s.v">{{ s.label }}</option>
            </select>
          </div>
        </div>

        <div class="row">
          <div class="field grow">
            <label for="rdw">去重窗口（秒）</label>
            <input id="rdw" v-model.number="dedupSec" type="number" min="0" />
            <span class="hint">同一次异常的持续期间只通知一次</span>
          </div>
        </div>

        <div class="field">
          <label>通知到</label>
          <div v-if="store.channels.length === 0" class="no-chan">
            还没有通知通道，创建规则后不会实际发出通知
          </div>
          <div v-else class="chan-list">
            <label v-for="c in store.channels" :key="c.id" class="check">
              <input type="checkbox" :value="c.id" v-model="selectedChannels" />
              <span>{{ c.name }}</span>
            </label>
          </div>
        </div>

        <div class="row">
          <label class="check"><input v-model="enabled" type="checkbox" />
            <span>创建后立即启用</span></label>
        </div>

        <div class="actions">
          <button type="submit" class="btn-primary" :disabled="creating">
            {{ creating ? '创建中…' : '创建规则' }}
          </button>
        </div>
      </form>
    </CardBox>
  </div>
</template>

<style scoped>
.rules { display: flex; flex-direction: column; gap: var(--space-4); }
.msg-ok { padding: var(--space-2) var(--space-3); background: var(--down-subtle);
  color: var(--down); border-radius: var(--radius-md); font-size: var(--font-sm); }
.msg-err { padding: var(--space-2) var(--space-3); background: var(--critical-subtle);
  color: var(--critical); border-radius: var(--radius-md); font-size: var(--font-sm); }

.list { list-style: none; margin: 0; padding: 0; }
.item { display: flex; align-items: center; gap: var(--space-3);
  padding: var(--space-3) var(--space-5); border-bottom: 1px solid var(--line-color); }
.item:last-child { border-bottom: none; }
.item-main { flex: 1; min-width: 0; }
.item-head { display: flex; align-items: center; gap: var(--space-2); flex-wrap: wrap; }
.item-name { font-size: var(--font-sm); font-weight: 500; }
.sev-tag { font-size: 10px; padding: 2px 6px; border-radius: var(--radius-sm);
  background: var(--neutral-subtle); color: var(--text-secondary); }
.sev-tag.sev-warning { background: var(--warning-subtle); color: var(--warning); }
.sev-tag.sev-critical { background: var(--critical-subtle); color: var(--critical); }
.off-tag { font-size: 10px; padding: 2px 6px; border-radius: var(--radius-sm);
  background: var(--neutral-subtle); color: var(--text-tertiary); }
.item-cond { font-size: var(--font-xs); color: var(--text-secondary); margin-top: 2px; }
.item-chan { font-size: var(--font-xs); margin-top: 1px; }

.btn-del { flex-shrink: 0; height: 26px; padding: 0 var(--space-2);
  border: none; background: transparent; color: var(--text-tertiary);
  font-size: var(--font-xs); font-family: inherit; cursor: pointer;
  border-radius: var(--radius-sm); }
.btn-del:hover { background: var(--critical-subtle); color: var(--critical); }

.form { display: flex; flex-direction: column; gap: var(--space-4); }
.row { display: flex; gap: var(--space-3); align-items: flex-end; }
.field { display: flex; flex-direction: column; gap: var(--space-2); }
.field.grow { flex: 1; }
.field label { font-size: var(--font-sm); color: var(--text-secondary); font-weight: 500; }
.field input, .field select { height: 36px; padding: 0 var(--space-3);
  border: 1px solid var(--line-color); border-radius: var(--radius-md);
  background: var(--bg-body); color: var(--text-primary);
  font-size: var(--font-sm); font-family: inherit; }
.field input:focus, .field select:focus { outline: none; border-color: var(--accent); }
.hint { font-size: var(--font-xs); color: var(--text-tertiary); }
.check { display: flex; align-items: center; gap: var(--space-2);
  font-size: var(--font-sm); color: var(--text-secondary); cursor: pointer; }
.chan-list { display: flex; flex-wrap: wrap; gap: var(--space-3); }
.no-chan { font-size: var(--font-xs); color: var(--warning);
  padding: var(--space-2) var(--space-3); background: var(--warning-subtle);
  border-radius: var(--radius-md); }

.actions { display: flex; justify-content: flex-end; }
.btn-primary { height: 34px; padding: 0 var(--space-4); border: none;
  border-radius: var(--radius-md); background: var(--accent); color: #fff;
  font-size: var(--font-sm); font-family: inherit; font-weight: 500; cursor: pointer; }
.btn-primary:hover:not(:disabled) { background: var(--accent-hover); }
.btn-primary:disabled { opacity: 0.6; cursor: not-allowed; }

@media (max-width: 767px) {
  .row { flex-direction: column; align-items: stretch; }
  .item { padding: var(--space-3); }
}
</style>
