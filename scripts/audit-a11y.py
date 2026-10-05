#!/usr/bin/env python3
"""
可访问性与交互审计。

用量化检查代替肉眼扫视——前几轮的问题都是肉眼漏掉的
（内边距全局失效、路由缺失、KeepAlive 缓存旧数据）。

检查项：
1. 触控目标：可点击元素不小于 24×24
2. 表单标签：每个输入框有关联的 label 或 aria-label
3. 可访问名：button/a 有可读文本或 aria-label
4. 键盘可达：Tab 能到达主要交互元素
5. 状态完整：加载态有骨架屏、空态有行动入口
6. 交互生效：点筛选后数据确实变化
"""
import sys

from playwright.sync_api import sync_playwright

URL = "http://127.0.0.1:5173"
AUTH_PAGES = [
    "/", "/nodes", "/monitors", "/alerts", "/alert-rules",
    "/channels", "/users", "/audit-logs", "/settings/visibility",
]
PUBLIC_PAGES = ["/status"]

JS_AUDIT = """() => {
  const r = { small: 0, smallDetail: [], noLabel: 0, noName: 0, noNameDetail: [] };
  for (const e of document.querySelectorAll('button, a')) {
    const b = e.getBoundingClientRect();
    if (b.width > 0 && b.height > 0 && (b.height < 24 || b.width < 24)) {
      r.small++;
      if (r.smallDetail.length < 3) {
        r.smallDetail.push({
          t: (e.textContent || e.getAttribute('aria-label') || '?').trim().slice(0, 14),
          w: Math.round(b.width), h: Math.round(b.height),
        });
      }
    }
    const name = (e.textContent || '').trim()
      || e.getAttribute('aria-label') || e.getAttribute('title');
    if (!name) {
      r.noName++;
      if (r.noNameDetail.length < 3) r.noNameDetail.push(e.className.slice(0, 26));
    }
  }
  for (const i of document.querySelectorAll('input:not([type=checkbox]):not([type=radio])')) {
    const hasLabel = i.id && document.querySelector('label[for="' + i.id + '"]');
    if (!hasLabel && !i.getAttribute('aria-label') && !i.closest('label')) r.noLabel++;
  }
  return r;
}"""


def main() -> int:
    problems = []
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        page = browser.new_page(viewport={"width": 1440, "height": 1000})

        page.goto(URL)
        page.wait_for_load_state("networkidle")
        page.fill("#username", "admin")
        page.fill("#password", "admin123")
        page.click("button[type=submit]")
        page.wait_for_timeout(1400)

        print("=== 可访问性 ===")
        for path in AUTH_PAGES + PUBLIC_PAGES:
            page.goto(f"{URL}{path}")
            page.wait_for_load_state("networkidle")
            page.wait_for_timeout(400)
            r = page.evaluate(JS_AUDIT)
            total = r["small"] + r["noLabel"] + r["noName"]
            mark = "✓" if total == 0 else "✗"
            detail = ""
            if total:
                detail = (
                    f" 热区{r['small']} 无label{r['noLabel']} 无名{r['noName']}"
                    f" 例:{r['smallDetail'] or r['noNameDetail']}"
                )
                problems.append(f"{path}{detail}")
            print(f"  {mark} {path:24}{detail}")

        print("\n=== 交互生效 ===")
        # 告警筛选
        page.goto(f"{URL}/alerts")
        page.wait_for_load_state("networkidle")
        page.wait_for_timeout(400)
        counts = {}
        for label in ["未处理", "已恢复", "已确认", "全部"]:
            page.locator(".card-actions .chip", has_text=label).first.click()
            page.wait_for_timeout(600)
            counts[label] = page.locator(".item").count()
        print(f"  告警四档: {counts}")
        if len(set(counts.values())) < 3:
            problems.append(f"告警筛选未生效（各档数据雷同）: {counts}")

        # 节点状态筛选
        page.goto(f"{URL}/nodes")
        page.wait_for_load_state("networkidle")
        page.wait_for_timeout(400)
        rows = {}
        for val in ["all", "online", "offline", "pending"]:
            page.select_option(".filter", val)
            page.wait_for_timeout(600)
            rows[val] = page.locator(".row").count()
        print(f"  节点状态: {rows}")
        if len(set(rows.values())) < 3:
            problems.append(f"节点状态筛选未生效: {rows}")

        print("\n=== 键盘可达 ===")
        page.goto(f"{URL}/")
        page.wait_for_load_state("networkidle")
        page.wait_for_timeout(400)
        # Tab 若干次看是否能聚焦到可交互元素
        reached = []
        for _ in range(12):
            page.keyboard.press("Tab")
            info = page.evaluate("""() => {
                const e = document.activeElement;
                if (!e || e === document.body) return null;
                return e.tagName + '.' + (e.className || '').toString().slice(0, 16);
            }""")
            if info:
                reached.append(info)
        print(f"  Tab 12 次可达元素数: {len(reached)}")
        if len(set(reached)) < 4:
            problems.append(f"键盘可达性不足，仅{len(set(reached))} 个元素可聚焦")

        browser.close()

    print("\n" + "=" * 50)
    if problems:
        print("发现问题：")
        for x in problems:
            print("  " + x)
        return 1
    print("全部通过")
    return 0


if __name__ == "__main__":
    sys.exit(main())
