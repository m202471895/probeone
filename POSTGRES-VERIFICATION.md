# PostgreSQL 支持：验证状态与待验证项

## 已验证（自动化测试覆盖）

| 项 | 覆盖方式 | 状态 |
|---|---|---|
| DSN 拼装（6 种场景） | `internal/store/postgres/postgres_test.go` | ✓ |
| 端口拼接边界（0/负数） | 同上 | ✓ |
| 缺配置时报错 | 同上 | ✓ |
| 连接失败时Open 返回错误 | 同上 | ✓ |
| 唯一约束识别（PG 类型化 + SQLite 文案） | `internal/store/errors_test.go` | ✓ |
| 迁移文件无 SQLite 专用语法 | 静态检查（已手工核对 0001/0002） | ✓ |
| 仓储层 SQL 方言无关 | 静态检查（无 AUTOINCREMENT / INSERT OR REPLACE / datetime() / PRAGMA 残留） | ✓ |

## 未验证（无PG 实例，需在有环境时补）

⚠️ **当前开发环境没有 PostgreSQL 实例，以上测试只覆盖了不依赖数据库的逻辑。**
以下是接入真实 PG 后必须跑的验证，未跑过之前不要认为 PG 路径可用：

- [ ] `0001_postgres_init.sql` 能否在真实 PG 上执行成功
- [ ] `0002_postgres_node_geo.sql` 同上（含 `ON CONFLICT` 预聚合表）
- [ ] 11 个仓储的CRUD 在 PG 上正确（含时间比较、JSON 列、upsert）
- [ ] 迁移幂等性（重复执行不重复记录版本）
- [ ] 事务回滚行为
- [ ] 时间类型读写（`timestamptz` 与 `time.Time` 的映射）
- [ ] 布尔列读写
- [ ] 连接池在高并发下的表现

## 如何补做

```bash
# 起一个 PG 容器
docker run -d --name pg-probeone \
  -e POSTGRES_PASSWORD=probeone \
  -e POSTGRES_DB=probeone \
  -e POSTGRES_USER=probeone \
  -p 5432:5432 postgres:16-alpine

# 跑迁移与基础验证
PROBEONE_DB_DRIVER=postgres \
PROBEONE_DB_HOST=127.0.0.1 \
PROBEONE_DB_PORT=5432 \
PROBEONE_DB_USER=probeone \
PROBEONE_DB_PASSWORD=probeone \
PROBEONE_DB_NAME=probeone \
PROBEONE_DB_SSLMODE=disable \
PROBEONE_MASTER_KEY=verification-master-key-32-bytes-min \
  ./scripts/verify-postgres.sh
```

该脚本尚未编写 —— 上面这批待验证项需要一次性覆盖。
建议在有 Docker 的环境下补写并执行。

## 已知风险点

1. **`modernc.org/sqlite` 与 `pgx` 的类型差异**
   JSON 列（`monitor.config` 等）在两个驱动下的读写路径可能不同。
   SQLite 存TEXT，PG 若用 `jsonb` 需要不同的Scan 目标类型。

2. **时间比较语义**
   已在上一轮统一 `sqlbase.UTC` 归一，但 PG 的 `timestamptz`
   本身带时区，行为需要实测确认。

3. **`isUniqueViolation` 的字符串兜底**
   PG 分支走类型化错误（SQLSTATE 23505），SQLite 分支走文案。
   两条路径都有测试，但真实错误对象的包装层级需实测确认。
