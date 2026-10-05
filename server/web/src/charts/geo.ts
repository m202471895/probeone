/**
 * GeoJSON → SVG path
 *
 * 独立成文件是因为它只在 WorldMap 用到，
 * 且逻辑与组件渲染无关（纯几何计算）。
 */

// 最小 GeoJSON 类型声明。不装 @types/geojson 也要能跑。
export interface GeoGeometry {
  type: 'Polygon' | 'MultiPolygon' | 'LineString' | 'MultiLineString'
  coordinates: number[][][] | number[][] | number[][][][] | number[][]
}

export interface GeoFeature {
  type: 'Feature'
  id?: string | number
  properties?: Record<string, unknown>
  geometry: GeoGeometry | null
}

/**
 * 把一个 Feature 的几何转成 SVG path 的 d 属性。
 * 只处理面（Polygon / MultiPolygon），线与点忽略。
 */
export function featureToPath(
  feature: GeoFeature,
  width: number,
  height: number,
  /** 裁切线：y 超过它的点被丢弃 */
  minY: number,
): string {
  const g = feature.geometry
  if (!g) return ''

  /*
   * 整块剔除南极洲。
   *
   * 单靠 minY 裁切不够：南极洲的海岸线有一段贴着南纬 50–60 度，
   * 裁切后残留的那截仍横跨整幅地图。那里没有任何节点，
   * 整块去掉视觉上毫无损失。
   */
  if (isAntarctica(feature)) return ''

  const parts: string[] = []

  /*
   * project 可能返回 null，表示该点被裁掉（如南极洲）。
   * 返回 null 的点不参与绘制，环会在该处断开——
   * 不断开的话，最后一点连回第一点会横穿整幅图。
   */
  const project = (lon: number, lat: number): [number, number] | null => {
    const x = ((lon + 180) / 360) * width
    const y = ((90 - lat) / 180) * height
    if (y < minY) return null
    return [x, y]
  }

  if (g.type === 'Polygon') {
    for (const ring of g.coordinates as number[][][]) {
      appendRing(parts, ring, project)
    }
  } else if (g.type === 'MultiPolygon') {
    for (const poly of g.coordinates as number[][][][]) {
      for (const ring of poly) {
        appendRing(parts, ring, project)
      }
    }
  }

  return parts.join(' ')
}

/** 南极洲判定：源数据的 name 字段。 */
function isAntarctica(f: GeoFeature): boolean {
  return f.properties?.['name'] === 'Antarctica'
}

/**
 * 跨经线的判定阈值（SVG 用户单位）。
 *
 * 地图宽1000，x 半幅是 500。相邻两点 x 差超过这个值，
 * 说明环跨过了 ±180° 经线（俄罗斯、斐济这类横跨日期变更线的国家）——
 * 直接闭合会画出一条横穿整幅图的横线。
 */
const JUMP_X = 500

function appendRing(
  out: string[],
  ring: number[][],
  project: (lon: number, lat: number) => [number, number] | null,
): void {
  if (!ring || ring.length === 0) return

  // 投影后的点序列
  /*
   * 投影后的点序列。
   * project 返回 null 的点直接跳过——它落在裁切线之外，
   * 保留会让路径变形。
   */
  const pts: Array<[number, number]> = []
  for (const c of ring) {
    if (!c) continue
    const xy = project(c[0] ?? 0, c[1] ?? 0)
    if (xy) pts.push(xy)
  }
  if (pts.length < 2) return

  /*
   * 跨 ±180° 经线的环必须拆分。
   *
   * 直接闭合会把环的最后一点连回第一点，
   * 而这两点分处地图两侧，于是画出一条横穿整幅图的横线——
   * 格陵兰会变成一条从北美横到欧亚的大陆桥，非常显眼。
   *
   * 做法：检测到经度跳变就在该处断开，
   * 断点两侧各自作为独立子路径，不做 Z 闭合。
   */
  const segments: Array<Array<[number, number]>> = [[pts[0]!]]
  for (let i = 1; i < pts.length; i++) {
    /*
     * 判断跨经线：投影后两点 x 相差超过半幅地图宽度，
     * 说明它们分处地图两侧（图上本该是相邻的）。
     * 阈值直接用 SVG 坐标，不依赖 featureToPath 的参数——
     * appendRing 拿不到那些参数。
     */
    const jumpX = Math.abs(pts[i]![0] - pts[i - 1]![0])
    if (jumpX > JUMP_X) {
      // 跳变处断开，新开一段
      segments.push([pts[i]!])
    } else {
      segments[segments.length - 1]!.push(pts[i]!)
    }
  }

  for (const seg of segments) {
    if (seg.length < 2) continue
    let d = ''
    for (let i = 0; i < seg.length; i++) {
      const [x, y] = seg[i]!
      // 保留两位小数：路径数据量能减半，肉眼看不出差别
      d += (i === 0 ? 'M' : 'L') + x.toFixed(2) + ',' + y.toFixed(2)
    }
    // 只有单段（未断开）的环才闭合——多段时闭合会重新引入横线
    out.push(segments.length === 1 ? d + 'Z' : d)
  }
}

/**
 * 英文国名 → ISO 3166-1 alpha-2。
 *
 * 为什么需要这个表：world-atlas的 countries-110m里
 * properties 只有 { name: "Fiji" }，id 是数字（242），
 * **没有 ISO 字母代码**。要跟节点的国家代码对齐只能按名字映射。
 *
 * 只收录监控场景常见的国家，未收录的仍会绘制国界（用数字 id 占位），
 * 只是不参与按国家高亮。
 */
const NAME_TO_ISO: Record<string, string> = {
  China: 'CN',
  'Hong Kong': 'HK',
  Macao: 'MO',
  Taiwan: 'TW',
  Japan: 'JP',
  'South Korea': 'KR',
  'North Korea': 'KP',
  Singapore: 'SG',
  India: 'IN',
  Indonesia: 'ID',
  Thailand: 'TH',
  Vietnam: 'VN',
  Philippines: 'PH',
  Malaysia: 'MY',
  Cambodia: 'KH',
  Myanmar: 'MM',
  Mongolia: 'MN',
  'United States of America': 'US',
  Canada: 'CA',
  Mexico: 'MX',
  Brazil: 'BR',
  Argentina: 'AR',
  Chile: 'CL',
  Colombia: 'CO',
  Peru: 'PE',
  Venezuela: 'VE',
  Ecuador: 'EC',
  Bolivia: 'BO',
  Uruguay: 'UY',
  Paraguay: 'PY',
  'United Kingdom': 'GB',
  Germany: 'DE',
  France: 'FR',
  Netherlands: 'NL',
  Russia: 'RU',
  Sweden: 'SE',
  Norway: 'NO',
  Finland: 'FI',
  Denmark: 'DK',
  Ireland: 'IE',
  Poland: 'PL',
  Austria: 'AT',
  Switzerland: 'CH',
  Italy: 'IT',
  Spain: 'ES',
  Portugal: 'PT',
  Belgium: 'BE',
  Czechia: 'CZ',
  Romania: 'RO',
  Greece: 'GR',
  Hungary: 'HU',
  Ukraine: 'UA',
  Turkey: 'TR',
  Israel: 'IL',
  'United Arab Emirates': 'AE',
  Saudi: 'SA',
  Qatar: 'QA',
  Egypt: 'EG',
  Nigeria: 'NG',
  'South Africa': 'ZA',
  Australia: 'AU',
  'New Zealand': 'NZ',
  Kazakhstan: 'KZ',
}

/**
 * 取Feature 的国家代码。
 *
 * 优先级：
 *   1. properties里若已有 ISO 字母代码，直接用
 *   2. 按英文名查表
 *   3. 都没有 → 返回 undefined（国界仍会画，用数字 id 作key）
 */
export function isoCodeOf(feature: GeoFeature): string | undefined {
  const props = feature.properties ?? {}
  const direct = (props['iso_a2'] ?? props['ISO_A2']) as string | undefined
  if (typeof direct === 'string' && /^[A-Za-z]{2}$/.test(direct)) {
    return direct.toUpperCase()
  }
  const name = props['name']
  if (typeof name === 'string') {
    return NAME_TO_ISO[name.trim()]
  }
  return undefined
}

/** Feature 的稳定 key（用于 v-for）。 */
export function featureKey(feature: GeoFeature, index: number): string {
  return isoCodeOf(feature) ?? String(feature.id ?? index)
}
