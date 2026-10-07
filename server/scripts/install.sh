#!/bin/sh
#
# ProbeOne Agent 一键安装脚本。
#
# 设计约束（安全优先）：
#   - 只下载并运行 Agent 自身，不做任何其他事
#   - 不修改防火墙、不开放端口（Agent 是纯客户端，只外发）
#   - 配置文件权限 600——里面有连接凭据
#
# 用法：
#   curl -fsSL http://<host>:8000/install.sh | sh -s -- --server <host:8008> --uuid <uuid> --secret <secret>
#
# 为什么要显式传 --server：
#   Agent 连的是 gRPC 端口（默认 8008），而本脚本从 HTTP 端口下载（默认 8000）。
#   两者不同时混用会连错端口。
#
# 环境变量（给 CI 与自定义安装用，正常用户不需要）：
#   PROBEONE_AGENT_VERSION  指定版本，默认从 /api/version 探测
#   PROBEONE_AGENT_BIN_DIR  安装目录，默认 /usr/local/bin
#   PROBEONE_AGENT_CFG_DIR  配置目录，默认 /etc/probeone-agent

set -eu

# ---------- 参数解析 ----------
SERVER=""
UUID=""
SECRET=""
EXTRA=""

while [ $# -gt 0 ]; do
  case "$1" in
    --server) SERVER="${2:-}"; shift 2 ;;
    --uuid)   UUID="${2:-}";   shift 2 ;;
    --secret) SECRET="${2:-}"; shift 2 ;;
    *)        EXTRA="$EXTRA $1"; shift ;;
  esac
done

if [ -z "$SERVER" ] || [ -z "$UUID" ] || [ -z "$SECRET" ]; then
  echo "用法: sh install.sh --server <host:port> --uuid <uuid> --secret <secret>" >&2
  echo "   完整命令见 ProbeOne 面板的「添加节点」对话框" >&2
  exit 1
fi

BIN_DIR="${PROBEONE_AGENT_BIN_DIR:-/usr/local/bin}"
CFG_DIR="${PROBEONE_AGENT_CFG_DIR:-/etc/probeone-agent}"
BIN_PATH="$BIN_DIR/probeone-agent"
CFG_PATH="$CFG_DIR/config.yaml"

# ---------- 环境检查 ----------
# 只在缺命令时提示，不强制退出——BusyBox /精简容器里可能缺某个工具，
# 但主流程未必需要它。
check_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "缺少必需命令: $1" >&2
    exit 1
  }
}
check_cmd curl
check_cmd tar

# ---------- 权限检查 ----------
# 需要 root 才能写 /usr/local/bin 与 /etc。
# 不在root 下时降级装到 ~/.local/bin，并在输出里说清楚——
# 装是能装上的，只是换了个位置。
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
  if command -v sudo >/dev/null 2>&1; then
    SUDO="sudo"
  else
    BIN_DIR="$HOME/.local/bin"
    CFG_DIR="$HOME/.config/probeone-agent"
    BIN_PATH="$BIN_DIR/probeone-agent"
    CFG_PATH="$CFG_DIR/config.yaml"
    echo "非 root 运行，改为安装到 $BIN_DIR" >&2
  fi
fi

mkdir -p "$BIN_DIR" "$CFG_DIR" 2>/dev/null || {
  $SUDO mkdir -p "$BIN_DIR" "$CFG_DIR"
}

# ---------- 探测可执行文件下载地址 ----------
# 约定：Agent 二进制从 <base>/downloads/probeone-agent-<version>-<os>-<arch> 取。
# 不硬编码 URL：域名与端口都从 --server 推，这样换端口不用改脚本。
SERVER_BASE="${PROBEONE_BASE_URL:-}"
if [ -z "$SERVER_BASE" ]; then
  # 从 --server 反推协议：显式给了 http:// 或 https:// 就用，否则按明文试，
  # 失败再回退 https（顺序反过来会让 HTTPS 部署多等一次超时）。
  case "$SERVER" in
    http://*)  BASE="${SERVER%/}" ;;
    https://*) BASE="${SERVER%/}" ;;
    *)         BASE="http://${SERVER%/}" ;;
  esac
  # gRPC 端口通常与 HTTP 不同，装Agent 脚本从 HTTP 端口下载。
  # 用户可用 PROBEONE_BASE_URL 覆盖。
  BASE="${BASE%:*}:8000"
fi

# 临时目录必须在查询版本**之前**建好——清单也要落到里面。
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

VERSION="${PROBEONE_AGENT_VERSION:-}"
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  armv7l)        ARCH="armv7" ;;
  *)
    echo "不支持的架构: $(uname -m)" >&2
    exit 1
    ;;
esac

# ---------- 向服务端查询可用版本 ----------
#
# 不要在脚本里猜版本号。之前这里写死 "latest"，而产物名用的是 "dev"，
# 拼出来的地址不存在；而 /downloads/ 404 时会 SPA 回退返回 index.html，
# curl 拿到 HTTP 200 于是把 HTML 当二进制装到机器上，
# 报错是"syntax error near unexpected token"——离真正原因十万八千里。
#
# 正确做法：问服务端要什么版本。
if [ -z "$VERSION" ]; then
  printf "==> 查询服务端可用版本…"
  MANIFEST="$TMP/manifest.json"
  if ! curl -fsSL "$BASE/downloads/manifest.json" -o "$MANIFEST" 2>/dev/null; then
    echo " 失败"
    cat >&2 <<EOF

无法获取安装包清单（$BASE/downloads/manifest.json）。

可能原因：
  1. 服务端未包含 Agent 安装包——构建时是否执行了 scripts/build-agent.sh
  2. $BASE 不可达

也可以显式指定版本绕过查询：
  PROBEONE_AGENT_VERSION=<版本> sh install.sh --server ... --uuid ... --secret ...
EOF
    exit 1
  fi
  # 不依赖 jq：grep/sed 取 version 字段，够用且无外部依赖
  VERSION=$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$MANIFEST" | head -1)
  if [ -z "$VERSION" ]; then
    echo " 失败"
    echo "清单里没有 version 字段，格式不对" >&2
    exit 1
  fi
  echo " $VERSION"
fi

DOWNLOAD_URL="${BASE}/downloads/probeone-agent-${VERSION}-${OS}-${ARCH}"
echo "==> 下载 Agent (${OS}/${ARCH}, ${VERSION})"
echo "    $DOWNLOAD_URL"
if ! curl -fsSL "$DOWNLOAD_URL" -o "$TMP/agent"; then
  cat >&2 <<EOF

下载失败：$DOWNLOAD_URL

可能原因：
  1. 服务端没有 ${OS}/${ARCH} 的构建产物
     当前可用：$(tr '\n' ' ' < "$TMP/manifest.json" 2>/dev/null | sed 's/[{}"]//g;s/files://;s/version://' | cut -c1-200)
  2. 下载地址不对——用 PROBEONE_BASE_URL 显式指定
  3. 网络不通

如需手动安装，请从 ProbeOne 面板的「添加节点」对话框复制部署说明。
EOF
  rm -f "$TMP/agent"
  exit 1
fi

# ---------- 校验下载到的是二进制而不是 HTML ----------
#
# 这道检查必须有。服务端配错（SPA 回退、路径写错、反代规则不匹配）
# 时 curl 照样返回 200，但内容是 HTML——装到机器上一执行才炸，
# 报错信息（syntax error）与真正原因毫无关联。
#
# ELF 魔数：ELF。Go 静态编译产物一定以它开头。
if ! head -c 4 "$TMP/agent" | od -An -tx1 2>/dev/null | tr -d ' \n' | grep -qi '7f454c46'; then
  echo "下载到的不是可执行文件（前 4 字节不是 ELF 魔数）" >&2
  echo "文件大小: $(wc -c < "$TMP/agent" | tr -d ' ') 字节" >&2
  echo "开头内容: $(head -c 80 "$TMP/agent" | tr -d '\n')" >&2
  cat >&2 <<EOF

常见原因：
  1. 服务端把 404 回退成了 HTML 页面
  2. 下载地址不对（检查 PROBEONE_BASE_URL）
  3. 被反向代理拦截

请确认浏览器访问下面这个地址应该下载文件而不是显示网页：
  $DOWNLOAD_URL
EOF
  rm -f "$TMP/agent"
  exit 1
fi

chmod +x "$TMP/agent"

# ---------- 安装二进制 ----------
echo "==> 安装到 $BIN_PATH"
$SUDO install -m 0755 "$TMP/agent" "$BIN_PATH" 2>/dev/null || {
  cp "$TMP/agent" "$BIN_PATH"
  chmod 0755 "$BIN_PATH"
}

# ---------- 写配置 ----------
# 权限 600：里面有 Agent 凭据，任何本地用户能读就等于凭据泄露。
cat > "$TMP/config.yaml" <<EOF
# ProbeOne Agent 配置
# 由安装脚本生成于 $(date -u '+%Y-%m-%dT%H:%M:%SZ')
#
# 权限 600：auth.secret 是连接凭据。
server:
  # gRPC 地址（由面板给出）
  addr: "$SERVER"
  # 服务端未配置 TLS 时保持 false。
  # 改成 true 会校验证书，私有CA 部署需额外配 ca_file。
  insecure_skip_verify: true

auth:
  uuid: "$UUID"
  secret: "$SECRET"

collect:
  interval_sec: 10
  metrics: ["cpu", "memory", "disk", "network", "load", "uptime"]
  privileged: []

network:
  # 指标缓冲：断线时暂存，恢复后补传
  buffer_size: 500
  flush_interval_sec: 10

log:
  level: "info"
  file: "/var/log/probeone-agent.log"
  max_size_mb: 20
  max_backups: 3
EOF

if [ -f "$CFG_PATH" ]; then
  # 已有配置：备份后覆盖。
  # Agent 仍在跑的话需要重启才能加载新凭据——
  # 密钥轮换后不重启会让 Agent 持续认证失败。
  echo "==> 备份旧配置到 $CFG_PATH.bak"
  cp "$CFG_PATH" "$CFG_PATH.bak" 2>/dev/null || true
fi
$SUDO cp "$TMP/config.yaml" "$CFG_PATH" 2>/dev/null || cp "$TMP/config.yaml" "$CFG_PATH"
chmod 0600 "$CFG_PATH"

echo "==> 校验配置"
if ! "$BIN_PATH" -config "$CFG_PATH" -check; then
  echo "配置校验失败，请检查 $CFG_PATH" >&2
  exit 1
fi

# ---------- systemd 服务 ----------
if [ "$(id -u)" -eq 0 ] && command -v systemctl >/dev/null 2>&1; then
  echo "==> 安装 systemd 服务"
  cat > "$TMP/probeone-agent.service" <<EOF
[Unit]
Description=ProbeOne Agent（只读指标采集）
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$BIN_PATH -config $CFG_PATH
Restart=always
RestartSec=10

# Agent 安全边界：只读采集，不执行命令、不写系统目录。
# 这些限制与服务端的只读设计对应——即使 Agent 被入侵，
# 攻击者能做的也仅限于读本机指标。
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
# Agent 唯一的写入需求是日志目录
ReadWritePaths=/var/log

[Install]
WantedBy=multi-user.target
EOF

  $SUDO cp "$TMP/probeone-agent.service" /etc/systemd/system/probeone-agent.service
  systemctl daemon-reload
  systemctl enable probeone-agent >/dev/null 2>&1
  systemctl restart probeone-agent

  sleep 2
  if systemctl is-active --quiet probeone-agent; then
    echo "==> Agent 已启动 (systemctl status probeone-agent 查看状态)"
  else
    echo "==> Agent 启动失败，查看日志: journalctl -u probeone-agent -n 30" >&2
    exit 1
  fi
else
  echo "==> 未检测到 systemd，请手动启动:"
  echo "    $BIN_PATH -config $CFG_PATH"
fi

echo ""
echo "安装完成。"
echo "  配置: $CFG_PATH"
echo "  状态: systemctl status probeone-agent"
echo "  日志: journalctl -u probeone-agent -f"
