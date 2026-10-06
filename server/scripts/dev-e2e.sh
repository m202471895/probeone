#!/usr/bin/env bash
# 前后端联调：同一条命令内启动「后端 + 前端」，跑端到端验证，然后关闭。
#
# 为什么必须写在一条命令里：这个执行环境会在命令结束时回收子进程，
# 分开跑必然连不上（表现是 HTTP 000）。
#
# 用法：
#   ./scripts/dev-e2e.sh          # 联调 + 验证
#   ./scripts/dev-e2e.sh --keep   # 验证后保留服务（需手动 Ctrl+C 停止）

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WEB="$ROOT/web"
KEEP=0
[ "${1:-}" = "--keep" ] && KEEP=1

HTTP_PORT="${PROBEONE_DEV_PORT:-8010}"
GRPC_PORT="${PROBEONE_DEV_GRPC_PORT:-8018}"
WEB_PORT="${PROBEONE_WEB_PORT:-5173}"
DB="$ROOT/data/dev.db"

cleanup() {
  [ -n "${SRV_PID:-}" ] && kill "$SRV_PID" 2>/dev/null
  [ -n "${WEB_PID:-}" ] && kill "$WEB_PID" 2>/dev/null
  wait 2>/dev/null
}
[ "$KEEP" = "1" ] || trap cleanup EXIT INT TERM

# ---------- 1. 后端 ----------
mkdir -p "$ROOT/data" "$ROOT/bin"
if [ ! -f "$ROOT/bin/probeone" ]; then
  echo "编译服务端…"
  go build -o "$ROOT/bin/probeone" ./cmd/probeone || exit 1
fi

if [ -f "$ROOT/.env" ]; then
  set -a; . "$ROOT/.env"; set +a
else
  set -a; . "$ROOT/.env.example"; set +a
  export PROBEONE_MASTER_KEY="dev-only-master-key-change-in-production"
fi
export PROBEONE_HTTP_PORT="$HTTP_PORT"
export PROBEONE_GRPC_PORT="$GRPC_PORT"
export PROBEONE_DB_PATH="$DB"
export PROBEONE_LOG_FORMAT=text
export PROBEONE_COOKIE_SECURE=false   # 开发走 HTTP，Secure Cookie 会导致登录态丢失

"$ROOT/bin/probeone" > "$ROOT/data/backend.log" 2>&1 &
SRV_PID=$!

# ---------- 2. 前端 ----------
# VITE_DEV_PROXY_TARGET 决定 dev server 把 /api 转发到哪
(
  cd "$WEB" || exit 1
  VITE_DEV_PROXY_TARGET="http://127.0.0.1:${HTTP_PORT}" \
    npx vite --port "$WEB_PORT" --host 127.0.0.1 --strictPort
) > "$ROOT/data/frontend.log" 2>&1 &
WEB_PID=$!

# ---------- 3. 等待就绪 ----------
wait_http() {
  local url="$1" tries=60
  while [ $tries -gt 0 ]; do
    if curl -s --noproxy '*' -o /dev/null --max-time 1 "$url" 2>/dev/null; then
      return 0
    fi
    tries=$((tries - 1))
    sleep 0.5
  done
  return 1
}

if ! wait_http "http://127.0.0.1:${HTTP_PORT}/healthz"; then
  echo "✗ 后端未就绪："; head -15 "$ROOT/data/backend.log"; exit 1
fi
echo "✓ 后端就绪  http://127.0.0.1:${HTTP_PORT}"

if ! wait_http "http://127.0.0.1:${WEB_PORT}/"; then
  echo "✗ 前端未就绪："; head -15 "$ROOT/data/frontend.log"; exit 1
fi
echo "✓ 前端就绪  http://127.0.0.1:${WEB_PORT}"
echo ""

# ---------- 4. 端到端验证 ----------
PROBEONE_BASE="http://127.0.0.1:${WEB_PORT}" \
PROBEONE_BACKEND="http://127.0.0.1:${HTTP_PORT}" \
PROBEONE_E2E_USER="${PROBEONE_E2E_USER:-admin}" \
PROBEONE_E2E_PASS="${PROBEONE_E2E_PASS:-Admin12345678}" \
  "$ROOT/scripts/e2e.sh"
RC=$?

if [ "$KEEP" = "1" ]; then
  echo ""
  echo "服务已保留："
  echo "  前端 http://127.0.0.1:${WEB_PORT}"
  echo "  后端 http://127.0.0.1:${HTTP_PORT}"
  echo "按 Ctrl+C 停止"
  wait
fi
exit $RC
