<script setup lang="ts">
/** 空状态。给出原因与下一步动作，而不是只画个插画。 */
withDefaults(
  defineProps<{
    title?: string
    description?: string
    icon?: 'nodes' | 'monitors' | 'alerts' | 'search' | 'error'
  }>(),
  { title: '暂无数据', icon: 'search' },
)
</script>

<template>
  <div class="empty">
    <div class="empty-icon" aria-hidden="true">
      <svg v-if="icon === 'nodes'" viewBox="0 0 24 24" width="28" height="28" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
        <rect x="3" y="4" width="18" height="7" rx="1.5" /><rect x="3" y="13" width="18" height="7" rx="1.5" />
        <path d="M7 7.5h.01M7 16.5h.01" />
      </svg>
      <svg v-else-if="icon === 'monitors'" viewBox="0 0 24 24" width="28" height="28" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
        <circle cx="12" cy="12" r="9" /><path d="M3 12h18M12 3a15 15 0 0 1 0 18 15 15 0 0 1 0-18" />
      </svg>
      <svg v-else-if="icon === 'alerts'" viewBox="0 0 24 24" width="28" height="28" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
        <path d="M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9" /><path d="M13.7 21a2 2 0 0 1-3.4 0" />
      </svg>
      <svg v-else-if="icon === 'error'" viewBox="0 0 24 24" width="28" height="28" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
        <circle cx="12" cy="12" r="9" /><path d="M12 8v5M12 16h.01" />
      </svg>
      <svg v-else viewBox="0 0 24 24" width="28" height="28" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
        <circle cx="11" cy="11" r="7" /><path d="m20 20-3.5-3.5" />
      </svg>
    </div>
    <div class="empty-text">
      <p class="empty-title">{{ title }}</p>
      <p v-if="description" class="empty-desc">{{ description }}</p>
    </div>
    <div v-if="$slots.default" class="empty-action">
      <slot />
    </div>
  </div>
</template>

<style scoped>
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: var(--space-3);
  padding: var(--space-8) var(--space-5);
  text-align: center;
  min-height: 180px;
}

.empty-icon {
  color: var(--text-tertiary);
  opacity: 0.6;
}

.empty-text {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
}

.empty-title {
  font-size: var(--font-base);
  color: var(--text-secondary);
  font-weight: 500;
}

.empty-desc {
  font-size: var(--font-sm);
  color: var(--text-tertiary);
  max-width: 380px;
  line-height: 1.6;
}

.empty-action {
  margin-top: var(--space-2);
}
</style>
