#!/usr/bin/env bash
# security-check-selftest.sh — 验证安全断言**能真正拦住**违规
#
# 为什么需要这个脚本：PRD 3.6.4 与 9.2 都明确要求断言必须能真正失败。
# 一个永远返回 0 的安全脚本比没有脚本更危险——它给人虚假的安全感。
# 因此每次修改 security-check.sh 后都必须跑这个自测。
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

TMPFILES=()
cleanup() {
  for f in "${TMPFILES[@]:-}"; do
    [ -n "$f" ] && rm -f "$f"
  done
}
trap cleanup EXIT

PASS=0
FAILED=0

# expect_blocked <描述> <写违规代码的文件路径> <违规内容>
expect_blocked() {
  local desc="$1" target="$2" content="$3"
  mkdir -p "$(dirname "$target")"
  printf '%s\n' "$content" > "$target"
  TMPFILES+=("$target")

  if bash scripts/security-check.sh >/dev/null 2>&1; then
    echo "  [失败] $desc —— 断言**没有**拦住违规代码"
    FAILED=$((FAILED + 1))
  else
    echo "  [通过] $desc —— 已被正确拦截"
    PASS=$((PASS + 1))
  fi
  rm -f "$target"
}

# expect_ignored <描述> <只含注释的代码> —— 断言不应误报
expect_ignored() {
  local desc="$1" target="$2" content="$3"
  mkdir -p "$(dirname "$target")"
  printf '%s\n' "$content" > "$target"
  TMPFILES+=("$target")

  if bash scripts/security-check.sh >/dev/null 2>&1; then
    echo "  [通过] $desc —— 未误报"
    PASS=$((PASS + 1))
  else
    echo "  [失败] $desc —— 误报！注释中提到关键词不应触发断言"
    FAILED=$((FAILED + 1))
  fi
  rm -f "$target"
}

echo "=== 安全断言自测 ==="
echo
echo "【1】必须被拦截的违规"
echo

expect_blocked "Agent 引用 os/exec" \
  "agent/internal/collect/evil_exec.go" \
  'package collect
import "os/exec"
var _ = exec.Command'

expect_blocked "Agent 监听端口" \
  "agent/internal/transport/evil_listen.go" \
  'package transport
import "net"
var _ = net.Listen'

expect_blocked "Agent fork 子进程" \
  "agent/internal/collect/evil_fork.go" \
  'package collect
import "syscall"
var _ = syscall.ForkExec'

expect_blocked "结构体序列化硬禁止字段" \
  "server/internal/model/evil_dto.go" \
  'package model
type BadDTO struct {
  AgentSecret string `json:"agent_secret"`
}'

expect_blocked "硬编码默认口令" \
  "server/internal/auth/evil_cred.go" \
  'package auth
const defaultCred = "admin:admin"'

expect_blocked "proto 出现命令下发 RPC" \
  "api/agent/v1/evil.proto" \
  'service BadSvc {
  rpc RunCommand(Req) returns (Resp);
}'

expect_blocked "proto 出现终端 RPC" \
  "api/agent/v1/evil_terminal.proto" \
  'service BadSvc {
  rpc OpenTerminal(Req) returns (Resp);
}'

echo
echo "【2】不应误报的情况"
echo

expect_ignored "注释中提到 net.Listen" \
  "agent/internal/collect/doc_only.go" \
  'package collect
// 本文件不监听端口，实现中不会出现 net.Listen。
// 这段说明特意提到 net.Listen 与 os/exec 两个关键词来验证断言不误报。
var _ = 1'

expect_ignored "空文件" \
  "agent/internal/collect/empty.go" \
  'package collect'

expect_ignored "注释中提到 admin:admin" \
  "server/internal/auth/doc_only.go" \
  'package auth
// 旧版本曾有 admin:admin 默认口令，现已移除，此处仅作历史说明。'

echo
echo "=== 结果 ==="
echo "通过 $PASS 项，失败 $FAILED 项"
if [ "$FAILED" -gt 0 ]; then
  echo "断言不可靠，必须修复后才能合入"
  exit 1
fi
echo "断言可靠：能拦住违规，也不因注释误报"
exit 0
