-- 0003 节点 CPU 核数字段（PostgreSQL）
--
-- 与 SQLite 版同义，理由见0003_node_cpu_cores.sql。

ALTER TABLE nodes ADD COLUMN IF NOT EXISTS cores_physical INTEGER;
