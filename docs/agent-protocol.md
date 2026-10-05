# Agent 通信协议

契约来源：[`api/agent/v1/agent.proto`](../api/agent/v1/agent.proto)。proto 是唯一真相来源。

## 安全红线

**Agent 是只读的。** 协议中不存在任何有执行能力的 RPC。

这不是功能缺失，而是设计前提。参考 nezhahq/nezha 的教训：它的
Command（任意命令执行）、Terminal（交互式终端）、File Manager（文件增删下载）
三个任务类型被安全厂商披露为 RAT 滥用载体，攻击者可用其获得 root / SYSTEM
shell，且该二进制在 72 家杀毒厂商处检出率为零。

因此本协议的服务端→客户端方向只允许 `Ack` 与 `ConfigSync` 两种消息，
后者仅携带采集配置（采样间隔、启用的指标项、时区），无任何可执行语义。

CI 强制：`scripts/security-check.sh` 的 A5/A6 断言扫描 `api/` 下所有 proto，
出现白名单外的 RPC 即构建失败。断言自测见 `security-check-selftest.sh`。

## 通信方式

Agent 主动外连，服务端**不能**主动连接 Agent。NAT 与防火墙友好。

传输层必须 TLS。可选 mTLS：首次握手用 token，server 签发客户端证书。

## 三个 RPC

| RPC | 用途 |
|---|---|
| `Handshake` | 一次性鉴权，返回 session_id 与服务端建议的采集间隔 |
| `ReportStream` | 双向流，Agent 持续推送指标，Server 只回 ACK |
| `ReportOnce` | 降级路径，无流能力时每次上报一次 |

## 鉴权流程

```
Agent                                          Server
  │ 读取 config.yaml                              │
  │ ② TLS 握手                                    │
  │ ───────────────────────────────────────────►│
  │ ③ Handshake                                   │
  │   metadata: client-uuid, client-secret        │
  │ ───────────────────────────────────────────►│ ④ 校验 argon2id(secret)
  │                                              │   失败 → UNAUTHENTICATED + 计数
  │                                              │   5 次失败 → 锁 5 分钟
  │                                              │   20 次 → 封 24 小时
  │ ◄─────────────────────────────────────────── │ ⑤ session_id + 下发间隔
  │ ⑥ 开 ReportStream                             │
  │ ───────────────────────────────────────────►│
  │ ⑧ 每 10s ReportMetrics                        │
  │ ───────────────────────────────────────────►│ ⑨ 落库 + 预聚合 + 告警
  │                                              │
  │ ◄─────────────────────────────────────────── │ ⑩ Ack(seq)
```

### 鉴权细则

- `client_secret` 明文仅在生成时展示一次，服务端存 argon2id 哈希
- 失败计数按 `client_uuid + IP` 维度
- `session_id` 为 32 字节随机（base64url），有效期 = 3 × 报告间隔
- 同一 `client_uuid` 重复握手时，**旧 session 立即失效**（防重放）
- 断线重连指数退避：1s → 2s → 4s → 8s → 30s 封顶，带 ±20% 抖动

## 信息上报频率

按变化频率分三类，详见 PRD 3.1.2。

| 类别 | 字段 | 上报时机 |
|---|---|---|
| A 类·不可变 | hostname、fqdn、os_type、os_version、arch、agent_version | 首次握手后一次 |
| B 类·硬件规格 | cpu_model、cores、mem_total、disk_info、hardware_fp | **每轮** |
| C 类·运行时 | boot_time、public_ip、agent_started_at | 每轮覆盖 |

B 类必须每轮上报：升配（vCPU 4→8、内存翻倍）要能被服务端发现。
`hardware_fp` 是 `SHA256(cores + mem_total + 排序后磁盘列表 + cpu_model)[:16]`，
服务端比对指纹判断升配，连续 3 轮相同的新指纹才确认变更，避免云主机 vCPU 抖动误报。
