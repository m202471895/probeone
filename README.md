<div align="center">

# ProbeOne

**一个 Agent 只能上报数据、不能执行命令的探针系统。**

自托管的服务器监控 + 网站监控探针。Go 编写，单二进制部署，零外部依赖。

</div>

---

## 这个项目在解决什么问题

市面上的探针系统大多功能完备，但**Agent 往往具备远程命令执行能力**。

以 [nezhahq/nezha](https://github.com/nezhahq/nezha)（10.3k star）为例，它的 Agent 支持三类任务：

| 任务类型         | ID | 能力            |
| ------------ | -- | ------------- |
| Command      | 4  | 任意命令执行        |
| Terminal     | 8  | 交互式终端（PTY）    |
| File Manager | 11 | 文件浏览、上传、下载、删除 |

[Ontinue 的安全研究](https://www.ontinue.com/resource/nezha-the-monitoring-tool-thats-also-a-perfect-rat/) 披露了这个设计的代价：攻击者用上述任务可获得 root / NT AUTHORITY\SYSTEM shell，**而该二进制在 72 家杀毒厂商处检出率为零**。原因很清晰——

1. 单端口复用 HTTP + gRPC，Agent 通道与管理界面共享 8008，攻击面重叠
2. Agent 密钥**按用户共享**，一个 `client_secret` 覆盖该用户下所有 Agent
3. 默认凭据 `admin:admin`
4. 任务下发通道无二次授权，Dashboard 一旦被横向移动，Agent 立刻变成后门

**ProbeOne 的做法是把这个口子从协议层焊死**：

- `agent.proto` 中**不存在**任何有执行能力的 RPC。服务端只能接收数据。
- 端口物理隔离：gRPC 8008 / HTTP 8000 / metrics 9100，攻破 Web 端无法横向到 Agent 通道
- 每节点独立密钥 + argon2id 存储，绝不按用户共享
- 首次启动随机生成密码，代码中不存在 `admin:admin`
- CI 硬断言：`agent/` 目录里 `os/exec` 与 `net.Listen` 零引用，出现即构建失败

服务端被攻破的后果，止于"数据泄露 + 静默"，不能升级为"远程 shell"。

## 特性

- **只读 Agent** — 无命令下发能力，这是设计前提而非功能缺失
- **升配感知** — 硬件指纹比对，vCPU/内存/磁盘变化自动识别并留痕
- **网站监控** — HTTP / TCP / Ping / DNS / SSL 证书五类探针
- **分级脱敏** — 公网 IP 登录后可见，硬件规格可公开，配置化调整
- **预聚合存储** — 原始数据 7 天，之后按 1分钟/1小时/1天三级降采样
- **告警去重与防风暴** — 同一目标 30 分钟内不重复通知
- **单二进制** — Go 静态编译，无运行时依赖，`scp` 上去就能跑
- **三种部署方式** — Docker Compose / 单二进制 + systemd / 宝塔面板

## 与主流项目的差异

| 项目                                                     | 定位                   | 差异                                       |
| ------------------------------------------------------ | -------------------- | ---------------------------------------- |
| [nezha](https://github.com/nezhahq/nezha)              | 探针 + 运维（Web 终端、计划任务） | nezha 有命令下发能力；ProbeOne 从协议层移除            |
| [beszel](https://github.com/henrygd/beszel)            | 轻量服务器监控              | beszel 指标边界克制（值得学）；ProbeOne 额外做网站监控与升配感知 |
| [Uptime Kuma](https://github.com/louislam/uptime-kuma) | 网站可用性监控              | Uptime Kuma 只管网站；ProbeOne 把服务器与网站合到一处    |
| [netdata](https://github.com/netdata/netdata)          | 深度可观测                | netdata 每节点 100–350MB；ProbeOne 单二进制无依赖   |

不做贬低比较——上述项目都值得借鉴，ProbeOne 只是选了不同的安全边界。

## 快速开始

### Docker Compose

```bash
git clone https://github.com/m202471895/probeone.git
cd probeone
cp .env.example .env

# 生成主密钥（必填）
echo "PROBEONE_MASTER_KEY=$(openssl rand -base64 32)" >> .env

docker compose up -d
docker compose logs -f probeone    # 首次启动会打印随机管理员密码
```

访问 `http://<服务器IP>:8000`，用日志里的密码登录。

### 单二进制

```bash
make build-server
./bin/probeone
```

### 宝塔面板

见 [docs/deploy-baota.md](docs/deploy-baota.md)。

> **宝塔用户注意**：不配 `grpc_pass` 的 Nginx location 段，**Agent 会全部离线**。这是最高频的求助原因。

## Agent 安装

服务端添加节点后，面板会给出安装命令：

```bash
curl -fsSL https://probe.example.com/agent.sh | \
  sh -s -- --server probe.example.com:8008 --uuid <UUID> --secret <SECRET>
```

Agent 支持 Linux（amd64/arm64）、macOS、Windows。

## 架构

```
┌──────────────────────────────────────────────────┐
│          服务端（单机即可）8000 / 8008             │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐          │
│  │ HTTP/API │ │采集调度器│ │ 告警引擎 │          │
│  └────┬─────┘ └────┬─────┘ └────┬─────┘          │
│       └────────────┴────────────┘                │
│                存储（SQLite / PostgreSQL）        │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐          │
│  │gRPC:8008 │ │ Web 前端 │ │证书检查器│          │
│  └────▲─────┘ └──────────┘ └──────────┘          │
└───────┼──────────────────────────────────────────┘
        │ Agent 主动外连（gRPC 双向流，TLS）
        │ 心跳 30s | 指标 10s | 断线指数退避
┌───────┴──────────────────────────────────────────┐
│  被监控主机                                     │
│  ┌────────┐  ┌────────┐  ┌────────┐              │
│  │ Agent  │  │ Agent  │  │ Agent  │              │
│  │只读采集 │  │只读采集 │  │只读采集 │              │
│  │无命令执行│  │无命令执行│  │无命令执行│             │
│  └────────┘  └────────┘  └────────┘              │
└──────────────────────────────────────────────────┘
```

## 安全模型

完整说明见 [docs/security.md](docs/security.md)。

| 威胁         | 防护                                 |
| ---------- | ---------------------------------- |
| 服务端被攻破     | Agent 通道无命令下发能力，无法远程控制节点           |
| Agent 密钥泄露 | 每节点独立密钥、argon2id、失败锁定、session 短有效期 |
| 中间人        | 强制 TLS，可选 mTLS 与证书 pin             |
| Web 端越权    | 全部接口鉴权 + RBAC，按分组过滤                |
| 字段泄露       | 非登录态强制过脱敏层，DTO 不含敏感字段，兜底中间件扫描      |
| 资产侧信道      | 状态页排除离线节点，避免"是否出现"反推隐藏资产           |
| SSRF       | 默认禁止监控内网保留段                        |

## 开发

```bash
make deps            # 下载依赖
make build           # 构建服务端 + Agent
make test            # 单元测试
make vet             # 静态检查
make security-check  # 安全断言
make check           # 提交前完整检查
make proto           # 生成 protobuf 代码（需 protoc）
```

## 路线图

- [x] P0 骨架：协议契约、配置、日志、安全断言
- [ ] P1 存储层
- [ ] P2 Agent 采集器
- [ ] P3 gRPC 流水线与升配检测
- [ ] P4 网站监控
- [ ] P5 告警引擎
- [ ] P6 前端界面
- [ ] P7 状态页与脱敏
- [ ] P8 验收

## 致谢

本项目在设计阶段调研并参考了以下开源项目：

- [nezhahq/nezha](https://github.com/nezhahq/nezha) — Apache-2.0，gRPC 双向流架构
- [henrygd/beszel](https://github.com/henrygd/beszel) — MIT，指标边界的克制
- [louislam/uptime-kuma](https://github.com/louislam/uptime-kuma) — MIT，探针类型设计
- [shirou/gopsutil](https://github.com/shirou/gopsutil) — BSD-3-Clause，跨平台指标采集
- [huilang-me/CF-Server-Monitor](https://github.com/huilang-me/CF-Server-Monitor) — MIT，零成本部署思路

若这些项目对你的工作有帮助，欢迎给它们一颗 ⭐。

## License

[Apache-2.0](LICENSE)
