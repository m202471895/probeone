#!/usr/bin/env bash
# 编译 Agent 多架构二进制，并生成 manifest.json。
#
# 为什么需要 manifest：install.sh 必须知道该下载哪个版本。
# 之前脚本里写死 "latest"，而产物名用的是 "dev"，
# 猜错就下载不到——而且 /downloads/ 会SPA 回退到index.html，
# curl 拿到 200 于是把 HTML 当二进制装到机器上，一执行就报语法错误。
#
# 让服务端自己报版本是唯一可靠的做法。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="$ROOT/server/internal/webembed/dist/downloads"
# 版本号从 agent 自己取。
# 不要用 `probeone -version`——那个命令会先跑配置校验，
# 没设 PROBEONE_MASTER_KEY 时直接退出，取不到版本。
VERSION="${PROBEONE_VERSION:-$(cd "$ROOT/agent" && go run ./cmd/agent -version 2>/dev/null | awk '{print $NF}')}"
VERSION="${VERSION:-dev}"

mkdir -p "$OUT"

build_one() {
  local os="$1" arch="$2"
  local name="probeone-agent-${VERSION}-${os}-${arch}"
  echo "  编译 ${os}/${arch} -> ${name}"
  ( cd "$ROOT/agent" && \
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -ldflags="-s -w" -o "$OUT/$name" ./cmd/agent )
}

build_one linux amd64
build_one linux arm64

# 清单：install.sh 用它查版本号并拼下载地址。
# 用 ls 而不是手写数组——新增架构时这里自动跟上，不会漏。
{
  echo '{'
  echo "  \"version\": \"${VERSION}\","
  echo '  "files": ['
  first=1
  for f in "$OUT"/probeone-agent-*; do
    [ -f "$f" ] || continue
    name=$(basename "$f")
    size=$(wc -c < "$f" | tr -d ' ')
    if [ $first -eq 0 ]; then echo ','; fi
    first=0
    printf '    { "name": "%s", "size": %s }' "$name" "$size"
  done
  echo
  echo '  ]'
  echo '}'
} > "$OUT/manifest.json"

echo "版本：${VERSION}"
echo "清单：${OUT}/manifest.json"
ls -lh "$OUT" | tail -n +2 | awk '{print "  "$5"  "$9}'
