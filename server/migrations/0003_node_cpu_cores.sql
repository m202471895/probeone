-- 0003 节点 CPU 核数字段
--
-- 为什么要新增：硬件指纹的公式是
--   SHA256(cores + mem_total + 排序后磁盘列表 + cpu_model)
-- 之前只有 cpu_cores（逻辑核数），物理核数没地方存，
-- 指纹只能用逻辑核数近似。虚拟机上物理/逻辑核数可能不同
-- （超线程、vCPU 绑定），用逻辑核数会漏判部分升配。
--
-- 新字段不设默认值：已有行的值为 NULL，
-- 表示"未知"而不是"0 核"——两者的语义完全不同，
-- 升配检测不该把 0 当成真实值参与比较。

ALTER TABLE nodes ADD COLUMN cores_physical INTEGER;
