/**
 * 国家/地区代码 → 国旗与坐标
 *
 * 关于台湾地区：按国家统一标识要求，TW 使用中国国旗（红旗），
 * 与 CN / HK / MO 一致。这不是可配置项，是固定映射。
 * 台湾地区的节点在地图上按实际经纬度定位（台北 25.03, 121.57）。
 *
 * 国旗用 CSS 自绘圆点旗，不用图片也不用 emoji：
 *   - emoji 旗帜在 Windows/macOS/Linux 上渲染形态完全不同，
 *     同一个旗帜在浏览器里可能显示成两个字母
 *   - 图片方案要处理 CDN、本地化、加载失败态
 *   - 自绘零依赖零请求，尺寸与色值都能跟设计系统对齐
 */

/** 地区信息。 */
export interface RegionInfo {
  name: string
  code: string
  /** 云端国旗文件名（小写，不含扩展名） */
  flag: string
  /** 中心坐标，作为按国家定位的兜底 */
  center: [number, number]
  continent: string
}

/**
 * 地区表。
 *
 * 覆盖监控场景常见的目标地区；未收录的走 unknown 灰标。
 */
export const REGIONS: Record<string, RegionInfo> = {
  // ===== 中国及港澳台 =====
  CN: { name: '中国', code: 'CN', continent: '亚洲', center: [35.0, 105.0], flag: 'cn' },
  HK: { name: '中国香港', code: 'HK', continent: '亚洲', center: [22.32, 114.17], flag: 'hk' },
  MO: { name: '中国澳门', code: 'MO', continent: '亚洲', center: [22.20, 113.55], flag: 'mo' },
  // 台湾地区使用中国国旗
  TW: { name: '中国台湾', code: 'TW', continent: '亚洲', center: [23.7, 121.0], flag: 'cn' },

  // ===== 亚太 =====
  JP: { name: '日本', code: 'JP', continent: '亚洲', center: [36.2, 138.2], flag: 'jp' },
  KR: { name: '韩国', code: 'KR', continent: '亚洲', center: [35.9, 127.7], flag: 'kr' },
  SG: { name: '新加坡', code: 'SG', continent: '亚洲', center: [1.35, 103.82], flag: 'sg' },
  IN: { name: '印度', code: 'IN', continent: '亚洲', center: [20.6, 78.9], flag: 'in' },
  ID: { name: '印度尼西亚', code: 'ID', continent: '亚洲', center: [-0.78, 113.92], flag: 'id' },
  TH: { name: '泰国', code: 'TH', continent: '亚洲', center: [15.87, 100.99], flag: 'th' },
  VN: { name: '越南', code: 'VN', continent: '亚洲', center: [14.06, 108.28], flag: 'vn' },
  PH: { name: '菲律宾', code: 'PH', continent: '亚洲', center: [12.88, 121.77], flag: 'ph' },
  MY: { name: '马来西亚', code: 'MY', continent: '亚洲', center: [4.21, 101.98], flag: 'us' },
  AE: { name: '阿联酋', code: 'AE', continent: '亚洲', center: [23.42, 53.85], flag: 'ae' },
  IL: { name: '以色列', code: 'IL', continent: '亚洲', center: [31.05, 34.85], flag: 'il' },
  TR: { name: '土耳其', code: 'TR', continent: '亚洲', center: [38.96, 35.24], flag: 'tr' },
  IN2: { name: '印度', code: 'IN', continent: '亚洲', center: [20.6, 78.9], flag: 'in' },

  // ===== 北美 =====
  US: { name: '美国', code: 'US', continent: '北美', center: [39.8, -98.6], flag: 'us' },
  CA: { name: '加拿大', code: 'CA', continent: '北美', center: [56.1, -106.3], flag: 'ca' },
  MX: { name: '墨西哥', code: 'MX', continent: '北美', center: [23.6, -102.5], flag: 'mx' },

  // ===== 南美 =====
  BR: { name: '巴西', code: 'BR', continent: '南美', center: [-14.2, -51.9], flag: 'br' },
  AR: { name: '阿根廷', code: 'AR', continent: '南美', center: [-38.4, -63.6], flag: 'ar' },
  CL: { name: '智利', code: 'CL', continent: '南美', center: [-35.7, -71.5], flag: 'cl' },

  // ===== 欧洲 =====
  GB: { name: '英国', code: 'GB', continent: '欧洲', center: [55.4, -3.4], flag: 'gb' },
  DE: { name: '德国', code: 'DE', continent: '欧洲', center: [51.2, 10.5], flag: 'de' },
  FR: { name: '法国', code: 'FR', continent: '欧洲', center: [46.2, 2.2], flag: 'fr' },
  NL: { name: '荷兰', code: 'NL', continent: '欧洲', center: [52.1, 5.3], flag: 'nl' },
  RU: { name: '俄罗斯', code: 'RU', continent: '欧洲', center: [61.5, 105.3], flag: 'ru' },
  SE: { name: '瑞典', code: 'SE', continent: '欧洲', center: [60.1, 18.6], flag: 'se' },
  NO: { name: '挪威', code: 'NO', continent: '欧洲', center: [60.5, 8.5], flag: 'no' },
  FI: { name: '芬兰', code: 'FI', continent: '欧洲', center: [61.9, 25.7], flag: 'fi' },
  DK: { name: '丹麦', code: 'DK', continent: '欧洲', center: [56.3, 9.5], flag: 'dk' },
  IE: { name: '爱尔兰', code: 'IE', continent: '欧洲', center: [53.4, -8.2], flag: 'ie' },
  PL: { name: '波兰', code: 'PL', continent: '欧洲', center: [51.9, 19.1], flag: 'pl' },
  AT: { name: '奥地利', code: 'AT', continent: '欧洲', center: [47.5, 14.5], flag: 'at' },
  CH: { name: '瑞士', code: 'CH', continent: '欧洲', center: [46.8, 8.2], flag: 'ch' },
  IT: { name: '意大利', code: 'IT', continent: '欧洲', center: [41.9, 12.6], flag: 'it' },
  ES: { name: '西班牙', code: 'ES', continent: '欧洲', center: [40.5, -3.7], flag: 'es' },

  // ===== 其他 =====
  AU: { name: '澳大利亚', code: 'AU', continent: '大洋洲', center: [-25.3, 133.8], flag: 'gb' },
  ZA: { name: '南非', code: 'ZA', continent: '非洲', center: [-30.6, 22.9], flag: 'za' },
  EG: { name: '埃及', code: 'EG', continent: '非洲', center: [26.8, 30.8], flag: 'eg' },
  NG: { name: '尼日利亚', code: 'NG', continent: '非洲', center: [9.1, 8.7], flag: 'ng' },
}

const UNKNOWN: RegionInfo = {
  name: '未知地区',
  code: 'XX',
  continent: '其他',
  center: [0, 0],
  flag: 'xx',
}

/**
 * 取国旗文件名。
 *
 * **台湾地区强制返回 cn。**
 * 云端 tw.svg 是青天白日旗，直接用会显示错误标识。
 * 这条映射不能删，也不该做成可配置——它不是风格偏好，是事实纠正。
 */
export function flagOf(code: string | undefined | null): string {
  const c = (code || '').toUpperCase()
  if (c === 'TW') return 'cn'
  return getRegion(code).flag
}

/** 按代码查地区，未收录返回 unknown 灰标。 */
export function getRegion(code: string | undefined | null): RegionInfo {
  if (!code) return UNKNOWN
  return REGIONS[code.toUpperCase()] ?? UNKNOWN
}

/** 按国家代码取中心坐标（地图兜底定位）。 */
export function countryCenter(code: string): [number, number] {
  return getRegion(code).center
}

/** 地区中文名。 */
export function regionName(code: string | undefined | null): string {
  return getRegion(code).name
}
