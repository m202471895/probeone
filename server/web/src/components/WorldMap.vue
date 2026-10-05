<script setup lang="ts">
/**
 * 世界地图（自绘 SVG）
 *
 * 为什么不用 Leaflet / Mapbox / Google Maps：
 *   那些都要向第三方发请求，等于把"我的服务器在哪个城市"告诉别人，
 *   与本项目"数据不出你的服务器"的前提直接冲突。
 *
 * 实现：world-atlas 的 110m 国界数据（Natural Earth，公有领域）
 * 随包打进前端，运行时零外部请求。TopoJSON 在浏览器端转成 SVG path。
 *
 * 投影：等距圆柱（Equirectangular）。
 *   x = (lon + 180) / 360 * width
 *   y = (90 - lat) / 180 * height
 * 简单可逆，代价是高纬度地区横向拉伸——对示意图够用。
 */
import { computed, ref } from 'vue'
import { feature } from 'topojson-client'
import worldData from 'world-atlas/countries-110m.json'
import { featureToPath, featureKey, isoCodeOf, type GeoFeature } from '@/charts/geo'


export interface MapPoint {
  id: number | string
  name: string
  lat: number
  lon: number
  status: 'online' | 'offline' | 'pending' | 'up' | 'down' | 'paused'
  /** 国家代码，用于地图上按国家高亮 */
  country?: string
  /** 点击后跳转的地址，通常是 /nodes/{uid} */
  to?: string
}

const props = withDefaults(
  defineProps<{
    points: MapPoint[]
    height?: number
  }>(),
  { height: 320 },
)

// viewBox 固定 1000x500（2:1，等距圆柱的标准比例）
const W = 1000
const H = 500

/*
 * 裁掉南极洲。
 *
 * 原始投影是 -90..90 全纬度，但南纬 60 度以南没有任何节点，
 * 却占了约 1/4 画面，把北半球的有效区域压扁。
 * 把 viewBox 上移并收窄，只保留 -60..90 纬度。
 */
const MIN_LAT = -60     // 可见的最南纬度
const VIEW_Y = -60      // viewBox 起始 y（与 MIN_LAT 对应）
const VIEW_H = 420      // viewBox 高度


const svgRef = ref<SVGSVGElement | null>(null)
const paths = ref<Array<{ d: string; id: string }>>([])

/**
 * 经纬度 → SVG 坐标。
 *
 * 南纬 60 度以下的点直接丢弃（返回 null）。
 * 不能只靠 viewBox 裁切——被裁掉的路径仍会参与绘制，
 * 会在边界处留下半截线条（南极洲的横线就是这么来的）。
 */
function project(lon: number, lat: number): [number, number] | null {
  if (lat < MIN_LAT) return null
  const x = ((lon + 180) / 360) * W
  const y = ((90 - lat) / 180) * H
  return [x, y]
}

/**
 * 预生成国界 path。
 *
 * 只算一次并缓存：TopoJSON 转换涉及 thousands of坐标点，
 * 每次渲染重算会明显卡顿。
 */
const countryPaths = computed(() => {
  const topology = worldData as unknown as {
    objects: { countries: Parameters<typeof feature>[0] }
  }
  const fc = feature(
    worldData as never,
    topology.objects.countries as never,
  ) as unknown as { features: GeoFeature[] }

  // 全部国界都要画出来。ISO 匹配不上只是拿不到高亮能力，
  // 不该让整块陆地消失——之前按 iso 过滤导致国界一条都没渲染出来。
  return fc.features
    .map((f, i) => {
      const d = featureToPath(f, W, H, VIEW_Y)
      return d ? { d, iso: isoCodeOf(f) ?? '', key: featureKey(f, i) } : null
    })
    .filter((x): x is { d: string; iso: string; key: string } => x !== null)
})

/**
 * 节点投影后的坐标。
 * 无坐标的节点直接过滤掉——地图上画一个位置错误的点比不画更糟。
 */
const projected = computed(() =>
  props.points
    .map((p) => {
      const xy = project(p.lon, p.lat)
      return xy ? { ...p, x: xy[0], y: xy[1] } : null
    })
    .filter((p): p is NonNullable<typeof p> => p !== null),
)

/**
 * 有节点的国家代码集合。
 * 用于在地图上给这些国家加一层轻微高亮，方便"哪个区域有节点"一眼可见。
 */
const hitCountries = computed(() => {
  const m = new Set<string>()
  for (const p of props.points) {
    // points 里若带 country 就用；不带则不参与高亮
    const c = (p as MapPoint & { country?: string }).country
    if (c) m.add(c.toUpperCase())
  }
  return m
})

/** 状态对应的圆点颜色 */
function dotColor(status: MapPoint['status']): string {
  switch (status) {
    case 'online':
    case 'up':
      return 'var(--down)' // 绿
    case 'offline':
    case 'down':
      return 'var(--critical)' // 红
    case 'paused':
      return 'var(--neutral)'
    default:
      return 'var(--warning)' // 黄
  }
}

/**
 * 有坐标的节点数。
 * 地图只画有坐标的，所以提示要分开说——
 * 否则用户会以为"24 个节点地图上就该有 24 个点"。
 */
const total = computed(() => props.points.length)
const withGeo = computed(() => projected.value.length)

const hovered = ref<MapPoint | null>(null)

function onEnter(p: MapPoint): void {
  hovered.value = p
}

function onLeave(): void {
  hovered.value = null
}

// 屏幕坐标 → SVG 坐标（tooltip 定位用）
const tooltipStyle = computed(() => {
  if (!hovered.value || !svgRef.value) return {}
  const rect = svgRef.value.getBoundingClientRect()
  const scaleX = rect.width / W
  const scaleY = rect.height / H
  return {
    left: `${hovered.value.x * scaleX}px`,
    top: `${hovered.value.y * scaleY}px`,
  }
})

</script>

<template>
  <div class="world-map" :style="{ height: `${height}px` }">
    <div class="map-hint">{{ withGeo }} / {{ total }} 个节点</div>
    <svg
      ref="svgRef"
      :viewBox="`0 ${VIEW_Y} ${W} ${VIEW_H}`"
      class="map-svg"
      role="img"
      aria-label="节点世界分布图"
    >
      <!-- 海洋底色 -->
      <rect :width="W" :height="H" class="map-ocean" />

      <!-- 经纬网格 -->
      <g class="map-grid">
        <line v-for="i in 5" :key="'h' + i" :x1="0" :y1="(i * H) / 6" :x2="W" :y2="(i * H) / 6" />
        <line v-for="i in 11" :key="'v' + i" :x1="(i * W) / 12" :y1="0" :x2="(i * W) / 12" :y2="H" />
      </g>

      <!-- 国界 -->
      <g class="map-land">
        <path
          v-for="c in countryPaths"
          :key="c.key"
          :d="c.d"
          class="map-country"
          :class="{ 'has-node': c.iso && hitCountries.has(c.iso) }"
        />
      </g>

      <!-- 节点 -->
      <g class="map-points">
        <g
          v-for="p in projected"
          :key="p.id"
          class="map-point"
          @mouseenter="onEnter(p)"
          @mouseleave="onLeave"
        >
          <!-- 命中区域放大，指针更容易命中小圆点 -->
          <circle :cx="p.x" :cy="p.y" r="10" class="point-hit" />
          <circle
            :cx="p.x"
            :cy="p.y"
            r="4"
            class="point-dot"
            :style="{ fill: dotColor(p.status) }"
          />
          <circle
            :cx="p.x"
            :cy="p.y"
            r="7"
            class="point-ring"
            :style="{ stroke: dotColor(p.status) }"
          />
        </g>
      </g>
    </svg>

    <!-- tooltip -->
    <div
      v-if="hovered"
      class="map-tip"
      :style="tooltipStyle"
    >
      <span class="tip-name">{{ hovered.name }}</span>
      <span class="tip-status" :style="{ color: dotColor(hovered.status) }">
        {{ { online: '在线', offline: '离线', pending: '待接入', up: '正常', down: '异常', paused: '已暂停' }[hovered.status] }}
      </span>
    </div>

    <!-- 有节点缺坐标时的说明 -->
    <div v-if="total > withGeo" class="map-note">
      {{ total - withGeo }} 个节点暂无坐标，未在地图上显示
    </div>

    <!-- 图例 -->
    <div class="map-legend">
      <span v-for="l in [
        { c: 'var(--down)', t: '在线' },
        { c: 'var(--critical)', t: '离线' },
        { c: 'var(--warning)', t: '待接入' },
      ]" :key="l.t" class="legend-item">
        <i class="legend-dot" :style="{ background: l.c }" />
        {{ l.t }}
      </span>
    </div>
  </div>
</template>

<style scoped>
.world-map {
  position: relative;
  background: var(--bg-body);
  border-radius: var(--radius-md);
  overflow: hidden;
  border: 1px solid var(--line-color);
}

.map-hint {
  position: absolute;
  top: var(--space-2);
  left: var(--space-3);
  font-size: var(--font-xs);
  color: var(--text-tertiary);
  z-index: 2;
  pointer-events: none;
}

.map-svg {
  width: 100%;
  height: 100%;
  display: block;
}

.map-ocean {
  /* 海洋比陆地略深，形成"陆地浮出水面"的层次 */
  fill: var(--bg-surface);
}

.map-grid line {
  stroke: var(--line-color);
  stroke-width: 0.3;
  fill: none;
}

.map-country {
  /*
   * 描边用 --line-strong 而非 --line-color。
   * 后者只有 9% 不透明度，高纬度的俄罗斯与加拿大北部
   * 会糊成一片，看着像一条横带——实际是描边看不见。
   */
  fill: var(--bg-active);
  stroke: var(--line-strong);
  stroke-width: 0.6;
  stroke-linejoin: round;
  vector-effect: non-scaling-stroke;
  transition: fill var(--duration-base) var(--ease-out);
}

/* 有节点的国家略微提亮，让区域分布一眼可辨 */
.map-country.has-node {
  fill: var(--bg-hover);
}

.map-point {
  cursor: pointer;
}

.point-dot {
  transition: r var(--duration-fast) var(--ease-out);
}

.point-ring {
  fill: none;
  stroke-width: 1;
  opacity: 0.45;
  transition: opacity var(--duration-fast) var(--ease-out), r var(--duration-fast) var(--ease-out);
}

.map-point:hover .point-ring {
  opacity: 0.9;
  r: 9;
}

.map-point:hover .point-dot {
  r: 5;
}

.point-hit {
  fill: transparent;
  cursor: pointer;
}

.map-tip {
  position: absolute;
  transform: translate(-50%, calc(-100% - 12px));
  background: var(--bg-elevated);
  border: 1px solid var(--line-strong);
  border-radius: var(--radius-md);
  padding: var(--space-1) var(--space-2);
  font-size: var(--font-xs);
  white-space: nowrap;
  pointer-events: none;
  z-index: 3;
  display: flex;
  gap: var(--space-2);
  align-items: center;
}

.tip-name {
  color: var(--text-primary);
  font-weight: 500;
}

.tip-status {
  color: var(--text-secondary);
}

.map-note {
  position: absolute;
  bottom: var(--space-2);
  left: var(--space-3);
  font-size: var(--font-xs);
  color: var(--warning);
  z-index: 2;
  pointer-events: none;
}

.map-legend {
  position: absolute;
  bottom: var(--space-2);
  right: var(--space-3);
  display: flex;
  gap: var(--space-3);
  font-size: var(--font-xs);
  color: var(--text-secondary);
  z-index: 2;
  pointer-events: none;
}

.legend-item {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.legend-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  display: inline-block;
}
</style>
