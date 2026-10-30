-- 0009: 通用设置 KV 表（2026-10-30，LLM 工位模型设置远程化，docs/llm-settings-2026-10-29.md
-- 「远程库存储」一节）。llm-config.json 的远程事实源：多台工作机配同一 PG_DSN 即共享
-- 供应商台账（URL/Key/模型名）与工位绑定。key 现只落一行 'llm_settings'（Settings 全量
-- JSON），表做通用 KV 留给后续设置面复用。
-- 自管口径：store.Open 里同款幂等 CREATE TABLE IF NOT EXISTS，本文件留作运维对账。
CREATE TABLE IF NOT EXISTS app_settings (
    key        TEXT PRIMARY KEY,
    value      JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
