-- 帧述 v1 初始结构（PostgreSQL 15+；gen_random_uuid 为内置）
-- 执行：psql -h <host> -U <admin> -d zhenshu -f 0001_init.sql
-- 前置：CREATE DATABASE zhenshu;  并建最小权限账号：
--   CREATE ROLE zhenshu LOGIN PASSWORD '***';
--   GRANT CONNECT ON DATABASE zhenshu TO zhenshu;（建表后再 GRANT 各表）

BEGIN;

-- 项目（业务根命名空间：聊天、文档、产物全挂在下面）
CREATE TABLE IF NOT EXISTS projects (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name        TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'created',   -- created|storyboard|style|producing|video|exported
  aspect      TEXT NOT NULL DEFAULT '9:16',
  user_id     UUID,                              -- 商业化接缝：1.0 全填系统用户，多用户时不迁移表
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 文稿（前置件 1/3，内容权威；版本化）
CREATE TABLE IF NOT EXISTS manuscripts (
  id          BIGSERIAL PRIMARY KEY,
  project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  content     TEXT NOT NULL,
  source      TEXT NOT NULL DEFAULT 'paste',     -- paste | link | ai
  version     INT  NOT NULL DEFAULT 1,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_manuscripts_project ON manuscripts(project_id, version DESC);

-- 分镜（前置件 2/3，中枢数据结构；segments 整体 JSONB 版本化，段级寻址用 JSON 路径）
CREATE TABLE IF NOT EXISTS storyboards (
  id           BIGSERIAL PRIMARY KEY,
  project_id   UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  timing_basis TEXT NOT NULL DEFAULT 'narration',
  plan         JSONB NOT NULL DEFAULT '{}',
  style_id     TEXT,
  segments     JSONB NOT NULL,
  version      INT  NOT NULL DEFAULT 1,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_storyboards_project ON storyboards(project_id, version DESC);

-- 风格模板（方法库 §4.6；冷启动由成片提炼，origin 可溯源）
CREATE TABLE IF NOT EXISTS style_packs (
  id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name               TEXT NOT NULL,
  tokens             JSONB NOT NULL,              -- 色板/字阶/组件/动效偏好 + 样张 HTML
  origin_project_id  UUID REFERENCES projects(id),
  published          BOOLEAN NOT NULL DEFAULT false,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 经验卡（方法库 §4.6 五类）
CREATE TABLE IF NOT EXISTS method_notes (
  id          BIGSERIAL PRIMARY KEY,
  kind        TEXT NOT NULL,                     -- prompt|gotcha|shot|storyboard_arc|manuscript
  content     TEXT NOT NULL,
  tags        TEXT[] NOT NULL DEFAULT '{}',
  origin      TEXT,                              -- 来源项目/自修复案例等
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_method_notes_kind ON method_notes(kind);

-- 渲染任务（draft / delivery；段级并行信息记在 payload）
CREATE TABLE IF NOT EXISTS render_jobs (
  id          BIGSERIAL PRIMARY KEY,
  project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  quality     TEXT NOT NULL,                     -- draft | delivery
  status      TEXT NOT NULL DEFAULT 'queued',    -- queued|running|done|failed|cancelled
  payload     JSONB NOT NULL DEFAULT '{}',
  output_path TEXT,
  error       TEXT,
  started_at  TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_render_jobs_project ON render_jobs(project_id, status);

-- 聊天案卷（追加-only 事件流：SSE/审计/回放/方法库挖掘共用一张表）
CREATE TABLE IF NOT EXISTS chat_messages (
  id          BIGSERIAL PRIMARY KEY,             -- 全局自增 = 项目内排序 + SSE 增量游标
  project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  role        TEXT NOT NULL,                     -- user | agent | system
  type        TEXT NOT NULL,                     -- text | tool_call | progress | error
  content     TEXT,
  refs        JSONB,                             -- 引用卡片（段/秒/帧/元素）
  tool_name   TEXT,
  tool_args   JSONB,
  tool_result JSONB,
  meta        JSONB,                             -- model / token / 耗时（成本记账）
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_chat_stream ON chat_messages(project_id, id);

COMMIT;
