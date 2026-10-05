/**
 * 路由
 *
 * 用动态 import 让每个页面白名单独立 chunk——
 * 首屏只加载总览页的代码，这是控制首屏体积最有效的一招。
 */

import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/LoginView.vue'),
    meta: { public: true, title: '登录' },
  },
  {
    path: '/',
    name: 'dashboard',
    component: () => import('@/views/DashboardView.vue'),
    meta: { title: '总览' },
  },
  {
    path: '/nodes',
    name: 'nodes',
    component: () => import('@/views/NodesView.vue'),
    meta: { title: '服务器' },
  },
  {
    path: '/nodes/new',
    name: 'node-new',
    component: () => import('@/views/NodeEditView.vue'),
    meta: { title: '添加节点', requires: 'admin' },
  },
  {
    path: '/nodes/:uid',
    name: 'node-detail',
    component: () => import('@/views/NodeDetailView.vue'),
    meta: { title: '节点详情' },
  },
  {
    path: '/monitors',
    name: 'monitors',
    component: () => import('@/views/MonitorsView.vue'),
    meta: { title: '网站监控' },
  },
  {
    path: '/monitors/new',
    name: 'monitor-new',
    component: () => import('@/views/MonitorEditView.vue'),
    meta: { title: '添加监控', requires: 'admin' },
  },
  {
    path: '/monitors/:id',
    name: 'monitor-detail',
    component: () => import('@/views/MonitorDetailView.vue'),
    meta: { title: '监控详情' },
  },
  {
    path: '/alerts',
    name: 'alerts',
    component: () => import('@/views/AlertsView.vue'),
    meta: { title: '告警' },
  },
  {
    path: '/alert-rules',
    name: 'alert-rules',
    component: () => import('@/views/AlertRulesView.vue'),
    meta: { title: '告警规则', requires: 'admin' },
  },
  {
    path: '/channels',
    name: 'channels',
    component: () => import('@/views/ChannelsView.vue'),
    meta: { title: '通知通道', requires: 'admin' },
  },
  {
    path: '/users',
    name: 'users',
    component: () => import('@/views/UsersView.vue'),
    meta: { title: '用户管理', requires: 'owner' },
  },
  {
    path: '/audit-logs',
    name: 'audit-logs',
    component: () => import('@/views/AuditLogsView.vue'),
    meta: { title: '审计日志', requires: 'owner' },
  },
  {
    // /settings 直接重定向到可见性策略——
    // 目前 Owner 只有这一个设置页，与其显示一个空壳不如直达
    path: '/settings',
    redirect: '/settings/visibility',
  },
  {
    path: '/settings/visibility',
    name: 'settings-visibility',
    component: () => import('@/views/SettingsView.vue'),
    meta: { title: '可见性策略', requires: 'owner' },
  },
  {
    path: '/status',
    name: 'public-status',
    component: () => import('@/views/PublicStatusView.vue'),
    meta: { public: true, title: '服务状态' },
  },
  {
    path: '/:pathMatch(.*)*',
    name: 'not-found',
    component: () => import('@/views/NotFoundView.vue'),
    meta: { public: true, title: '页面不存在' },
  },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior(to, _from, savedPosition) {
    // 后退时恢复滚动位置，前进时回到顶部
    return savedPosition ?? { top: 0 }
  },
})

router.beforeEach(async (to) => {
  const auth = useAuthStore()

  // 首次进入时加载用户信息（刷新页面后令牌还在但用户信息没了）
  if (!auth.initialized) {
    await auth.loadUser()
  }

  if (to.meta.public) {
    // 已登录用户不该再看登录页
    if (to.name === 'login' && auth.isLoggedIn) return { name: 'dashboard' }
    return true
  }

  if (!auth.isLoggedIn) {
    // 带上原目标，登录后跳回去
    return { name: 'login', query: { redirect: to.fullPath } }
  }

  const required = to.meta.requires
  if (required && !auth.canAccess(required as 'admin' | 'owner')) {
    return { name: 'dashboard' }
  }

  return true
})
