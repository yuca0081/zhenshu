-- 0002：projects 增加 disk_id（磁盘项目 ID = API 对外主键；uuid 主键保留给商业化多用户）
-- 执行：psql -h <host> -U <admin> -d zhenshu -f 0002_disk_id.sql

ALTER TABLE projects ADD COLUMN IF NOT EXISTS disk_id TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_disk_id ON projects(disk_id) WHERE disk_id IS NOT NULL;
