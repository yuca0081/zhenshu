-- 0006: 项目账行加厚（2026-10-16 存储改造二期，docs/storage-cloud-migration.md §12）
-- 决策：内容产物（文稿/分镜稿/媒体）不入库——正本在 MinIO zhenshu-data 桶，
-- projects.storage_prefix 存桶内相对路径 key（不带域名/桶名，换端点不受影响）；
-- 本表只存可检索的元数据。store.Open 里有同款幂等 ALTER（自管口径），
-- 本文件留作运维对账。
-- 不入账的字段：style_id（风格可不来自风格库，prep ② 自造风格时为空，做账意义
-- 小）与 target_seconds（用户拍板不要）——两者仍留在磁盘 project.json 事实源里。
ALTER TABLE projects ADD COLUMN IF NOT EXISTS topic          TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN IF NOT EXISTS storage_prefix TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN IF NOT EXISTS updated_at     TIMESTAMPTZ NOT NULL DEFAULT now();
