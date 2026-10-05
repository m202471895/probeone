<script setup lang="ts">
/**
 * 可见性策略
 *
 * 这一页是 PRD 3.6 决策的落地界面。设计要点：
 *  - 顶部固定警示条：把公网 IP 设为可见会让攻击者直接定位资产
 *  - 硬禁止字段（凭据类）显示为锁定且**不可提交**，
 *    因为服务端本就不接受这类配置，UI 上给假开关只会误导
 *  - 有「预览未登录视图」按钮，直接调真实接口展示脱敏结果，
 *    不靠前端模拟——前端模拟与后端实现必然漂移
 */
import { onMounted, ref } from 'vue'
import CardBox from '@/components/CardBox.vue'
import { api, humanizeError } from '@/api/client'
import { useThemeStore } from '@/stores/theme'

interface Policy {
  id: number
  scope: string
  field: string
  visible: boolean
  mask_mode: string
  mask_rule?: string | null
}

const theme = useThemeStore()
const policies = ref<Policy[]>([])
const loading = ref(true)
const saving = ref<number | null>(null)
const error = ref('')
const preview = ref('')
const previewing = ref(false)

const SCOPE_LABEL: Record<string, string> = {
  public_status: '公开状态页',
  api_unauth: '免鉴权 API',
  export: '数据导出',
}
const FIELD_LABEL: Record<string, string> = {
  name: '节点显示名', public_ip: '公网 IP', geo_country: '国家',
  geo_city: '城市', hostname: '主机名', fqdn: 'FQDN',
  cpu_model: 'CPU 型号', cores_logical: 'CPU 核心数', mem_total: '内存总量',
  disk_info: '磁盘信息', os_type: '系统类型', os_version: '系统版本',
  remark: '备注', cpu_usage: 'CPU 使用率', mem_usage: '内存使用率',
  uptime_30d: '可用率', is_online: '在线状态',
}
/** 硬禁止字段：服务端无论如何都不会返回，UI 上锁死且不提交。 */
const HARD_DENIED = new Set(['agent_secret', 'internal_ip', 'mac_address', 'client_secret'])

const byScope = (scope: string) => policies.value.filter((p) => p.scope === scope)

async function load(): Promise<void> {
  try {
    policies.value = await api.get<Policy[]>('/api/visibility')
  } catch (err) {
    error.value = humanizeError(err, '加载策略失败')
  } finally {
    loading.value = false
  }
}

async function toggle(p: Policy): Promise<void> {
  if (HARD_DENIED.has(p.field)) return
  saving.value = p.id
  try {
    await api.put(`/api/visibility/${p.field}`, {
      scope: p.scope, visible: !p.visible,
      mask_mode: p.mask_mode, mask_rule: p.mask_rule,
    })
    p.visible = !p.visible
  } catch (err) {
    error.value = humanizeError(err, '保存失败')
  } finally {
    saving.value = null
  }
}

async function changeMask(p: Policy, mode: string): Promise<void> {
  if (mode === p.mask_mode) return
  saving.value = p.id
  try {
    await api.put(`/api/visibility/${p.field}`, {
      scope: p.scope, visible: p.visible, mask_mode: mode, mask_rule: p.mask_rule,
    })
    p.mask_mode = mode
  } catch (err) {
    error.value = humanizeError(err, '保存失败')
  } finally {
    saving.value = null
  }
}

/** 调真实的免鉴权接口，拿到服务端脱敏后的结果。 */
async function loadPreview(): Promise<void> {
  previewing.value = true
  preview.value = ''
  try {
    const data = await api.get('/api/status/nodes')
    preview.value = JSON.stringify(data, null, 2)
  } catch (err) {
    preview.value = `预览失败：${humanizeError(err)}`
  } finally {
    previewing.value = false
  }
}

onMounted(() => { theme.init(); void load() })
</script>

<template>
  <div class="settings">
    <div class="warn-bar">
      <strong>可见性策略决定哪些信息对未登录访客公开。</strong>
      将「公网 IP」设为可见，会让攻击者能直接定位你的服务器资产。
    </div>

    <p v-if="error" class="error">{{ error }}</p>

    <CardBox v-for="(label, scope) in SCOPE_LABEL" :key="scope" :title="label">
      <div v-if="loading" class="loading">加载中…</div>
      <table v-else class="policy-table">
        <thead>
          <tr><th>字段</th><th>是否公开</th><th>掩码方式</th></tr>
        </thead>
        <tbody>
          <tr v-for="p in byScope(scope)" :key="p.id">
            <td>
              <span class="field-name">{{ FIELD_LABEL[p.field] || p.field }}</span>
              <code class="field-key">{{ p.field }}</code>
            </td>
            <td>
              <span v-if="HARD_DENIED.has(p.field)" class="locked" title="该字段无论如何都不会对外返回">
                锁定
              </span>
              <label v-else class="switch">
                <input type="checkbox" :checked="p.visible"
                  :disabled="saving === p.id" @change="toggle(p)" />
                <span class="track"><span class="thumb" /></span>
              </label>
            </td>
            <td>
              <select v-if="!HARD_DENIED.has(p.field)" class="mask-select"
                :value="p.mask_mode" :disabled="saving === p.id"
                @change="changeMask(p, ($event.target as HTMLSelectElement).value)">
                <option value="full">完整显示</option>
                <option value="partial">部分脱敏</option>
                <option value="hide">完全隐藏</option>
              </select>
              <span v-else class="text-tertiary">—</span>
            </td>
          </tr>
        </tbody>
      </table>
    </CardBox>

    <CardBox title="未登录视图预览">
      <template #actions>
        <button class="btn-ghost" :disabled="previewing" @click="loadPreview">
          {{ previewing ? '加载中…' : '刷新预览' }}
        </button>
      </template>
      <p class="hint">
        下面是免登录访客实际能看到的数据（直接来自服务端脱敏接口）。
      </p>
      <pre v-if="preview" class="preview">{{ preview }}</pre>
      <p v-else class="hint">点击右上角按钮加载预览</p>
    </CardBox>
  </div>
</template>

<style scoped>
.settings { display: flex; flex-direction: column; gap: var(--space-4); }
.warn-bar { padding: var(--space-3) var(--space-4); background: var(--warning-subtle);
  color: var(--warning); border-radius: var(--radius-md); font-size: var(--font-sm);
  line-height: 1.6; }
.warn-bar strong { color: var(--text-primary); }
.error { padding: var(--space-2) var(--space-3); background: var(--critical-subtle);
  color: var(--critical); border-radius: var(--radius-md); font-size: var(--font-sm); }
.loading { color: var(--text-tertiary); font-size: var(--font-sm); padding: var(--space-4); }

.policy-table { width: 100%; border-collapse: collapse; font-size: var(--font-sm); }
.policy-table th { text-align: left; font-size: var(--font-xs); font-weight: 500;
  color: var(--text-tertiary); padding: 0 var(--space-3) var(--space-2) 0;
  border-bottom: 1px solid var(--line-color); }
.policy-table td { padding: var(--space-2) var(--space-3) var(--space-2) 0;
  border-bottom: 1px solid var(--line-color); vertical-align: middle; }
.policy-table tr:last-child td { border-bottom: none; }

.field-name { display: block; }
.field-key { font-size: 10px; color: var(--text-tertiary); }

/* 开关：纯 CSS，无JS 状态 */
.switch { display: inline-flex; cursor: pointer; }
.switch input { display: none; }
.track { width: 34px; height: 20px; border-radius: var(--radius-full);
  background: var(--line-strong); position: relative; display: block;
  transition: background-color var(--duration-fast) var(--ease-out); }
.thumb { position: absolute; top: 2px; left: 2px; width: 16px; height: 16px;
  border-radius: 50%; background: #fff; transition: transform var(--duration-fast) var(--ease-out); }
.switch input:checked + .track { background: var(--accent); }
.switch input:checked + .track .thumb { transform: translateX(14px); }

.locked { font-size: var(--font-xs); color: var(--text-tertiary);
  padding: 2px 8px; border-radius: var(--radius-sm); background: var(--bg-hover); }

.mask-select { height: 28px; padding: 0 var(--space-2);
  border: 1px solid var(--line-color); border-radius: var(--radius-md);
  background: var(--bg-body); color: var(--text-secondary);
  font-size: var(--font-xs); font-family: inherit; cursor: pointer; }

.btn-ghost { height: 28px; padding: 0 var(--space-3); border: 1px solid var(--line-color);
  border-radius: var(--radius-md); background: transparent; color: var(--text-secondary);
  font-size: var(--font-xs); font-family: inherit; cursor: pointer; }
.btn-ghost:hover:not(:disabled) { background: var(--bg-hover); }
.btn-ghost:disabled { opacity: 0.6; cursor: not-allowed; }

.hint { font-size: var(--font-xs); color: var(--text-tertiary); margin-bottom: var(--space-2); }
.preview { margin: 0; padding: var(--space-3); background: var(--bg-body);
  border: 1px solid var(--line-color); border-radius: var(--radius-md);
  font-size: var(--font-xs); overflow-x: auto; max-height: 320px;
  overflow-y: auto; line-height: 1.6; }

@media (max-width: 767px) {
  .policy-table th:nth-child(3), .policy-table td:nth-child(3) { display: none; }
}
</style>
