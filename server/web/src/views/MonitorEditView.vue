<script setup lang="ts">
/** 添加监控 */
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import CardBox from '@/components/CardBox.vue'
import { humanizeError } from '@/api/client'
import { useMonitorStore } from '@/stores/monitor'
import type { MonitorType } from '@/api/types'

const router = useRouter()
const store = useMonitorStore()

const type = ref<MonitorType>('http')
const name = ref('')
const target = ref('')
const intervalSec = ref(60)
const timeoutSec = ref(10)
const isPublic = ref(false)
// HTTP 专用
const method = ref('GET')
const expectStatus = ref('200')
const expectKeywords = ref('')
const maxLatencyMs = ref(0)
// TCP 专用
const port = ref<number | null>(null)
// DNS 专用
const record = ref('A')
const expectIPs = ref('')
// SSL 专用
const warnDaysLeft = ref(30)

const submitting = ref(false)
const error = ref('')

async function submit(): Promise<void> {
  if (!name.value.trim() || !target.value.trim()) {
    error.value = '请填写名称与目标地址'
    return
  }
  submitting.value = true
  error.value = ''
  try {
    const config: Record<string, unknown> = {}
    if (type.value === 'http') {
      config['method'] = method.value
      config['expect_status'] = expectStatus.value
        .split(',').map((s) => parseInt(s.trim(), 10)).filter((n) => !Number.isNaN(n))
      if (expectKeywords.value.trim()) {
        config['expect_keywords'] = expectKeywords.value.split(',').map((s) => s.trim()).filter(Boolean)
      }
      if (maxLatencyMs.value > 0) config['max_latency_ms'] = maxLatencyMs.value
    } else if (type.value === 'tcp') {
      if (!port.value) { error.value = '请填写端口'; submitting.value = false; return }
      config['port'] = port.value
    } else if (type.value === 'dns') {
      config['record'] = record.value
      if (expectIPs.value.trim()) {
        config['expect_ips'] = expectIPs.value.split(',').map((s) => s.trim()).filter(Boolean)
      }
    } else if (type.value === 'ssl') {
      if (port.value) config['port'] = port.value
      config['warn_days_left'] = warnDaysLeft.value
    }

    await store.create({
      name: name.value.trim(),
      type: type.value,
      target: target.value.trim(),
      interval_sec: intervalSec.value,
      timeout_sec: timeoutSec.value,
      is_public: isPublic.value,
      config,
    } as never)
    void router.push('/monitors')
  } catch (err) {
    error.value = humanizeError(err, '创建监控失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="edit">
    <CardBox title="添加监控">
      <form class="form" @submit.prevent="submit">
        <div class="type-row">
          <label v-for="t in (['http','tcp','dns','ssl'] as const)" :key="t"
            class="type-opt" :class="{ active: type === t }">
            <input v-model="type" type="radio" :value="t" />
            <span>{{ { http: 'HTTP(S)', tcp: 'TCP 端口', dns: 'DNS 解析', ssl: 'SSL 证书' }[t] }}</span>
          </label>
        </div>

        <div class="field">
          <label for="mname">名称</label>
          <input id="mname" v-model="name" type="text" placeholder="如：官网首页" required />
        </div>

        <div class="field">
          <label for="target">目标</label>
          <input id="target" v-model="target" type="text"
            :placeholder="type === 'http' ? 'https://example.com' : type === 'tcp' ? 'example.com' : type === 'dns' ? 'example.com' : 'example.com'"
            required />
          <span class="hint">
            {{ type === 'http' ? '完整 URL' : type === 'tcp' ? '主机名或 IP（端口在下方填）' : '域名（不带协议）' }}
          </span>
        </div>

        <div class="two-col">
          <div class="field">
            <label for="iv">检查间隔（秒）</label>
            <input id="iv" v-model.number="intervalSec" type="number" min="10" max="86400" />
          </div>
          <div class="field">
            <label for="to">超时（秒）</label>
            <input id="to" v-model.number="timeoutSec" type="number" min="1" max="60" />
          </div>
        </div>

        <!-- HTTP 专属 -->
        <template v-if="type === 'http'">
          <div class="two-col">
            <div class="field">
              <label for="method">请求方法</label>
              <select id="method" v-model="method">
                <option>GET</option><option>HEAD</option><option>POST</option>
              </select>
            </div>
            <div class="field">
              <label for="st">期望状态码</label>
              <input id="st" v-model="expectStatus" type="text" placeholder="200" />
              <span class="hint">多个用逗号分隔；留空则 2xx/3xx 均算正常</span>
            </div>
          </div>
          <div class="field">
            <label for="kw">关键字</label>
            <input id="kw" v-model="expectKeywords" type="text" placeholder="登录成功,dashboard" />
            <span class="hint">响应体需包含全部关键字（不区分大小写），多个用逗号分隔</span>
          </div>
          <div class="field">
            <label for="ml">响应时间阈值（毫秒）</label>
            <input id="ml" v-model.number="maxLatencyMs" type="number" min="0" placeholder="0" />
            <span class="hint">0 表示不限制；超过阈值记为「响应过慢」</span>
          </div>
        </template>

        <!-- TCP 专属 -->
        <div v-else-if="type === 'tcp'" class="field">
          <label for="port">端口</label>
          <input id="port" v-model.number="port" type="number" min="1" max="65535" placeholder="443" />
        </div>

        <!-- DNS 专属 -->
        <template v-else-if="type === 'dns'">
          <div class="two-col">
            <div class="field">
              <label for="rec">记录类型</label>
              <select id="rec" v-model="record">
                <option value="A">A</option><option value="AAAA">AAAA</option>
                <option value="CNAME">CNAME</option>
              </select>
            </div>
            <div class="field">
              <label for="eip">期望解析结果</label>
              <input id="eip" v-model="expectIPs" type="text" placeholder="203.0.113.10" />
              <span class="hint">多个用逗号分隔；留空则只检查能否解析</span>
            </div>
          </div>
        </template>

        <!-- SSL 专属 -->
        <template v-else-if="type === 'ssl'">
          <div class="two-col">
            <div class="field">
              <label for="sport">端口</label>
              <input id="sport" v-model.number="port" type="number" min="1" max="65535" placeholder="443" />
            </div>
            <div class="field">
              <label for="warn">临期提醒（天）</label>
              <input id="warn" v-model.number="warnDaysLeft" type="number" min="1" max="365" />
              <span class="hint">剩余天数低于该值时告警</span>
            </div>
          </div>
        </template>

        <div class="field">
          <label class="check">
            <input v-model="isPublic" type="checkbox" />
            <span>在公开状态页显示</span>
          </label>
        </div>

        <p v-if="error" class="error">{{ error }}</p>

        <div class="actions">
          <button type="button" class="btn-ghost" @click="router.back()">取消</button>
          <button type="submit" class="btn-primary" :disabled="submitting">
            {{ submitting ? '创建中…' : '创建监控' }}
          </button>
        </div>
      </form>
    </CardBox>
  </div>
</template>

<style scoped>
.edit { max-width: 680px; }
.form { display: flex; flex-direction: column; gap: var(--space-4); }
.type-row { display: grid; grid-template-columns: repeat(4, 1fr); gap: var(--space-2); }
.type-opt { display: flex; align-items: center; justify-content: center; gap: var(--space-2);
  height: 36px; border: 1px solid var(--line-color); border-radius: var(--radius-md);
  font-size: var(--font-sm); color: var(--text-secondary); cursor: pointer;
  transition: border-color var(--duration-fast) var(--ease-out); }
.type-opt.active { border-color: var(--accent); background: var(--accent-subtle); color: var(--accent); }
.type-opt input { display: none; }

.field { display: flex; flex-direction: column; gap: var(--space-2); }
.field label { font-size: var(--font-sm); color: var(--text-secondary); font-weight: 500; }
.field input, .field select { height: 36px; padding: 0 var(--space-3);
  border: 1px solid var(--line-color); border-radius: var(--radius-md);
  background: var(--bg-body); color: var(--text-primary);
  font-size: var(--font-base); font-family: inherit; }
.field input:focus, .field select:focus { outline: none; border-color: var(--accent); }
.hint { font-size: var(--font-xs); color: var(--text-tertiary); }

.two-col { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-3); }

.check { display: flex; align-items: center; gap: var(--space-2); cursor: pointer;
  font-weight: 400 !important; }

.error { font-size: var(--font-sm); color: var(--critical); padding: var(--space-2) var(--space-3);
  background: var(--critical-subtle); border-radius: var(--radius-md); }

.actions { display: flex; justify-content: flex-end; gap: var(--space-2); }
.btn-primary, .btn-ghost { height: 34px; padding: 0 var(--space-4);
  border-radius: var(--radius-md); font-size: var(--font-sm); font-family: inherit;
  font-weight: 500; cursor: pointer; }
.btn-primary { background: var(--accent); color: #fff; border: none; }
.btn-primary:hover:not(:disabled) { background: var(--accent-hover); }
.btn-primary:disabled { opacity: 0.6; cursor: not-allowed; }
.btn-ghost { background: transparent; color: var(--text-secondary); border: 1px solid var(--line-color); }
.btn-ghost:hover { background: var(--bg-hover); }

@media (max-width: 767px) {
  .type-row { grid-template-columns: repeat(2, 1fr); }
  .two-col { grid-template-columns: 1fr; }
}
</style>
