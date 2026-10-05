<script setup lang="ts">
/**
 * 国旗
 *
 * 数据源：cloud.andnode.com 的 flag-icons 圆点旗（512×512，单文件 0.5–2.5KB）。
 * 选它而不是 emoji 或位图：emoji 旗帜在 Windows/macOS/Linux 上渲染形态
 * 完全不同，浏览器里可能显示成两个字母；位图要处理多倍图与暗色模式。
 *
 * **台湾地区固定使用中国国旗。**
 * 云端 tw.svg 实际是青天白日旗（2506 字节，明显不是红旗），
 * 不符合要求，所以 TW 在代码层强制映射到 cn.svg——
 * 这是数据源纠偏，不是显示偏好。
 * 映射集中在 regions.ts 的 flagOf()，不散落到各处if 判断。
 */
import { computed, ref, watch } from 'vue'
import { flagOf, regionName } from '@/utils/regions'

const props = withDefaults(
  defineProps<{
    /** 国家/地区代码，如 CN / US / TW */
    code: string
    size?: number
    title?: string
  }>(),
  { size: 16 },
)

const CDN = 'https://cloud.andnode.com/static/images/country'

/** 实际请求的文件名（TW 会被映射为 cn） */
const file = computed(() => flagOf(props.code))
const src = computed(() => `${CDN}/${file.value.toLowerCase()}.svg`)

const failed = ref(false)
const label = computed(() => props.title ?? regionName(props.code))

// 切换国家时重置失败态，否则会沿用上一个的加载结果
watch(
  () => props.code,
  () => {
    failed.value = false
  },
)
</script>

<template>
  <span
    v-if="!failed"
    class="flag"
    :style="{ width: `${size}px`, height: `${size}px` }"
    :title="label"
    role="img"
    :aria-label="label"
  >
    <img
      :src="src"
      :alt="label"
      :width="size"
      :height="size"
      loading="lazy"
      decoding="async"
      @error="failed = true"
    />
  </span>

  <!-- 降级：显示两位代码，比破图可读 -->
  <span
    v-else
    class="flag flag-fallback"
    :style="{
      width: `${size}px`,
      height: `${size}px`,
      fontSize: `${Math.max(9, size - 5)}px`,
    }"
    :title="`${label}（${file.toUpperCase()}）`"
    role="img"
    :aria-label="label"
  >
    {{ file.toUpperCase() }}
  </span>
</template>

<style scoped>
/*
 * 圆形国旗。
 *
 * 为什么改圆：flag-icons 的 512×512 方图在方形容器里会显得方，
 * 侧边栏和列表行里非常扎眼。圆形裁切后视觉重量更轻，
 * 且与 StatusDot 的圆形状态点形成统一的圆角语言。
 */
.flag {
  display: inline-flex;
  overflow: hidden;
  border-radius: 50%;
  flex-shrink: 0;
  vertical-align: -0.18em;
  background: var(--bg-hover);
  /* 环形描边：让浅色旗帜在深浅两种背景下都有轮廓 */
  box-shadow:
    0 0 0 1px var(--line-color),
    inset 0 0 0 1px var(--line-color);
}

.flag img {
  width: 100%;
  height: 100%;
  /* 圆形裁切交给 border-radius，图片本身填满容器即可 */
  object-fit: cover;
  display: block;
}

/* 加载失败时的降级展示 */
.flag-fallback {
  align-items: center;
  justify-content: center;
  font-weight: 600;
  color: var(--text-secondary);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  letter-spacing: -0.04em;
  line-height: 1;
}
</style>
