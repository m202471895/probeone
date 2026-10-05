<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import { humanizeError } from '@/api/client'

const auth = useAuthStore()
const theme = useThemeStore()
const router = useRouter()
const route = useRoute()

const username = ref('')
const password = ref('')
const totp = ref('')
const showTotp = ref(false)
const submitting = ref(false)
const error = ref('')
const showPassword = ref(false)

const canSubmit = computed(
  () => username.value.trim() !== '' && password.value !== '' && !submitting.value,
)

async function submit(): Promise<void> {
  if (!username.value || !password.value) {
    error.value = '请输入用户名和密码'
    return
  }
  submitting.value = true
  error.value = ''
  try {
    await auth.login(username.value, password.value, totp.value)
    const redirect = route.query.redirect
    void router.push(typeof redirect === 'string' ? redirect : '/')
  } catch (err) {
    error.value = humanizeError(err, '登录失败，请稍后重试')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="login-page">
    <button
      class="theme-toggle"
      :aria-label="theme.resolved === 'dark' ? '切换到浅色主题' : '切换到深色主题'"
      @click="theme.toggle()"
    >
      <svg
        v-if="theme.resolved === 'dark'"
        viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor"
        stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"
      >
        <circle cx="12" cy="12" r="4" />
        <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
      </svg>
      <svg
        v-else
        viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor"
        stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"
      >
        <path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z" />
      </svg>
    </button>

    <!-- 左栏：品牌与价值说明（宽屏才出现，给页面撑起结构） -->
    <aside class="aside">
      <div class="brand">
        <svg
          class="brand-mark" viewBox="0 0 24 24" width="30" height="30" fill="none"
          stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
          aria-hidden="true"
        >
          <path d="M3 12h4l3-7 4 14 3-7h4" />
        </svg>
        <span class="brand-name">ProbeOne</span>
      </div>

      <h2 class="aside-title">看得见的服务器，<br />靠得住的网站。</h2>
      <p class="aside-desc">
        自托管的探针系统。数据不出你的服务器，Agent 只读不执行命令。
      </p>

      <ul class="features">
        <li v-for="f in [
          { t: '资源监控', d: 'CPU、内存、磁盘、网络，10 秒一次' },
          { t: '网站可用性', d: '响应时间、状态码、SSL 证书临期提醒' },
          { t: '升配感知', d: '规格变了自动记录，不漏一次变更' },
          { t: '通知触达', d: '企微、钉钉、飞书、Telegram、邮件' },
        ]" :key="f.t" class="feature">
          <span class="feature-dot" />
          <div>
            <span class="feature-title">{{ f.t }}</span>
            <span class="feature-desc">{{ f.d }}</span>
          </div>
        </li>
      </ul>
    </aside>

    <!-- 右栏：登录表单 -->
    <main class="main">
      <div class="box">
        <div class="box-head">
          <h1 class="box-title">登录</h1>
          <p class="box-sub">使用管理员账号进入控制台</p>
        </div>

        <form class="form" @submit.prevent="submit">
          <div class="field">
            <label for="username">用户名</label>
            <input
              id="username"
              v-model="username"
              type="text"
              autocomplete="username"
              placeholder="请输入用户名"
              :disabled="submitting"
              required
            />
          </div>

          <div class="field">
            <label for="password">密码</label>
            <div class="input-wrap">
              <input
                id="password"
                v-model="password"
                :type="showPassword ? 'text' : 'password'"
                autocomplete="current-password"
                placeholder="请输入密码"
                :disabled="submitting"
                required
              />
              <button
                type="button"
                class="toggle-pw"
                :aria-label="showPassword ? '隐藏密码' : '显示密码'"
                @click="showPassword = !showPassword"
              >
                {{ showPassword ? '隐藏' : '显示' }}
              </button>
            </div>
          </div>

          <!-- 动态验证码：默认收起，多数用户不需要 -->
          <div class="field">
            <button
              type="button"
              class="link-btn"
              :aria-expanded="showTotp"
              @click="showTotp = !showTotp"
            >
              {{ showTotp ? '收起验证码' : '已开启两步验证？' }}
            </button>
            <input
              v-if="showTotp"
              v-model="totp"
              type="text"
              inputmode="numeric"
              autocomplete="one-time-code"
              placeholder="6 位动态验证码"
              maxlength="6"
              class="totp"
            />
          </div>

          <p v-if="error" class="error" role="alert">
            <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor"
              stroke-width="2" stroke-linecap="round">
              <circle cx="12" cy="12" r="9" /><path d="M12 8v5M12 16h.01" />
            </svg>
            <span>{{ error }}</span>
          </p>

          <button type="submit" class="submit" :disabled="!canSubmit">
            <span v-if="submitting" class="spinner" aria-hidden="true" />
            {{ submitting ? '登录中' : '登录' }}
          </button>
        </form>

        <RouterLink to="/status" class="status-link">
          查看公开状态页
          <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor"
            stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
            <path d="M5 12h14M13 6l6 6-6 6" />
          </svg>
        </RouterLink>
      </div>
    </main>
  </div>
</template>

<style scoped>
.login-page {
  min-height: 100vh;
  display: grid;
  grid-template-columns: 1fr;
  /*
   * 整行垂直居中用 align-content；
   * 列内用 align-items: start 让两栏顶部对齐——
   * 之前两层都用 center，行高被 aside 撑高后右侧卡片悬在中间，
   * 与左栏底部不齐，视觉上像"没对齐"。
   */
  align-content: center;
  align-items: start;
  justify-items: stretch;
  padding: var(--space-6) var(--space-5);
  background: var(--bg-body);
  position: relative;
}

@media (min-width: 960px) {
  .login-page {
    grid-template-columns: 1fr 400px;
    column-gap: var(--space-8);
    max-width: 1060px;
    margin: 0 auto;
  }
}

.theme-toggle {
  position: absolute;
  top: var(--space-5);
  right: var(--space-5);
  width: 32px;
  height: 32px;
  display: flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--line-color);
  border-radius: var(--radius-md);
  background: var(--bg-surface);
  color: var(--text-secondary);
  cursor: pointer;
  z-index: 10;
  transition: color var(--duration-fast) var(--ease-out);
}

.theme-toggle:hover {
  color: var(--text-primary);
}

/* ---- 左栏品牌区 ---- */
.aside {
  display: none;
}

@media (min-width: 960px) {
  .aside {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
    padding-right: var(--space-6);
    /* 卡片内边距是 space-6，这里补一点让两栏视觉顶线齐平 */
    padding-top: var(--space-2);
  }
}

.brand {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}

.brand-mark {
  color: var(--accent);
}

.brand-name {
  font-size: var(--font-md);
  font-weight: 600;
  letter-spacing: -0.01em;
}

.aside-title {
  font-size: 34px;
  line-height: 1.3;
  font-weight: 600;
  letter-spacing: -0.02em;
  color: var(--text-primary);
  margin: var(--space-2) 0 0;
}

.aside-desc {
  font-size: var(--font-base);
  color: var(--text-secondary);
  line-height: 1.7;
  max-width: 40ch;
}

.features {
  list-style: none;
  margin: var(--space-3) 0 0;
  padding: var(--space-4) 0 0;
  border-top: 1px solid var(--line-color);
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}

.feature {
  display: flex;
  align-items: flex-start;
  gap: var(--space-3);
}

.feature-dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--accent);
  margin-top: 7px;
  flex-shrink: 0;
}

.feature-title {
  display: block;
  font-size: var(--font-sm);
  font-weight: 500;
  color: var(--text-primary);
}

.feature-desc {
  display: block;
  font-size: var(--font-xs);
  color: var(--text-tertiary);
  margin-top: 1px;
}

/* ---- 右栏表单 ---- */
.main {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  padding: var(--space-5);
}

@media (min-width: 960px) {
  .main {
    padding: 0;
  }
}

.box {
  width: 100%;
  max-width: 360px;
  background: var(--bg-surface);
  border: 1px solid var(--line-color);
  border-radius: var(--radius-lg);
  padding: var(--space-6);
  /* 扁平化：不用阴影，用边框 + 表面色差表达层级 */
}

@media (max-width: 559px) {
  .box {
    background: transparent;
    border: none;
    padding: 0;
  }
}

.box-head {
  margin-bottom: var(--space-5);
}

.box-title {
  font-size: var(--font-xl);
  font-weight: 600;
  letter-spacing: -0.02em;
  margin: 0 0 var(--space-1);
}

.box-sub {
  font-size: var(--font-sm);
  color: var(--text-tertiary);
  margin: 0;
}

.form {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}

.field {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}

.field label {
  font-size: var(--font-sm);
  color: var(--text-secondary);
  font-weight: 500;
}

.field input {
  height: 40px;
  padding: 0 var(--space-3);
  border: 1px solid var(--line-color);
  border-radius: var(--radius-md);
  background: var(--bg-surface);
  color: var(--text-primary);
  font-size: var(--font-base);
  font-family: inherit;
  transition:
    border-color var(--duration-fast) var(--ease-out),
    box-shadow var(--duration-fast) var(--ease-out);
}

.field input:focus {
  outline: none;
  border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-subtle);
}

.field input:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.field input::placeholder {
  color: var(--text-tertiary);
}

.input-wrap {
  position: relative;
  display: flex;
}

.input-wrap input {
  flex: 1;
  padding-right: 56px;
}

.toggle-pw {
  position: absolute;
  right: var(--space-2);
  top: 50%;
  transform: translateY(-50%);
  border: none;
  background: none;
  color: var(--text-tertiary);
  font-size: var(--font-xs);
  font-family: inherit;
  cursor: pointer;
  padding: 4px 6px;
  border-radius: var(--radius-sm);
  transition: color var(--duration-fast) var(--ease-out);
}

.toggle-pw:hover {
  color: var(--text-primary);
}

.link-btn {
  align-self: flex-start;
  border: none;
  background: none;
  color: var(--text-tertiary);
  font-size: var(--font-xs);
  font-family: inherit;
  cursor: pointer;
  padding: 0;
  transition: color var(--duration-fast) var(--ease-out);
}

.link-btn:hover {
  color: var(--accent);
}

.totp {
  letter-spacing: 0.2em;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}

.error {
  display: flex;
  align-items: flex-start;
  gap: var(--space-2);
  font-size: var(--font-sm);
  color: var(--critical);
  padding: var(--space-2) var(--space-3);
  background: var(--critical-subtle);
  border-radius: var(--radius-md);
  line-height: 1.5;
}

.error svg {
  flex-shrink: 0;
  margin-top: 1px;
}

.submit {
  height: 40px;
  border: none;
  border-radius: var(--radius-md);
  /* 未填完整时降透明度要克制——太淡会让人以为是禁用而不是"还没填完" */
  background: var(--accent);
  color: #fff;
  font-size: var(--font-base);
  font-weight: 500;
  font-family: inherit;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--space-2);
  transition:
    background-color var(--duration-fast) var(--ease-out),
    opacity var(--duration-fast) var(--ease-out);
}

.submit:hover:not(:disabled) {
  background: var(--accent-hover);
}

.submit:disabled {
  opacity: 0.65;
  cursor: not-allowed;
}

.spinner {
  width: 13px;
  height: 13px;
  border: 2px solid rgba(255, 255, 255, 0.35);
  border-top-color: #fff;
  border-radius: 50%;
  animation: spin 0.7s linear infinite;
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}

@media (prefers-reduced-motion: reduce) {
  .spinner {
    animation-duration: 1.5s;
  }
}

.status-link {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin-top: var(--space-5);
  font-size: var(--font-sm);
  color: var(--text-tertiary);
  transition: color var(--duration-fast) var(--ease-out);
}

.status-link:hover {
  color: var(--accent);
}

@media (max-width: 480px) {
  .main {
    padding: var(--space-4);
  }
}
</style>
