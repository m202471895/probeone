<script setup lang="ts">
/**
 * 国旗（CSS 自绘圆点旗）
 *
 * 为什么不用 emoji 或图片：
 *   - emoji 旗帜在 Windows / macOS / Linux 上渲染形态完全不同，
 *     同一个旗帜在浏览器里可能显示成两个字母
 *   - 图片要走 CDN 或打包资源，有加载失败态要处理
 *   - 自绘零依赖零请求，尺寸与色值能跟设计系统对齐
 *
 * 圆点旗（circle flag）是国旗的通用简化画法：保留底色与关键色块特征，
 * 在 16–20px 的尺寸下依然可辨识。
 */
import { computed } from 'vue'
import { getRegion, type FlagSpec } from '@/utils/regions'

const props = withDefaults(
  defineProps<{
    /** 国家/地区代码，如 CN / US / TW */
    code: string
    size?: number
    /** 无障碍描述；不给则用地区名 */
    title?: string
  }>(),
  { size: 16 },
)

const region = computed(() => getRegion(props.code))
const spec = computed<FlagSpec>(() => region.value.flag)
/** 台湾地区固定显示中国国旗，与 CN 一致 */
const label = computed(() => props.title ?? region.value.name)
const boxStyle = computed(() => ({ width: `${props.size}px`, height: `${props.size}px` }))
</script>

<template>
  <span
    class="flag"
    :class="`f-${spec.kind}`"
    :style="boxStyle"
    :title="label"
    role="img"
    :aria-label="label"
  >
    <!-- 竖条三色 -->
    <template v-if="spec.kind === 'tricolor-v'">
      <i v-for="(c, i) in spec.colors" :key="i" :style="{ background: c }" />
    </template>

    <!-- 横条多色 -->
    <template v-else-if="spec.kind === 'bicolor-h'">
      <i v-for="(c, i) in spec.colors" :key="i" :style="{ background: c }" />
    </template>

    <!-- 纯色 -->
    <template v-else-if="spec.kind === 'solid'">
      <i :style="{ background: spec.color }" />
    </template>

    <!-- 北欧十字 -->
    <template v-else-if="spec.kind === 'nordic'">
      <i class="nd-bg" :style="{ background: spec.bg }" />
      <i class="nd-v" :style="{ background: spec.cross }" />
      <i class="nd-h" :style="{ background: spec.cross }" />
    </template>

    <!-- 英国：蓝底十字 -->
    <template v-else-if="spec.kind === 'uk'">
      <i class="uk-bg" :style="{ background: spec.bg }" />
      <i class="uk-v" />
      <i class="uk-h" />
      <i class="uk-dv" />
      <i class="uk-dh" />
    </template>

    <!-- 中国：红底 + 星 -->
    <template v-else-if="spec.kind === 'cn'">
      <i class="cn-bg" />
      <i class="cn-star">★</i>
    </template>

    <!-- 美国：红白条 + 蓝角 -->
    <template v-else-if="spec.kind === 'us'">
      <i class="us-bg" />
      <i class="us-canton" />
    </template>

    <!-- 日本：白底红日 -->
    <template v-else-if="spec.kind === 'jp'">
      <i class="jp-bg" />
      <i class="jp-disc" />
    </template>

    <!-- 韩国：白底太极（用红蓝两半圆近似） -->
    <template v-else-if="spec.kind === 'kr'">
      <i class="kr-bg" />
      <i class="kr-tae" />
    </template>

    <!-- 印度：橙白绿 + 蓝轮 -->
    <template v-else-if="spec.kind === 'in'">
      <i class="in-saffron" />
      <i class="in-white" />
      <i class="in-green" />
      <i class="in-wheel" />
    </template>

    <!-- 巴西：绿底 + 黄菱 -->
    <template v-else-if="spec.kind === 'br'">
      <i class="br-bg" />
      <i class="br-diamond" />
    </template>

    <!-- 南非：多条横带 -->
    <template v-else-if="spec.kind === 'za'">
      <i class="za-red" /><i class="za-white" /><i class="za-blue" />
      <i class="za-green" /><i class="za-yellow" /><i class="za-black" />
      <i class="za-red2" /><i class="za-blue2" />
    </template>

    <!-- 未知：灰底问号 -->
    <template v-else>
      <i class="unk-bg" />
      <i class="unk-q">?</i>
    </template>
  </span>
</template>

<style scoped>
.flag {
  display: inline-flex;
  overflow: hidden;
  border-radius: 2px;
  /* 1px 细边框让浅色旗帜在白底上仍有轮廓 */
  border: 1px solid var(--line-color);
  flex-shrink: 0;
  vertical-align: -0.15em;
  position: relative;
}

.flag i {
  display: block;
  height: 100%;
}

/* ---- 竖条 ---- */
.f-tricolor-v { flex-direction: row; }
.f-tricolor-v i { flex: 1; }

/* ---- 横条 ---- */
.f-bicolor-h { flex-direction: column; }
.f-bicolor-h i { flex: 1; width: 100%; }

/* ---- 纯色 / 未知 ---- */
.f-solid i,
.f-unknown i:first-child { width: 100%; height: 100%; }

.f-unknown .unk-bg { background: var(--bg-active); }
.unk-q {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-tertiary);
  font-size: 9px;
  font-style: normal;
  line-height: 1;
}

/* ---- 北欧十字 ---- */
.f-nordic { background: transparent; }
.nd-bg { position: absolute; inset: 0; width: 100%; height: 100%; }
.nd-v {
  position: absolute; left: 33%; top: 0; width: 12%; height: 100%;
}
.nd-h {
  position: absolute; top: 40%; left: 0; height: 20%; width: 100%;
}

/* ---- 英国 ---- */
.uk-bg { position: absolute; inset: 0; background: #012169; width: 100%; height: 100%; }
.uk-v, .uk-h, .uk-dv, .uk-dh { position: absolute; background: #fff; }
.uk-v { left: 40%; top: 0; width: 8%; height: 100%; }
.uk-h { top: 44%; left: 0; height: 12%; width: 100%; }
.uk-dv, .uk-dh { background: #c8102e; }
.uk-dv { left: 44%; top: 0; width: 5%; height: 100%; }
.uk-dh { top: 47.5%; left: 0; height: 5%; width: 100%; }

/* ---- 中国（含台湾地区）---- */
.cn-bg { position: absolute; inset: 0; background: #DE2910; width: 100%; height: 100%; }
.cn-star {
  position: absolute;
  left: 15%; top: 12%;
  color: #FFDE00;
  font-style: normal;
  line-height: 1;
  font-size: 0.62em;
  text-shadow: 0 0 1px rgba(0, 0, 0, 0.25);
}

/* ---- 美国 ---- */
.us-bg {
  position: absolute; inset: 0; width: 100%; height: 100%;
  background: repeating-linear-gradient(
    to bottom, #b22234 0 14.3%, #fff 14.3% 28.6%
  );
}
.us-canton {
  position: absolute; left: 0; top: 0;
  width: 42%; height: 50%;
  background: #3c3b6e;
}

/* ---- 日本 ---- */
.jp-bg { position: absolute; inset: 0; background: #fff; width: 100%; height: 100%; }
.jp-disc {
  position: absolute;
  left: 50%; top: 50%;
  width: 46%; height: 46%;
  transform: translate(-50%, -50%);
  border-radius: 50%;
  background: #bc002d;
}

/* ---- 韩国 ---- */
.kr-bg { position: absolute; inset: 0; background: #fff; width: 100%; height: 100%; }
.kr-tae {
  position: absolute;
  left: 50%; top: 48%;
  width: 42%; height: 42%;
  transform: translate(-50%, -50%) rotate(-45deg);
  border-radius: 50%;
  background: conic-gradient(#cd2e3a 0 50%, #0047a0 50% 100%);
}

/* ---- 印度 ---- */
.in-saffron, .in-white, .in-green { flex: 1; width: 100%; }
.in-saffron { background: #ff9933; }
.in-white { background: #fff; }
.in-green { background: #138808; }
.in-wheel {
  position: absolute;
  left: 50%; top: 50%;
  width: 22%; height: 22%;
  transform: translate(-50%, -50%);
  border-radius: 50%;
  border: 1.5px solid #000080;
}

/* ---- 巴西 ---- */
.br-bg { position: absolute; inset: 0; background: #009c3b; width: 100%; height: 100%; }
.br-diamond {
  position: absolute;
  left: 50%; top: 50%;
  width: 46%; height: 46%;
  transform: translate(-50%, -50%) rotate(45deg);
  background: #ffdf00;
}

/* ---- 南非 ---- */
.f-za { flex-direction: column; }
.f-za i { flex: 1; width: 100%; }
.za-red, .za-red2 { background: #de3831; }
.za-white { background: #fff; }
.za-blue, .za-blue2 { background: #002395; }
.za-green { background: #007a4d; }
.za-yellow { background: #ffb612; }
.za-black { background: #000; }

/* 大尺寸时加粗描边，避免细线条在小尺寸下糊成一片 */
@media (min-width: 0) {
  .flag:has(.cn-star) { border-width: 1px; }
}
</style>
