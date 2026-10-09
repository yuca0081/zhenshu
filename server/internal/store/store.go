// Package store：元数据持久层（PostgreSQL，GORM 只作查询层不迁移表）。
// PG_DSN 已配置却连不上 = 直接报错拒绝启动（fail-fast）：静默降级内存会丢全部
// 聊天记录，且用户无感知。开发机临时没库：不配 PG_DSN（内存），或显式
// STORE_FALLBACK=1 接受降级。
package store

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Msg struct {
	ID         int64     `gorm:"primaryKey"           json:"id"`
	ProjectID  string    `gorm:"column:project_id"    json:"project_id"` // 磁盘项目 ID（api 主键）
	Role       string    `json:"role"`                                   // user | agent | system
	Type       string    `json:"type"`                                   // text | tool_call | progress | error
	Content    string    `json:"content"`
	Refs       Refs      `gorm:"type:jsonb"            json:"refs,omitempty"` // 引用卡片（段/秒/帧/元素，plan §3.5）
	ToolName   string    `gorm:"column:tool_name"      json:"tool_name,omitempty"`
	ToolArgs   string    `gorm:"type:jsonb"            json:"tool_args,omitempty"`
	ToolResult string    `gorm:"type:jsonb"           json:"tool_result,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// Ref 引用卡片：段级 {idx,key}；元素级再带 t（全局秒）与元素 id/人话名（四级寻址之段/秒/帧/元素）。
// JSON 字段名与前端 ChatRef 对齐（camelCase）。
type Ref struct {
	Idx         int     `json:"idx"`
	Key         string  `json:"key,omitempty"`
	T           float64 `json:"t,omitempty"`
	ElementID   string  `json:"elementId,omitempty"`
	ElementName string  `json:"elementName,omitempty"`
}

// Refs 兼顾两层：PG 里是 jsonb（Value/Scan 走 JSON 文本），接口层序列化为数组。
type Refs []Ref

func (r Refs) Value() (driver.Value, error) {
	if len(r) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(r)
	return string(b), err
}

func (r *Refs) Scan(src any) error {
	if src == nil {
		*r = nil
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("refs: 不支持的扫描类型 %T", src)
	}
	return json.Unmarshal(b, r)
}

type ProjectRow struct {
	ID        string    `gorm:"column:disk_id" json:"id"` // 对外一律用磁盘 ID
	UUID      string    `gorm:"primaryKey;column:id" json:"-"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

func (ProjectRow) TableName() string { return "projects" }

// RenderJobRow render_jobs 行：一次制作/重做一行（kind: produce|rework），
// status: running|done|failed|cancelled。quality 恒 draft（管线暂只此一档），
// kind 存 payload JSONB。磁盘 manifest 仍是事实源，这里是可查询镜像。
type RenderJobRow struct {
	ID         int64      `gorm:"column:id"          json:"id"`
	ProjectID  string     `gorm:"column:project_id"  json:"-"` // 内存实现用；PG 查询不取
	Kind       string     `gorm:"column:kind"        json:"kind"`
	Status     string     `gorm:"column:status"      json:"status"`
	OutputPath string     `gorm:"column:output_path" json:"output_path,omitempty"`
	Error      string     `gorm:"column:error"       json:"error,omitempty"`
	StartedAt  time.Time  `gorm:"column:started_at"  json:"started_at"`
	FinishedAt *time.Time `gorm:"column:finished_at" json:"finished_at,omitempty"`
}

// Store 聊天案卷 + 项目元数据。chat_messages.project_id 在 DDL 里是 UUID 外键，
// PG 实现按 disk_id 换写；内存实现直接存。
type Store interface {
	CreateProject(diskID, name string) error
	EnsureProject(diskID, name string) error // 已存在则跳过
	UpdateStatus(diskID, status string) error
	DeleteProject(diskID string) error // 行+聊天+任务镜像全删（磁盘目录由调用方删）
	SaveMsg(m *Msg) error
	ListMsgs(projectID string, afterID int64, limit int) ([]Msg, error)
	// render_jobs 记账：起跑插 running 行，终态回写该项目最近一行 running
	//（同项目同时最多一个任务，见 produce.running 幂等锁）。
	StartRenderJob(projectID, kind string) error
	FinishRenderJob(projectID, status, outputPath, errMsg string) error
	ListRenderJobs(projectID string) ([]RenderJobRow, error)
	Kind() string
}

// Open 先试 PG（3s 超时）。未配 DSN = 内存模式（开发）；配了但连不上 = 报错，
// 除非 STORE_FALLBACK=1 显式接受降级（重启丢聊天记录）。
func Open(dsn string) (Store, error) {
	if dsn == "" {
		log.Println("[store] PG_DSN 未配置，用内存存储（重启丢聊天记录）")
		return newMem(), nil
	}
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  dsn + " connect_timeout=3",
		PreferSimpleProtocol: true,
	}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		if os.Getenv("STORE_FALLBACK") == "1" {
			log.Printf("[store] PG 连接失败（%v），STORE_FALLBACK=1 显式降级内存存储", err)
			return newMem(), nil
		}
		return nil, fmt.Errorf("PG 不可达（%v）：修好数据库再启动，或设 STORE_FALLBACK=1 显式接受内存模式（丢聊天记录）", err)
	}
	sqlDB, _ := db.DB()
	if sqlDB != nil {
		sqlDB.SetMaxOpenConns(4)
		sqlDB.SetConnMaxLifetime(time.Hour)
	}
	log.Println("[store] PG 已连接")
	return &pgStore{db: db}, nil
}

type pgStore struct{ db *gorm.DB }

func (s *pgStore) Kind() string { return "pg" }

func (s *pgStore) findUUID(diskID string) (string, error) {
	var row struct {
		UUID string `gorm:"column:id"`
	}
	err := s.db.Table("projects").Select("id").Where("disk_id = ?", diskID).Scan(&row).Error
	if err != nil || row.UUID == "" {
		return "", fmt.Errorf("项目 %s 无 DB 行", diskID)
	}
	return row.UUID, nil
}

func (s *pgStore) CreateProject(diskID, name string) error {
	return s.db.Exec(`INSERT INTO projects (name, status, disk_id) VALUES (?, 'created', ?)`, name, diskID).Error
}

func (s *pgStore) EnsureProject(diskID, name string) error {
	var n int64
	s.db.Table("projects").Where("disk_id = ?", diskID).Count(&n)
	if n > 0 {
		return nil
	}
	return s.CreateProject(diskID, name)
}

func (s *pgStore) UpdateStatus(diskID, status string) error {
	return s.db.Exec(`UPDATE projects SET status = ? WHERE disk_id = ?`, status, diskID).Error
}

// DeleteProject 删项目镜像：聊天、任务、行。无行不报错（磁盘才是事实源）。
func (s *pgStore) DeleteProject(diskID string) error {
	uuid, err := s.findUUID(diskID)
	if err != nil {
		return nil
	}
	if err := s.db.Exec(`DELETE FROM chat_messages WHERE project_id = ?`, uuid).Error; err != nil {
		return err
	}
	if err := s.db.Exec(`DELETE FROM render_jobs WHERE project_id = ?`, uuid).Error; err != nil {
		return err
	}
	return s.db.Exec(`DELETE FROM projects WHERE id = ?`, uuid).Error
}

func (s *pgStore) StartRenderJob(projectID, kind string) error {
	uuid, err := s.findUUID(projectID)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"kind": kind})
	return s.db.Exec(`INSERT INTO render_jobs (project_id, quality, status, payload, started_at)
		VALUES (?, 'draft', 'running', ?::jsonb, now())`, uuid, string(payload)).Error
}

func (s *pgStore) FinishRenderJob(projectID, status, outputPath, errMsg string) error {
	uuid, err := s.findUUID(projectID)
	if err != nil {
		return err
	}
	return s.db.Exec(`UPDATE render_jobs SET status = ?, output_path = NULLIF(?, ''), error = NULLIF(?, ''), finished_at = now()
		WHERE id = (SELECT max(id) FROM render_jobs WHERE project_id = ? AND status = 'running')`,
		status, outputPath, errMsg, uuid).Error
}

func (s *pgStore) ListRenderJobs(projectID string) ([]RenderJobRow, error) {
	uuid, err := s.findUUID(projectID)
	if err != nil {
		return nil, err
	}
	var rows []RenderJobRow
	err = s.db.Table("render_jobs").
		Select("id, payload->>'kind' AS kind, status, output_path, error, started_at, finished_at").
		Where("project_id = ?", uuid).Order("id DESC").Limit(50).Scan(&rows).Error
	return rows, err
}

func (s *pgStore) SaveMsg(m *Msg) error {
	uuid, err := s.findUUID(m.ProjectID)
	if err != nil {
		return err
	}
	return s.db.Exec(`INSERT INTO chat_messages (project_id, role, type, content, refs, tool_name, tool_args, tool_result)
		VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, '')::jsonb, NULLIF(?, '')::jsonb)`,
		uuid, m.Role, m.Type, m.Content, m.Refs, m.ToolName, m.ToolArgs, m.ToolResult).Error
}

func (s *pgStore) ListMsgs(projectID string, afterID int64, limit int) ([]Msg, error) {
	uuid, err := s.findUUID(projectID)
	if err != nil {
		return nil, err
	}
	var rows []Msg
	err = s.db.Table("chat_messages").
		Select("id, role, type, content, refs, tool_name, created_at").
		Where("project_id = ? AND id > ? AND type IN ('text','tool_call','error')", uuid, afterID).
		Order("id ASC").Limit(limit).Scan(&rows).Error
	for i := range rows {
		rows[i].ProjectID = projectID
	}
	return rows, err
}

// ── 内存实现（PG 不可达时的降级）────────────────────────────

type memStore struct {
	mu       sync.Mutex
	projects []ProjectRow
	msgs     []Msg
	jobs     []RenderJobRow
	jobSeq   int64
	seq      int64
}

func newMem() *memStore { return &memStore{} }

func (s *memStore) Kind() string { return "mem" }

func (s *memStore) upsert(diskID, name string) {
	for i := range s.projects {
		if s.projects[i].ID == diskID {
			return
		}
	}
	s.projects = append([]ProjectRow{{ID: diskID, Name: name, Status: "created", CreatedAt: time.Now()}}, s.projects...)
}

func (s *memStore) CreateProject(diskID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upsert(diskID, name)
	return nil
}

func (s *memStore) EnsureProject(diskID, name string) error { return s.CreateProject(diskID, name) }

func (s *memStore) UpdateStatus(diskID, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.projects {
		if s.projects[i].ID == diskID {
			s.projects[i].Status = status
		}
	}
	return nil
}

func (s *memStore) DeleteProject(diskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	projects := s.projects[:0]
	for _, p := range s.projects {
		if p.ID != diskID {
			projects = append(projects, p)
		}
	}
	s.projects = projects
	msgs := s.msgs[:0]
	for _, m := range s.msgs {
		if m.ProjectID != diskID {
			msgs = append(msgs, m)
		}
	}
	s.msgs = msgs
	jobs := s.jobs[:0]
	for _, j := range s.jobs {
		if j.ProjectID != diskID {
			jobs = append(jobs, j)
		}
	}
	s.jobs = jobs
	return nil
}

func (s *memStore) SaveMsg(m *Msg) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	m.ID = s.seq
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	s.msgs = append(s.msgs, *m)
	return nil
}

func (s *memStore) StartRenderJob(projectID, kind string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobSeq++
	s.jobs = append(s.jobs, RenderJobRow{ID: s.jobSeq, ProjectID: projectID, Kind: kind, Status: "running", StartedAt: time.Now()})
	return nil
}

func (s *memStore) FinishRenderJob(projectID, status, outputPath, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.jobs) - 1; i >= 0; i-- {
		if s.jobs[i].ProjectID == projectID && s.jobs[i].Status == "running" {
			s.jobs[i].Status = status
			s.jobs[i].OutputPath = outputPath
			s.jobs[i].Error = errMsg
			now := time.Now()
			s.jobs[i].FinishedAt = &now
			return nil
		}
	}
	return nil // 无 running 行（起跑未记账）：manifest 是事实源，不虚报
}

func (s *memStore) ListRenderJobs(projectID string) ([]RenderJobRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []RenderJobRow
	for i := len(s.jobs) - 1; i >= 0; i-- { // 新在前，与 PG 的 id DESC 同序
		if s.jobs[i].ProjectID == projectID {
			out = append(out, s.jobs[i])
		}
	}
	return out, nil
}

func (s *memStore) ListMsgs(projectID string, afterID int64, limit int) ([]Msg, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Msg
	for _, m := range s.msgs {
		if m.ProjectID == projectID && m.ID > afterID && (m.Type == "text" || m.Type == "tool_call" || m.Type == "error") {
			out = append(out, m)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}
