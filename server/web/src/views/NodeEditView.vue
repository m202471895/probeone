<script setup lang="ts">
/**
 * 添加节点
 *
 * 创建后**必须**明确提示用户保存密钥——
 * 明文 secret 只在这次响应里出现，之后永远取不回来。
 * 这是不可逆操作，UI 上要给出足够强的提示。
 */
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import CardBox from '@/components/CardBox.vue'
import { api, humanizeError } from '@/api/client'
import { useNodeStore } from '@/stores/node'
import type { NodeCreated } from '@/api/types'

const router = useRouter()
const store = useNodeStore()

const name = ref('')
const groupId = ref<number | null>(null)
const isPublic = ref(false)
const submitting = ref(false)
const error = ref('')
const created = ref<NodeCreated | null>(null)
const copied = ref('')

async function submit(): Promise<void> {
  if (!name.value.trim()) {
    error.value = '请填写节点名称'
    return
  }
  submitting.value = true
  error.value = ''
  try {
    created.value = await api.post<NodeCreated>('/api/nodes', {
      name: name.value.trim(),
      group_id: groupId.value,
      is_public: isPublic.value,
    })
    await store.fetch()
  } catch (err) {
    error.value = humanizeError(err, '创建节点失败')
  } finally {
    submitting.value = false
  }
}

async function copy(text: string, label: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text)
    copied.value = label
    setTimeout(() => (copied.value = ''), 2000)
  } catch {
    error.value = '复制失败，请手动选中复制'
  }
}

onMounted(() => void store.fetchGroups())
</script>

<template>
  <div class="edit">
    <CardBox v-if="!created" title="添加节点">
      <form class="form" @submit.prevent="submit">
        <div class="field">
          <label for="name">节点名称</label>
          <input id="name" v-model="name" type="text" placeholder="如：香港节点 01" required />
        </div>

        <div class="field">
          <label for="group">所属分组</label>
          <select id="group" v-model="groupId">
            <option :value="null">不分组</option>
            <option v-for="g in store.groups" :key="g.id" :value="g.id">{{ g.name }}</option>
          </select>
        </div>

        <div class="field">
          <label class="check">
            <input v-model="isPublic" type="checkbox" />
            <span>
              在公开状态页显示
              <span class="hint">（仅显示名称与运行状态，不会暴露 IP、主机名与硬件信息）</span>
            </span>
          </label>
        </div>

        <p v-if="error" class="error">{{ error }}</p>

        <div class="actions">
          <button type="button" class="btn-ghost" @click="router.back()">取消</button>
          <button type="submit" class="btn-primary" :disabled="submitting">
            {{ submitting ? '创建中…' : '创建节点' }}
          </button>
        </div>
      </form>
    </CardBox>

    <CardBox v-else title="安装 Agent">
      <div class="warn">
        <strong>请立即保存以下信息。</strong>
        密钥<strong>只显示这一次</strong>，关闭本页后无法再次查看。
      </div>

      <div class="secret-block">
        <div class="secret-label">节点密钥</div>
        <div class="secret-row">
          <code class="secret font-mono">{{ created.secret }}</code>
          <button class="copy-btn" @click="copy(created.secret, '密钥已复制')">
            {{ copied === '密钥已复制' ? '已复制' : '复制' }}
          </button>
        </div>
      </div>

      <div class="secret-block">
        <div class="secret-label">安装命令</div>
        <div class="secret-row">
          <code class="cmd font-mono">{{ created.install_command }}</code>
          <button class="copy-btn" @click="copy(created.install_command, '命令已复制')">
            {{ copied === '命令已复制' ? '已复制' : '复制' }}
          </button>
        </div>
      </div>

      <ol class="steps">
        <li>在目标服务器上以 root 或有 sudo 权限执行上面的命令</li>
        <li>等待约 10 秒，节点状态应变为「在线」</li>
        <li>如果状态仍是「待接入」，检查 8008 端口是否被防火墙拦截</li>
      </ol>

      <div class="actions">
        <button class="btn-ghost" @click="router.push('/nodes')">返回列表</button>
        <button class="btn-primary" @click="router.push(`/nodes/${created.uid}`)">
          查看节点详情
        </button>
      </div>
    </CardBox>
  </div>
</template>

<style scoped>
.edit { max-width: 640px; }
.form { display: flex; flex-direction: column; gap: var(--space-4); }
.field { display: flex; flex-direction: column; gap: var(--space-2); }
.field label { font-size: var(--font-sm); color: var(--text-secondary); font-weight: 500; }
.field input[type='text'], .field select {
  height: 36px; padding: 0 var(--space-3); border: 1px solid var(--line-color);
  border-radius: var(--radius-md); background: var(--bg-body);
  color: var(--text-primary); font-size: var(--font-base); font-family: inherit; }
.field input:focus, .field select:focus { outline: none; border-color: var(--accent); }

.check { display: flex; align-items: flex-start; gap: var(--space-2);
  cursor: pointer; font-weight: 400 !important; font-size: var(--font-sm) !important; }
.hint { color: var(--text-tertiary); font-size: var(--font-xs); }

.error { font-size: var(--font-sm); color: var(--critical);
  padding: var(--space-2) var(--space-3); background: var(--critical-subtle);
  border-radius: var(--radius-md); }

.actions { display: flex; justify-content: flex-end; gap: var(--space-2); margin-top: var(--space-2); }
.btn-primary, .btn-ghost { height: 34px; padding: 0 var(--space-4);
  border-radius: var(--radius-md); font-size: var(--font-sm); font-family: inherit;
  font-weight: 500; cursor: pointer; }
.btn-primary { background: var(--accent); color: #fff; border: none; }
.btn-primary:hover:not(:disabled) { background: var(--accent-hover); }
.btn-primary:disabled { opacity: 0.6; cursor: not-allowed; }
.btn-ghost { background: transparent; color: var(--text-secondary); border: 1px solid var(--line-color); }
.btn-ghost:hover { background: var(--bg-hover); }

.warn { padding: var(--space-3) var(--space-4); background: var(--warning-subtle);
  color: var(--warning); border-radius: var(--radius-md); font-size: var(--font-sm);
  margin-bottom: var(--space-4); line-height: 1.6; }
.warn strong { color: var(--text-primary); }

.secret-block { margin-bottom: var(--space-4); }
.secret-label { font-size: var(--font-xs); color: var(--text-tertiary); margin-bottom: var(--space-2); }
.secret-row { display: flex; gap: var(--space-2); align-items: stretch; }
.secret, .cmd { flex: 1; padding: var(--space-3); background: var(--bg-body);
  border: 1px solid var(--line-color); border-radius: var(--radius-md);
  font-size: var(--font-xs); word-break: break-all; line-height: 1.6; color: var(--text-primary); }
.copy-btn { flex-shrink: 0; padding: 0 var(--space-3); border: 1px solid var(--line-color);
  border-radius: var(--radius-md); background: var(--bg-surface);
  color: var(--text-secondary); font-size: var(--font-xs); font-family: inherit; cursor: pointer; }
.copy-btn:hover { background: var(--bg-hover); }

.steps { margin: 0 0 var(--space-4); padding-left: var(--space-5);
  font-size: var(--font-sm); color: var(--text-secondary); line-height: 2; }
</style>
