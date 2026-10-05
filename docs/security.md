# 安全模型

## 核心设计前提

**Agent 不能执行命令。** 这是本项目与主流探针系统最根本的差异。

哪吒监控（nezhahq/nezha，10.3k star）因在 Agent 中提供 Command、Terminal、
File Manager 三类任务，被 [Ontinue](https://www.ontinue.com/resource/nezha-the-monitoring-tool-thats-also-a-perfect-rat/)
披露为 RAT 滥用载体：攻击者可直接获得 root / NT AUTHORITY\SYSTEM shell，
而该二进制在 72 家杀毒厂商处检出率为**零**。

ProbeOne 把这个口子从协议层焊死：
- `api/agent/v1/agent.proto` 中不存在任何有执行能力的 RPC
- 服务端→Agent 方向只允许 `Ack` 与 `ConfigSync`（后者仅含采集配置）
- 由 CI 断言强制，不依赖人工 review

**后果**：服务端被攻破，攻击者能读到所有节点数据，但不能远程控制任何节点。
损失止于"数据泄露 + 静默"。

## 威胁与防护

| 威胁 | 防护 |
|---|---|
| 服务端被攻破 | Agent 通道无命令下发能力 |
| Agent 密钥泄露 | 每节点独立密钥、argon2id、失败锁定、session 短有效期、旧 session 立即失效 |
| 中间人 | 强制 TLS，可选 mTLS 与证书 pin |
| Web 端越权 | 全部接口鉴权 + RBAC，按分组过滤 |
| SQL 注入 | 一律参数化查询 |
| XSS | Vue 默认转义 + CSP `script-src 'self'` |
| CSRF | SameSite=Strict + Origin 校验 |
| 暴力破解 | 登录失败计数 + 指数锁定 + 可选 TOTP |
| 路径遍历 | `filepath.Clean` + 前缀白名单 |
| SSRF | 默认禁止内网保留段（127/8、10/8、172.16/12、192.168/16、169.254/16） |
| 敏感字段泄露 | 非登录态强制过脱敏层 + DTO 类型约束 + 兜底中间件 |
| 资产侧信道 | 状态页排除离线节点 |
| 弱口令 | 首次启动随机生成，代码中无 `admin:admin` |

## 敏感信息脱敏

详见 PRD 3.6。要点：

- `is_public` 只控制"能否出现在状态页"，**不代表字段全公开**
- 三个 scope：`public_status` / `api_unauth` / `export`
- 已登录且有权限（`full`）是唯一不过滤的路径
- `agent_secret`、`internal_ip`、`mac_address` 硬编码禁止返回，无API 可开启

### 防漏网设计

脱敏最常见的失效方式不是算法写错，而是**新加的接口忘了接**。因此：

1. 禁止直接序列化数据库实体（禁 `c.JSON(200, node)`）
2. DTO 结构体不定义硬禁止字段，从类型层面杜绝误序列化
3. Gin 兜底中间件扫描响应 JSON，命中硬禁止字段置空并记 WARN
4. CI 断言禁止任何结构体以硬禁止字段名做 JSON tag
5. 状态页筛选固定为 `is_public=true AND status!='offline'`

## 端口隔离

| 端口 | 用途 | 对外 |
|---|---|---|
| 8000 | HTTP/面板 | 经 Nginx 反代，绑127.0.0.1 |
| 8008 | Agent gRPC | 可经反代走 443，或直连 |
| 9100 | 自身指标 | 仅内网 |

不学哪吒的单端口复用（8008 同时跑 HTTP 与 gRPC）——攻破 Web 端即可横向到 Agent 通道。

## Agent 能力边界

Agent **不做**以下任何一项，由 `scripts/security-check.sh` 断言：

- 不执行外部命令（零引用 `os/exec`）
- 不 fork 子进程
- 不监听端口（零引用 `net.Listen`）
- 不读取监控无关的文件
- 不写入除自身配置目录与日志之外的文件

systemd 单元提供第二道防线：

```ini
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
```

## 已知风险（本版本接受）

1. **Agent 本机被攻破后可读取本机部分信息** —— 限于性能指标与主机基础信息，是监控系统的固有暴露面。低权运行 + `ProtectSystem=strict` 缓解。
2. **能读 `/proc` 的用户可获取进程信息** —— v1.0 不采集进程列表，减少此暴露。
3. **Web 端 XSS 残余风险** —— Vue 模板默认转义，无 `v-html`（状态页富文本渲染处走白名单净化）。
4. **无 Agent 固件签名** —— 二进制完整性依赖安装脚本的 SHA256 校验 + HTTPS。v1.1 计划引入 cosign。

## 部署检查

`PROBEONE_TRUST_PROXY` 配错的方向相反都很糟：

- 留空但实际有反代 → 审计日志 IP 全是 `127.0.0.1`，爆破限流形同虚设
- 填 `0.0.0.0/0` → 任何人可伪造 `X-Forwarded-For` 绕过限流

启动时会检测 `0.0.0.0/0` 并打 WARN。正确取值见 `docs/deploy.md`。
