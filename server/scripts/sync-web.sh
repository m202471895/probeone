#!/usr/bin/env bash
# 把前端产物复制到 internal/webembed/dist 供 go:embed 嵌入。
#
# 为什么需要这一步：go:embed 只能嵌入**当前包目录树内**的文件。
# 前端产物在 server/web/dist，不在 internal/webembed/ 下，
# 直接 //go:embed ../../web/dist 会被 Go 拒绝。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC="$ROOT/web/dist"
DST="$ROOT/internal/webembed/dist"

if [ ! -f "$SRC/index.html" ]; then
  echo "前端产物不存在，请先构建："
  echo "  cd web && npm run build"
  exit 1
fi

# 只删前端产物，不动 downloads/。
#
# downloads/ 里是 Agent 二进制（几 MB），不属于 web/dist，
# 但 rm -rf "$DST" 会把它一起清掉——而这个脚本的语义是
# "同步前端"，不该顺手删掉别的东西。
rm -rf "$DST/assets" "$DST/index.html" "$DST/install.sh"
mkdir -p "$DST"
cp -R "$SRC/." "$DST/"
echo "前端产物已同步：$DST"
echo "  文件数: $(find "$DST" -type f | wc -l | tr -d ' ')"
