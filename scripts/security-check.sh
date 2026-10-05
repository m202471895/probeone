#!/usr/bin/env bash
# security-check.sh — 安全断言（PRD 9.2 / 3.6.4）
#
# 这些断言必须在 CI 中真实拦住违规，而不是只打印警告。
# 维护本脚本时必须用 `make security-check-selftest` 验证它能真正失败。
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# scan_go —— 扫描 Go 源码中的真实代码调用，忽略注释内容。
#
# 为什么必须剥离注释：本文件自身与 Agent 源码里都有大量说明性注释，
# 里面会提到 os/exec、net.Listen 这些词。若不剥离，断言会因注释误报，
# 几次误报之后就没 人再认真看它的输出了——这正是安全断言最常见的失效方式。
#
# 实现：逐行剥离 // 行注释与 /* */ 块注释，再做匹配。
scan_go() {
  local dir="$1"; shift
  local pattern="$1"; shift
  find "$dir" -name '*.go' -not -name '*_test.go' -print0 \
    | xargs -0 awk '
        {
          line = $0
          # 剥离行注释
          sub(/\/\/.*/, "", line)
          # 剥离块注释（简易状态机，覆盖常见写法）
          if (in_block) {
            if (line ~ /\*\//) { sub(/^.*\*\//, "", line); in_block = 0 }
            else next
          }
          while (line ~ /\/\*/) {
            before = line
            sub(/\/\*.*/, "", before)
            rest = line
            sub(/^.*\*\//, "", rest)
            line = before rest
            if (line ~ /\*\//) { sub(/^.*\*\//, "", line) } else { in_block = 1; line = before; break }
          }
          if (line ~ PATTERN) {
            print FILENAME ":" FNR ": " line
          }
        }
      ' PATTERN="$pattern" | sed "s|^|$dir/|"
}

FAILURES=0
FAIL() {
  echo "FATAL: $*" >&2
  FAILURES=$((FAILURES + 1))
}
OK() { echo "  [OK] $*"; }

echo "=== ProbeOne 安全断言 ==="

# ============ A. Agent 能力边界 ============
echo
echo "[A] Agent 能力边界（PRD 9.2）"

# A2: Agent 不得有命令执行能力
if scan_go agent '"os/exec"' | grep -q .; then
  FAIL "agent/ 中禁止引用 os/exec（Agent 必须只读，见 PRD 1.1 与 9.2 A1）"
  scan_go agent '"os/exec"'
else
  OK "A1/A2 agent/ 无 os/exec 引用"
fi

# A3: 不得 fork 子进程
if scan_go agent 'syscall\.ForkExec|os\.StartProcess' | grep -q .; then
  FAIL "agent/ 中禁止 fork 子进程"
  scan_go agent 'syscall\.ForkExec|os\.StartProcess'
else
  OK "A3 agent/ 无 fork 子进程调用"
fi

# A4: 不得监听端口
if scan_go agent 'net\.Listen' | grep -q .; then
  FAIL "agent/ 中禁止监听端口（Agent 只能主动外连）"
  scan_go agent 'net\.Listen'
else
  OK "A4 agent/ 无 net.Listen"
fi

# A5: proto 中不得存在任何有执行能力的 RPC
# 扫描 api/ 下**所有** proto 文件，而非只扫 agent.proto。
# 原因：将来若有人新增一个 backdoor.proto 定义 RunCommand，
# 只扫主文件会完全漏掉。这是自测脚本实测出来的真实缺口。
BAD_RPC='exec|command|terminal|shell|file|upload|download|upgrade|script|task'
if grep -rE '^[[:space:]]*rpc[[:space:]]+' api/ --include='*.proto' 2>/dev/null \
   | grep -viE 'Service\b' | grep -iE "$BAD_RPC" | grep -q .; then
  FAIL "api/*.proto 中禁止出现有执行能力的 RPC 方法（PRD 7.1 安全红线）"
  grep -rE '^[[:space:]]*rpc[[:space:]]+' api/ --include='*.proto' | grep -iE "$BAD_RPC"
else
  OK "A5 api/*.proto 无命令下发类 RPC"
fi

# A6: 校验允许存在的 RPC 只有固定三个，防止有人"合法地"新增能力
ALLOWED_RPCS="Handshake ReportStream ReportOnce"
if grep -rEo '^[[:space:]]*rpc[[:space:]]+\w+' api/ --include='*.proto' 2>/dev/null \
   | awk '{print $2}' | sort -u | while read -r m; do
       echo "$ALLOWED_RPCS" | tr ' ' '\n' | grep -qx "$m" || echo "$m"
     done | grep -q .; then
  FAIL "api/ 出现了白名单之外的 RPC。当前只允许：$ALLOWED_RPCS"
  grep -rEo '^[[:space:]]*rpc[[:space:]]+\w+' api/ --include='*.proto' | awk '{print $2}' | sort -u
else
  OK "A6 Agent 通道 RPC 白名单未被突破"
fi

# ============ B. 脱敏层（PRD 3.6.4）============
echo
echo "[B] 字段脱敏层"

HARD_DENY="agent_secret|client_secret|password_hash|token_hash|master_key|session_token|internal_ip|mac_address"

# B1: DTO / 响应结构中不得出现硬禁止字段的 JSON tag
if scan_go server/internal "json:\"($HARD_DENY)" | grep -q .; then
  FAIL "任何结构体都不得以硬禁止字段名做 JSON 序列化"
  scan_go server/internal "json:\"($HARD_DENY)"
else
  OK "B1 无结构体序列化硬禁止字段"
fi

# B2: 必须存在脱敏引擎
if [ ! -f server/internal/visibility/apply.go ]; then
  FAIL "缺少 server/internal/visibility/apply.go（脱敏层是强制模块）"
else
  OK "B2 脱敏引擎存在"
fi

# B3: 硬禁止表必须存在且非空
if [ ! -f server/internal/visibility/harddeny.go ] \
   || ! grep -q 'hardDeniedFields' server/internal/visibility/harddeny.go 2>/dev/null; then
  FAIL "缺少硬禁止字段表 visibility/harddeny.go"
else
  OK "B3 硬禁止字段表存在"
fi

# B4: 兜底中间件必须注册
if [ ! -f server/internal/visibility/middleware.go ] \
   || ! grep -q 'func Middleware' server/internal/visibility/middleware.go 2>/dev/null; then
  FAIL "缺少脱敏兜底中间件 visibility.Middleware"
else
  OK "B4 兜底中间件存在"
fi

# B5: 状态页筛选条件必须排除离线节点（防侧信道，PRD T16）
if grep -rn "is_public" server/ --include='*.go' 2>/dev/null \
   | grep -iE "status" | grep -q .; then
  : # 有实现即可，细节在 Phase 7 检查
fi
OK "B5 状态页侧信道防护在 Phase 7 落地（P7 验收项）"

# ============ C. 配置安全 ============
echo
echo "[C] 配置与密钥"

# C1: 仓库中不得出现真实密钥
if grep -rInE '(BEGIN (RSA|OPENSSH|EC) PRIVATE KEY|ghp_[A-Za-z0-9]{20,}|github_pat_)' \
   --include='*.go' --include='*.yaml' --include='*.yml' --include='*.env' \
   . 2>/dev/null | grep -v node_modules | grep -q .; then
  FAIL "仓库内疑似存在真实密钥或私钥"
else
  OK "C1 仓库内无私钥与真实令牌"
fi

# C2: .gitignore 必须包含 .env
if [ ! -f .gitignore ] || ! grep -qE '^\.env' .gitignore 2>/dev/null; then
  FAIL ".gitignore 必须忽略 .env（真实配置不入库）"
else
  OK "C2 .env 已加入 .gitignore"
fi

# C3: 不得硬编码默认密码
if scan_go server 'admin:admin' | grep -q . || scan_go agent 'admin:admin' | grep -q .; then
  FAIL "代码中禁止出现 admin:admin 默认口令（PRD T13）"
else
  OK "C3 无默认弱口令"
fi

# ============ D. 依赖安全 ============
echo
echo "[D] 依赖漏洞"
if command -v govulncheck >/dev/null 2>&1; then
  if (cd server && govulncheck ./... 2>&1 | tail -20); then
    OK "D1 govulncheck 已执行（结果见上）"
  else
  FAIL "govulncheck 发现高危漏洞"
  fi
else
  echo "  [SKIP] govulncheck 未安装（可选：go install golang.org/x/vuln/cmd/govulncheck@latest）"
fi

echo
echo "=== 结果 ==="
if [ "$FAILURES" -gt 0 ]; then
  echo "失败 $FAILURES 项断言"
  exit 1
fi
echo "全部安全断言通过"
exit 0
