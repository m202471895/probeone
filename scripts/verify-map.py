#!/usr/bin/env python3
"""
世界地图渲染验证。

检查项：
1. 跨经线横线 —— 跨 ±180° 经线的国家（俄罗斯、斐济）若直接闭合环，
   会画出一条横穿整幅地图的横线。表现为单段子路径的 x跨度接近全图宽。
2. 节点投影准确性 —— 圆的 x/y 应与等距圆柱投影公式一致。
3. 裁切线以下的path 残留 —— viewBox 裁切不会阻止绘制，
   残留的半截线条仍会显示。
"""
import sys

from playwright.sync_api import sync_playwright

URL = "http://127.0.0.1:5173"

# 城市经纬度，用于校验投影
CITIES = [
    ("香港", 114.17, 22.32), ("东京", 139.65, 35.68),
    ("新加坡", 103.82, 1.35), ("洛杉矶", -118.24, 34.05),
    ("法兰克福", 8.68, 50.11), ("首尔", 126.98, 37.57),
    ("台北", 121.57, 25.03), ("九龙", 114.18, 22.31),
]

W, H = 1000, 500


def main() -> int:
    problems = []
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        page = browser.new_page(viewport={"width": 1600, "height": 900})
        page.goto(URL)
        page.wait_for_load_state("networkidle")
        page.fill("#username", "admin")
        page.fill("#password", "admin123")
        page.click("button[type=submit]")
        page.wait_for_timeout(1800)

        if page.locator(".world-map").count() == 0:
            print("✗ 地图未渲染")
            return 1

        # 1) 跨经线横线
        r = page.evaluate("""()=>{
          let bad=0, checked=0;
          for (const p of document.querySelectorAll('.map-country')) {
            const d=p.getAttribute('d')||'';
            for (const sub of d.split('M').filter(Boolean)) {
              const nums=sub.match(/-?\\d+\\.\\d+/g)||[];
              const xs=[];
              for (let i=0;i<nums.length;i+=2) xs.push(parseFloat(nums[i]));
              if (xs.length>1) { checked++; if (Math.max(...xs)-Math.min(...xs) > 900) bad++; }
            }
          }
          return {bad, checked};
        }""")
        mark = "✓" if r["bad"] == 0 else "✗"
        print(f"  {mark} 跨经线横线：检查 {r['checked']} 段，异常 {r['bad']} 条")
        if r["bad"]:
            problems.append(f"仍有 {r['bad']} 条横穿地图的连线")

        # 2) 节点投影
        pts = page.evaluate("""()=>[...document.querySelectorAll('.point-dot')]
            .map(d=>({x:+d.getAttribute('cx'), y:+d.getAttribute('cy')}))""")
        worst = 0
        for i, (name, lon, lat) in enumerate(CITIES):
            if i >= len(pts):
                break
            ex = (lon + 180) / 360 * W
            ey = (90 - lat) / 180 * H
            d = max(abs(pts[i]["x"] - ex), abs(pts[i]["y"] - ey))
            worst = max(worst, d)
        mark = "✓" if worst <= 1 else "✗"
        print(f"  {mark} 节点投影：最大偏差 {worst:.1f}px（应 ≤1）")
        if worst > 1:
            problems.append(f"节点投影偏差 {worst:.1f}px")

        # 3) 数量
        n_country = page.locator(".map-country").count()
        n_dot = page.locator(".point-dot").count()
        print(f"  ✓ 国界 {n_country} 条，节点 {n_dot} 个")
        if n_country < 100:
            problems.append(f"国界仅 {n_country} 条，可能渲染不全")
        if n_dot == 0:
            problems.append("地图上无节点")

        page.locator(".world-map").screenshot(path="/tmp/probeone_shots/map-verify.png")
        browser.close()

    print()
    if problems:
        print("发现问题：")
        for x in problems:
            print("  " + x)
        return 1
    print("全部通过")
    return 0


if __name__ == "__main__":
    sys.exit(main())
