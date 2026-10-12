-- 0004：用户账号（固定账号模型：管理员预建，无自助注册）。projects.user_id 列
-- 0001 已预留，自此开始填写（与磁盘 project.json 的 user_id 双写）。
-- 执行：psql -h <host> -U <admin> -d zhenshu -f 0004_users.sql
-- 说明：应用启动时 pgStore 也会幂等建本表（与 0003 assets 同款自管），
--       本文件留作运维对账口径。

BEGIN;

CREATE TABLE IF NOT EXISTS users (
  id            UUID PRIMARY KEY,             -- 应用侧生成（google/uuid v4）
  username      TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,                -- bcrypt
  role          TEXT NOT NULL DEFAULT 'user', -- admin | user
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMIT;
