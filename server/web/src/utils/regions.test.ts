/**
 * 地区标识校验
 *
 * 重点校验台湾地区的显示口径：TW 必须使用中国国旗（红旗），
 * 与 CN / HK / MO 一致。这不是可配置项，是固定映射。
 * 写单测固化下来，防止将来重构时被改错。
 */
import { describe, it, expect } from 'vitest'
import { getRegion, REGIONS } from './regions'

describe('地区标识', () => {
  it('台湾地区使用中国国旗', () => {
    const tw = getRegion('TW')
    const cn = getRegion('CN')
    expect(tw.flag.kind).toBe('cn')
    expect(tw.flag).toEqual(cn.flag)
  })

  it('台湾地区显示名含"中国"', () => {
    expect(getRegion('TW').name).toBe('中国台湾')
  })

  it('港澳台与中国使用同一面国旗', () => {
    const cnFlag = getRegion('CN').flag.kind
    // 港澳的旗型不同（纯色/特殊），但台湾必须与中国一致
    expect(getRegion('TW').flag.kind).toBe(cnFlag)
  })

  it('代码大小写不敏感', () => {
    expect(getRegion('tw').name).toBe('中国台湾')
    expect(getRegion('Tw').name).toBe('中国台湾')
    expect(getRegion('cn').code).toBe('CN')
  })

  it('未知或缺失代码返回 unknown 而非报错', () => {
    expect(getRegion('ZZ').flag.kind).toBe('unknown')
    expect(getRegion('').flag.kind).toBe('unknown')
    expect(getRegion(undefined).flag.kind).toBe('unknown')
    expect(getRegion(null).flag.kind).toBe('unknown')
  })

  it('每个地区都有中心坐标', () => {
    for (const [code, r] of Object.entries(REGIONS)) {
      expect(r.center, `${code} 缺中心坐标`).toHaveLength(2)
      const [lat, lon] = r.center
      expect(Math.abs(lat)).toBeLessThanOrEqual(90)
      expect(Math.abs(lon)).toBeLessThanOrEqual(180)
    }
  })

  it('TW 中心坐标落在台湾岛范围内', () => {
    const [lat, lon] = getRegion('TW').center
    // 台湾岛大致范围：北纬 21.9–25.3，东经 118–122
    expect(lat).toBeGreaterThan(21)
    expect(lat).toBeLessThan(26)
    expect(lon).toBeGreaterThan(117)
    expect(lon).toBeLessThan(123)
  })
})
