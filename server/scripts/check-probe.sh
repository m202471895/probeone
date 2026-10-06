#!/usr/bin/env bash
# 验证「立即检查」真的能探测。
#
# 这个端点之前返回 501（引擎没实现 ProbeNow），
# 现在改为直接调monitor.Prober。这条脚本验证它确实在工作：
# 造一个监控 → 立即检查 → 确认状态已更新 → 确认历史已落库。
#
# 关键：不只测"返回 200"，还要确认结果真的写进了数据库——
# 返回成功但没落库是最常见的假成功。

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PORT="${PROBEONE_TEST_PORT:-8010}"
BASE="http://127.0.0.1:${PORT}"
DB="$ROOT/data/check-probe.db"

cleanup() { [ -n "${SRV_PID:-}" ] && kill "$SRV_PID" 2>/dev/null; }
trap cleanup EXIT INT TERM

PASS=0; FAIL=0
C() { curl -s --noproxy '*' "$@"; }
ok()   { printf '  ✓ %-46s %s\n' "$1" "${2:-}"; PASS=$((PASS+1)); }
bad()  { printf '  ✗ %-46s %s\n' "$1" "${2:-}"; FAIL=$((FAIL+1)); }

mkdir -p "$ROOT/data" "$ROOT/bin"
go build -o "$ROOT/bin/probeone" ./cmd/probeone || exit 1

rm -f "$DB" "$DB-wal" "$DB-shm" 2>/dev/null

set -a; . "$ROOT/.env.example"; set +a
export PROBEONE_HTTP_PORT="$PORT"
export PROBEONE_GRPC_PORT="$((PORT+8))"
export PROBEONE_DB_PATH="$DB"
# master key 必须够长：后端启动时校验长度，
# 短密钥会被直接拒绝启动（这是有意的安全约束，不是缺陷）。
export PROBEONE_MASTER_KEY="check-probe-test-master-key-32-bytes-minimum-length"
export PROBEONE_LOG_FORMAT=text
export PROBEONE_COOKIE_SECURE=false
# 测试目标是本机临时服务，属于内网地址。
# SSRF 防护默认会拦（这是有意的安全约束），
# 所以这里显式开启——顺便验证该开关真的生效。
export PROBEONE_ALLOW_INTERNAL_TARGETS=true

# 库是空的，先建owner 用户（登录需要）
PROBEONE_DB_PATH="$DB" \
PROBEONE_MASTER_KEY="check-probe-test-master-key-32-bytes-minimum-length" \
PROBEONE_INIT_USER=admin \
PROBEONE_INIT_PASSWORD='Admin12345678' \
  "$ROOT/scripts/init-db.sh" >/dev/null 2>&1

"$ROOT/bin/probeone" > "$ROOT/data/check-probe.log" 2>&1 &
SRV_PID=$!

for _ in $(seq 1 30); do
  C -o /dev/null --max-time 1 "$BASE/healthz" 2>/dev/null && break
  sleep 0.5
done

# ---------- 准备：登录 + 建监控 ----------
PW='Admin12345678'
TOKEN=$(C -X POST "$BASE/api/auth/login" -H "Content-Type: application/json" \
  -d "{\"username\":\"admin\",\"password\":\"$PW\"}" \
  | python3 -c "import json,sys;print(json.load(sys.stdin).get('data',{}).get('token',''))" 2>/dev/null)

if [ -z "$TOKEN" ]; then
  echo "✗ 无法登录——先跑 scripts/init-db.sh 创建 owner 用户"
  head -10 "$ROOT/data/check-probe.log"
  exit 1
fi
AUTH="Authorization: Bearer $TOKEN"

# 目标用 httpbin 类的公共回显服务不可靠（外网可能不通），
# 改用本地临时服务：起一个最小 HTTP 服务器当探测目标。
python3 - <<'PY' > /tmp/probe_target.log 2>&1 &
from http.server import BaseHTTPRequestHandler, HTTPServer
class H(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/html")
        self.end_headers()
        self.wfile.write(b"<html>ok</html>")
    def log_message(self, *a): pass
HTTPServer(("127.0.0.1", 8099), H).serve_forever()
PY
TARGET_PID=$!
sleep 1.5

echo "=== 1. 创建 HTTP 监控（目标为本地临时服务）==="
MID=$(C -X POST "$BASE/api/monitors" -H "$AUTH" -H "Content-Type: application/json" \
  -d '{"name":"立即检查验证","type":"http","target":"http://127.0.0.1:8099","interval_sec":600,"timeout_sec":5,"config":{"method":"GET","expect_status":[200]}}' \
  | python3 -c "import json,sys;print(json.load(sys.stdin).get('data',{}).get('id',''))" 2>/dev/null)

if [ -z "$MID" ] || [ "$MID" = "None" ]; then
  bad "创建监控"
  C -X POST "$BASE/api/monitors" -H "$AUTH" -H "Content-Type: application/json" \
    -d '{"name":"立即检查验证","type":"http","target":"http://127.0.0.1:8099","interval_sec":600,"timeout_sec":5,"config":{"method":"GET","expect_status":[200]}}' | head -c 250
  echo
  kill "$TARGET_PID" 2>/dev/null
  exit 1
fi
ok "创建监控" "id=$MID"

echo ""
echo "=== 2. 立即检查 ==="
RES=$(C -X POST "$BASE/api/monitors/$MID/check" -H "$AUTH")
echo "$RES" | head -c 300; echo ""
PROBE_OK=$(echo "$RES" | python3 -c "import json,sys;print(json.load(sys.stdin).get('data',{}).get('ok',''))" 2>/dev/null)
LATENCY=$(echo "$RES" | python3 -c "import json,sys;print(json.load(sys.stdin).get('data',{}).get('latency_ms',''))" 2>/dev/null)

if [ "$PROBE_OK" = "True" ]; then
  ok "探测返回成功" "延迟 ${LATENCY}ms"
else
  bad "探测返回失败" "ok=$PROBE_OK"
fi

echo ""
echo "=== 3. 状态已落库（关键：不能只是返回 200）==="
ST=$(C "$BASE/api/monitors/$MID" -H "$AUTH" \
  | python3 -c "import json,sys;print(json.load(sys.stdin).get('data',{}).get('status',''))" 2>/dev/null)
if [ "$ST" = "up" ]; then
  ok "监控状态已更新" "$ST"
else
  bad "状态未更新" "实际=$ST"
fi

echo ""
echo "=== 4. 探测历史已落库 ==="
CNT=$(C "$BASE/api/monitors/$MID/results" -H "$AUTH" \
  | python3 -c "import json,sys;print(len(json.load(sys.stdin).get('data',{}).get('items',[])))" 2>/dev/null)
if [ "${CNT:-0}" -ge 1 ] 2>/dev/null; then
  ok "历史已记录" "$CNT 条"
else
  bad "历史未记录" "条数=$CNT"
fi

echo ""
echo "=== 5. 失败探测也应有结论（不是 4xx/5xx）==="
MID2=$(C -X POST "$BASE/api/monitors" -H "$AUTH" -H "Content-Type: application/json" \
  -d '{"name":"失败路径验证","type":"tcp","target":"127.0.0.1","interval_sec":600,"timeout_sec":3,"config":{"port":9}}' \
  | python3 -c "import json,sys;print(json.load(sys.stdin).get('data',{}).get('id',''))" 2>/dev/null)
if [ -n "$MID2" ] && [ "$MID2" != "None" ]; then
  CODE=$(C -o /tmp/cp.json -w '%{http_code}' -X POST "$BASE/api/monitors/$MID2/check" -H "$AUTH")
  ROK=$(python3 -c "import json;print(json.load(open('/tmp/cp.json')).get('data',{}).get('ok',''))" 2>/dev/null)
  REASON=$(python3 -c "import json;print(json.load(open('/tmp/cp.json')).get('data',{}).get('reason',''))" 2>/dev/null)
  if [ "$CODE" = "200" ] && [ "$ROK" = "False" ]; then
    ok "探测失败返回 200 + ok=false" "reason=$REASON"
  else
    bad "失败探测的处理不对" "HTTP=$CODE ok=$ROK"
  fi
else
  printf '  - %-46s 跳过（内网目标被 SSRF 拦截）\n' "失败探测"
fi

kill "$TARGET_PID" 2>/dev/null

echo ""
echo "======================================"
echo "通过 $PASS 项，失败 $FAIL 项"
[ "$FAIL" -eq 0 ] || exit 1
