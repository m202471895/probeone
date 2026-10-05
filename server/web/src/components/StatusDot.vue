<script setup lang="ts">
/** 状态圆点。三种状态：在线/离线/待接入。 */
withDefaults(
  defineProps<{
    status: 'online' | 'offline' | 'pending' | 'up' | 'down' | 'paused'
    /** 闪烁动画。用于"正在连接"这类中间态 */
    pulse?: boolean
    size?: number
  }>(),
  { pulse: false, size: 8 },
)

const LABELS: Record<string, string> = {
  online: '在线',
  offline: '离线',
  pending: '待接入',
  up: '正常',
  down: '异常',
  paused: '已暂停',
}
</script>

<template>
  <span class="dot-wrap" :title="LABELS[status] ?? status">
    <span
      class="dot"
      :class="[`is-${status}`, { pulse }]"
      :style="{ width: `${size}px`, height: `${size}px` }"
    />
  </span>
</template>

<style scoped>
.dot-wrap {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  line-height: 0;
}

.dot {
  display: block;
  border-radius: 50%;
  flex-shrink: 0;
}

.is-online,
.is-up {
  background: var(--down);
}

.is-offline,
.is-down {
  background: var(--critical);
}

.is-pending {
  background: var(--warning);
}

.is-paused {
  background: var(--neutral);
}

.pulse {
  animation: dot-pulse 2s ease-in-out infinite;
}

@keyframes dot-pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.35;
  }
}

@media (prefers-reduced-motion: reduce) {
  .pulse {
    animation: none;
  }
}
</style>
