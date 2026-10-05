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
): string {
  const g = feature.geometry
  if (!g) return ''

  const parts: string[] = []

  const project = (lon: number, lat: number): [number, number] => {
    const x = ((lon + 180) / 360) * width
    const y = ((90 - lat) / 180) * height
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

function appendRing(
  out: string[],
  ring: number[][],
  project: (lon: number, lat: number) => [number, number],
): void {
  if (!ring || ring.length === 0) return
  let d = ''
  for (let i = 0; i < ring.length; i++) {
    const c = ring[i]
    if (!c) continue
    const [x, y] = project(c[0] ?? 0, c[1] ?? 0)
    // 保留两位小数：路径数据量能减半，肉眼看不出差别
    d += (i === 0 ? 'M' : 'L') + x.toFixed(2) + ',' + y.toFixed(2)
  }
  if (d) out.push(d + 'Z')
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
