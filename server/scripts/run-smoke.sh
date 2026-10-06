#!/usr/bin/env bash
# P7 冒烟测试：在同一条命令内完成「启动 → 测试 → 关闭」。
#
# 为什么不用后台常驻：这个执行环境会在命令结束时回收子进程，
# 分开跑必然连不上（表现是全部 HTTP 000）。
# 启动与测试必须在同一进程组内完成。

set -uo pipefail

cd "$(dirname "$0")/.." || exit 1
ROOT=$(pwd)
BIN="${ROOT}/bin/probeone"
PORT="${PROBEONE_TEST_PORT:-8010}"
GRPC_PORT="${PROBEONE_TEST_GRPC_PORT:-8018}"
DB="${ROOT}/data/smoke.db"
LOG="${ROOT}/data/smoke.log"

# ---------- 准备 ----------
mkdir -p "${ROOT}/data" "${ROOT}/bin"
if [ ! -f "$BIN" ]; then
  echo "编译服务端…"
  if ! go build -o "$BIN" ./cmd/probeone 2>/tmp/build_err.txt; then
    echo "✗ 编译失败："; cat /tmp/build_err.txt; exit 1
  fi
fi

# 加载配置（.env 不存在时用 .env.example，MasterKey 为空也允许启动）
if [ -f "${ROOT}/.env" ]; then
  set -a; . "${ROOT}/.env"; set +a
else
  set -a; . "${ROOT}/.env.example"; set +a
  export PROBEONE_MASTER_KEY="${PROBEONE_MASTER_KEY:-smoke-test-master-key-not-for-production}"
fi

export PROBEONE_HTTP_PORT="$PORT"
export PROBEONE_GRPC_PORT="$GRPC_PORT"
export PROBEONE_DB_PATH="$DB"
export PROBEONE_LOG_FORMAT=text

rm -f "$DB" "$DB-wal" "$DB-shm" 2>/dev/null

# ---------- 启动 ----------
"$BIN" > "$LOG" 2>&1 &
SRV=$!
cleanup() {
  kill "$SRV" 2>/dev/null
  wait "$SRV" 2>/dev/null
}
trap cleanup EXIT INT TERM

# 等待端口就绪，最多 15 秒
READY=0
for _ in $(seq 1 30); do
  if curl -s --noproxy '*' -o /dev/null --max-time 1 "http://127.0.0.1:${PORT}/healthz" 2>/dev/null; then
    READY=1; break
  fi
  # 进程死了就别等了
  if ! kill -0 "$SRV" 2>/dev/null; then
    echo "✗ 服务启动即退出："; head -20 "$LOG"; exit 1
  fi
  sleep 0.5
done

if [ "$READY" -ne 1 ]; then
  echo "✗ 服务 15 秒内未就绪："; head -20 "$LOG"; exit 1
fi
echo "✓ 服务已就绪（HTTP ${PORT} / gRPC ${GRPC_PORT}）"
echo ""

# ---------- 测试 ----------
export PROBEONE_BASE="http://127.0.0.1:${PORT}"
"${ROOT}/scripts/smoke.sh"
RC=$?

echo ""
echo "启动日志（前 8 行）："
head -8 "$LOG"
exit $RC
