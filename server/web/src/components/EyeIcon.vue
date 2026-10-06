<!--
  EyeIcon —— 密码可见性切换图标。

  为什么内联 SVG 而不用图标库：
  项目里已有 17 处内联 SVG（EmptyState、SidebarNav 等），
  为一个图标引入整个依赖库不划算。

  用 currentColor 跟随主题：这样明暗主题下都正确，
  写死颜色会在深色主题下变成看不清的深灰。
-->
<script setup lang="ts">
/** open 为true 时显示"睁眼"（当前密码可见），否则"闭眼"。 */
defineProps<{ open: boolean }>()
</script>

<template>
  <!-- 睁眼：能看到瞳孔，表示"点击隐藏" -->
  <svg
    v-if="open"
    viewBox="0 0 24 24" width="18" height="18"
    fill="none" stroke="currentColor" stroke-width="1.6"
    stroke-linecap="round" stroke-linejoin="round"
    aria-hidden="true"
  >
    <path d="M2 12s3.5-6.5 10-6.5S22 12 22 12s-3.5 6.5-10 6.5S2 12 2 12z" />
    <circle cx="12" cy="12" r="2.8" />
  </svg>
  <!-- 闭眼：一条横线，表示"点击显示" -->
  <svg
    v-else
    viewBox="0 0 24 24" width="18" height="18"
    fill="none" stroke="currentColor" stroke-width="1.6"
    stroke-linecap="round" stroke-linejoin="round"
    aria-hidden="true"
  >
    <path d="M2 12s3.5-6.5 10-6.5c1.5 0 2.8.32 3.9.82" />
    <path d="M21.9 14.2A11.4 11.4 0 0 1 12 18.5C5.5 18.5 2 12 2 12a18.6 18.6 0 0 1 3.1-3.7" />
    <path d="M3 3l18 18" />
  </svg>
</template>
