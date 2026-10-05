<script setup lang="ts">
/**
 * 侧边栏
 *
 * 响应式（PRD 11.3）：
 *  ≥1024px  展开 220px
 *  768–1023 折叠为图标 64px
 *  <768     隐藏，由底部 Tab 承担导航
 *
 * 移动端重复一遍导航不是冗余：底部 Tab 是拇指可达区，
 * 侧边栏在窄屏上够不着。
 */
import { computed } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useAlertStore } from '@/stores/alert'

interface NavItem {
  path: string
  label: string
  icon: string
  requires?: 'admin' | 'owner'
}

interface NavGroup {
  label: string
  items: NavItem[]
}

const route = useRoute()
const auth = useAuthStore()
const alerts = useAlertStore()

const groups: NavGroup[] = [
  {
    label: '监控',
    items: [
      { path: '/', label: '总览', icon: 'grid' },
      { path: '/nodes', label: '服务器', icon: 'server' },
      { path: '/monitors', label: '网站监控', icon: 'globe' },
      { path: '/alerts', label: '告警', icon: 'bell' },
    ],
  },
  {
    label: '管理',
    requires: 'admin',
    items: [
      { path: '/alert-rules', label: '告警规则', icon: 'sliders', requires: 'admin' },
      { path: '/channels', label: '通知通道', icon: 'send', requires: 'admin' },
    ],
  },
  {
    label: '系统',
    requires: 'owner',
    items: [
      { path: '/settings/visibility', label: '可见性策略', icon: 'eye', requires: 'owner' },
      { path: '/users', label: '用户管理', icon: 'users', requires: 'owner' },
      { path: '/audit-logs', label: '审计日志', icon: 'history', requires: 'owner' },
    ],
  },
]

const visibleGroups = computed(() =>
  groups
    .filter((g) => !g.requires || auth.canAccess(g.requires))
    .map((g) => ({
      ...g,
      items: g.items.filter((i) => !i.requires || auth.canAccess(i.requires)),
    }))
    .filter((g) => g.items.length > 0),
)

function isActive(path: string): boolean {
  if (path === '/') return route.path === '/'
  return route.path.startsWith(path)
}

function badgeOf(path: string): number {
  if (path === '/alerts') return alerts.unackedCount
  return 0
}
</script>

<template>
  <aside class="sidebar">
    <div class="brand">
      <svg class="brand-mark" viewBox="0 0 24 24" width="22" height="22" aria-hidden="true">
        <path
          d="M3 12h4l3-7 4 14 3-7h4"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
        />
      </svg>
      <span class="brand-name">ProbeOne</span>
    </div>

    <nav class="nav" aria-label="主导航">
      <div v-for="group in visibleGroups" :key="group.label" class="nav-group">
        <div class="nav-group-label">{{ group.label }}</div>
        <RouterLink
          v-for="item in group.items"
          :key="item.path"
          :to="item.path"
          class="nav-item"
          :class="{ active: isActive(item.path) }"
          :title="item.label"
        >
          <span class="nav-icon" aria-hidden="true">
            <svg viewBox="0 0 24 24" width="17" height="17" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
              <template v-if="item.icon === 'grid'">
                <rect x="3" y="3" width="7" height="7" rx="1" /><rect x="14" y="3" width="7" height="7" rx="1" />
                <rect x="3" y="14" width="7" height="7" rx="1" /><rect x="14" y="14" width="7" height="7" rx="1" />
              </template>
              <template v-else-if="item.icon === 'server'">
                <rect x="3" y="4" width="18" height="7" rx="1.5" /><rect x="3" y="13" width="18" height="7" rx="1.5" />
                <path d="M7 7.5h.01M7 16.5h.01" />
              </template>
              <template v-else-if="item.icon === 'globe'">
                <circle cx="12" cy="12" r="9" /><path d="M3 12h18M12 3a15 15 0 0 1 0 18 15 15 0 0 1 0-18" />
              </template>
              <template v-else-if="item.icon === 'bell'">
                <path d="M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9" /><path d="M13.7 21a2 2 0 0 1-3.4 0" />
              </template>
              <template v-else-if="item.icon === 'sliders'">
                <path d="M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6" />
              </template>
              <template v-else-if="item.icon === 'send'">
                <path d="M22 2 11 13M22 2l-7 20-4-9-9-4 20-7z" />
              </template>
              <template v-else-if="item.icon === 'eye'">
                <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z" /><circle cx="12" cy="12" r="3" />
              </template>
              <template v-else-if="item.icon === 'users'">
                <path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2" /><circle cx="9" cy="7" r="4" />
                <path d="M23 21v-2a4 4 0 0 0-3-3.87M16 3.13a4 4 0 0 1 0 7.75" />
              </template>
              <template v-else-if="item.icon === 'history'">
                <path d="M3 3v5h5" /><path d="M3.05 13A9 9 0 1 0 6 5.3L3 8" /><path d="M12 7v5l4 2" />
              </template>
            </svg>
          </span>
          <span class="nav-label">{{ item.label }}</span>
          <span v-if="badgeOf(item.path) > 0" class="nav-badge">{{ badgeOf(item.path) }}</span>
        </RouterLink>
      </div>
    </nav>
  </aside>
</template>

<style scoped>
.sidebar {
  display: flex;
  flex-direction: column;
  width: var(--sidebar-width);
  flex-shrink: 0;
  height: 100vh;
  position: sticky;
  top: 0;
  background: var(--bg-surface);
  border-right: 1px solid var(--line-color);
  overflow-y: auto;
  overflow-x: hidden;
}

.brand {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  height: var(--header-height);
  padding: 0 var(--space-4);
  flex-shrink: 0;
  border-bottom: 1px solid var(--line-color);
}

.brand-mark {
  color: var(--accent);
  flex-shrink: 0;
}

.brand-name {
  font-size: var(--font-md);
  font-weight: 600;
  letter-spacing: -0.01em;
  white-space: nowrap;
}

.nav {
  flex: 1;
  padding: var(--space-4) var(--space-3);
}

.nav-group + .nav-group {
  margin-top: var(--space-5);
}

.nav-group-label {
  font-size: var(--font-xs);
  color: var(--text-tertiary);
  padding: 0 var(--space-3);
  margin-bottom: var(--space-2);
  font-weight: 500;
  letter-spacing: 0.02em;
}

.nav-item {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: 0 var(--space-3);
  height: 36px;
  border-radius: var(--radius-md);
  color: var(--text-secondary);
  font-size: var(--font-base);
  transition:
    background-color var(--duration-fast) var(--ease-out),
    color var(--duration-fast) var(--ease-out);
}

.nav-item:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.nav-item.active {
  background: var(--accent-subtle);
  color: var(--accent);
  font-weight: 500;
}

.nav-icon {
  display: flex;
  flex-shrink: 0;
}

.nav-label {
  flex: 1;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.nav-badge {
  min-width: 18px;
  height: 18px;
  padding: 0 5px;
  border-radius: var(--radius-full);
  background: var(--critical);
  color: #fff;
  font-size: 11px;
  font-weight: 600;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

/* 中屏：折叠为纯图标 */
@media (max-width: 1023px) {
  .sidebar {
    width: var(--sidebar-collapsed);
  }
  .brand-name,
  .nav-label,
  .nav-group-label {
    display: none;
  }
  .brand {
    justify-content: center;
    padding: 0;
  }
  .nav {
    padding: var(--space-4) var(--space-2);
  }
  .nav-item {
    justify-content: center;
    padding: 0;
  }
  .nav-badge {
    position: absolute;
    transform: translate(10px, -8px);
  }
  .nav-item {
    position: relative;
  }
}

/* 窄屏：完全隐藏，改用底部 Tab */
@media (max-width: 767px) {
  .sidebar {
    display: none;
  }
}
</style>
