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
 * 简单可逆，代价是高纬度地区横向拉伸 —— 对示意图够用。
 *
 * 缩放拖动：改 viewBox 而非 CSS transform。
 * 改viewBox 的好处是矢量在任何缩放级别都清晰，
 * 且屏幕坐标与地理坐标的换算不用额外维护变换矩阵。
 */
import { computed, onBeforeUnmount, ref } from 'vue'
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
}

const props = withDefaults(
  defineProps<{
    points: MapPoint[]
    height?: number
  }>(),
  { height: 320 },
)

// 完整世界范围的宽高（2:1，等距圆柱的标准比例）
const W = 1000
const H = 500

/*
 * 默认视图。
 *
 * 裁掉南极洲：南纬 60 度以南没有任何节点，却占约 1/4 画面。
 * 投影层会丢弃超出的点，不用只靠 viewBox 裁切。
 */
const DEFAULT_VIEW = { x: 0, y: -60, w: W, h: 420 }

/** 缩放范围：最小看全图，最大放到区域级别。 */
const MIN_SCALE = 1
const MAX_SCALE = 12

const view = ref({ ...DEFAULT_VIEW })
const svgRef = ref<SVGSVGElement | null>(null)
const dragging = ref(false)
const hovered = ref<MapPoint | null>(null)

/** 预生成国界 path。转换涉及数千坐标点，每次渲染重算会卡。 */
const countryPaths = computed(() => {
  const topology = worldData as unknown as {
    objects: { countries: Parameters<typeof feature>[0] }
  }
  const fc = feature(
    worldData as never,
    topology.objects.countries as never,
  ) as unknown as { features: GeoFeature[] }

  return fc.features
    .map((f, i) => {
      const d = featureToPath(f, W, H, DEFAULT_VIEW.y)
      return d ? { d, iso: isoCodeOf(f) ?? '', key: featureKey(f, i) } : null
    })
    .filter((x): x is { d: string; iso: string; key: string } => x !== null)
})

/** 经纬度 → SVG 坐标 */
function project(lon: number, lat: number): [number, number] | null {
  if (lat < DEFAULT_VIEW.y) return null
  const x = ((lon + 180) / 360) * W
  const y = ((90 - lat) / 180) * H
  return [x, y]
}

/** 节点投影后的坐标。无坐标的直接过滤 —— 画错位置比不画更糟。 */
const projected = computed(() =>
  props.points
    .map((p) => {
      const xy = project(p.lon, p.lat)
      return xy ? { ...p, x: xy[0], y: xy[1] } : null
    })
    .filter((p): p is NonNullable<typeof p> => p !== null),
)

const total = computed(() => props.points.length)
const withGeo = computed(() => projected.value.length)

/** 有节点的国家代码集合，用于轻微高亮 */
const hitCountries = computed(() => {
  const m = new Set<string>()
  for (const p of props.points) {
    if (p.country) m.add(p.country.toUpperCase())
  }
  return m
})

const viewBox = computed(
  () => `${view.value.x} ${view.value.y} ${view.value.w} ${view.value.h}`,
)

/** 当前缩放倍率，1 = 全图 */
const scale = computed(() => W / view.value.w)

/** 圆点与命中圈的半径随缩放反向补偿，视觉大小保持稳定 */
const dotR = computed(() => Math.max(2.5, 4 / scale.value))
const ringR = computed(() => Math.max(4, 7 / scale.value))
const hitR = computed(() => Math.max(6, 11 / scale.value))

// ---------- 缩放 ----------

/** 以某个屏幕点为锚缩放，鼠标下的地理位置保持不动 */
function zoomAt(clientX: number, clientY: number, factor: number): void {
  const svg = svgRef.value
  if (!svg) return
  const rect = svg.getBoundingClientRect()
  if (rect.width === 0 || rect.height === 0) return

  // 鼠标在 viewBox 坐标里的位置
  const px = ((clientX - rect.left) / rect.width) * view.value.w + view.value.x
  const py = ((clientY - rect.top) / rect.height) * view.value.h + view.value.y

  const newW = view.value.w * factor
  const newH = view.value.h * factor

  // 夹住缩放范围
  const clampedW = Math.min(Math.max(newW, W / MAX_SCALE), W / MIN_SCALE)
  const clampedH = clampedW * (view.value.h / view.value.w)

  // 保持锚点位置：锚点在当前视图里的相对比例不变
  const ratioX = (px - view.value.x) / view.value.w
  const ratioY = (py - view.value.y) / view.value.h
  view.value = {
    x: px - clampedW * ratioX,
    y: py - clampedH * ratioY,
    w: clampedW,
    h: clampedH,
  }
}

function zoomAtCenter(factor: number): void {
  const svg = svgRef.value
  if (!svg) return
  const r = svg.getBoundingClientRect()
  zoomAt(r.left + r.width / 2, r.top + r.height / 2, factor)
}

function zoomIn(): void {
  zoomAtCenter(1 / 1.4)
}

function zoomOut(): void {
  zoomAtCenter(1.4)
}

function resetView(): void {
  view.value = { ...DEFAULT_VIEW }
}

// ---------- 拖动 ----------

let dragStartX = 0
let dragStartY = 0
let dragViewX = 0
let dragViewY = 0

function onPointerDown(e: PointerEvent): void {
  // 只响应主键与触摸
  if (e.pointerType === 'mouse' && e.button !== 0) return
  dragging.value = true
  dragStartX = e.clientX
  dragStartY = e.clientY
  dragViewX = view.value.x
  dragViewY = view.value.y
  ;(e.currentTarget as Element).setPointerCapture(e.pointerId)
}

function onPointerMove(e: PointerEvent): void {
  if (!dragging.value) return
  const svg = svgRef.value
  if (!svg) return
  const rect = svg.getBoundingClientRect()
  if (rect.width === 0) return

  // 屏幕位移换算成 viewBox 位移
  const dx = ((e.clientX - dragStartX) / rect.width) * view.value.w
  const dy = ((e.clientY - dragStartY) / rect.height) * view.value.h
  view.value = { ...view.value, x: dragViewX + dx, y: dragViewY + dy }
}

function onPointerUp(e: PointerEvent): void {
  dragging.value = false
  const t = e.currentTarget as Element
  if (t.hasPointerCapture?.(e.pointerId)) t.releasePointerCapture(e.pointerId)
}

// ---------- 滚轮缩放 ----------

function onWheel(e: WheelEvent): void {
  // 必须 preventDefault，否则页面会跟着一起滚
  e.preventDefault()
  zoomAt(e.clientX, e.clientY, e.deltaY > 0 ? 1.15 : 1 / 1.15)
}

// ---------- 触屏双指缩放 ----------

const touches = new Map<number, { x: number; y: number }>()
let pinchStartDist = 0
let pinchStartW = 0

function touchDistance(): number {
  const pts = Array.from(touches.values())
  if (pts.length < 2) return 0
  const dx = pts[0]!.x - pts[1]!.x
  const dy = pts[0]!.y - pts[1]!.y
  return Math.hypot(dx, dy)
}

function touchMid(): { x: number; y: number } {
  const pts = Array.from(touches.values())
  if (pts.length < 2) return { x: 0, y: 0 }
  return { x: (pts[0]!.x + pts[1]!.x) / 2, y: (pts[0]!.y + pts[1]!.y) / 2 }
}

function onTouchStart(e: TouchEvent): void {
  for (const t of Array.from(e.touches)) {
    touches.set(t.identifier, { x: t.clientX, y: t.clientY })
  }
  if (touches.size === 2) {
    pinchStartDist = touchDistance()
    pinchStartW = view.value.w
  }
}

function onTouchMove(e: TouchEvent): void {
  for (const t of Array.from(e.touches)) {
    touches.set(t.identifier, { x: t.clientX, y: t.clientY })
  }
  if (touches.size !== 2 || pinchStartDist === 0 || pinchStartW === 0) return
  e.preventDefault()

  const dist = touchDistance()
  if (dist === 0) return
  // 手指张开 → 放大（viewBox 变小）
  const targetW = pinchStartW * (pinchStartDist / dist)
  const mid = touchMid()
  zoomAt(mid.x, mid.y, targetW / view.value.w)
}

function onTouchEnd(e: TouchEvent): void {
  for (const t of Array.from(e.changedTouches)) touches.delete(t.identifier)
  if (touches.size < 2) {
    pinchStartDist = 0
    pinchStartW = 0
  }
}

// ---------- tooltip ----------

const tooltipStyle = computed(() => {
  if (!hovered.value || !svgRef.value) return {}
  const rect = svgRef.value.getBoundingClientRect()
  const sx = ((hovered.value.x - view.value.x) / view.value.w) * rect.width
  const sy = ((hovered.value.y - view.value.y) / view.value.h) * rect.height
  return { left: `${sx}px`, top: `${sy}px` }
})

function statusColor(status: MapPoint['status']): string {
  switch (status) {
    case 'online':
    case 'up':
      return 'var(--down)'
    case 'offline':
    case 'down':
      return 'var(--critical)'
    case 'paused':
      return 'var(--neutral)'
    default:
      return 'var(--warning)'
  }
}

const STATUS_TEXT: Record<string, string> = {
  online: '在线',
  offline: '离线',
  pending: '待接入',
  up: '正常',
  down: '异常',
  paused: '已暂停',
}

onBeforeUnmount(() => {
  touches.clear()
  pinchStartDist = 0
  pinchStartW = 0
})
</script>

<template>
  <div class="world-map" :class="{ dragging }" :style="{ height: `${height}px` }">
    <svg
      ref="svgRef"
      :viewBox="viewBox"
      class="map-svg"
      preserveAspectRatio="none"
      role="img"
      aria-label="节点世界分布图，可缩放拖动"
      @pointerdown="onPointerDown"
      @pointermove="onPointerMove"
      @pointerup="onPointerUp"
      @pointercancel="onPointerUp"
      @wheel="onWheel"
      @touchstart="onTouchStart"
      @touchmove="onTouchMove"
      @touchend="onTouchEnd"
      @touchcancel="onTouchEnd"
    >
      <!-- 海洋底色。尺寸随 viewBox 变化，始终铺满当前视野 -->
      <rect
        :x="view.x" :y="view.y" :width="view.w" :height="view.h"
        class="map-ocean"
      />

      <!-- 经纬网格：跟随视野重新划分，缩放后间距看起来保持一致 -->
      <g class="map-grid">
        <template v-for="i in 9" :key="'v' + i">
          <line
            :x1="view.x + (view.w * i) / 9" :y1="view.y"
            :x2="view.x + (view.w * i) / 9" :y2="view.y + view.h"
          />
        </template>
        <template v-for="i in 5" :key="'h' + i">
          <line
            :x1="view.x" :y1="view.y + (view.h * i) / 5"
            :x2="view.x + view.w" :y2="view.y + (view.h * i) / 5"
          />
        </template>
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
          @mouseenter="hovered = p"
          @mouseleave="hovered = null"
        >
          <!-- 命中区随缩放反向补偿，保证小圆点也好点 -->
          <circle :cx="p.x" :cy="p.y" :r="hitR" class="point-hit" />
          <circle
            :cx="p.x" :cy="p.y" :r="dotR"
            class="point-dot"
            :style="{ fill: statusColor(p.status) }"
          />
          <circle
            :cx="p.x" :cy="p.y" :r="ringR"
            class="point-ring"
            :style="{ stroke: statusColor(p.status) }"
          />
        </g>
      </g>
    </svg>

    <!-- 缩放控件 -->
    <div class="map-controls">
      <button class="ctl" title="放大" aria-label="放大地图" @click.stop="zoomIn">+</button>
      <button class="ctl" title="缩小" aria-label="缩小地图" @click.stop="zoomOut">−</button>
      <button class="ctl" title="重置视图" aria-label="重置地图视图" @click.stop="resetView">↺</button>
    </div>

    <!-- 缩放倍率 -->
    <div v-if="scale > 1.05" class="map-zoom-hint">{{ scale.toFixed(1) }}×</div>

    <!-- 节点统计 -->
    <div class="map-hint">{{ withGeo }} / {{ total }} 个节点</div>

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

    <!-- tooltip -->
    <div v-if="hovered" class="map-tip" :style="tooltipStyle">
      <span class="tip-name">{{ hovered.name }}</span>
      <span class="tip-status" :style="{ color: statusColor(hovered.status) }">
        {{ STATUS_TEXT[hovered.status] }}
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
  /* 铺满卡片宽度 */
  width: 100%;
}

.map-svg {
  width: 100%;
  height: 100%;
  display: block;
  cursor: grab;
  /* 让触屏事件由 JS 处理，不被浏览器的手势识别抢走 */
  touch-action: none;
  /* 拖动时不要触发文本选中，鼠标操作才启用，避免影响触屏 */
  user-select: none;
}

.world-map.dragging .map-svg {
  cursor: grabbing;
}

.map-ocean {
  fill: var(--bg-body);
}

.map-grid line {
  stroke: var(--line-color);
  stroke-width: 0.5;
  fill: none;
  vector-effect: non-scaling-stroke;
}

.map-country {
  /*
   * 描边用 --line-strong 而非 --line-color。
   * 后者只有 9% 不透明度，高纬度的俄罗斯与加拿大北部
   * 会糊成一片，看着像一条横带 —— 实际是描边看不见。
   */
  fill: var(--bg-active);
  stroke: var(--line-strong);
  stroke-width: 0.8;
  stroke-linejoin: round;
  /* 关掉 non-scaling-stroke：缩放时国界要跟着变粗，
     否则放大后描边会细得看不见 */
}

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
  stroke-width: 1.2;
  opacity: 0.45;
  vector-effect: non-scaling-stroke;
  transition: opacity var(--duration-fast) var(--ease-out);
}

.map-point:hover .point-ring {
  opacity: 0.9;
}

.point-hit {
  fill: transparent;
  cursor: pointer;
}

/* ---- 控件 ---- */
.map-controls {
  position: absolute;
  top: var(--space-2);
  right: var(--space-2);
  display: flex;
  flex-direction: column;
  gap: 2px;
  z-index: 3;
}

.ctl {
  /* 28px 满足 24px 触控底线，比按钮稍大一点便于点按 */
  width: 28px;
  height: 28px;
  min-width: 28px;
  display: flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--line-color);
  border-radius: var(--radius-sm);
  background: var(--bg-elevated);
  color: var(--text-secondary);
  font-size: 14px;
  line-height: 1;
  font-family: inherit;
  cursor: pointer;
  transition:
    background-color var(--duration-fast) var(--ease-out),
    color var(--duration-fast) var(--ease-out);
}

.ctl:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.ctl:active {
  transform: scale(0.94);
}

.map-zoom-hint {
  position: absolute;
  top: var(--space-2);
  left: 50%;
  transform: translateX(-50%);
  font-size: var(--font-xs);
  color: var(--text-tertiary);
  background: var(--bg-elevated);
  padding: 2px var(--space-2);
  border-radius: var(--radius-full);
  z-index: 2;
  pointer-events: none;
  font-variant-numeric: tabular-nums;
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
  background: var(--bg-elevated);
  padding: 3px var(--space-2);
  border-radius: var(--radius-full);
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

/* ---- tooltip ---- */
.map-tip {
  position: absolute;
  transform: translate(-50%, calc(-100% - 10px));
  background: var(--bg-elevated);
  border: 1px solid var(--line-strong);
  border-radius: var(--radius-md);
  padding: var(--space-1) var(--space-2);
  font-size: var(--font-xs);
  white-space: nowrap;
  pointer-events: none;
  z-index: 4;
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

@media (prefers-reduced-motion: reduce) {
  .point-dot,
  .point-ring {
    transition: none;
  }
}
</style>
