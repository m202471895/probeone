<!--
  GroupManager —— 分组管理对话框。

  样式全部走 ModalDialog 与项目 tokens（--bg-surface / --line-color /
  --accent …）。此前自造了 --bg-primary / --border-primary 这类
  不存在的变量，导致遮罩失效、面板透明、内容直接铺在页面上。
-->
<script setup lang="ts">
import { ref } from 'vue'
import ModalDialog from '@/components/ModalDialog.vue'
import { useNodeStore } from '@/stores/node'
import { humanizeError } from '@/api/client'
import type { NodeGroup } from '@/api/types'

const emit = defineEmits<{ close: [] }>()
const store = useNodeStore()

const newName = ref('')
const editing = ref<number | null>(null)
const editName = ref('')
const busy = ref(false)
const error = ref('')

/** 每个分组下的节点数。与 store 分离计算，避免为了计数而重拉列表。 */
function countOf(groupId: number): number {
  return store.nodes.filter((n) => n.group_id === groupId).length
}

async function create(): Promise<void> {
  const name = newName.value.trim()
  if (!name) return
  busy.value = true
  error.value = ''
  try {
    await store.createGroup(name, store.groups.length)
    newName.value = ''
  } catch (err) {
    error.value = humanizeError(err, '创建分组失败')
  } finally {
    busy.value = false
  }
}

function startEdit(g: NodeGroup): void {
  editing.value = g.id
  editName.value = g.name
  error.value = ''
}

async function saveEdit(g: NodeGroup): Promise<void> {
  const name = editName.value.trim()
  if (!name) {
    error.value = '分组名称不能为空'
    return
  }
  busy.value = true
  error.value = ''
  try {
    await store.updateGroup(g.id, name, g.sort)
    editing.value = null
  } catch (err) {
    error.value = humanizeError(err, '改名失败')
  } finally {
    busy.value = false
  }
}

async function remove(g: NodeGroup): Promise<void> {
  /*
   * 确认文案里必须说明"节点不会被删除"——
   * 否则用户会以为删分组等于删节点，不敢点。
   */
  if (!window.confirm(`删除分组「${g.name}」？\n\n该分组下的节点不会被删除，只是不再归属任何分组。`)) {
    return
  }
  busy.value = true
  error.value = ''
  try {
    await store.deleteGroup(g.id)
  } catch (err) {
    error.value = humanizeError(err, '删除分组失败')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <ModalDialog title="节点分组" close-label="关闭分组管理" @close="emit('close')">
    <p v-if="error" class="alert alert-error" role="alert">{{ error }}</p>

    <p v-if="store.groups.length === 0" class="empty">
      还没有分组。分组可以按机房、线路或用途把节点归类。
    </p>

    <ul v-else class="list">
      <li v-for="g in store.groups" :key="g.id" class="row">
        <template v-if="editing === g.id">
          <input
            v-model="editName"
            class="input"
            maxlength="64"
            @keyup.enter="saveEdit(g)"
            @keyup.esc="editing = null"
          />
          <button class="btn btn-sm" :disabled="busy" @click="saveEdit(g)">保存</button>
          <button class="btn btn-sm" @click="editing = null">取消</button>
        </template>
        <template v-else>
          <div class="row-main">
            <span class="name">{{ g.name }}</span>
            <span class="count">{{ countOf(g.id) }} 个节点</span>
          </div>
          <div class="row-actions">
            <button class="btn btn-sm" :disabled="busy" @click="startEdit(g)">改名</button>
            <button class="btn btn-sm btn-danger-ghost" :disabled="busy" @click="remove(g)">
              删除
            </button>
          </div>
        </template>
      </li>
    </ul>

    <div class="create">
      <input
        v-model="newName"
        class="input"
        placeholder="新分组名称"
        maxlength="64"
        @keyup.enter="create"
      />
      <button class="btn btn-primary" :disabled="busy || !newName.trim()" @click="create">
        创建
      </button>
    </div>

    <template #footer>
      <button class="btn" @click="emit('close')">完成</button>
    </template>
  </ModalDialog>
</template>

<style scoped>
.list {
  list-style: none;
  margin: 0 0 var(--space-4);
  padding: 0;
}

.row {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-3) 0;
  border-bottom: 1px solid var(--line-color);
}

.row:last-child {
  border-bottom: none;
}

.row-main {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
}

.row-actions {
  display: flex;
  gap: var(--space-2);
  flex-shrink: 0;
}

.name {
  font-size: var(--font-sm);
  color: var(--text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.count {
  font-size: var(--font-xs);
  color: var(--text-tertiary);
  flex-shrink: 0;
}

.create {
  display: flex;
  gap: var(--space-2);
  padding-top: var(--space-4);
  border-top: 1px solid var(--line-color);
}

.input {
  flex: 1;
  min-width: 0;
  height: 34px;
  padding: 0 var(--space-3);
  border: 1px solid var(--line-color);
  border-radius: var(--radius-md);
  background: var(--bg-surface);
  color: var(--text-primary);
  font-size: var(--font-sm);
  font-family: inherit;
}

.input:focus {
  outline: none;
  border-color: var(--accent);
}

.empty {
  color: var(--text-tertiary);
  font-size: var(--font-sm);
  margin: 0 0 var(--space-4);
}

.alert {
  padding: var(--space-2) var(--space-3);
  border-radius: var(--radius-md);
  font-size: var(--font-sm);
  margin: 0 0 var(--space-4);
}

.alert-error {
  background: var(--critical-subtle);
  color: var(--critical);
}
</style>

<style>
/*
 * 按钮样式放在非 scoped 块里：NodesView 等页面也用同一套按钮，
 * scoped 会把它们各自的定义互相隔离掉。
 */
.btn {
  height: 34px;
  padding: 0 var(--space-4);
  border: 1px solid var(--line-color);
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--text-secondary);
  font-size: var(--font-sm);
  font-family: inherit;
  font-weight: 500;
  cursor: pointer;
  white-space: nowrap;
  transition: background var(--duration-fast) var(--ease-out),
              color var(--duration-fast) var(--ease-out),
              border-color var(--duration-fast) var(--ease-out);
}

.btn:hover:not(:disabled) {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.btn:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 1px;
}

.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.btn-sm {
  height: 28px;
  padding: 0 var(--space-3);
  font-size: var(--font-xs);
}

.btn-primary {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--text-inverse);
}

.btn-primary:hover:not(:disabled) {
  background: var(--accent-hover);
  border-color: var(--accent-hover);
  color: var(--text-inverse);
}

.btn-danger-ghost {
  color: var(--critical);
  border-color: transparent;
}

.btn-danger-ghost:hover:not(:disabled) {
  background: var(--critical-subtle);
  color: var(--critical);
}
</style>
