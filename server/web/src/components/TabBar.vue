<script setup lang="ts">
/** 底部 Tab 导航。仅 <768px 可见。 */
import { RouterLink, useRoute } from 'vue-router'
import { useAlertStore } from '@/stores/alert'

const route = useRoute()
const alerts = useAlertStore()

const tabs = [
  { path: '/', label: '总览', icon: 'grid' },
  { path: '/nodes', label: '服务器', icon: 'server' },
  { path: '/monitors', label: '监控', icon: 'globe' },
  { path: '/alerts', label: '告警', icon: 'bell' },
  { path: '/settings', label: '我的', icon: 'user' },
]

function isActive(path: string): boolean {
  if (path === '/') return route.path === '/'
  if (path === '/settings') return route.path.startsWith('/settings')
  return route.path.startsWith(path)
}
</script>

<template>
  <nav class="tabbar" aria-label="底部导航">
    <RouterLink
      v-for="tab in tabs"
      :key="tab.path"
      :to="tab.path"
      class="tab"
      :class="{ active: isActive(tab.path) }"
    >
      <span class="tab-icon">
        <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
          <template v-if="tab.icon === 'grid'">
            <rect x="3" y="3" width="7" height="7" rx="1" /><rect x="14" y="3" width="7" height="7" rx="1" />
            <rect x="3" y="14" width="7" height="7" rx="1" /><rect x="14" y="14" width="7" height="7" rx="1" />
          </template>
          <template v-else-if="tab.icon === 'server'">
            <rect x="3" y="4" width="18" height="7" rx="1.5" /><rect x="3" y="13" width="18" height="7" rx="1.5" />
          </template>
          <template v-else-if="tab.icon === 'globe'">
            <circle cx="12" cy="12" r="9" /><path d="M3 12h18M12 3a15 15 0 0 1 0 18 15 15 0 0 1 0-18" />
          </template>
          <template v-else-if="tab.icon === 'bell'">
            <path d="M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9" /><path d="M13.7 21a2 2 0 0 1-3.4 0" />
          </template>
          <template v-else>
            <circle cx="12" cy="8" r="4" /><path d="M4 21v-1a6 6 0 0 1 6-6h4a6 6 0 0 1 6 6v1" />
          </template>
        </svg>
        <span v-if="tab.path === '/alerts' && alerts.unackedCount > 0" class="dot" />
      </span>
      <span class="tab-label">{{ tab.label }}</span>
    </RouterLink>
  </nav>
</template>

<style scoped>
/* 移动端底部 Tab——只在窄屏出现 */
.tabbar {
  display: none;
}

@media (max-width: 767px) {
  .tabbar {
    position: fixed;
    bottom: 0;
    left: 0;
    right: 0;
    height: calc(var(--tabbar-height) + env(safe-area-inset-bottom, 0px));
    padding-bottom: env(safe-area-inset-bottom, 0px);
    display: flex;
    background: var(--bg-surface);
    border-top: 1px solid var(--line-color);
    z-index: 50;
  }

  .tab {
    flex: 1;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 2px;
    color: var(--text-tertiary);
    font-size: 10px;
    transition: color var(--duration-fast) var(--ease-out);
  }

  .tab.active {
    color: var(--accent);
  }

  .tab-icon {
    position: relative;
    display: flex;
  }

  .tab-label {
    font-size: 10px;
  }

  .dot {
    position: absolute;
    top: 0;
    right: -3px;
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--critical);
  }
}
</style>
