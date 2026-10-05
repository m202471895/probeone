import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import { setUnauthorizedHandler, humanizeError } from './api/client'
import { realtime } from './api/realtime'
import { useAuthStore } from './stores/auth'
import { useThemeStore } from './stores/theme'

import './styles/tokens.css'
import './styles/base.css'

const app = createApp(App)
const pinia = createPinia()

app.use(pinia)
app.use(router)

// 主题要在首屏渲染前定好，否则会闪一下白
useThemeStore().init()

// 401 时统一处理：断实时连接 + 清登录态 + 跳登录页。
// 散落在各页面处理会漏，且漏了就是"页面一直空白不知道为啥"。
setUnauthorizedHandler(() => {
  realtime.disconnect()
  useAuthStore().clear()
  const current = router.currentRoute.value
  if (current.name !== 'login') {
    void router.replace({ name: 'login', query: { redirect: current.fullPath } })
  }
})

// 全局兜底：未捕获的 promise 异常至少要留下痕迹，
// 否则页面上会表现为"按钮点了没反应"且无迹可寻。
window.addEventListener('unhandledrejection', (evt) => {
  const msg = humanizeError(evt.reason)
  if (msg) console.error('[probeone] 未处理的异步异常:', msg, evt.reason)
})

window.addEventListener('error', (evt) => {
  console.error('[probeone] 未捕获错误:', evt.error ?? evt.message)
})

app.mount('#app')

// 移除首屏加载层。
// 它只在挂载前存在，作用是遮住"JS 还没执行完"的那几毫秒；
// 挂载完成后必须移除，否则会一直盖在页面上（position: fixed），
// 表现为「页面一直在加载」。
const boot = document.getElementById('app-boot')
if (boot) {
  // 先淡出再移除，避免硬跳变
  boot.style.opacity = '0'
  window.setTimeout(() => boot.remove(), 200)
}
