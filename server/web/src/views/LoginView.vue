<script setup lang="ts">
import { ref } from 'vue'
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
const submitting = ref(false)
const error = ref('')
const showPassword = ref(false)

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
    <button class="theme-toggle" @click="theme.toggle()" aria-label="切换主题">
      <svg v-if="theme.resolved === 'dark'" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
        <circle cx="12" cy="12" r="4" />
        <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
      </svg>
      <svg v-else viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
        <path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z" />
      </svg>
    </button>

    <div class="login-box">
      <div class="login-head">
        <svg class="logo" viewBox="0 0 24 24" width="28" height="28" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path d="M3 12h4l3-7 4 14 3-7h4" />
        </svg>
        <h1>ProbeOne</h1>
        <p class="subtitle">服务器与网站监控</p>
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

        <div class="field">
          <label for="totp">
            验证码
            <span class="optional">（已开启两步验证时填写）</span>
          </label>
          <input
            id="totp"
            v-model="totp"
            type="text"
            inputmode="numeric"
            autocomplete="one-time-code"
            placeholder="6 位动态验证码"
            maxlength="6"
          />
        </div>

        <p v-if="error" class="error" role="alert">{{ error }}</p>

        <button type="submit" class="submit" :disabled="submitting">
          {{ submitting ? '登录中…' : '登录' }}
        </button>
      </form>

      <RouterLink to="/status" class="status-link">查看公开状态页 →</RouterLink>
    </div>
  </div>
</template>

<style scoped>
.login-page {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: var(--space-5);
  background: var(--bg-body);
  position: relative;
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
  transition: color var(--duration-fast) var(--ease-out);
}

.theme-toggle:hover {
  color: var(--text-primary);
}

.login-box {
  width: 100%;
  max-width: 360px;
  background: var(--bg-surface);
  border: 1px solid var(--line-color);
  border-radius: var(--radius-lg);
  padding: var(--space-7);
}

.login-head {
  text-align: center;
  margin-bottom: var(--space-6);
}

.logo {
  color: var(--accent);
  margin-bottom: var(--space-3);
}

.login-head h1 {
  font-size: var(--font-lg);
  letter-spacing: -0.01em;
}

.subtitle {
  margin-top: var(--space-1);
  font-size: var(--font-sm);
  color: var(--text-tertiary);
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

.optional {
  color: var(--text-tertiary);
  font-weight: 400;
}

.field input {
  height: 38px;
  padding: 0 var(--space-3);
  border: 1px solid var(--line-color);
  border-radius: var(--radius-md);
  background: var(--bg-body);
  color: var(--text-primary);
  font-size: var(--font-base);
  font-family: inherit;
  transition: border-color var(--duration-fast) var(--ease-out);
}

.field input:focus {
  outline: none;
  border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-subtle);
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
  cursor: pointer;
  padding: 4px 6px;
  border-radius: var(--radius-sm);
}

.toggle-pw:hover {
  color: var(--text-primary);
}

.error {
  font-size: var(--font-sm);
  color: var(--critical);
  padding: var(--space-2) var(--space-3);
  background: var(--critical-subtle);
  border-radius: var(--radius-md);
}

.submit {
  height: 38px;
  border: none;
  border-radius: var(--radius-md);
  background: var(--accent);
  color: #fff;
  font-size: var(--font-base);
  font-weight: 500;
  font-family: inherit;
  cursor: pointer;
  transition:
    background-color var(--duration-fast) var(--ease-out),
    opacity var(--duration-fast) var(--ease-out);
}

.submit:hover:not(:disabled) {
  background: var(--accent-hover);
}

.submit:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.status-link {
  display: block;
  text-align: center;
  margin-top: var(--space-5);
  font-size: var(--font-sm);
  color: var(--text-tertiary);
}

.status-link:hover {
  color: var(--accent);
}

@media (max-width: 480px) {
  .login-box {
    padding: var(--space-5);
  }
}
</style>
