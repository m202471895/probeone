/**
 * 地区标识校验
 *
 * 重点校验台湾地区的显示口径：TW 必须使用中国国旗（红旗），
 * 与 CN / HK / MO 一致。这不是可配置项，是固定映射。
 * 写单测固化下来，防止将来重构时被改错。
 */
import { describe, it, expect } from 'vitest'
import { getRegion, flagOf, REGIONS } from './regions'

describe('地区标识', () => {
  it('台湾地区请求中国国旗文件', () => {
    // 云端 tw.svg 是青天白日旗，必须在代码层强制映射到 cn
    expect(flagOf('TW')).toBe('cn')
    expect(flagOf('tw')).toBe('cn')
    expect(flagOf('TW')).toBe(flagOf('CN'))
  })

  it('台湾地区请求的是 cn.svg 而非 tw.svg', () => {
    // 防止有人"顺手修正"成直接用 tw
    expect(flagOf('TW')).not.toBe('tw')
  })

  it('台湾地区显示名含"中国"', () => {
    expect(getRegion('TW').name).toBe('中国台湾')
  })

  it('港澳各用自己的旗号文件', () => {
    // 香港、澳门、台湾是三个不同文件，不是同一张图
    expect(flagOf('HK')).toBe('hk')
    expect(flagOf('MO')).toBe('mo')
    expect(flagOf('TW')).toBe('cn')
  })

  it('代码大小写不敏感', () => {
    expect(getRegion('tw').name).toBe('中国台湾')
    expect(flagOf('Us')).toBe('us')
    expect(getRegion('Tw').name).toBe('中国台湾')
    expect(getRegion('cn').code).toBe('CN')
  })

  it('未知或缺失代码返回 xx 而非报错', () => {
    expect(flagOf('ZZ')).toBe('xx')
    expect(flagOf('')).toBe('xx')
    expect(flagOf(undefined)).toBe('xx')
    expect(flagOf(null)).toBe('xx')
  })

  it('每个地区的旗号文件都是小写无扩展名', () => {
    for (const [code, r] of Object.entries(REGIONS)) {
      expect(r.flag, `${code} 的旗号异常`).toMatch(/^[a-z]{2}$/)
    }
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
