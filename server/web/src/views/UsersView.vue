<script setup lang="ts">
import { onMounted, ref } from 'vue'
import CardBox from '@/components/CardBox.vue'
import EmptyState from '@/components/EmptyState.vue'
import Skeleton from '@/components/Skeleton.vue'
import { api, humanizeError } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { formatDateTime } from '@/utils/format'
import type { Role, User } from '@/api/types'

const auth = useAuthStore()
const users = ref<User[]>([])
const loading = ref(true)
const error = ref('')

const username = ref('')
const password = ref('')
const role = ref<Role>('viewer')
const creating = ref(false)

const ROLES: Array<{ v: Role; label: string; desc: string }> = [
  { v: 'owner', label: '所有者', desc: '全部权限，含删除系统与管理用户' },
  { v: 'admin', label: '管理员', desc: '管理节点、监控与告警，不能改系统设置' },
  { v: 'viewer', label: '只读', desc: '只能查看，无任何写权限' },
]

async function load(): Promise<void> {
  try {
    users.value = await api.get<User[]>('/api/users')
  } catch (err) {
    error.value = humanizeError(err, '加载用户失败')
  } finally {
    loading.value = false
  }
}

async function create(): Promise<void> {
  if (!username.value.trim() || !password.value) {
    error.value = '请填写用户名与密码'
    return
  }
  creating.value = true
  error.value = ''
  try {
    const u = await api.post<User>('/api/users', {
      username: username.value.trim(), password: password.value, role: role.value,
    })
    users.value.push(u)
    username.value = ''
    password.value = ''
  } catch (err) {
    error.value = humanizeError(err, '创建用户失败')
  } finally {
    creating.value = false
  }
}

async function changeRole(u: User, newRole: Role): Promise<void> {
  if (u.id === auth.user?.id) {
    error.value = '不能修改自己的角色'
    return
  }
  const prev = u.role
  u.role = newRole // 乐观更新，失败后回滚
  try {
    await api.put(`/api/users/${u.id}`, { role: newRole })
  } catch (err) {
    u.role = prev
    error.value = humanizeError(err, '修改角色失败')
  }
}

async function remove(u: User): Promise<void> {
  if (u.id === auth.user?.id) {
    error.value = '不能删除自己'
    return
  }
  if (!window.confirm(`确定删除用户「${u.username}」？该操作不可撤销。`)) return
  try {
    await api.delete(`/api/users/${u.id}`)
    users.value = users.value.filter((x) => x.id !== u.id)
  } catch (err) {
    error.value = humanizeError(err, '删除失败')
  }
}

onMounted(load)
</script>

<template>
  <div class="users">
    <p v-if="error" class="msg-err">{{ error }}</p>

    <CardBox title="用户列表" :padded="false">
      <div v-if="loading" class="loading">加载中…</div>
      <Skeleton v-else-if="loading" type="table" :cols="4" :table-rows="4" />
      <EmptyState v-else-if="users.length === 0" icon="search" title="没有用户" />
      <table v-else class="table">
        <thead>
          <tr><th>用户名</th><th>角色</th><th>创建时间</th><th></th></tr>
        </thead>
        <tbody>
          <tr v-for="u in users" :key="u.id">
            <td>
              <span class="uname">{{ u.username }}</span>
              <span v-if="u.id === auth.user?.id" class="me-tag">当前登录</span>
            </td>
            <td>
              <select class="role-select" :value="u.role"
                :disabled="u.id === auth.user?.id"
                @change="changeRole(u, ($event.target as HTMLSelectElement).value as Role)">
                <option v-for="r in ROLES" :key="r.v" :value="r.v">{{ r.label }}</option>
              </select>
            </td>
            <td class="text-tertiary">{{ formatDateTime(u.created_at) }}</td>
            <td class="act-cell">
              <button v-if="u.id !== auth.user?.id" class="btn-del" @click="remove(u)">删除</button>
            </td>
          </tr>
        </tbody>
      </table>
    </CardBox>

    <CardBox title="新建用户">
      <form class="form" @submit.prevent="create">
        <div class="row">
          <div class="field grow">
            <label for="uname">用户名</label>
            <input id="uname" v-model="username" type="text" required />
          </div>
          <div class="field grow">
            <label for="upass">密码</label>
            <input id="upass" v-model="password" type="password"
              placeholder="至少 10 位，含大小写字母与数字" required />
          </div>
          <div class="field">
            <label for="urole">角色</label>
            <select id="urole" v-model="role">
              <option v-for="r in ROLES" :key="r.v" :value="r.v">{{ r.label }}</option>
            </select>
          </div>
        </div>
        <p class="hint">{{ ROLES.find((r) => r.v === role)?.desc }}</p>
        <div class="actions">
          <button type="submit" class="btn-primary" :disabled="creating">
            {{ creating ? '创建中…' : '创建用户' }}
          </button>
        </div>
      </form>
    </CardBox>
  </div>
</template>

<style scoped>
.users { display: flex; flex-direction: column; gap: var(--space-4); }
.msg-err { padding: var(--space-2) var(--space-3); background: var(--critical-subtle);
  color: var(--critical); border-radius: var(--radius-md); font-size: var(--font-sm); }
.loading { padding: var(--space-5); color: var(--text-tertiary); font-size: var(--font-sm); }

.table { width: 100%; border-collapse: collapse; font-size: var(--font-sm); }
.table th { text-align: left; font-size: var(--font-xs); font-weight: 500;
  color: var(--text-tertiary); padding: var(--space-2) var(--space-4);
  border-bottom: 1px solid var(--line-color); }

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
.table td { padding: var(--space-3) var(--space-4);
  border-bottom: 1px solid var(--line-color); }
.table tr:last-child td { border-bottom: none; }
.uname { font-weight: 500; }
.me-tag { font-size: 10px; padding: 2px 6px; border-radius: var(--radius-sm);
  background: var(--accent-subtle); color: var(--accent); margin-left: var(--space-2); }

.role-select { height: 28px; padding: 0 var(--space-2);
  border: 1px solid var(--line-color); border-radius: var(--radius-md);
  background: var(--bg-body); color: var(--text-secondary);
  font-size: var(--font-xs); font-family: inherit; cursor: pointer; }
.role-select:disabled { opacity: 0.5; cursor: not-allowed; }

.act-cell { text-align: right; width: 60px; }
.btn-del { border: none; background: transparent; color: var(--text-tertiary);
  font-size: var(--font-xs); font-family: inherit; cursor: pointer;
  padding: 2px 6px; border-radius: var(--radius-sm); }
.btn-del:hover { background: var(--critical-subtle); color: var(--critical); }

.form { display: flex; flex-direction: column; gap: var(--space-3); }
.row { display: flex; gap: var(--space-3); align-items: flex-end; }
.field { display: flex; flex-direction: column; gap: var(--space-2); }
.field.grow { flex: 1; }
.field label { font-size: var(--font-sm); color: var(--text-secondary); font-weight: 500; }
.field input, .field select { height: 36px; padding: 0 var(--space-3);
  border: 1px solid var(--line-color); border-radius: var(--radius-md);
  background: var(--bg-body); color: var(--text-primary);
  font-size: var(--font-sm); font-family: inherit; }
.field input:focus { outline: none; border-color: var(--accent); }
.hint { font-size: var(--font-xs); color: var(--text-tertiary); }

.actions { display: flex; justify-content: flex-end; }
.btn-primary { height: 34px; padding: 0 var(--space-4); border: none;
  border-radius: var(--radius-md); background: var(--accent); color: #fff;
  font-size: var(--font-sm); font-family: inherit; font-weight: 500; cursor: pointer; }
.btn-primary:hover:not(:disabled) { background: var(--accent-hover); }

@media (max-width: 767px) {
  .row { flex-direction: column; align-items: stretch; }
  .table th:nth-child(3), .table td:nth-child(3) { display: none; }
}
</style>
