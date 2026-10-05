<script setup lang="ts">
/**
 * 应用外壳：侧边栏 + 顶栏 + 内容区
 */
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import SidebarNav from '@/components/SidebarNav.vue'
import TabBar from '@/components/TabBar.vue'
import { realtime } from '@/api/realtime'
import { useAlertStore } from '@/stores/alert'
import { useAuthStore } from '@/stores/auth'
import { useNodeStore } from '@/stores/node'
import { useMonitorStore } from '@/stores/monitor'
import { useThemeStore } from '@/stores/theme'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const theme = useThemeStore()
const alerts = useAlertStore()
const nodes = useNodeStore()
const monitors = useMonitorStore()

const connected = ref(false)

const pageTitle = computed(() => {
  const map: Record<string, string> = {
    '/': '总览',
    '/nodes': '服务器',
    '/monitors': '网站监控',
    '/alerts': '告警',
    '/alert-rules': '告警规则',
    '/channels': '通知通道',
    '/users': '用户管理',
    '/audit-logs': '审计日志',
    '/settings': '系统设置',
    '/settings/visibility': '可见性策略',
  }
  if (map[route.path]) return map[route.path] ?? ''
  if (route.path.startsWith('/nodes/')) return '节点详情'
  if (route.path.startsWith('/settings')) return '系统设置'
  return 'ProbeOne'
})

async function handleLogout(): Promise<void> {
  await auth.logout()
  realtime.disconnect()
  void router.push('/login')
}

function toggleTheme(): void {
  theme.toggle()
}

onMounted(() => {
  theme.init()
  realtime.onStatusChange((v) => (connected.value = v))

  // 实时消息合并进对应 store，而不是各自维护一份数据
  realtime.subscribe((msg) => {
    switch (msg.type) {
      case 'metrics':
        nodes.mergeLatest(msg.node_id, msg.data)
        break
      case 'node_status':
        nodes.mergeStatus(msg.node_id, msg.status)
        break
      case 'monitor_status':
        monitors.mergeStatus(msg.monitor_id, msg.status, msg.latency_ms)
        break
      case 'alert':
        alerts.mergeEvent(msg.data)
        break
    }
  })

  if (auth.isLoggedIn) {
    realtime.connect()
    void alerts.fetchEvents()
  }
})
</script>

<template>
  <!--
    logged-in 类名是显式的，不依赖 :has()。
    之前用 .app:has(> .sidebar) 判定，但 Vue 的 scoped 编译会把属性选择器
    写成 .app[data-v-xxx]:has(> .sidebar)，而 .sidebar 是子组件根元素、
    不带父组件的 data-v 属性，选择器永远匹配不上——规则等于没写。
  -->
  <div class="app" :class="{ 'logged-in': auth.isLoggedIn }">
    <template v-if="auth.isLoggedIn">
      <SidebarNav />

      <div class="main">
        <header class="topbar">
          <h1 class="page-title">{{ pageTitle }}</h1>

          <div class="topbar-right">
            <span
              class="conn"
              :class="{ live: connected }"
              :title="connected ? '实时连接正常' : '实时连接已断开，正在重连'"
            >
              <span class="conn-dot" />
              <span class="conn-text">{{ connected ? '实时' : '离线' }}</span>
            </span>

            <button
              class="icon-btn"
              :title="theme.resolved === 'dark' ? '切换到浅色' : '切换到深色'"
              :aria-label="theme.resolved === 'dark' ? '切换到浅色主题' : '切换到深色主题'"
              @click="toggleTheme"
            >
              <svg v-if="theme.resolved === 'dark'" viewBox="0 0 24 24" width="17" height="17" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
                <circle cx="12" cy="12" r="4" />
                <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
              </svg>
              <svg v-else viewBox="0 0 24 24" width="17" height="17" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
                <path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z" />
              </svg>
            </button>

            <div class="user-menu">
              <span class="username">{{ auth.user?.username }}</span>
              <span class="role-tag">{{ auth.role }}</span>
              <button class="link-btn" @click="handleLogout">退出</button>
            </div>
          </div>
        </header>

        <main class="content">
          <RouterView v-slot="{ Component }">
            <!-- keep-alive 保留列表页的筛选与滚动位置，
                 代价是内存占用略增。对监控面板来说体验收益更大。 -->
            <KeepAlive :max="6">
              <component :is="Component" />
            </KeepAlive>
          </RouterView>
        </main>
      </div>

      <TabBar />
    </template>

    <template v-else>
      <RouterView />
    </template>
  </div>
</template>

<style scoped>
.app {
  /* 未登录时保持 block —— 登录页/状态页/404 需要它自己控制水平方向 */
  min-height: 100vh;
  background: var(--bg-body);
}

.app.logged-in {
  display: flex;
}

.main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
}

.topbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-4);
  height: var(--header-height);
  padding: 0 var(--space-5);
  background: var(--bg-surface);
  border-bottom: 1px solid var(--line-color);
  position: sticky;
  top: 0;
  z-index: 40;
}

.page-title {
  font-size: var(--font-md);
  font-weight: 600;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.topbar-right {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex-shrink: 0;
}

/* 连接状态：断线时必须显眼，否则用户会盯着不动的图表干瞪眼 */
.conn {
  display: flex;
  align-items: center;
  gap: 5px;
  font-size: var(--font-xs);
  color: var(--text-tertiary);
  padding: 3px var(--space-2);
  border-radius: var(--radius-full);
  background: var(--bg-hover);
}

.conn.live {
  color: var(--down);
  background: var(--down-subtle);
}

.conn-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
}

.conn:not(.live) .conn-dot {
  animation: blink 1.2s ease-in-out infinite;
}

@keyframes blink {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.3; }
}

.icon-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  border: 1px solid var(--line-color);
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--text-secondary);
  cursor: pointer;
  transition:
    background-color var(--duration-fast) var(--ease-out),
    color var(--duration-fast) var(--ease-out),
    border-color var(--duration-fast) var(--ease-out);
}

.icon-btn:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
  border-color: var(--line-strong);
}

.user-menu {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--font-sm);
}

.username {
  color: var(--text-secondary);
}

.role-tag {
  font-size: 10px;
  padding: 2px 6px;
  border-radius: var(--radius-sm);
  background: var(--accent-subtle);
  color: var(--accent);
  font-weight: 500;
}

.link-btn {
  border: none;
  background: none;
  color: var(--text-tertiary);
  font-size: var(--font-sm);
  cursor: pointer;
  padding: 0;
  transition: color var(--duration-fast) var(--ease-out);
}

.link-btn:hover {
  color: var(--critical);
}

.content {
  flex: 1;
  min-width: 0;
  padding: var(--space-5);
  max-width: var(--content-max);
  width: 100%;
  margin: 0 auto;
}

@media (max-width: 1023px) {
  .content {
    padding: var(--space-4);
  }
  .topbar {
    padding: 0 var(--space-4);
  }
  /* 折叠侧边栏后空间紧张，隐藏低优先信息 */
  .username,
  .role-tag {
    display: none;
  }
}

@media (max-width: 767px) {
  .content {
    padding: var(--space-3);
    /* 给底部 Tab 让位，否则最后一条内容会被遮住 */
    padding-bottom: calc(var(--tabbar-height) + var(--space-5));
  }
  .topbar {
    padding: 0 var(--space-3);
  }
  .conn-text {
    display: none;
  }
  .conn {
    padding: 4px;
  }
}
</style>
