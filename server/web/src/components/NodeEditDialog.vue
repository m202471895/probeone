<!--
  NodeEditDialog —— 节点编辑对话框。

  覆盖此前完全缺失的三件事：
    - 改名称 / 备注 / 分组 / 公开状态
    - 轮换密钥（拿到新的安装命令）
    - 删除节点（带二次确认）

  为什么删除要二次确认且要求输入节点名：
  删除会连带清掉该节点的历史指标，且**无法恢复**——
  节点重新接入后 UID 与密钥都会变，历史数据关联不上。
  误点一次就可能丢几周的曲线。
-->
<script setup lang="ts">
import { ref, watch } from 'vue'
import ModalDialog from '@/components/ModalDialog.vue'
import { useNodeStore } from '@/stores/node'
import { humanizeError } from '@/api/client'
import { copyToClipboard } from '@/utils/clipboard'
import type { Node } from '@/api/types'

const props = defineProps<{ node: Node | null }>()
const emit = defineEmits<{ close: []; saved: [] }>()

const store = useNodeStore()

const name = ref('')
const remark = ref('')
const groupId = ref<number | null>(null)
const isPublic = ref(false)
const geoCountry = ref('')
const geoCity = ref('')

const saving = ref(false)
const error = ref('')
/** 删���确认弹窗的可见状态。 */
const confirmVisible = ref(false)
const deleting = ref(false)
/** 轮换密钥后展示新安装命令——明文只出现这一次。 */
const rotated = ref<{ secret: string; install_command: string } | null>(null)
const copied = ref('')

watch(
  () => props.node,
  (n) => {
    if (!n) return
    name.value = n.name
    remark.value = n.remark ?? ''
    groupId.value = n.group_id ?? null
    isPublic.value = n.is_public
    geoCountry.value = n.geo_country ?? ''
    geoCity.value = n.geo_city ?? ''
    error.value = ''
    confirmVisible.value = false
    rotated.value = null
  },
  { immediate: true },
)

async function save(): Promise<void> {
  if (!props.node) return
  if (!name.value.trim()) {
    error.value = '请填写节点名称'
    return
  }
  saving.value = true
  error.value = ''
  try {
    await store.update(props.node.uid, {
      name: name.value.trim(),
      remark: remark.value.trim(),
      group_id: groupId.value,
      is_public: isPublic.value,
      geo_country: geoCountry.value.trim().toUpperCase(),
      geo_city: geoCity.value.trim(),
    })
    emit('saved')
  } catch (err) {
    error.value = humanizeError(err, '保存失败')
  } finally {
    saving.value = false
  }
}

/**
 * 轮换密钥。
 *
 * 旧密钥立即失效——已连着的 Agent 会开始失败，这是预期行为。
 * 所以轮换后要明确告诉用户"需要在所有机器上重新安装"。
 */
async function rotate(): Promise<void> {
  if (!props.node) return
  if (!window.confirm('轮换后旧密钥立即失效，所有节点上的 Agent 需要重新安装。确定继续？')) {
    return
  }
  saving.value = true
  error.value = ''
  try {
    rotated.value = await store.rotateSecret(props.node.uid)
  } catch (err) {
    error.value = humanizeError(err, '轮换密钥失败')
  } finally {
    saving.value = false
  }
}

async function copy(text: string, label: string): Promise<void> {
  const r = await copyToClipboard(text)
  if (r === 'failed') {
    error.value = '复制失败，请手动选中后按 Ctrl+C'
    return
  }
  copied.value = label
  setTimeout(() => (copied.value = ''), 2000)
}

async function remove(): Promise<void> {
  if (!props.node) return
  deleting.value = true
  error.value = ''
  try {
    await store.remove(props.node.uid)
    confirmVisible.value = false
    emit('saved')
    emit('close')
  } catch (err) {
    error.value = humanizeError(err, '删除失败')
  } finally {
    deleting.value = false
  }
}

/**
 * 打开删除确认弹窗。
 *
 * 用弹窗而不是 window.confirm：原生确认框在浏览器里样式不可控，
 * 且无法把"历史指标会一并删除"这种关键信息说清楚。
 * 弹窗里必须写明后果——删除不可恢复。
 */
function askDelete(): void {
  confirmVisible.value = true
}
</script>

<template>
  <ModalDialog
    v-if="node"
    title="编辑节点"
    close-label="关闭编辑对话框"
    :close-on-overlay="false"
    @close="emit('close')"
  >
      <div class="node-form">
        <p v-if="error" class="error" role="alert">{{ error }}</p>

        <!-- 轮换密钥后的结果 -->
        <section v-if="rotated" class="rotated">
          <h3>密钥已轮换</h3>
          <p class="hint warn">
            旧密钥已失效。需要在所有机器上用下面的新命令重新安装 Agent，
            否则它们会开始上报失败。
          </p>
          <label class="field-label" for="nd-cmd">安装命令</label>
          <div class="copy-row">
            <code id="nd-cmd" class="cmd">{{ rotated.install_command }}</code>
            <button class="btn" @click="copy(rotated.install_command, 'cmd')">
              {{ copied === 'cmd' ? '已复制' : '复制' }}
            </button>
          </div>
          <label class="field-label" for="nd-secret">密钥</label>
          <div class="copy-row">
            <code id="nd-secret" class="cmd">{{ rotated.secret }}</code>
            <button class="btn" @click="copy(rotated.secret, 'secret')">
              {{ copied === 'secret' ? '已复制' : '复制' }}
            </button>
          </div>
          <p class="hint">密钥只显示这一次，关闭后无法再查看。</p>
        </section>

        <div class="field">
          <label for="nd-name">名称</label>
          <input id="nd-name" v-model="name" maxlength="64" :disabled="saving" />
        </div>

        <div class="field">
          <label for="nd-remark">备注</label>
          <input id="nd-remark" v-model="remark" maxlength="256" :disabled="saving" />
        </div>

        <div class="field">
          <label for="nd-group">分组</label>
          <select id="nd-group" v-model="groupId" :disabled="saving">
            <!-- 用空值表示"不分组"：后端把 null 视为清空分组 -->
            <option :value="null">未分组</option>
            <option v-for="g in store.groups" :key="g.id" :value="g.id">{{ g.name }}</option>
          </select>
        </div>

        <div class="field-row">
          <div class="field">
            <label for="nd-country">国家/地区代码</label>
            <input
              id="nd-country"
              v-model="geoCountry"
              maxlength="2"
              placeholder="如HK"
              :disabled="saving"
            />
          </div>
          <div class="field">
            <label for="nd-city">城市</label>
            <input id="nd-city" v-model="geoCity" :disabled="saving" />
          </div>
        </div>
        <p class="hint">
          留空则地图上不显示。填了坐标会在世界地图上打点。
        </p>

        <label class="checkbox">
          <input v-model="isPublic" type="checkbox" :disabled="saving" />
          <span>在公开状态页显示</span>
        </label>
        <p class="hint">
          公开后仅显示名称与在线状态，IP、硬件规格等信息不会暴露。
        </p>

        <section class="danger">
          <h3>危险操作</h3>
          <div class="danger-row">
            <div>
              <p class="danger-title">轮换密钥</p>
              <p class="hint">旧密钥立即失效，需重新安装 Agent。</p>
            </div>
            <button class="btn" :disabled="saving" @click="rotate">轮换</button>
          </div>

          <div class="danger-row">
            <div>
              <p class="danger-title">删除节点</p>
              <p class="hint">连同历史指标一并删除，不可恢复。</p>
            </div>
            <button class="btn btn-danger-ghost" :disabled="saving" @click="askDelete">
              删除…
            </button>
          </div>
        </section>
      </div>

    <template #footer>
      <button class="btn" @click="emit('close')">取消</button>
      <button class="btn btn-primary" :disabled="saving" @click="save">
        {{ saving ? '保存中…' : '保存' }}
      </button>
    </template>
  </ModalDialog>

  <!--
    删除确认弹窗。放在 ModalDialog 之外但同一层级：
    编辑框本身已经是一个对话框，嵌套两层遮罩会让 z-index 与
    焦点管理变复杂（ModalDialog 的 Esc 监听会互相干扰）。
  -->
  <ModalDialog
    v-if="confirmVisible && node"
    title="删除节点"
    close-label="取消删除"
    :close-on-overlay="true"
    @close="confirmVisible = false"
  >
    <p class="confirm-lead">
      确定要删除节点 <strong>{{ node.name }}</strong> 吗？
    </p>
    <ul class="confirm-list">
      <li>该节点的历史指标与统计数据会一并删除</li>
      <li>节点上的 Agent 将无法再上报数据</li>
      <li>此操作<b>不可恢复</b></li>
    </ul>
    <p class="confirm-note">
      重新添加节点会生成新的 UID，历史数据无法关联回来。
      如果只是想暂停监控，删除前可以先停用 Agent 而不删节点。
    </p>

    <template #footer>
      <button class="btn" :disabled="deleting" @click="confirmVisible = false">取消</button>
      <button class="btn btn-danger" :disabled="deleting" @click="remove">
        {{ deleting ? '删除中…' : '确认删除' }}
      </button>
    </template>
  </ModalDialog>
</template>

<style scoped>
/*
 * 样式说明：
 * - 容器（遮罩/面板/头尾）由 ModalDialog 提供，这里不重复定义
 * - 全部使用项目 tokens（--bg-surface / --line-color / --accent …）
 * - 按钮用全局 .btn 系列（GroupManager.vue 的非 scoped 块提供）
 */

.node-form {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}

.field {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
}

.field-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--space-3);
}

label,
.field-label {
  font-size: var(--font-xs);
  color: var(--text-secondary);
}

input:not([type='checkbox']),
select {
  height: 34px;
  padding: 0 var(--space-3);
  border: 1px solid var(--line-color);
  border-radius: var(--radius-md);
  background: var(--bg-surface);
  color: var(--text-primary);
  font-size: var(--font-sm);
  font-family: inherit;
}

input:not([type='checkbox']):focus,
select:focus {
  outline: none;
  border-color: var(--accent);
}

.checkbox {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--font-sm);
  color: var(--text-primary);
  cursor: pointer;
}

.hint {
  font-size: var(--font-xs);
  color: var(--text-tertiary);
  line-height: 1.5;
}

.hint.warn {
  color: var(--warning);
}

.error {
  padding: var(--space-2) var(--space-3);
  border-radius: var(--radius-md);
  background: var(--critical-subtle);
  color: var(--critical);
  font-size: var(--font-sm);
}

/* 密钥轮换结果 */
.rotated {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-4);
  border: 1px solid var(--accent-border);
  border-radius: var(--radius-md);
  background: var(--accent-subtle);
}

.rotated h3 {
  margin: 0;
  font-size: var(--font-sm);
  font-weight: 600;
  color: var(--warning);
}

.copy-row {
  display: flex;
  gap: var(--space-2);
  align-items: stretch;
}

.cmd {
  flex: 1;
  min-width: 0;
  padding: var(--space-2);
  border: 1px solid var(--line-color);
  border-radius: var(--radius-sm);
  background: var(--bg-surface);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: var(--font-xs);
  line-height: 1.5;
  word-break: break-all;
}

/* 危险操作区 */
.danger {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding-top: var(--space-4);
  border-top: 1px solid var(--line-color);
}

.danger h3 {
  margin: 0;
  font-size: var(--font-xs);
  font-weight: 600;
  color: var(--text-tertiary);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.danger-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  padding: var(--space-2) 0;
}

.danger-title {
  margin: 0 0 2px;
  font-size: var(--font-sm);
  color: var(--text-primary);
}

/* 删除二次确认 */

/* 删除确认弹窗 */
.confirm-lead {
  margin: 0 0 var(--space-3);
  font-size: var(--font-sm);
  color: var(--text-primary);
  line-height: 1.6;
}

.confirm-lead strong {
  font-weight: 600;
}

.confirm-list {
  margin: 0 0 var(--space-3);
  padding-left: var(--space-5);
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
}

.confirm-list li {
  font-size: var(--font-sm);
  color: var(--text-secondary);
  line-height: 1.6;
}

.confirm-list b {
  color: var(--critical);
  font-weight: 600;
}

.confirm-note {
  margin: 0;
  padding: var(--space-3);
  border-radius: var(--radius-md);
  background: var(--bg-subtle);
  font-size: var(--font-xs);
  color: var(--text-tertiary);
  line-height: 1.6;
}
</style>
