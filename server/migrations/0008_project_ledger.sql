-- 0008: projects 升级为列表事实源（2026-10-28 存储重构，docs/storage-rework-2026-10-28.md §4 Q4/Q1）。
-- 方案甲拍板：PG 行是项目元数据账本（一等公民），磁盘 project.json 降级为随货快照。
-- 列表所需字段进账本，写点接在状态转移/建项处（store.Open 里有同款幂等 ALTER 自管口径）：
--   mode          驱动模式（script=文稿驱动 / music=音乐驱动 PV），createProject 落账；
--   last_event    最近一次制作/重做终态事件名（produce.done / rework.error …），
--                 NotifyResult 读 manifest 终态写入；列表「上次制作结果」由此直出，
--                 不再回放磁盘 manifest；
--   last_event_ts 该终态时间。
ALTER TABLE projects ADD COLUMN IF NOT EXISTS mode          TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN IF NOT EXISTS last_event    TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN IF NOT EXISTS last_event_ts TIMESTAMPTZ;
