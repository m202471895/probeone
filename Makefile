# ProbeOne Makefile
#
# 统一构建入口。所有 target 幂等，CI 与本地使用同一套命令。

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

# 版本信息注入
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

GO       ?= go
GOBIN    := $(shell $(GO) env GOPATH)/bin
BIN_DIR  := bin
LDFLAGS  := -s -w \
  -X github.com/m202471895/probeone/server/internal/util.Version=$(VERSION) \
  -X github.com/m202471895/probeone/server/internal/util.Commit=$(COMMIT) \
  -X github.com/m202471895/probeone/server/internal/util.BuildTime=$(BUILD_TIME)

.PHONY: help
help: ## 显示所有可用 target
	@echo "ProbeOne — 可用命令："
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

# ---------- 构建 ----------

.PHONY: build
build: build-server build-agent ## 构建服务端与 Agent

.PHONY: build-server
build-server: ## 构建服务端二进制（CGO 关闭，静态链接）
	@mkdir -p $(BIN_DIR)
	cd server && CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" \
		-o ../$(BIN_DIR)/probeone ./cmd/probeone
	@echo "→ $(BIN_DIR)/probeone"

.PHONY: build-agent
build-agent: ## 构建 Agent 二进制（必须 CGO_ENABLED=0，便于静态分发）
	@mkdir -p $(BIN_DIR)
	cd agent && CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w" \
		-o ../$(BIN_DIR)/probeone-agent ./cmd/agent
	@echo "→ $(BIN_DIR)/probeone-agent"

.PHONY: build-agent-all
build-agent-all: ## 交叉编译 Agent 到所有支持平台
	@mkdir -p $(BIN_DIR)/agent
	cd agent && for pair in \
		"linux amd64" "linux arm64" "darwin amd64" "darwin arm64" \
		"windows amd64" "windows arm64"; do \
		set -- $$pair; \
		echo "  building $$1/$$2"; \
		CGO_ENABLED=0 GOOS=$$1 GOARCH=$$2 $(GO) build -trimpath -ldflags="-s -w" \
			-o ../$(BIN_DIR)/agent/probeone-agent-$$1-$$2 ./cmd/agent || exit 1; \
	done
	@echo "→ $(BIN_DIR)/agent/"

# ---------- 依赖与代码生成 ----------

.PHONY: deps
deps: ## 下载 Go 依赖
	cd server && $(GO) mod download
	cd agent && $(GO) mod download

.PHONY: proto
proto: ## 生成 protobuf Go 代码
	@command -v protoc >/dev/null 2>&1 || { \
		echo "protoc 未安装。安装方式："; \
		echo "  macOS: brew install protobuf"; \
		echo "  Debian: apt install -y protobuf"; \
		echo "或用 buf：https://buf.build/docs/installation"; \
		exit 1; }
	./scripts/gen-proto.sh

# ---------- 质量检查 ----------

.PHONY: vet
vet: ## go vet 静态检查
	cd server && $(GO) vet ./...
	cd agent && $(GO) vet ./...

.PHONY: test
test: ## 运行全部单元测试
	cd server && $(GO) test -race -cover ./...
	cd agent && $(GO) test -race -cover ./...

.PHONY: security-check
security-check: ## 运行安全断言（PRD 9.2 / 3.6.4）
	@bash scripts/security-check.sh

.PHONY: vuln
vuln: ## 依赖漏洞扫描
	@command -v govulncheck >/dev/null 2>&1 || { \
		echo "安装 govulncheck："; \
		echo "  go install golang.org/x/vuln/cmd/govulncheck@latest"; \
		exit 1; }
	cd server && govulncheck ./...
	cd agent && govulncheck ./...

.PHONY: check
check: vet test security-check ## 提交前的完整检查

# ---------- 运行 ----------

.PHONY: migrate
migrate: ## 执行数据库迁移
	cd server && $(GO) run ./cmd/probeone --migrate-only

.PHONY: seed
seed: ## 灌入演示数据
	@bash scripts/seed-demo-data.sh

.PHONY: run-server
run-server: ## 本地启动服务端
	cd server && $(GO) run ./cmd/probeone

.PHONY: run-agent
run-agent: ## 本地启动 Agent
	cd agent && $(GO) run ./cmd/agent

.PHONY: smoke-test
smoke-test: ## 端到端冒烟测试
	@bash scripts/smoke-test.sh

# ---------- 清理 ----------

.PHONY: clean
clean: ## 清理构建产物
	rm -rf $(BIN_DIR)

.PHONY: distclean
distclean: clean ## 清理构建产物与依赖缓存
	cd server && $(GO) clean -modcache -testcache || true
	cd agent && $(GO) clean -modcache -testcache || true
