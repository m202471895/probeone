#!/usr/bin/env bash
# 验证后台任务真的在跑。
#
# 之前 collector.Scheduler 501 行实现完整，但从来没被启动过——
# 监控不会自动探测、节点不会被标记离线、预聚合不会算。
# 这个脚本验证它现在真的工作。
#
# 关键：不只测"进程启动成功"，而是验证**定时任务产生了实际效果**
# （监控被探测、状态被更新）。启动日志里写"已启动"是不作数的。

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PORT="${PROBEONE_TEST_PORT:-8010}"
BASE="http://127.0.0.1:${PORT}"
DB="$ROOT/data/sched-test.db"
PW='Admin12345678'
MK="scheduler-test-master-key-at-least-32-bytes"

cleanup() { [ -n "${SRV_PID:-}" ] && kill "$SRV_PID" 2>/dev/null; }
trap cleanup EXIT INT TERM
[ -n "${TARGET_PID:-}" ] && kill "$TARGET_PID" 2>/dev/null

PASS=0; FAIL=0
C() { curl -s --noproxy '*' "$@"; }
ok()  { printf '  ✓ %-44s %s\n' "$1" "${2:-}"; PASS=$((PASS+1)); }
bad() { printf '  ✗ %-44s %s\n' "$1" "${2:-}"; FAIL=$((FAIL+1)); }

mkdir -p "$ROOT/data" "$ROOT/bin"
go build -o "$ROOT/bin/probeone" ./cmd/probeone || exit 1
rm -f "$DB" "$DB-wal" "$DB-shm" 2>/dev/null

# ---------- 建owner ----------
PROBEONE_DB_PATH="$DB" PROBEONE_MASTER_KEY="$MK" \
PROBEONE_INIT_USER=admin PROBEONE_INIT_PASSWORD="$PW" \
  "$ROOT/scripts/init-db.sh" >/dev/null 2>&1

# ---------- 起本地探测目标 ----------
python3 - <<'PY' >/dev/null 2>&1 &
from http.server import BaseHTTPRequestHandler, HTTPServer
class H(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/html")
        self.end_headers()
        self.wfile.write(b"ok")
    def log_message(self, *a): pass
HTTPServer(("127.0.0.1", 8098), H).serve_forever()
PY
TARGET_PID=$!
sleep 1.5

# ---------- 启动服务端 ----------
set -a; . "$ROOT/.env.example"; set +a
export PROBEONE_HTTP_PORT="$PORT"
export PROBEONE_GRPC_PORT="$((PORT+8))"
export PROBEONE_DB_PATH="$DB"
export PROBEONE_MASTER_KEY="$MK"
export PROBEONE_LOG_FORMAT=text
export PROBEONE_COOKIE_SECURE=false
export PROBEONE_ALLOW_INTERNAL_TARGETS=true
# 缩短周期，让"定时任务真的跑了"在测试时限内可观察
export PROBEONE_WEB_INTERVAL=3s
export PROBEONE_OFFLINE_GRACE=8s
export PROBEONE_ROLLUP_INTERVAL=5s

"$ROOT/bin/probeone" > "$ROOT/data/sched-test.log" 2>&1 &
SRV_PID=$!

for _ in $(seq 1 30); do
  C -o /dev/null --max-time 1 "$BASE/healthz" 2>/dev/null && break
  sleep 0.5
done

echo "=== 1. 后台任务已启动（启动日志）==="
if grep -q "后台任务已启动" "$ROOT/data/sched-test.log"; then
  ok "启动日志确认" "$(grep -o '后台任务已启动' "$ROOT/data/sched-test.log" | head -1)"
else
  bad "未看到后台任务启动日志"
  head -12 "$ROOT/data/sched-test.log"
  exit 1
fi

echo ""
echo "=== 2. 登录并创建监控 ==="
TOKEN=$(C -X POST "$BASE/api/auth/login" -H "Content-Type: application/json" \
  -d "{\"username\":\"admin\",\"password\":\"$PW\"}" \
  | python3 -c "import json,sys;print(json.load(sys.stdin).get('data',{}).get('token',''))" 2>/dev/null)
[ -z "$TOKEN" ] && { bad "登录"; exit 1; }
AUTH="Authorization: Bearer $TOKEN"

MID=$(C -X POST "$BASE/api/monitors" -H "$AUTH" -H "Content-Type: application/json" \
  -d '{"name":"后台任务验证","type":"http","target":"http://127.0.0.1:8098","interval_sec":10,"timeout_sec":3,"config":{"method":"GET","expect_status":[200]}}' \
  | python3 -c "import json,sys;print(json.load(sys.stdin).get('data',{}).get('id',''))" 2>/dev/null)

if [ -n "$MID" ] && [ "$MID" != "None" ]; then
  ok "创建监控" "id=$MID"
else
  bad "创建监控失败"
  exit 1
fi

echo ""
echo "=== 3. 等待自动探测（不手动触发）==="
echo "    等待 25 秒（扫描周期 3s + 监控间隔 10s，够跑两轮以上）…"
sleep 25

echo ""
echo "=== 4. 监控被自动探测了（关键：不是手动点的）==="
CNT=$(C "$BASE/api/monitors/$MID/results" -H "$AUTH" \
  | python3 -c "import json,sys;print(len(json.load(sys.stdin).get('data',{}).get('items',[])))" 2>/dev/null)
if [ "${CNT:-0}" -ge 2 ] 2>/dev/null; then
  ok "自动探测已落库" "$CNT 条（≥2 说明不止一次）"
else
  bad "未见自动探测" "仅 $CNT 条"
fi

echo ""
echo "=== 5. 状态被自动更新为 up ==="
ST=$(C "$BASE/api/monitors/$MID" -H "$AUTH" \
  | python3 -c "import json,sys;print(json.load(sys.stdin).get('data',{}).get('status',''))" 2>/dev/null)
if [ "$ST" = "up" ]; then
  ok "状态已更新" "$ST"
else
  bad "状态未更新" "实际=$ST"
fi

echo ""
echo "=== 6. 探测记录里的时间戳是后台写的（不是启动那一刻）==="
SPREAD=$(C "$BASE/api/monitors/$MID/results" -H "$AUTH" | python3 -c "
import json,sys
items=json.load(sys.stdin).get('data',{}).get('items',[])
if len(items)<2: print('不足'); raise SystemExit
ts=[i.get('checked_at','') for i in items if i.get('checked_at')]
print('有多个不同时间戳' if len(set(ts))>1 else '时间戳相同')
" 2>/dev/null)
if [ "$SPREAD" = "有多个不同时间戳" ]; then
  ok "记录来自多轮探测" "时间戳分散"
else
  bad "记录时间戳异常" "$SPREAD"
fi

echo ""
echo "=== 7. 停机时后台任务能干净退出 ==="
kill -TERM "$SRV_PID" 2>/dev/null
wait "$SRV_PID" 2>/dev/null
if grep -q "后台任务已全部退出" "$ROOT/data/sched-test.log"; then
  ok "后台任务随停机退出"
else
  bad "未看到后台任务退出日志"
  tail -6 "$ROOT/data/sched-test.log"
fi
if grep -q "已安全退出" "$ROOT/data/sched-test.log"; then
  ok "进程正常退出"
else
  bad "进程未正常退出"
fi

kill "$TARGET_PID" 2>/dev/null

echo ""
echo "======================================"
echo "通过 $PASS 项，失败 $FAIL 项"
[ "$FAIL" -eq 0 ] || exit 1
