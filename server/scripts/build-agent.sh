#!/usr/bin/env bash
# 编译 Agent 多架构二进制到内嵌目录，供 install.sh 下载。
#
# 为什么放服务端内嵌：一键安装需要从面板拿二进制，
# 而被监控机可能连不上 GitHub。放服务端保证"面板能访问就能装"。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="$ROOT/server/internal/webembed/dist/downloads"
VERSION="${PROBEONE_VERSION:-dev}"

mkdir -p "$OUT"

# 只编 amd64 与 arm64：覆盖绝大多数 VPS 与家宽盒子。
# armv7（树莓派）需要额外设 GOARM=7，需要时再单独加。
build_one() {
  local os="$1" arch="$2"
  echo "  编译 ${os}/${arch} ..."
  ( cd "$ROOT/agent" && \
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -ldflags="-s -w" \
      -o "$OUT/probeone-agent-${VERSION}-${os}-${arch}" ./cmd/agent )
}

build_one linux amd64
build_one linux arm64

echo "产物："
ls -lh "$OUT" | tail -n +2 | awk '{print "  "$5"  "$9}'
