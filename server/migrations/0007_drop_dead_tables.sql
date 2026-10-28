-- 0007：清退死表（2026-10-28 全清重启时拍板，六张一次退完）。
-- 这六张表在 v3 代码里没有任何读写方（store/stylelib 的自管建表也不含它们），
-- 属 v1/v2 规划遗产：
--   manuscripts / storyboards —— v1 设想「内容进 PG」，v3 事实源定在磁盘
--     （manuscript.json / storyboard.json，Keeper 上云），Store 接口从未有过它们的方法；
--   style_packs —— v2 风格模板，语义与 v3 styles 不兼容（0005 注释立此存照）；
--   method_notes —— 经验卡从未走库，方法库实际是 git 里的 data/library/methods；
--   assets —— 0003 规划的「素材库镜像」，配套 assetlib 包从未落地，孤儿表；
--   render_jobs —— 制作起跑/终态的只写不读镜像，manifest.jsonl 才是事实源
--     （Keeper 上云）；运维统计真要做时，从 manifest 反填或届时再建表。
-- 执行：psql -h <host> -U <admin> -d zhenshu -f 0007_drop_dead_tables.sql
-- 存量：2026-10-28 已全量 TRUNCATE 过，DROP 零数据损失。

BEGIN;

DROP TABLE IF EXISTS manuscripts CASCADE;
DROP TABLE IF EXISTS storyboards CASCADE;
DROP TABLE IF EXISTS style_packs CASCADE;
DROP TABLE IF EXISTS method_notes CASCADE;
DROP TABLE IF EXISTS assets CASCADE;
DROP TABLE IF EXISTS render_jobs CASCADE;

COMMIT;
