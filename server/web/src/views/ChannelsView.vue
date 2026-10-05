<script setup lang="ts">
import { onMounted, ref } from 'vue'
import CardBox from '@/components/CardBox.vue'
import EmptyState from '@/components/EmptyState.vue'
import Skeleton from '@/components/Skeleton.vue'
import { humanizeError } from '@/api/client'
import { useAlertStore } from '@/stores/alert'
import { maskSecret } from '@/utils/format'
import type { AlertChannel, ChannelType } from '@/api/types'

const store = useAlertStore()
const creating = ref(false)
const testing = ref<number | null>(null)
const message = ref('')
const error = ref('')

const name = ref('')
const type = ref<ChannelType>('webhook')
const enabled = ref(true)
/** 通道配置按类型不同，存成 Record 后整体提交 */
const cfg = ref<Record<string, string>>({})
const webhookUrl = ref('')
const webhookSecret = ref('')
const wecomKey = ref('')
const dingToken = ref('')
const dingSecret = ref('')
const dingKeyword = ref('')
const fsWebhook = ref('')
const fsSecret = ref('')
const tgToken = ref('')
const tgChatId = ref('')
const barkKey = ref('')
const barkServer = ref('https://api.day.app')
const smtpHost = ref('')
const smtpPort = ref<number | null>(null)
const smtpFrom = ref('')
const smtpTo = ref('')
const smtpUser = ref('')
const smtpPass = ref('')
const smtpTLS = ref(false)
const smtpStartTLS = ref(false)

const TYPES: Array<{ v: ChannelType; label: string }> = [
  { v: 'webhook', label: 'Webhook' },
  { v: 'wecom', label: '企业微信机器人' },
  { v: 'dingtalk', label: '钉钉机器人' },
  { v: 'feishu', label: '飞书机器人' },
  { v: 'telegram', label: 'Telegram' },
  { v: 'bark', label: 'Bark（iOS）' },
  { v: 'email', label: '邮件 SMTP' },
]

const TYPE_LABEL: Record<string, string> = Object.fromEntries(TYPES.map((t) => [t.v, t.label]))

/** 通道配置里的敏感键，列表里只显示掩码。 */
const SECRET_KEYS = new Set(['secret', 'password', 'bot_token', 'device_key', 'access_token', 'key'])

function displayConfig(c: AlertChannel): string {
  const parts: string[] = []
  for (const [k, v] of Object.entries(c.config)) {
    if (v === undefined || v === null || v === '') continue
    parts.push(`${k} = ${SECRET_KEYS.has(k) ? maskSecret(String(v)) : String(v)}`)
  }
  return parts.join('，') || '—'
}

function buildConfig(): Record<string, unknown> {
  switch (type.value) {
    case 'webhook':
      return { url: webhookUrl.value, secret: webhookSecret.value || undefined }
    case 'wecom':
      return { key: wecomKey.value }
    case 'dingtalk':
      return {
        access_token: dingToken.value,
        secret: dingSecret.value || undefined,
        keyword: dingKeyword.value || undefined,
      }
    case 'feishu':
      return { webhook: fsWebhook.value, secret: fsSecret.value || undefined }
    case 'telegram':
      return { bot_token: tgToken.value, chat_id: tgChatId.value }
    case 'bark':
      return { device_key: barkKey.value, server: barkServer.value }
    case 'email':
      return {
        host: smtpHost.value, port: smtpPort.value, from: smtpFrom.value,
        to: smtpTo.value, user: smtpUser.value || undefined,
        password: smtpPass.value || undefined,
        tls: smtpTLS.value, starttls: smtpStartTLS.value,
      }
    default:
      return {}
  }
}

async function create(): Promise<void> {
  if (!name.value.trim()) { error.value = '请填写通道名称'; return }
  creating.value = true
  error.value = ''
  try {
    await store.fetchChannels()
    const created = await (
      await import('@/api/client')
    ).api.post<AlertChannel>('/api/channels', {
      name: name.value.trim(), type: type.value,
      config: buildConfig(), enabled: enabled.value,
    })
    store.channels.unshift(created)
    message.value = `通道「${created.name}」已创建`
    name.value = ''
  } catch (err) {
    error.value = humanizeError(err, '创建通道失败')
  } finally {
    creating.value = false
  }
}

async function test(c: AlertChannel): Promise<void> {
  testing.value = c.id
  message.value = ''
  try {
    await store.testChannel(c.id)
    message.value = `「${c.name}」测试消息已发送，请检查目标端`
  } catch (err) {
    error.value = humanizeError(err, '测试失败')
  } finally {
    testing.value = null
  }
}

onMounted(() => void store.fetchChannels())
</script>

<template>
  <div class="channels">
    <p v-if="message" class="msg-ok">{{ message }}</p>
    <p v-if="error" class="msg-err">{{ error }}</p>

    <CardBox title="已有通道" :padded="false">
      <Skeleton v-if="store.loading && store.channels.length === 0" type="row" :rows="3" />

      <EmptyState v-else-if="store.channels.length === 0" icon="search"
        title="还没有配置通知通道" description="配置后告警才能推送到你的 IM 或邮箱" />
      <ul v-else class="list">
        <li v-for="c in store.channels" :key="c.id" class="item">
          <div class="item-main">
            <div class="item-head">
              <span class="item-name">{{ c.name }}</span>
              <span class="type-tag">{{ TYPE_LABEL[c.type] || c.type }}</span>
              <span v-if="!c.enabled" class="off-tag">已停用</span>
            </div>
            <p class="item-cfg truncate" :title="displayConfig(c)">{{ displayConfig(c) }}</p>
          </div>
          <button class="btn-ghost" :disabled="testing === c.id" @click="test(c)">
            {{ testing === c.id ? '发送中…' : '测试' }}
          </button>
        </li>
      </ul>
    </CardBox>

    <CardBox title="新建通道">
      <form class="form" @submit.prevent="create">
        <div class="row">
          <div class="field grow">
            <label for="cname">通道名称</label>
            <input id="cname" v-model="name" type="text" placeholder="如：运维群" required />
          </div>
          <div class="field">
            <label for="ctype">类型</label>
            <select id="ctype" v-model="type">
              <option v-for="t in TYPES" :key="t.v" :value="t.v">{{ t.label }}</option>
            </select>
          </div>
        </div>

        <!-- Webhook -->
        <template v-if="type === 'webhook'">
          <div class="field">
            <label for="wurl">Webhook 地址</label>
            <input id="wurl" v-model="webhookUrl" type="text" placeholder="https://example.com/hook" />
          </div>
          <div class="field">
            <label for="wsec">签名密钥（可选）</label>
            <input id="wsec" v-model="webhookSecret" type="text" placeholder="留空则不签名" />
            <span class="hint">配置后会带上 X-ProbeOne-Signature: sha256=... 头</span>
          </div>
        </template>

        <template v-else-if="type === 'wecom'">
          <div class="field">
            <label for="wckey">机器人 Key</label>
            <input id="wckey" v-model="wecomKey" type="text" placeholder="群机器人 Webhook 的 key 参数" />
          </div>
        </template>

        <template v-else-if="type === 'dingtalk'">
          <div class="field">
            <label for="dtoken">access_token</label>
            <input id="dtoken" v-model="dingToken" type="text" />
          </div>
          <div class="row">
            <div class="field grow">
              <label for="dsec">加签密钥（可选）</label>
              <input id="dsec" v-model="dingSecret" type="text" />
            </div>
            <div class="field grow">
              <label for="dkw">关键词（可选）</label>
              <input id="dkw" v-model="dingKeyword" type="text" />
            </div>
          </div>
          <span class="hint">加签与关键词至少配置一个，否则钉钉会拒绝所有消息</span>
        </template>

        <template v-else-if="type === 'feishu'">
          <div class="field">
            <label for="fsurl">Webhook 地址</label>
            <input id="fsurl" v-model="fsWebhook" type="text" />
          </div>
          <div class="field">
            <label for="fssec">加签密钥（可选）</label>
            <input id="fssec" v-model="fsSecret" type="text" />
          </div>
        </template>

        <template v-else-if="type === 'telegram'">
          <div class="field">
            <label for="tgt">Bot Token</label>
            <input id="tgt" v-model="tgToken" type="text" placeholder="123456:ABC-DEF..." />
          </div>
          <div class="field">
            <label for="tgchat">Chat ID</label>
            <input id="tgchat" v-model="tgChatId" type="text" placeholder="-1001234567890" />
          </div>
        </template>

        <template v-else-if="type === 'bark'">
          <div class="row">
            <div class="field grow">
              <label for="bk">设备 Key</label>
              <input id="bk" v-model="barkKey" type="text" />
            </div>
            <div class="field grow">
              <label for="bs">服务器地址</label>
              <input id="bs" v-model="barkServer" type="text" />
            </div>
          </div>
        </template>

        <template v-else-if="type === 'email'">
          <div class="row">
            <div class="field grow"><label for="eh">SMTP 服务器</label>
              <input id="eh" v-model="smtpHost" type="text" placeholder="smtp.example.com" /></div>
            <div class="field"><label for="ep">端口</label>
              <input id="ep" v-model.number="smtpPort" type="number" placeholder="465" /></div>
          </div>
          <div class="field"><label for="ef">发件人</label>
            <input id="ef" v-model="smtpFrom" type="text" placeholder="probe@example.com" /></div>
          <div class="field"><label for="et">收件人</label>
            <input id="et" v-model="smtpTo" type="text" placeholder="多个用逗号分隔" /></div>
          <div class="row">
            <div class="field grow"><label for="eu">用户名（可选）</label>
              <input id="eu" v-model="smtpUser" type="text" /></div>
            <div class="field grow"><label for="ew">密码（可选）</label>
              <input id="ew" v-model="smtpPass" type="password" /></div>
          </div>
          <div class="row">
            <label class="check"><input v-model="smtpTLS" type="checkbox" />
              <span>TLS（465）</span></label>
            <label class="check"><input v-model="smtpStartTLS" type="checkbox" />
              <span>STARTTLS（587）</span></label>
          </div>
          <span class="hint">TLS 与 STARTTLS 不能同时开启</span>
        </template>

        <div class="row">
          <label class="check"><input v-model="enabled" type="checkbox" />
            <span>创建后立即启用</span></label>
        </div>

        <div class="actions">
          <button type="submit" class="btn-primary" :disabled="creating">
            {{ creating ? '创建中…' : '创建通道' }}
          </button>
        </div>
      </form>
    </CardBox>
  </div>
</template>

<style scoped>
.channels { display: flex; flex-direction: column; gap: var(--space-4); }
.msg-ok { padding: var(--space-2) var(--space-3); background: var(--down-subtle);
  color: var(--down); border-radius: var(--radius-md); font-size: var(--font-sm); }
.msg-err { padding: var(--space-2) var(--space-3); background: var(--critical-subtle);
  color: var(--critical); border-radius: var(--radius-md); font-size: var(--font-sm); }

.list { list-style: none; margin: 0; padding: 0; }
.item { display: flex; align-items: center; gap: var(--space-3);
  padding: var(--space-3) var(--space-5); border-bottom: 1px solid var(--line-color); }
.item:last-child { border-bottom: none; }
.item-main { flex: 1; min-width: 0; }
.item-head { display: flex; align-items: center; gap: var(--space-2); flex-wrap: wrap; }
.item-name { font-size: var(--font-sm); font-weight: 500; }
.type-tag { font-size: 10px; padding: 2px 6px; border-radius: var(--radius-sm);
  background: var(--bg-hover); color: var(--text-secondary); }
.off-tag { font-size: 10px; padding: 2px 6px; border-radius: var(--radius-sm);
  background: var(--neutral-subtle); color: var(--text-tertiary); }
.item-cfg { font-size: var(--font-xs); color: var(--text-tertiary); margin-top: 2px; }

.btn-ghost { flex-shrink: 0; height: 28px; padding: 0 var(--space-3);
  border: 1px solid var(--line-color); border-radius: var(--radius-md);
  background: transparent; color: var(--text-secondary);
  font-size: var(--font-xs); font-family: inherit; cursor: pointer; }
.btn-ghost:hover:not(:disabled) { background: var(--bg-hover); }
.btn-ghost:disabled { opacity: 0.6; cursor: not-allowed; }

.form { display: flex; flex-direction: column; gap: var(--space-4); }
.row { display: flex; gap: var(--space-3); align-items: flex-end; }
.field { display: flex; flex-direction: column; gap: var(--space-2); }
.field.grow { flex: 1; }
.field label { font-size: var(--font-sm); color: var(--text-secondary); font-weight: 500; }
.field input, .field select { height: 36px; padding: 0 var(--space-3);
  border: 1px solid var(--line-color); border-radius: var(--radius-md);
  background: var(--bg-body); color: var(--text-primary);
  font-size: var(--font-sm); font-family: inherit; }
.field input:focus, .field select:focus { outline: none; border-color: var(--accent); }
.hint { font-size: var(--font-xs); color: var(--text-tertiary); }
.check { display: flex; align-items: center; gap: var(--space-2);
  font-size: var(--font-sm); color: var(--text-secondary); cursor: pointer; }

.actions { display: flex; justify-content: flex-end; }
.btn-primary { height: 34px; padding: 0 var(--space-4); border: none;
  border-radius: var(--radius-md); background: var(--accent); color: #fff;
  font-size: var(--font-sm); font-family: inherit; font-weight: 500; cursor: pointer; }
.btn-primary:hover:not(:disabled) { background: var(--accent-hover); }
.btn-primary:disabled { opacity: 0.6; cursor: not-allowed; }

@media (max-width: 767px) {
  .row { flex-direction: column; align-items: stretch; }
  .item { padding: var(--space-3); }
}
</style>
