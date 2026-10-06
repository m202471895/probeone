#!/usr/bin/env bash
# 初始化数据库：创建首个 owner 用户。
#
# 为什么需要独立脚本而不是靠注册接口：
# PROBEONE_ALLOW_REGISTRATION 默认关闭（防止公网注册产生意外账号），
# 首个 owner 只能由部署者手动创建。
#
# 用法：
#   ./scripts/init-db.sh                     # 交互式（隐藏输入密码）
#   PROBEONE_DB_PATH=... ./scripts/init-db.sh   # 指定库
#   PROBEONE_INIT_PASSWORD=xxx ./scripts/init-db.sh  # 脚本化（CI 用）

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DB="${PROBEONE_DB_PATH:-$ROOT/data/dev.db}"
USERNAME="${PROBEONE_INIT_USER:-admin}"

# 密码强度与后端一致：至少 10 位，含大小写字母与数字。
# 这里只做前置检查，真正的校验在后端（auth.ValidatePasswordStrength）。
strong_enough() {
  [ ${#1} -ge 10 ] || return 1
  echo "$1" | grep -q '[a-z]' || return 1
  echo "$1" | grep -q '[A-Z]' || return 1
  echo "$1" | grep -q '[0-9]' || return 1
  return 0
}

if [ -z "${PROBEONE_INIT_PASSWORD:-}" ]; then
  if [ -t 0 ]; then
    read -r -s -p "设置 $USERNAME 的密码: " PW
    echo
    read -r -s -p "再输入一次: " PW2
    echo
    [ "$PW" = "$PW2" ] || { echo "两次输入不一致"; exit 1; }
  else
    echo "非交互环境请设置 PROBEONE_INIT_PASSWORD"
    exit 1
  fi
else
  PW="$PROBEONE_INIT_PASSWORD"
fi

if ! strong_enough "$PW"; then
  echo "密码强度不足：至少 10 位，且需含大写字母、小写字母与数字"
  exit 1
fi

# 走真实后端的密码哈希逻辑，不要自己实现一套 argon2id
# —— 两处实现不一致会导致登录永远失败。
PROBEONE_DB_PATH="$DB" \
PROBEONE_MASTER_KEY="${PROBEONE_MASTER_KEY:-dev-only-master-key-change-in-production}" \
go run ./cmd/inituser \
  -db "$DB" \
  -user "$USERNAME" \
  -pass "$PW" \
  -role owner

echo ""
echo "已创建 owner 用户：$USERNAME"
echo "登录地址：登录页 → 用户名 $USERNAME → 上面设置的密码"
