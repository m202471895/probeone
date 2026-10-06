<!--
  ModalDialog —— 通用模态框。

  为什么不各自实现：项目里此前没有任何模态框，
  两个对话框各写一套遮罩与容器样式，结果 CSS 变量用错、
  背景透明、布局散架。抽成组件后：
    - 遮罩、容器、头尾的样式只有一份，不会再漂移
    - 焦点管理与 Esc 关闭只实现一次
    - 变量名集中在这里，改主题时只改一处

  CSS 变量必须用项目 tokens.css 里的真实名字
  （--bg-surface / --line-color / --accent …），
  不要自造——不存在的变量会让整条声明失效，
  表现为"背景透明、边框消失"，很难一眼看出原因。
-->
<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'

const props = withDefaults(
  defineProps<{
    title: string
    /** 关闭按钮的 aria-label */
    closeLabel?: string
    /** 点击遮罩是否关闭。表单类对话框建议 false，避免误触丢输入。 */
    closeOnOverlay?: boolean
  }>(),
  { closeLabel: '关闭', closeOnOverlay: true },
)

const emit = defineEmits<{ close: [] }>()

const panel = ref<HTMLElement | null>(null)

function onKeydown(e: KeyboardEvent): void {
  if (e.key === 'Escape') {
    emit('close')
    return
  }
  // Tab 焦点循环：焦点跑到对话框外会让键盘用户"丢失位置"
  if (e.key !== 'Tab' || !panel.value) return
  const focusables = panel.value.querySelectorAll<HTMLElement>(
    'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
  )
  if (focusables.length === 0) return
  const first = focusables[0]!
  const last = focusables[focusables.length - 1]!
  if (e.shiftKey && document.activeElement === first) {
    e.preventDefault()
    last.focus()
  } else if (!e.shiftKey && document.activeElement === last) {
    e.preventDefault()
    first.focus()
  }
}

onMounted(() => {
  document.addEventListener('keydown', onKeydown)
  // 打开时把焦点移进对话框，键盘用户不用先 Tab 一遍找过来
  panel.value?.querySelector<HTMLElement>('input, select, button')?.focus()
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown)
})
</script>

<template>
  <div
    class="modal-overlay"
    @click.self="props.closeOnOverlay && emit('close')"
  >
    <div ref="panel" class="modal-panel" role="dialog" aria-modal="true" :aria-label="title">
      <header class="modal-head">
        <h2 class="modal-title">{{ title }}</h2>
        <button class="modal-close" type="button" :aria-label="closeLabel" @click="emit('close')">
          <svg
            viewBox="0 0 24 24" width="16" height="16"
            fill="none" stroke="currentColor" stroke-width="2"
            stroke-linecap="round" aria-hidden="true"
          >
            <path d="M6 6l12 12M18 6L6 18" />
          </svg>
        </button>
      </header>

      <div class="modal-body">
        <slot />
      </div>

      <footer v-if="$slots.footer" class="modal-foot">
        <slot name="footer" />
      </footer>
    </div>
  </div>
</template>

<style scoped>
.modal-overlay {
  position: fixed;
  inset: 0;
  /* 用 rgba 而不是 var()：tokens 里没有遮罩色变量，
     而自定义一个又会回到"变量不存在"的老问题 */
  background: rgb(15 23 42 / 45%);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: var(--space-4);
  z-index: 200;
}

.modal-panel {
  background: var(--bg-surface);
  border: 1px solid var(--line-color);
  border-radius: var(--radius-lg);
  width: min(560px, 100%);
  max-height: 85vh;
  display: flex;
  flex-direction: column;
  /* 遮罩之下要有明确的层次，否则对话框像"贴"在页面上 */
  box-shadow: 0 12px 32px rgb(15 23 42 / 18%);
  overflow: hidden;
}

.modal-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  padding: var(--space-4) var(--space-5);
  border-bottom: 1px solid var(--line-color);
  flex-shrink: 0;
}

.modal-title {
  margin: 0;
  font-size: var(--font-base);
  font-weight: 600;
  color: var(--text-primary);
}

.modal-close {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-tertiary);
  cursor: pointer;
  transition: background var(--duration-fast) var(--ease-out),
              color var(--duration-fast) var(--ease-out);
}

.modal-close:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.modal-close:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 1px;
}

.modal-body {
  padding: var(--space-5);
  overflow-y: auto;
  flex: 1;
}

.modal-foot {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-3);
  padding: var(--space-4) var(--space-5);
  border-top: 1px solid var(--line-color);
  background: var(--bg-subtle);
  flex-shrink: 0;
}
</style>
