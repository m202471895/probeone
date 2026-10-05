<script setup lang="ts">
/**
 * 卡片容器
 *
 * 扁平化的核心：**用 1px 边框而非阴影**表达层级。
 * 阴影在深色主题下会显脏，且在多卡片网格里容易糊成一片。
 */
defineProps<{
  title?: string
  /** 是否显示内容区的内边距。表格类内容可关掉让表格贴边。 */
  padded?: boolean
}>()
</script>

<template>
  <section class="card">
    <header v-if="title || $slots.actions" class="card-header">
      <h3 v-if="title" class="card-title">{{ title }}</h3>
      <div class="card-actions">
        <slot name="actions" />
      </div>
    </header>
    <div class="card-body" :class="{ 'padded': padded !== false }">
      <slot />
    </div>
  </section>
</template>

<style scoped>
.card {
  background: var(--bg-surface);
  border: 1px solid var(--line-color);
  border-radius: var(--radius-lg);
  display: flex;
  flex-direction: column;
  min-width: 0;
  transition: border-color var(--duration-base) var(--ease-out);
}

.card:hover {
  border-color: var(--line-strong);
}

.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  padding: var(--space-4) var(--space-5);
  border-bottom: 1px solid var(--line-color);
  min-height: 48px;
}

.card-title {
  font-size: var(--font-base);
  font-weight: 600;
  color: var(--text-primary);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.card-actions {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex-shrink: 0;
}

.card-body {
  flex: 1;
  min-width: 0;
}

.card-body.padded {
  padding: var(--space-5);
}

@media (max-width: 767px) {
  .card-header {
    padding: var(--space-3) var(--space-4);
    min-height: 44px;
  }
  .card-body.padded {
    padding: var(--space-4);
  }
}
</style>
