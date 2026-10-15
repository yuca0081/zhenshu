-- 0005：风格库上云——账进 PG（货在 MinIO zhenshu/style-media/<style_id>/，见 docs/style-library-cloud-storage.md）
-- 执行：psql -h <host> -U <admin> -d zhenshu -f 0005_styles.sql
-- 说明：应用启动时 stylelib.Migrate 会幂等自建这两张表（与 users 同款自管口径），
--       本文件留作运维对账口径。化石导入（data/library/styles.json/vocab.json → 表）
--       由启动迁移自动完成：表空且化石存在才导，事务整体成功后化石改名 .imported。
-- 注意：0001 的 style_packs 是 v2 遗留僵尸表，语义与 v3 条目不兼容，不复用、不清理。

BEGIN;

CREATE TABLE IF NOT EXISTS styles (
  id             TEXT PRIMARY KEY,               -- 条目 ID:vid-<refID>(参考视频解析)| sp<YYYYMMDD-HHMMSS>(成片反哺);与旧 styles.json 同构,迁移不换号
  name           TEXT NOT NULL,                  -- 风格名:人工可读,库页展示与选用入口
  tags_main      TEXT NOT NULL,                  -- 主标签,必填;须在 style_vocab 内(写入时 Go 侧核对,不上 DB 约束——词表会演进,约束改起来烦)
  tags_aux       JSONB NOT NULL DEFAULT '[]',    -- 辅标签字符串数组,≤2 个(Go 侧校验上限)。用 JSONB 不用 TEXT[]:pgx 把空切片编成 NULL(撞 NOT NULL)、gorm 不认裸 []string;数组内查询无需求
  subject        TEXT NOT NULL DEFAULT '',       -- 原片题材一句话:只供库页检索,永不进生成提示词(防题材中毒的红线,立此存照)
  origin_project TEXT NOT NULL DEFAULT '',       -- 溯源:"video:<refID>"=视频解析入库 | 项目 id=成片反哺;DraftFromProject 的幂等查重键之一
  published      BOOLEAN NOT NULL DEFAULT false, -- false=成片反哺草稿,待用户库页确认;true=已入库可选用;仅视频解析路径直接 true
  payload        JSONB NOT NULL,                 -- 整包嵌套数据:tag_evidence[](每个 tag 的证据帧)+ signatures[](0–6 条特色:kind 六类枚举、mechanism 必填、frames=MinIO 对象文件名);结构由 Go ValidateSignatures 校验,读方整读整写,故意不拆关系表
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),  -- 首建时间;同 ID 重解析覆盖时保留原值(列表排序稳定,对齐旧 upsert 语义)
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()   -- 最近写入;GORM 自动维护;前端媒体 URL 的 cache-buster(?v=)可用它
);

CREATE INDEX IF NOT EXISTS idx_styles_published ON styles(published);   -- 库页"已入库/草稿"两个视图
CREATE INDEX IF NOT EXISTS idx_styles_tags_main ON styles(tags_main);   -- 按主标签筛选(现在用不上,先备着)

-- 词表:主/辅标签共用;Go 侧 vocab.Has()/VocabList() 的数据源,从 data/library/vocab.json 迁入。
-- 词表不止一个词:id + 正式名 + 俗称 + 机制一句话 + 判别特征,后四样都注入打标提示词。
CREATE TABLE IF NOT EXISTS style_vocab (
  id         TEXT PRIMARY KEY,               -- 词表 id(小写蛇形英文,如 cassette_futurism)
  name       TEXT NOT NULL,                  -- 正式名(卡带未来主义)
  aliases    JSONB NOT NULL DEFAULT '[]',    -- 中英俗称字符串数组(JSONB 理由同 tags_aux);新词查重(归一化精确 + LLM 语义)与检索用
  brief      TEXT NOT NULL DEFAULT '',       -- 一句机制描述:色域/质感/光/字体气质(注入打标提示词)
  tells      TEXT NOT NULL DEFAULT '',       -- 判别特征:帧里认什么(注入打标提示词)
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMIT;
