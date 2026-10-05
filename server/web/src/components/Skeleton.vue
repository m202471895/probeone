<script setup lang="ts">
/**
 * 骨架屏
 *
 * 为什么需要：数据请求期间如果什么都不显示，用户看到的是一片空白，
 * 分不清"正在加载"和"没有数据"。骨架屏占住最终布局的高度，
 * 加载完成时内容就位，不会发生跳动。
 *
 * 用法：
 *   <Skeleton :rows="3" />
 *   <Skeleton type="stat" :count="4" />
 *   <Skeleton type="card" :count="6" />
 */
withDefaults(
  defineProps<{
    /** 行式骨架的条数 */
    rows?: number
    /** 块状骨架的数量（统计卡、节点卡等） */
    count?: number
    /** 形状：行 / 统计块 / 卡片 / 表格 */
    type?: 'row' | 'stat' | 'card' | 'table'
    /** 表格列数，type=table 时用于占位 */
    cols?: number
    tableRows?: number
  }>(),
  { rows: 3, count: 4, type: 'row', cols: 5, tableRows: 6 },
)
</script>

<template>
  <!-- 行式：详情页的字段占位 -->
  <div v-if="type === 'row'" class="sk-row-wrap" aria-busy="true" aria-label="加载中">
    <div v-for="i in rows" :key="i" class="sk-row">
      <div class="sk-bar" :style="{ width: `${28 + ((i * 13) % 34)}%` }" />
    </div>
  </div>

  <!-- 统计块 -->
  <div v-else-if="type === 'stat'" class="sk-grid" aria-busy="true" aria-label="加载中">
    <div v-for="i in count" :key="i" class="sk-stat">
      <div class="sk-bar sk-bar-sm" style="width: 40%" />
      <div class="sk-bar sk-bar-lg" style="width: 62%" />
    </div>
  </div>

  <!-- 卡片 -->
  <div v-else-if="type === 'card'" class="sk-cards" aria-busy="true" aria-label="加载中">
    <div v-for="i in count" :key="i" class="sk-card">
      <div class="sk-bar" style="width: 52%" />
      <div class="sk-bar" style="width: 84%" />
      <div class="sk-bar" style="width: 68%" />
    </div>
  </div>

  <!-- 表格 -->
  <div v-else-if="type === 'table'" class="sk-table" aria-busy="true" aria-label="加载中">
    <div v-for="r in tableRows" :key="r" class="sk-trow">
      <div
        v-for="c in cols"
        :key="c"
        class="sk-bar"
        :style="{ width: `${34 + ((r * 7 + c * 13) % 46)}%` }"
      />
    </div>
  </div>
</template>

<style scoped>
.skeleton-base {
  background: var(--bg-hover);
  border-radius: var(--radius-sm);
}

/*
 * 骨架屏用微光扫过（shimmer），但必须尊重 prefers-reduced-motion。
 * 扫光本身是 transform + background-position，不触发重排。
 */
.sk-bar {
  height: 11px;
  border-radius: var(--radius-sm);
  background: linear-gradient(
    90deg,
    var(--bg-hover) 25%,
    var(--bg-active) 50%,
    var(--bg-hover) 75%
  );
  background-size: 200% 100%;
  animation: sk-shimmer 1.4s ease-in-out infinite;
}

.sk-bar-sm { height: 9px; }
.sk-bar-lg { height: 20px; }

@keyframes sk-shimmer {
  from { background-position: 200% 0; }
  to { background-position: -200% 0; }
}

@media (prefers-reduced-motion: reduce) {
  .sk-bar {
    animation: none;
    background: var(--bg-hover);
  }
}

/* ---- 行式 ---- */
.sk-row-wrap {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  padding: var(--space-5);
}

.sk-row {
  display: flex;
  align-items: center;
  min-height: 14px;
}

/* ---- 统计块 ---- */
.sk-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: var(--space-3);
}

.sk-stat {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-4);
  background: var(--bg-surface);
  border: 1px solid var(--line-color);
  border-radius: var(--radius-lg);
}

@media (max-width: 900px) {
  .sk-grid { grid-template-columns: repeat(2, 1fr); }
}

/* ---- 卡片 ---- */
.sk-cards {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(190px, 1fr));
  gap: var(--space-2);
  padding: var(--space-4);
}

.sk-card {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-3);
  border: 1px solid var(--line-color);
  border-radius: var(--radius-md);
}

/* ---- 表格 ---- */
.sk-table {
  display: flex;
  flex-direction: column;
  padding: var(--space-4);
  gap: var(--space-4);
}

.sk-trow {
  display: flex;
  gap: var(--space-4);
  align-items: center;
}

.sk-trow .sk-bar {
  flex: 1;
}
</style>
