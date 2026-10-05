#!/usr/bin/env bash
# push.sh —— 带重试的推送脚本
#
# 为什么需要重试：本地出网代理对 git 的 CONNECT 隧道处理不稳定，
# 会间歇性返回 502 或 "HTTP2 framing layer" 错误。
# 这是环境问题不是仓库问题，重试即可绕过。
#
# 用法：./scripts/push.sh [分支名]
set -uo pipefail

BRANCH="${1:-$(git rev-parse --abbrev-ref HEAD)}"
MAX_RETRY="${PUSH_MAX_RETRY:-10}"

cd "$(git rev-parse --show-toplevel)"

# HTTP/1.1：代理对 HTTP/2 over CONNECT 隧道支持不好
git config --local http.version HTTP/1.1

for i in $(seq 1 "$MAX_RETRY"); do
  if GIT_TERMINAL_PROMPT=0 git push origin "$BRANCH" 2>/dev/null; then
    echo "✓ 推送成功（第 $i 次尝试）"
    exit 0
  fi
  echo "第 $i/$MAX_RETRY 次失败，3 秒后重试..."
  sleep 3
done

echo "✗ 推送失败：重试 $MAX_RETRY 次仍未成功" >&2
echo "  可检查：网络代理是否正常、PAT 是否有效" >&2
exit 1
