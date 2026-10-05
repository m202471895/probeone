<script setup lang="ts">
import { useThemeStore } from '@/stores/theme'
const theme = useThemeStore()
</script>
<template>
  <div class="nf">
    <button class="theme-btn" @click="theme.toggle()">切换主题</button>
    <div class="box">
      <p class="code">404</p>
      <p class="text">页面不存在</p>
      <RouterLink to="/" class="link">返回总览</RouterLink>
    </div>
  </div>
</template>
<style scoped>
.nf { min-height: 100vh; display: flex; align-items: center; justify-content: center;
  background: var(--bg-body); position: relative; }
.theme-btn { position: absolute; top: var(--space-5); right: var(--space-5);
  border: 1px solid var(--line-color); border-radius: var(--radius-md);
  background: var(--bg-surface); color: var(--text-secondary); font-size: var(--font-xs);
  font-family: inherit; padding: 4px 10px; cursor: pointer; }
.box { text-align: center; }
.code { font-size: 64px; font-weight: 700; color: var(--line-strong); line-height: 1; }
.text { margin-top: var(--space-3); color: var(--text-secondary); font-size: var(--font-md); }
.link { display: inline-block; margin-top: var(--space-5); color: var(--accent); font-size: var(--font-sm); }
</style>
