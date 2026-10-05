<script setup lang="ts">
import { onMounted, ref } from 'vue'
import CardBox from '@/components/CardBox.vue'
import EmptyState from '@/components/EmptyState.vue'
import Skeleton from '@/components/Skeleton.vue'
import { api, humanizeError } from '@/api/client'
import { formatDateTime } from '@/utils/format'
import type { AuditLog } from '@/api/types'

const logs = ref<AuditLog[]>([])
const total = ref(0)
const page = ref(1)
const size = 50
const loading = ref(true)
const error = ref('')
const actionFilter = ref('')
const expanded = ref<number | null>(null)

/** 审计动作 → 中文。未知动作直接显示原值，不猜。 */
const ACTION_LABEL: Record<string, string> = {
  login: '登录',
  logout: '登出',
  create_node: '创建节点',
  update_node: '修改节点',
  delete_node: '删除节点',
  rotate_secret: '重置密钥',
  create_monitor: '创建监控',
  update_monitor: '修改监控',
  delete_monitor: '删除监控',
  create_channel: '创建通知通道',
  update_channel: '修改通知通道',
  delete_channel: '删除通知通道',
  create_rule: '创建告警规则',
  update_rule: '修改告警规则',
  delete_rule: '删除告警规则',
  create_user: '创建用户',
  update_user: '修改用户',
  delete_user: '删除用户',
  update_visibility_policy: '修改可见性策略',
  'node.hardware_changed': '节点硬件变更',
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const params = new URLSearchParams()
    params.set('page', String(page.value))
    params.set('size', String(size))
    if (actionFilter.value) params.set('action', actionFilter.value)
    const res = await api.get<{ items: AuditLog[]; total: number }>(`/api/audit-logs?${params}`)
    logs.value = res.items
    total.value = res.total
  } catch (err) {
    error.value = humanizeError(err, '加载审计日志失败')
  } finally {
    loading.value = false
  }
}

function search(): void { page.value = 1; void load() }
function turn(delta: number): void { page.value += delta; void load() }

const totalPages = () => Math.max(1, Math.ceil(total.value / size))

onMounted(load)
</script>

<template>
  <CardBox title="审计日志" :padded="false">
    <template #actions>
      <div class="toolbar">
        <input
          v-model="actionFilter"
          type="search"
          class="search"
          aria-label="按动作过滤审计日志"
          placeholder="按动作过滤，如 create_node"
          @keyup.enter="search"
        />
        <button class="btn-ghost" @click="search">查询</button>
      </div>
    </template>

    <p v-if="error" class="msg-err">{{ error }}</p>

    <Skeleton v-if="loading && logs.length === 0" type="table" :cols="6" :table-rows="8" />

    <EmptyState v-else-if="logs.length === 0" icon="search"
      title="没有审计记录" description="所有写操作都会记录在这里" />

    <div v-else class="table-wrap">
      <table class="table">
        <thead>
          <tr>
            <th>时间</th><th>操作者</th><th>动作</th>
            <th>目标</th><th>来源 IP</th><th></th>
          </tr>
        </thead>
        <tbody>
          <template v-for="l in logs" :key="l.id">
            <tr class="row" @click="expanded = expanded === l.id ? null : l.id">
              <td class="text-tertiary">{{ formatDateTime(l.created_at) }}</td>
              <td>{{ l.username || '—' }}</td>
              <td>{{ ACTION_LABEL[l.action] || l.action }}</td>
              <td class="truncate">
                <span v-if="l.target_type" class="target">
                  {{ l.target_type }}<span v-if="l.target_id">#{{ l.target_id }}</span>
                </span>
                <span v-else class="text-tertiary">—</span>
              </td>
              <td class="font-mono text-tertiary">{{ l.ip || '—' }}</td>
              <td class="exp-cell">
                <span v-if="l.detail" class="exp">{{ expanded === l.id ? '收起' : '详情' }}</span>
              </td>
            </tr>
            <tr v-if="expanded === l.id && l.detail" :key="`${l.id}-d`" class="detail-row">
              <td colspan="6">
                <pre class="detail">{{ JSON.stringify(l.detail, null, 2) }}</pre>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
    </div>

    <div class="pager">
      <span class="text-tertiary">共 {{ total }} 条 · 第 {{ page }} / {{ totalPages() }} 页</span>
      <div class="pager-btns">
        <button class="btn-ghost" :disabled="page <= 1" @click="turn(-1)">上一页</button>
        <button class="btn-ghost" :disabled="page >= totalPages()" @click="turn(1)">下一页</button>
      </div>
    </div>
  </CardBox>
</template>

<style scoped>
.toolbar { display: flex; gap: var(--space-2); }
.search { height: 30px; width: 200px; padding: 0 var(--space-3);
  border: 1px solid var(--line-color); border-radius: var(--radius-md);
  background: var(--bg-body); color: var(--text-primary);
  font-size: var(--font-sm); font-family: inherit; }
.search:focus { outline: none; border-color: var(--accent); }
.btn-ghost { height: 30px; padding: 0 var(--space-3); border: 1px solid var(--line-color);
  border-radius: var(--radius-md); background: transparent; color: var(--text-secondary);
  font-size: var(--font-xs); font-family: inherit; cursor: pointer; }
.btn-ghost:hover:not(:disabled) { background: var(--bg-hover); }
.btn-ghost:disabled { opacity: 0.5; cursor: not-allowed; }

.msg-err { margin: var(--space-4); padding: var(--space-2) var(--space-3);
  background: var(--critical-subtle); color: var(--critical);
  border-radius: var(--radius-md); font-size: var(--font-sm); }

.table-wrap { overflow-x: auto; }
.table { width: 100%; border-collapse: collapse; font-size: var(--font-sm); }
.table th { text-align: left; font-size: var(--font-xs); font-weight: 500;
  color: var(--text-tertiary); padding: var(--space-2) var(--space-4);
  border-bottom: 1px solid var(--line-color); white-space: nowrap; }

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
.table td { padding: var(--space-2) var(--space-4);
  border-bottom: 1px solid var(--line-color); }
.row { cursor: pointer; transition: background-color var(--duration-fast) var(--ease-out); }
.row:hover { background: var(--bg-hover); }
.target { font-size: var(--font-xs); color: var(--text-secondary); }
.exp-cell { text-align: right; width: 60px; }
.exp { font-size: var(--font-xs); color: var(--accent); }

.detail-row td { padding: 0; background: var(--bg-subtle); }
.detail { margin: 0; padding: var(--space-3) var(--space-4);
  font-size: var(--font-xs); line-height: 1.6; overflow-x: auto;
  max-height: 240px; overflow-y: auto; color: var(--text-secondary); }

.pager { display: flex; align-items: center; justify-content: space-between;
  padding: var(--space-3) var(--space-4); border-top: 1px solid var(--line-color);
  font-size: var(--font-xs); }
.pager-btns { display: flex; gap: var(--space-2); }

@media (max-width: 767px) {
  .search { width: 140px; }
  .table th:nth-child(5), .table td:nth-child(5),
  .table th:nth-child(4), .table td:nth-child(4) { display: none; }
}
</style>
