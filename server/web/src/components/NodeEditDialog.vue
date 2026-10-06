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
import { computed, ref, watch } from 'vue'
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
/** 删除确认要用户输入节点名，完全匹配才放行。 */
const confirmText = ref('')
const deleting = ref(false)
/** 轮换密钥后展示新安装命令——明文只出现这一次。 */
const rotated = ref<{ secret: string; install_command: string } | null>(null)
const copied = ref('')

const canDelete = computed(
  () => props.node !== null && confirmText.value.trim() === props.node.name,
)

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
    confirmText.value = ''
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
  if (!props.node || !canDelete.value) return
  deleting.value = true
  error.value = ''
  try {
    await store.remove(props.node.uid)
    emit('saved')
    emit('close')
  } catch (err) {
    error.value = humanizeError(err, '删除失败')
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div v-if="node" class="overlay" @click.self="emit('close')">
    <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="nd-title">
      <header class="head">
        <h2 id="nd-title">编辑节点</h2>
        <button class="icon-btn" aria-label="关闭" @click="emit('close')">×</button>
      </header>

      <div class="body">
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
            <button class="btn-ghost" @click="copy(rotated.install_command, 'cmd')">
              {{ copied === 'cmd' ? '已复制' : '复制' }}
            </button>
          </div>
          <label class="field-label" for="nd-secret">密钥</label>
          <div class="copy-row">
            <code id="nd-secret" class="cmd">{{ rotated.secret }}</code>
            <button class="btn-ghost" @click="copy(rotated.secret, 'secret')">
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
            <button class="btn-ghost" :disabled="saving" @click="rotate">轮换</button>
          </div>

          <div class="danger-row">
            <div>
              <p class="danger-title">删除节点</p>
              <p class="hint">连同历史指标一并删除，不可恢复。</p>
            </div>
            <button class="btn-danger-ghost" :disabled="saving" @click="confirmText = ''">
              删除…
            </button>
          </div>

          <!--二次确认：必须输入节点名才能放行 -->
          <div v-if="!confirmText && !rotated" class="confirm">
            <p class="confirm-hint">
              请输入节点名 <code>{{ node.name }}</code> 以确认删除
            </p>
            <input
              v-model="confirmText"
              class="confirm-input"
              :placeholder="node.name"
              autocomplete="off"
              @keyup.enter="remove"
            />
            <div class="confirm-actions">
              <button class="btn-ghost" @click="confirmText = ''">取消</button>
              <button class="btn-danger" :disabled="!canDelete || deleting" @click="remove">
                {{ deleting ? '删除中…' : '确认删除' }}
              </button>
            </div>
          </div>
        </section>
      </div>

      <footer class="foot">
        <button class="btn-ghost" @click="emit('close')">关闭</button>
        <button class="btn-primary" :disabled="saving" @click="save">
          {{ saving ? '保存中…' : '保存' }}
        </button>
      </footer>
    </div>
  </div>
</template>

<style scoped>
.overlay {
  position: fixed;
  inset: 0;
  background: var(--overlay, rgb(0 0 0 / 45%));
  display: flex;
  align-items: center;
  justify-content: center;
  padding: var(--space-4);
  z-index: 100;
}

.dialog {
  background: var(--bg-primary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-lg);
  width: min(560px, 100%);
  max-height: 88vh;
  display: flex;
  flex-direction: column;
  box-shadow: var(--shadow-lg);
}

.head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: var(--space-4);
  border-bottom: 1px solid var(--border-secondary);
}

.head h2 {
  margin: 0;
  font-size: var(--font-base);
  font-weight: 600;
}

.icon-btn {
  border: none;
  background: none;
  font-size: 20px;
  line-height: 1;
  cursor: pointer;
  color: var(--text-tertiary);
  padding: 0 4px;
}

.body {
  padding: var(--space-4);
  overflow-y: auto;
  flex: 1;
}

.field {
  margin-bottom: var(--space-3);
}

.field-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--space-3);
}

label,
.field-label {
  display: block;
  font-size: var(--font-xs);
  color: var(--text-secondary);
  margin-bottom: var(--space-1);
}

input[type='text'],
input:not([type]),
input[type='number'],
select {
  width: 100%;
  padding: 8px 10px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-sm);
  background: var(--bg-secondary);
  color: var(--text-primary);
  font-size: var(--font-sm);
  font-family: inherit;
}

.checkbox {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--font-sm);
  color: var(--text-primary);
  margin: var(--space-3) 0;
}

.hint {
  font-size: var(--font-xs);
  color: var(--text-tertiary);
  margin: var(--space-1) 0;
}

.hint.warn {
  color: var(--color-warning, #b45309);
}

.error {
  background: var(--color-danger-bg, rgb(239 68 68 / 10%));
  color: var(--color-danger, #dc2626);
  padding: var(--space-2) var(--space-3);
  border-radius: var(--radius-sm);
  font-size: var(--font-sm);
  margin: 0 0 var(--space-3);
}

.rotated {
  background: var(--bg-secondary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-sm);
  padding: var(--space-3);
  margin-bottom: var(--space-4);
}

.rotated h3 {
  margin: 0 0 var(--space-2);
  font-size: var(--font-sm);
  color: var(--color-warning, #b45309);
}

.copy-row {
  display: flex;
  gap: var(--space-2);
  align-items: stretch;
  margin-bottom: var(--space-2);
}

.cmd {
  flex: 1;
  background: var(--bg-tertiary);
  padding: 6px 8px;
  border-radius: var(--radius-sm);
  font-family: var(--font-mono, monospace);
  font-size: var(--font-xs);
  word-break: break-all;
  overflow-x: auto;
  white-space: pre-wrap;
}

.danger {
  border-top: 1px solid var(--border-secondary);
  margin-top: var(--space-4);
  padding-top: var(--space-3);
}

.danger h3 {
  font-size: var(--font-xs);
  color: var(--text-tertiary);
  text-transform: uppercase;
  letter-spacing: 0.04em;
  margin: 0 0 var(--space-2);
}

.danger-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  padding: var(--space-2) 0;
}

.danger-title {
  margin: 0;
  font-size: var(--font-sm);
  color: var(--text-primary);
}

.confirm {
  background: var(--bg-secondary);
  border: 1px solid var(--color-danger, #dc2626);
  border-radius: var(--radius-sm);
  padding: var(--space-3);
  margin-top: var(--space-2);
}

.confirm-hint {
  font-size: var(--font-xs);
  color: var(--text-secondary);
  margin: 0 0 var(--space-2);
}

.confirm-input {
  width: 100%;
  padding: 8px 10px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-sm);
  background: var(--bg-primary);
  color: var(--text-primary);
  font-size: var(--font-sm);
}

.confirm-actions {
  display: flex;
  gap: var(--space-2);
  justify-content: flex-end;
  margin-top: var(--space-3);
}

.foot {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-2);
  padding: var(--space-3) var(--space-4);
  border-top: 1px solid var(--border-secondary);
}

.btn-primary,
.btn-ghost,
.btn-danger,
.btn-danger-ghost {
  padding: 7px 14px;
  border-radius: var(--radius-sm);
  font-size: var(--font-sm);
  font-family: inherit;
  cursor: pointer;
  border: 1px solid transparent;
}

.btn-primary {
  background: var(--color-primary);
  color: #fff;
}

.btn-ghost {
  background: var(--bg-secondary);
  color: var(--text-primary);
  border-color: var(--border-primary);
}

.btn-danger {
  background: var(--color-danger, #dc2626);
  color: #fff;
}

.btn-danger-ghost {
  background: transparent;
  color: var(--color-danger, #dc2626);
  border-color: var(--color-danger, #dc2626);
}

button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
</style>
