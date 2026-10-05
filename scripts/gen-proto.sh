#!/usr/bin/env bash
# gen-proto.sh —— 生成 protobuf Go 代码
#
# 前置：protoc + protoc-gen-go + protoc-gen-go-grpc
# 安装：
#   protoc                https://grpc.io/docs/protoc-installation/
#   protoc-gen-go         go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#   protoc-gen-go-grpc    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

OUT_DIR="api"
MODULE="github.com/m202471895/probeone/api"

for tool in protoc protoc-gen-go protoc-gen-go-grpc; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "错误：未找到 $tool" >&2
    case "$tool" in
      protoc)
        echo "  macOS:  brew install protobuf" >&2
        echo "  Debian: apt install -y protobuf-compiler" >&2
        ;;
      *)
        echo "  go install google.golang.org/protobuf/cmd/protoc-gen-go@latest" >&2
        echo "  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest" >&2
        ;;
    esac
    exit 1
  fi
done

echo "→ 生成 protobuf Go 代码"
protoc \
  --proto_path=api \
  --go_out="$OUT_DIR" \
  --go_opt=paths=source_relative \
  --go_opt=Magent.proto="${MODULE}/agent/v1" \
  --go-grpc_out="$OUT_DIR" \
  --go-grpc_opt=paths=source_relative \
  --go-grpc_opt=Magent.proto="${MODULE}/agent/v1" \
  api/agent/v1/agent.proto

echo "→ 校验生成结果"
if [ ! -f api/agent/v1/agent.pb.go ]; then
  echo "错误：未生成 agent.pb.go" >&2
  exit 1
fi

# 协议安全自检：生成代码后立刻确认没有命令下发方法。
# 把红线检查放在生成环节，而不是等人工 review——
# 这是防止协议能力被无意扩大的最有效位置。
echo "→ 协议安全自检"
if grep -E '^[[:space:]]*rpc[[:space:]]+' api/agent/v1/agent.proto \
   | grep -iE 'exec|command|terminal|shell|file|upload|download|upgrade|script'; then
  echo "错误：协议中出现有执行能力的 RPC，违反 PRD 7.1 安全红线" >&2
  exit 1
fi

echo "完成。生成文件："
ls -1 api/agent/v1/*.pb.go 2>/dev/null || true
