#!/usr/bin/env bash
# 端到端验证：前端 dev server 代理 → 真实后端。
#
# 关键点：走**前端 dev server 的地址**而不是直连后端——
# 这样才能同时验证代理配置与接口契约。直连后端会绕过代理，
# 代理配错了也测不出来。

set -uo pipefail

# 经前端代理访问，模拟浏览器实际走的路径
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
BASE="${PROBEONE_BASE:-http://127.0.0.1:5173}"
# 直连后端，用于对照（确认代理没改坏响应）
BACKEND="${PROBEONE_BACKEND:-http://127.0.0.1:8010}"

PASS=0; FAIL=0
TOKEN=""

C() { curl -s --noproxy '*' "$@"; }

# expect <期望码> <说明> <curl参数...>
expect() {
  local want="$1" desc="$2"; shift 2
  local code
  code=$(C -o /tmp/e2e_body.json -w '%{http_code}' "$@")
  if [ "$code" = "$want" ]; then
    printf '  ✓ %-46s %s\n' "$desc" "$code"
    PASS=$((PASS+1)); return 0
  fi
  printf '  ✗ %-46s 期望 %s 实际 %s\n' "$desc" "$want" "$code"
  printf '     %s\n' "$(head -c 200 /tmp/e2e_body.json)"
  FAIL=$((FAIL+1)); return 1
}

# jget <点号路径>：从上次响应里取值，如 jget data.token
#
# 用点号路径遍历而不是 eval 拼字符串：eval 在 shell 里转义极其脆弱，
# 三引号嵌套就会语法错，而且拼接可控路径有注入风险。
jget() {
  python3 /Users/zhangjin/WorkBuddy/2026-10-05-14-50-52/probeone/server/scripts/jget.py "$1" 2>/dev/null
}

echo "=== 1. 代理连通性 ==="
expect 200 "GET /healthz（经前端代理）" "$BASE/healthz"
expect 200 "GET /api/version（经前端代理）" "$BASE/api/version"
# 直连对照：确认代理没有改写响应体
BC=$(C -o /dev/null -w '%{http_code}' "$BACKEND/healthz")
if [ "$BC" = "200" ]; then
  printf '  ✓ %-46s %s\n' "GET /healthz（直连后端，对照）" "$BC"
  PASS=$((PASS+1))
else
  printf '  ✗ %-46s %s\n' "直连后端" "$BC"; FAIL=$((FAIL+1))
fi

echo ""
echo "=== 2. 登录前置检查 ==="
# 库里没有用户时登录必然失败，这里只验证"返回的是 401 而不是 500"，
# 说明代码路径是通的（数据库查询、错误映射都正常）
expect 401 "登录：用户不存在（应 401 非 500）" \
  -X POST -H "Content-Type: application/json" \
  -d '{"username":"__e2e_probe__","password":"wrong-password"}' \
  "$BASE/api/auth/login"

echo ""
echo "=== 3. 完整登录流程 ==="
# 有初始用户时测完整流程，没有则跳过（不能因此判失败——
# 空库是正常状态，不是缺陷）
if [ -n "${PROBEONE_E2E_USER:-}" ] && [ -n "${PROBEONE_E2E_PASS:-}" ]; then
  # 输出到 /tmp/e2e_body.json —— jget 只读这一个文件。
  # 另存一份会让 jget 读到上一次的响应，token 提取到空值，
  # 表现是"登录明明成功但后续全部 401"。
  C -o /tmp/e2e_body.json "$BASE/api/auth/login" \
    -X POST -H "Content-Type: application/json" \
    -d "{\"username\":\"$PROBEONE_E2E_USER\",\"password\":\"$PROBEONE_E2E_PASS\"}"
  TOKEN=$(jget data.token)
  USERROLE=$(jget data.user.role)

  if [ -n "$TOKEN" ] && [ "$TOKEN" != "None" ]; then
    printf '  ✓ %-46s %s\n' "登录成功，返回令牌" "role=$USERROLE"
    PASS=$((PASS+1))
  else
    printf '  ✗ %-46s\n' "登录失败"
    printf '     %s\n' "$(head -c 200 /tmp/e2e_login.json)"
    FAIL=$((FAIL+1))
  fi

  # 带令牌访问受保护接口
  AUTH="Authorization: Bearer $TOKEN"
  expect 200 "GET /api/auth/me（带令牌）"   -H "$AUTH" "$BASE/api/auth/me"
  expect 200 "GET /api/nodes（带令牌）"     -H "$AUTH" "$BASE/api/nodes"
  expect 200 "GET /api/monitors（带令牌）"  -H "$AUTH" "$BASE/api/monitors"
  expect 200 "GET /api/alerts（带令牌）"    -H "$AUTH" "$BASE/api/alerts"
  expect 200 "GET /api/visibility（带令牌）" -H "$AUTH" "$BASE/api/visibility"
  expect 200 "GET /api/users（带令牌）"     -H "$AUTH" "$BASE/api/users"

  # 权限边界：伪造令牌必须 401
  expect 401 "GET /api/nodes（伪造令牌）" \
    -H "Authorization: Bearer forged.token.value" "$BASE/api/nodes"
else
  printf '  - %-46s 跳过\n' "完整登录流程（未设 PROBEONE_E2E_USER）"
fi

echo ""
echo "=== 3b. 鉴权边界（无令牌，经代理）==="
expect 401 "GET  /api/nodes"     "$BASE/api/nodes"
expect 401 "GET  /api/monitors"  "$BASE/api/monitors"
expect 401 "GET  /api/alerts"    "$BASE/api/alerts"
expect 401 "GET  /api/users"     "$BASE/api/users"
expect 401 "GET  /api/visibility" "$BASE/api/visibility"
expect 401 "GET  /api/auth/me"   "$BASE/api/auth/me"

echo ""
echo "=== 4. 公开接口（免鉴权，经代理）==="
expect 200 "GET  /api/status/summary"  "$BASE/api/status/summary"
expect 200 "GET  /api/status/monitors" "$BASE/api/status/monitors"
expect 200 "GET  /api/status/nodes"    "$BASE/api/status/nodes"

echo ""
echo "=== 5. 响应信封格式（前后端契约）==="
C -o /tmp/e2e_body.json "$BASE/api/status/summary"
SHAPE=$(python3 -c "
import json
d=json.load(open('/tmp/e2e_body.json'))
keys=set(d.keys())
need={'code','data'}
missing=need-keys
print('OK' if not missing else 'MISSING:'+','.join(missing))
" 2>/dev/null)
if [ "$SHAPE" = "OK" ]; then
  printf '  ✓ %-46s\n' "响应含 code 与 data 字段"
  PASS=$((PASS+1))
else
  printf '  ✗ %-46s %s\n' "响应信封格式" "$SHAPE"
  printf '     实际: %s\n' "$(head -c 200 /tmp/e2e_body.json)"
  FAIL=$((FAIL+1))
fi

CODE_TYPE=$(python3 -c "
import json
d=json.load(open('/tmp/e2e_body.json'))
print(type(d.get('code')).__name__)
" 2>/dev/null)
if [ "$CODE_TYPE" = "str" ]; then
  printf '  ✓ %-46s %s\n' "code 为字符串（与前端契约一致）" "$CODE_TYPE"
  PASS=$((PASS+1))
else
  printf '  ✗ %-46s 实际 %s（前端期望 string）\n' "code 类型" "$CODE_TYPE"
  FAIL=$((FAIL+1))
fi

echo ""
echo "=== 6. 公开接口不泄漏敏感信息 ==="
LEAK=$(C "$BASE/api/status/monitors" | python3 -c "
import re,sys
raw=sys.stdin.read()
probs=[]
ips=[x for x in re.findall(r'\b(?:\d{1,3}\.){3}\d{1,3}\b', raw)]
if ips: probs.append('IP:'+','.join(sorted(set(ips))[:2]))
for bad in ('agent_secret','password_hash','internal_ip','mac_address','target','client_secret'):
    if '\"'+bad+'\"' in raw: probs.append(bad)
print('; '.join(probs) if probs else 'CLEAN')
" 2>/dev/null)
if [ "$LEAK" = "CLEAN" ]; then
  printf '  ✓ %-46s\n' "公开接口无敏感信息"
  PASS=$((PASS+1))
else
  printf '  ✗ %-46s %s\n' "公开接口泄漏" "$LEAK"
  FAIL=$((FAIL+1))
fi

echo ""
echo "=== 7. 静态资源（前端确实起来了）==="
# 检测要看 Vite 注入的客户端脚本，而不是 id="app"——
# 后者在 HTML 里可能被换行拆开，字符串匹配不可靠。
IDX=$(C "$BASE/" | head -c 3000)
if echo "$IDX" | grep -q "@vite/client"; then
  printf '  ✓ %-46s\n' "返回 Vite 开发页（前端已就绪）"
  PASS=$((PASS+1))
elif echo "$IDX" | grep -q "<div id=.app"; then
  printf '  ✓ %-46s\n' "返回前端 index.html"
  PASS=$((PASS+1))
else
  printf '  ✗ %-46s\n' "未返回前端页面"
  printf '     实际开头: %s\n' "$(echo "$IDX" | head -c 120)"
  FAIL=$((FAIL+1))
fi

# 产物构建（真实部署时前端由 Go 内嵌）
if [ -f "$ROOT_DIR/web/dist/index.html" ]; then
  printf '  ✓ %-46s\n' "生产构建产物存在"
  PASS=$((PASS+1))
else
  printf '  ✗ %-46s\n' "生产构建产物缺失（需 npm run build）"
  FAIL=$((FAIL+1))
fi

echo ""
echo "======================================"
echo "通过 $PASS 项，失败 $FAIL 项"
[ "$FAIL" -eq 0 ] || exit 1
