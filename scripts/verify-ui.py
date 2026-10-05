#!/usr/bin/env python3
"""
UI 验证：检查布局硬性不变量。

设计原则：验证不变量，而不是"看起来对"。
"表单在右栏内居中"可以直接量容器位置验证；
而"离视口中心差多少像素"这种手算期望值，
会因为 gap / max-width / box-sizing 算漏而本身就是错的。

背景：这套脚本是为了防住两个真实缺陷而写的——
  1. 首屏加载层 #app-boot 从未移除，position:fixed 盖住整个页面
  2. App.vue 根容器恒为 display:flex，登录框作为 flex 子元素宽度收缩，挤在左边
两个都是"构建通过、类型检查通过、但页面不能用"的问题，
只有真实渲染 + 几何测量才能发现。
"""
import os
import re
import sys

from playwright.sync_api import sync_playwright

URL = "http://127.0.0.1:5173"
OUT = "/tmp/probeone_shots"


def collect_console(page, errors):
    page.on(
        "console",
        lambda m: errors.append(f"[console.error] {m.text}")
        if m.type == "error"
        else None,
    )
    page.on("pageerror", lambda e: errors.append(f"[pageerror] {e}"))


def check_login(page, errors, width, height):
    page.set_viewport_size({"width": width, "height": height})
    page.goto(URL)
    page.wait_for_load_state("networkidle")
    page.wait_for_timeout(500)

    if page.locator("#app-boot").count() > 0:
        errors.append("✗ 首屏加载层仍存在，它会盖住整个页面")
    else:
        print("  ✓ 加载层已移除")

    box = page.locator(".box")
    if box.count() == 0:
        errors.append("✗ 找不到登录框 .box")
        return
    bb = box.bounding_box()
    box_cx = bb["x"] + bb["width"] / 2
    print(f"  登录框 x={bb['x']:.0f} w={bb['width']:.0f} cx={box_cx:.0f}（视口 {width}）")

    if width >= 960:
        mb = page.locator(".main").bounding_box()
        main_cx = mb["x"] + mb["width"] / 2
        drift = abs(box_cx - main_cx)
        print(f"  右栏中心 cx={main_cx:.0f}，偏差 {drift:.1f}px")
        if drift > 2:
            errors.append(f"✗ 表单未在右栏居中，偏离 {drift:.1f}px")
        else:
            print("  ✓ 表单在右栏内居中")
        if bb["x"] < width * 0.4:
            errors.append(f"✗ 表单挤在左侧（x={bb['x']:.0f}）—— flex 收缩症状仍在")
        ab = page.locator(".aside")
        if ab.count() > 0:
            box_a = ab.bounding_box()
            if box_a and box_a["width"] < 300:
                errors.append(f"✗ 左栏品牌区宽度仅 {box_a['width']:.0f}px，应至少 300px")
            else:
                print("  ✓ 左栏品牌区正常")
    else:
        drift = abs(box_cx - width / 2)
        print(f"  视口中心 {width / 2:.0f}，偏差 {drift:.1f}px")
        if drift > 2:
            errors.append(f"✗ 窄屏未居中，偏差 {drift:.1f}px")
        else:
            print("  ✓ 窄屏居中")
        aside = page.locator(".aside")
        if aside.count() > 0 and aside.is_visible():
            errors.append("✗ 窄屏下左栏品牌区不应显示")
        else:
            print("  ✓ 窄屏隐藏左栏")


def main() -> int:
    os.makedirs(OUT, exist_ok=True)
    errors = []

    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)

        print("=== 登录页 1440×900 ===")
        page = browser.new_page(viewport={"width": 1440, "height": 900})
        collect_console(page, errors)
        check_login(page, errors, 1440, 900)
        page.screenshot(path=f"{OUT}/login-1440.png")

        print("\n=== 登录页 390×844 ===")
        check_login(page, errors, 390, 844)
        page.screenshot(path=f"{OUT}/login-390.png")

        print("\n=== 登录后总览 ===")
        page.set_viewport_size({"width": 1440, "height": 900})
        page.goto(URL)
        page.wait_for_load_state("networkidle")
        page.fill("#username", "admin")
        page.fill("#password", "admin123")
        page.click("button[type=submit]")
        page.wait_for_timeout(1500)

        if "/login" in page.url:
            errors.append("✗ 登录后仍停留在登录页")
        else:
            print(f"  ✓ 登录成功 → {page.url}")

        for sel, name in [(".stat", "统计块"), (".node-card", "节点卡片")]:
            n = page.locator(sel).count()
            if n == 0:
                errors.append(f"✗ 总览缺少{name}")
            else:
                print(f"  ✓ {name}×{n}")
        page.screenshot(path=f"{OUT}/dashboard-1440.png")

        print("\n=== 总览 390×844（窄屏）===")
        page.set_viewport_size({"width": 390, "height": 844})
        page.wait_for_timeout(600)
        if page.locator(".tabbar").is_visible():
            print("  ✓ 底部 Tab 已显示")
        else:
            errors.append("✗ 窄屏下底部 Tab 未显示")
        sw = page.evaluate("document.documentElement.scrollWidth")
        if sw > 391:
            errors.append(f"✗ 存在横向滚动：scrollWidth={sw} > 视口 390")
        else:
            print("  ✓ 无横向滚动")
        page.screenshot(path=f"{OUT}/dashboard-390.png")

        print("\n=== 公开状态页（免鉴权，安全检查）===")
        ctx2 = browser.new_context(viewport={"width": 1440, "height": 900})
        pg2 = ctx2.new_page()
        collect_console(pg2, errors)
        pg2.goto(f"{URL}/status")
        pg2.wait_for_load_state("networkidle")
        pg2.wait_for_timeout(600)

        # 核心安全断言：免登录页面不得出现任何 IP
        html = pg2.content()
        ips = re.findall(r"\b(?:\d{1,3}\.){3}\d{1,3}\b", html)
        ips = [x for x in ips if not x.startswith("127.")]
        if ips:
            errors.append(f"✗ 公开状态页泄漏 IP: {sorted(set(ips))[:5]}")
        else:
            print("  ✓ 公开页无 IP 泄漏")

        for sel, name in [(".item", "状态条目")]:
            n = pg2.locator(sel).count()
            if n == 0:
                errors.append(f"✗ 状态页缺少{name}")
            else:
                print(f"  ✓ {name}×{n}")
        pg2.screenshot(path=f"{OUT}/status-public-1440.png")
        ctx2.close()

        browser.close()

    print("\n" + "=" * 52)
    if errors:
        print("发现问题：")
        for e in errors:
            print("  " + e)
        print(f"\n截图在 {OUT}")
        return 1
    print("全部通过")
    print(f"截图在 {OUT}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
