# 数据库迁移

## 命名规范

```
0001_init.sql              # SQLite 版
0001_postgres_init.sql     # PostgreSQL 版
0001_init.down.sql         # 可选，向上迁移的回滚脚本
```

规则：

- 版本号前导数字，必须递增
- 同一版本的 SQLite 与 PostgreSQL 文件**必须同时存在且版本号一致**
- 已发布的迁移**不可修改**——改了会导致校验和不匹配并中断启动
  （`Migrator.Status()` 的 `Mismatch` 字段会报出来）。要改就加新版本。

## 两套文件为何必须并存

`agent.proto` 与 Go 侧 Repository 已通过 `sqlbase.Dialect` 屏蔽了占位符差异，
但 DDL 层的类型映射 SQLite 做不到。两版只有三处差异，**其余完全一致**：

| PostgreSQL | SQLite | 原因 |
|---|---|---|
| `BIGSERIAL PRIMARY KEY` | `INTEGER PRIMARY KEY AUTOINCREMENT` | SQLite 只认 `INTEGER PRIMARY KEY` 才隐式自增。`BIGSERIAL` 被当普通类型，插入后 `id` 恒为 0，导致引用它的外键全部失败 |
| `TIMESTAMPTZ` | `TIMESTAMP` | 驱动参数 `_time_format=sqlite` 只对 SQLite 原生时间类型名生效，会把列直接扫成 `time.Time`；写 `TIMESTAMPTZ` 会退回字符串，扫描报 `unsupported Scan` |
| `DEFAULT now()` | `DEFAULT CURRENT_TIMESTAMP` | SQLite 的 `DEFAULT` 只接受常量字面量，不接受函数调用 |

改 schema 时**两个文件都要改**，否则两种数据库的表结构会悄悄漂移。

## 迁移器

`internal/migrate` 是自研的轻量实现（约 200 行，零依赖），理由见 PRD 附录 A：
不用 ORM、手写 SQL 的取向。功能覆盖：顺序执行、版本记录、校验和检测、
事务内单步回滚、方言选择。

```bash
probeone --migrate-only    # 只跑迁移后退出
```

## 校验和

已执行的迁移会被记录 FNV-1a 校验和。若文件内容事后被改动，
`Migrator.Status()` 会把该版本标记为 `Mismatch`。这能发现
"有人直接改了历史迁移"这类会破坏可重现性的操作。
