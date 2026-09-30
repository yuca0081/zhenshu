-- 0003：素材库镜像表（PG/MinIO 双写，磁盘仍为渲染事实源）
-- 执行：psql -h <host> -U <admin> -d zhenshu -f 0003_assets.sql
-- 说明：应用启动时 ensureSchema 会幂等建表（与 0001/0002 的手工执行不同，
--       素材表由 assetlib 自管）；本文件留作运维对账口径。

BEGIN;

CREATE TABLE IF NOT EXISTS assets (
  id          TEXT PRIMARY KEY,                    -- img-000123 / aud-calm（与 catalog.json 同键）
  type        TEXT NOT NULL,                       -- image | audio | video
  file        TEXT NOT NULL,                       -- media/<type>/<id>.<ext>（= MinIO 对象键去前缀）
  tags        JSONB NOT NULL DEFAULT '[]',
  description TEXT NOT NULL DEFAULT '',
  origin      TEXT NOT NULL DEFAULT '',            -- fetch | gen | upload | builtin | film
  meta        JSONB NOT NULL DEFAULT '{}',         -- hash/bytes/w/h/ingested_at…
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_assets_type ON assets(type);

COMMIT;
