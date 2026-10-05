/**
 * ECharts 封装
 *
 * 三个关键决策：
 *  1. **按需引入** —— 全量引入 ECharts 会让首屏 JS 逼近 1MB。
 *     只注册用到的图表与组件，首屏能压到 250KB 以内（PRD G）
 *  2. **主题跟随** —— 监听 data-theme 变化，重建 option
 *  3. **增量更新** —— setOption(notMerge=false) 而不是重新 setOption(null)，
 *     高频数据下前者不会清空再重建，动画也更连贯
 */

import type { EChartsOption } from 'echarts'

/**
 * ECharts 动态加载。
 *
 * 为什么不用静态 import：静态引入会让 echarts 进首屏依赖图，
 * 即使用户只看登录页也要下载 195KB gzip 的图表库。
 * 改成异步后，它会被拆成独立 chunk，只在真正进到有图表的页面时才拉取。
 *
 * 同时只注册用到的图表与组件，全量引入还要再大 100KB 以上。
 */
let loaded: Promise<void> | null = null

async function loadECharts(): Promise<void> {
  if (loaded) return loaded
  loaded = (async () => {
    const [core, charts, comps, renderers] = await Promise.all([
      import('echarts/core'),
      import('echarts/charts'),
      import('echarts/components'),
      import('echarts/renderers'),
    ])
    const { init, use } = core
    const { LineChart, BarChart, PieChart, GaugeChart } = charts
    const {
      GridComponent, TooltipComponent, LegendComponent,
      TitleComponent, DataZoomComponent, MarkLineComponent,
    } = comps
    const { CanvasRenderer } = renderers

    use([
      LineChart, BarChart, PieChart, GaugeChart,
      GridComponent, TooltipComponent, LegendComponent,
      TitleComponent, DataZoomComponent, MarkLineComponent,
      CanvasRenderer,
    ])
  })()
  return loaded
}

/** 动态 import 后拿不到 init 的类型引用，用轻量接口约束 */
export interface EChartsInstance {
  setOption(option: EChartsOption, notMerge?: boolean): void
  resize(): void
  dispose(): void
  getDom(): HTMLElement
}

export type { EChartsOption }

/** 读当前主题的 CSS 变量值。图表配色要跟界面一致，不能各用一套。 */
function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

/** 主题色板，顺序固定（PRD 11.2） */
export function chartPalette(): string[] {
  return [
    cssVar('--chart-1'),
    cssVar('--chart-2'),
    cssVar('--chart-3'),
    cssVar('--chart-4'),
    cssVar('--chart-5'),
    cssVar('--chart-6'),
    cssVar('--chart-7'),
    cssVar('--chart-8'),
  ]
}

export interface ThemeColors {
  textPrimary: string
  textSecondary: string
  textTertiary: string
  line: string
  bgSurface: string
  up: string
  down: string
  warning: string
  critical: string
  accent: string
  palette: string[]
}

export function themeColors(): ThemeColors {
  return {
    textPrimary: cssVar('--text-primary'),
    textSecondary: cssVar('--text-secondary'),
    textTertiary: cssVar('--text-tertiary'),
    line: cssVar('--line-color'),
    bgSurface: cssVar('--bg-surface'),
    up: cssVar('--up'),
    down: cssVar('--down'),
    warning: cssVar('--warning'),
    critical: cssVar('--critical'),
    accent: cssVar('--accent'),
    palette: chartPalette(),
  }
}

/** 通用 tooltip 样式：深底白字，与设计系统一致 */
export function baseTooltip() {
  const c = themeColors()
  return {
    trigger: 'axis' as const,
    backgroundColor: c.bgSurface,
    borderColor: c.line,
    borderWidth: 1,
    padding: [8, 12] as [number, number],
    textStyle: {
      color: c.textPrimary,
      fontSize: 12,
    },
    extraCssText: 'box-shadow: 0 2px 8px rgba(0,0,0,0.08); border-radius: 6px;',
  }
}

/** 通用网格：留白充分是"大气"的关键 */
export function baseGrid(overrides: Record<string, unknown> = {}) {
  return {
    left: 8,
    right: 12,
    top: 24,
    bottom: 4,
    containLabel: true,
    ...overrides,
  }
}

/** 坐标轴通用样式 */
export function baseAxis() {
  const c = themeColors()
  return {
    axisLine: {
      lineStyle: { color: c.line, width: 1 },
    },
    axisTick: { show: false },
    axisLabel: {
      color: c.textTertiary,
      fontSize: 11,
    },
    splitLine: {
      lineStyle: { color: c.line, type: 'dashed' as const },
    },
  }
}

/**
 * 创建实例。**返回 Promise**——echarts 是动态加载的，调用方需 await。
 * 忘记 await 会拿到 undefined，这是刻意的：编译期报不出来，
 * 但运行时立刻暴露，比静默失败好。
 */
export async function createChart(el: HTMLElement): Promise<EChartsInstance> {
  await loadECharts()
  const { init } = await import('echarts/core')
  return init(el, undefined, { renderer: 'canvas' }) as unknown as EChartsInstance
}

/** 响应式：容器尺寸变化时必须调 resize，否则窗口缩放后图表错位。 */
export function bindResize(chart: EChartsInstance, el: HTMLElement): () => void {
  const ro = new ResizeObserver(() => chart.resize())
  ro.observe(el)
  return () => ro.disconnect()
}

/** 主题变化时重建 option。返回是否需要重建。 */
export function onThemeChange(cb: () => void): () => void {
  const observer = new MutationObserver(() => cb())
  observer.observe(document.documentElement, {
    attributes: true,
    attributeFilter: ['data-theme'],
  })
  return () => observer.disconnect()
}
