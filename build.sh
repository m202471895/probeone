#!/usr/bin/env bash
# 一键构建全部产物（供本地开发与部署使用）。
#
# 存在的理由：go:embed 是**编译期快照**。
# 之前多次因为顺序搞错而漏掉内容——
# 先sync-web 再 build-agent，编译时 downloads/ 还是空的，
# 装Agent 时就 404。顺序必须固化成脚本，不靠记忆。
#
# 顺序：
#   1. 前端构建        → server/web/dist
#   2. 同步到embed 目录
#   3. 编译 Agent 多架构 → embed 目录的 downloads/
#   4. 生成 manifest    → install.sh 靠它查版本
#   5. 复制 install.sh
#   6. 编译 Go 主程序（内嵌以上全部）
#   7. 校验关键文件确实进了二进制
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

GREEN=$'\033[32m'; DIM=$'\033[2m'; YELLOW=$'\033[33m'; OFF=$'\033[0m'
say() { printf '%s==>%s %s\n' "$GREEN" "$OFF" "$1"; }
note() { printf '%s    %s%s\n' "$DIM" "$1" "$OFF"; }

# ---------- 1-2. 前端 ----------
say "构建前端"
cd "$ROOT/server/web"
[ -d node_modules ] || npm ci
npm run build:only >/dev/null
cd "$ROOT/server"
./scripts/sync-web.sh

# ---------- 3-4. Agent ----------
say "编译 Agent 多架构"
cd "$ROOT"
bash server/scripts/build-agent.sh | tail -3

# ---------- 5. install.sh ----------
say "同步 install.sh"
cp "$ROOT/server/scripts/install.sh" "$ROOT/server/internal/webembed/dist/install.sh"

# ---------- 6. 主程序 ----------
say "编译服务端"
cd "$ROOT/server"
go build ./...

# ---------- 7. 校验 ----------
say "校验内嵌内容"
missing=0

check_embedded() {
  local pattern="$1" label="$2"
  if strings "$ROOT/server/bin/probeone" | grep -q "$pattern"; then
    note "✓ $label"
  else
    printf '    %s✗ %s 未内嵌进二进制%s\n' "$YELLOW" "$label" "$OFF"
    missing=1
  fi
}

# 先确保本地 bin/probeone 存在
mkdir -p bin
go build -o bin/probeone ./cmd/probeone

# 一次性把字符串表转储到文件再grep。
# 逐个 `strings | grep -q` 在 set -e 下有问题：
# grep 提前退出会让 strings 收到 SIGPIPE，整个脚本被判定失败。
strings "$ROOT/server/bin/probeone" > "$ROOT/server/bin/.strings.txt" 2>/dev/null || true

check_embedded() {
  local pattern="$1" label="$2"
  if grep -q -- "$pattern" "$ROOT/server/bin/.strings.txt"; then
    note "✓ $label"
  else
    printf '    %s✗ %s 未内嵌进二进制%s\n' "$YELLOW" "$label" "$OFF"
    missing=1
  fi
}

check_embedded 'probeone-agent-dev-linux-amd64' "Agent 二进制 (amd64)"
check_embedded 'probeone-agent-dev-linux-arm64' "Agent 二进制 (arm64)"
check_embedded 'manifest.json'                "安装包清单"
check_embedded 'install.sh'                   "安装脚本"
rm -f "$ROOT/server/bin/.strings.txt"

if [ "$missing" -ne 0 ]; then
  echo ""
  echo "内嵌校验失败。Go 的 go:embed 是编译期快照——"
  echo "若刚改了 embed 目录下的文件，必须重新编译（go build）才会生效。"
  exit 1
fi

say "构建完成"
note "server/bin/probeone"