<!--
  GroupManager —— 分组管理对话框。

  此前后端有 GET/POST /api/groups、store 也有 fetchGroups，
  但界面上没有任何入口——用户建了分组也没法用。
  这个组件补上入口，并加上此前后端也缺的改名与删除。
-->
<script setup lang="ts">
import { ref } from 'vue'
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
   * 二次确认要说明"节点不会被删"——
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
  <div class="overlay" @click.self="emit('close')">
    <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="gm-title">
      <header class="head">
        <h2 id="gm-title">节点分组</h2>
        <button class="icon-btn" aria-label="关闭" @click="emit('close')">×</button>
      </header>

      <div class="body">
        <p v-if="error" class="error" role="alert">{{ error }}</p>

        <p v-if="store.groups.length === 0" class="empty">还没有分组。</p>

        <ul v-else class="list">
          <li v-for="g in store.groups" :key="g.id" class="item">
            <template v-if="editing === g.id">
              <input
                v-model="editName"
                class="edit-input"
                maxlength="64"
                @keyup.enter="saveEdit(g)"
                @keyup.esc="editing = null"
              />
              <button class="btn-ghost" :disabled="busy" @click="saveEdit(g)">保存</button>
              <button class="btn-ghost" @click="editing = null">取消</button>
            </template>
            <template v-else>
              <span class="name">{{ g.name }}</span>
              <span class="count">
                {{ store.nodes.filter((n) => n.group_id === g.id).length }} 个节点
              </span>
              <button class="btn-ghost" @click="startEdit(g)">改名</button>
              <button class="btn-danger-ghost" :disabled="busy" @click="remove(g)">删除</button>
            </template>
          </li>
        </ul>

        <div class="create">
          <input
            v-model="newName"
            placeholder="新分组名称"
            maxlength="64"
            @keyup.enter="create"
          />
          <button class="btn-primary" :disabled="busy || !newName.trim()" @click="create">
            创建
          </button>
        </div>
      </div>

      <footer class="foot">
        <button class="btn-ghost" @click="emit('close')">完成</button>
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
  width: min(460px, 100%);
  max-height: 80vh;
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

.empty {
  color: var(--text-tertiary);
  font-size: var(--font-sm);
  margin: 0 0 var(--space-3);
}

.list {
  list-style: none;
  margin: 0 0 var(--space-4);
  padding: 0;
}

.item {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2) 0;
  border-bottom: 1px solid var(--border-secondary);
}

.name {
  flex: 1;
  font-size: var(--font-sm);
  color: var(--text-primary);
}

.count {
  font-size: var(--font-xs);
  color: var(--text-tertiary);
}

.edit-input {
  flex: 1;
  padding: 6px 8px;
  border: 1px solid var(--color-primary);
  border-radius: var(--radius-sm);
  background: var(--bg-secondary);
  color: var(--text-primary);
  font-size: var(--font-sm);
}

.create {
  display: flex;
  gap: var(--space-2);
  padding-top: var(--space-3);
  border-top: 1px solid var(--border-secondary);
}

.create input {
  flex: 1;
  padding: 8px 10px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-sm);
  background: var(--bg-secondary);
  color: var(--text-primary);
  font-size: var(--font-sm);
}

.error {
  background: var(--color-danger-bg, rgb(239 68 68 / 10%));
  color: var(--color-danger, #dc2626);
  padding: var(--space-2);
  border-radius: var(--radius-sm);
  font-size: var(--font-sm);
  margin: 0 0 var(--space-3);
}

.foot {
  display: flex;
  justify-content: flex-end;
  padding: var(--space-3) var(--space-4);
  border-top: 1px solid var(--border-secondary);
}

.btn-primary,
.btn-ghost,
.btn-danger-ghost {
  padding: 6px 12px;
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
