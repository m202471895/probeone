#!/usr/bin/env bash
# P7 冒烟测试：验证 HTTP 路由、鉴权边界、公开接口不泄漏敏感信息。
#
# 用法：先启动服务端，再跑本脚本
#   PROBEONE_BASE=http://127.0.0.1:8010 ./scripts/smoke.sh
#
# 注意：必须 --noproxy '*'，否则 curl 会走系统代理，
# 本地地址被代理拦下表现为 502 Connection refused。

set -uo pipefail

BASE="${PROBEONE_BASE:-http://127.0.0.1:8010}"
PASS=0
FAIL=0

# check <期望码> <说明> <curl 参数...>
check() {
  local want="$1" desc="$2"; shift 2
  local code
  code=$(curl -s --noproxy '*' -o /tmp/smoke_body.json -w '%{http_code}' "$@")
  if [ "$code" = "$want" ]; then
    printf '  ✓ %-42s %s\n' "$desc" "$code"
    PASS=$((PASS+1))
  else
    printf '  ✗ %-42s 期望 %s 实际 %s\n' "$desc" "$want" "$code"
    printf '     响应: %s\n' "$(head -c 200 /tmp/smoke_body.json)"
    FAIL=$((FAIL+1))
  fi
}

# body_field <json路径>：从上次响应里取值
body_field() {
  python3 -c "
import json,sys
try:
    d=json.load(open('/tmp/smoke_body.json'))
    for k in '$1'.split('.'):
        d = d[k] if isinstance(d,dict) else None
        if d is None: break
    print(d)
except Exception:
    print('PARSE_ERROR')
" 2>/dev/null
}

echo "=== 1. 健康检查（免鉴权）==="
check 200 "GET /healthz"            "$BASE/healthz"
check 200 "GET /ready"              "$BASE/ready"
check 200 "GET /api/version"        "$BASE/api/version"

echo ""
echo "=== 2. 公开接口（免鉴权，必须能访问）==="
check 200 "GET /api/status/summary"  "$BASE/api/status/summary"
check 200 "GET /api/status/monitors" "$BASE/api/status/monitors"
check 200 "GET /api/status/nodes"    "$BASE/api/status/nodes"

echo ""
echo "=== 3. 鉴权边界（无令牌必须 401，这是安全断言）==="
check 401 "GET  /api/nodes"          "$BASE/api/nodes"
check 401 "GET  /api/monitors"       "$BASE/api/monitors"
check 401 "GET  /api/alerts"         "$BASE/api/alerts"
check 401 "GET  /api/users"          "$BASE/api/users"
check 401 "GET  /api/visibility"     "$BASE/api/visibility"
check 401 "GET  /api/audit-logs"     "$BASE/api/audit-logs"
check 401 "GET  /api/auth/me"        "$BASE/api/auth/me"

echo ""
echo "=== 4. 伪造令牌必须 401 ==="
check 401 "GET /api/nodes  伪造 Bearer" \
  -H "Authorization: Bearer forged-token-aaaaaaaaaaaaaaaaaaaa" "$BASE/api/nodes"
check 401 "GET /api/nodes  空 Bearer" \
  -H "Authorization: Bearer " "$BASE/api/nodes"

echo ""
echo "=== 5. 登录失败不应是 500（防用户名枚举）==="
check 401 "POST /api/auth/login 错误密码" \
  -X POST -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"definitely-wrong"}' \
  "$BASE/api/auth/login"

echo ""
echo "=== 6. 输入校验（400 而非 500）==="
check 400 "POST /api/auth/login 空请求体" \
  -X POST -H "Content-Type: application/json" -d '{}' \
  "$BASE/api/auth/login"
check 400 "POST /api/auth/login 非法 JSON" \
  -X POST -H "Content-Type: application/json" -d '{bad json' \
  "$BASE/api/auth/login"

echo ""
echo "=== 7. 公开接口不得泄漏敏感信息 ==="
curl -s --noproxy '*' -o /tmp/smoke_pub.json "$BASE/api/status/monitors" 2>/dev/null
LEAK=$(python3 - <<'PY'
import json, re, sys
try:
    raw = open('/tmp/smoke_pub.json').read()
except Exception:
    print("READ_FAIL"); sys.exit()

problems = []
# IP 地址
ips = [x for x in re.findall(r'\b(?:\d{1,3}\.){3}\d{1,3}\b', raw)]
if ips:
    problems.append(f"泄漏 IP: {sorted(set(ips))[:3]}")
# 凭据类字段名
for bad in ('agent_secret', 'password_hash', 'token_hash', 'internal_ip',
            'mac_address', 'client_secret', 'target'):
    if f'"{bad}"' in raw:
        problems.append(f"出现禁露字段: {bad}")
if problems:
    print('; '.join(problems))
else:
    print("CLEAN")
PY
)
if [ "$LEAK" = "CLEAN" ]; then
  printf '  ✓ %-42s\n' "公开接口无敏感信息"
  PASS=$((PASS+1))
else
  printf '  ✗ %-42s %s\n' "公开接口泄漏" "$LEAK"
  FAIL=$((FAIL+1))
fi

echo ""
echo "=== 8. 响应头安全项 ==="
HDRS=$(curl -s --noproxy '*' -D - -o /dev/null "$BASE/healthz" 2>/dev/null)
for h in "X-Content-Type-Options" "X-Frame-Options" "Referrer-Policy"; do
  if echo "$HDRS" | grep -qi "$h"; then
    printf '  ✓ %-42s\n' "$h"
    PASS=$((PASS+1))
  else
    printf '  ✗ %-42s 缺失\n' "$h"
    FAIL=$((FAIL+1))
  fi
done

echo ""
echo "======================================"
echo "通过 $PASS 项，失败 $FAIL 项"
[ "$FAIL" -eq 0 ] || exit 1
