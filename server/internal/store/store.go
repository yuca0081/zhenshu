// Package store：元数据持久层（PostgreSQL，GORM 只作查询层不迁移表）。
// PG_DSN 已配置却连不上 = 直接报错拒绝启动（fail-fast）：静默降级内存会丢全部
// 聊天记录，且用户无感知。开发机临时没库：不配 PG_DSN（内存），或显式
// STORE_FALLBACK=1 接受降级。
package store

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
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
	UserID    string    `gorm:"column:user_id" json:"-"` // 归属用户（空 = 系统项目，仅管理员可见）
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
	// 0006 加厚的账行字段（可检索元数据；内容产物不入库，正本在 MinIO）。
	// style_id/target_seconds 不入账：风格可不来自风格库（prep ② 自造时 project.json
	// 里 style_id 为空），时长用户拍板不要——两者仍留磁盘事实源。
	Topic         string    `gorm:"column:topic" json:"topic,omitempty"`
	Aspect        string    `gorm:"column:aspect" json:"aspect,omitempty"`
	StoragePrefix string    `gorm:"column:storage_prefix" json:"-"` // MinIO 桶内相对路径前缀
	UpdatedAt     time.Time `gorm:"column:updated_at" json:"-"`
}

func (ProjectRow) TableName() string { return "projects" }

// ProjectMeta 项目账行的可同步字段（磁盘 project.json 是事实源，这里是检索镜像）。
// StoragePrefix 是 MinIO 桶内相对路径（如 projects/p20261016-115229/）——「URL 永不
// 入库」原则不受影响：入账的是 key，不是带域名/桶名的 URL，换端点零迁移。
type ProjectMeta struct {
	Topic         string
	Aspect        string
	Status        string
	StoragePrefix string
}

// User 用户账号（固定账号模型：管理员预建，无自助注册）。PasswordHash 不出 JSON。
type User struct {
	ID           string    `gorm:"column:id"            json:"id"`
	Username     string    `gorm:"column:username"      json:"username"`
	PasswordHash string    `gorm:"column:password_hash" json:"-"`
	Role         string    `gorm:"column:role"          json:"role"` // admin | user
	CreatedAt    time.Time `gorm:"column:created_at"    json:"created_at"`
}

func (User) TableName() string { return "users" }

var (
	ErrNoUser        = errors.New("用户不存在")
	ErrDuplicateUser = errors.New("用户名已存在")
)

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
	CreateProject(diskID, name, userID string) error
	EnsureProject(diskID, name string) error // 已存在则跳过
	UpdateStatus(diskID, status string) error
	// UpdateProjectMeta 加厚账行（topic/画幅/风格/目标时长/状态/对象路径）。
	// 幂等覆盖写：磁盘 project.json 是事实源，调用方在读到盘上最新值后同步。
	UpdateProjectMeta(diskID string, meta ProjectMeta) error
	DeleteProject(diskID string) error // 行+聊天+任务镜像全删（磁盘目录由调用方删）
	SaveMsg(m *Msg) error
	ListMsgs(projectID string, afterID int64, limit int) ([]Msg, error)
	// ListMsgsTail 取该项目最近 limit 条消息，按时间正序返回。对话历史窗口用：
	// ListMsgs 恒从最旧往后取，消息一超量最新对话永远进不了 LLM 上下文。
	ListMsgsTail(projectID string, limit int) ([]Msg, error)
	// render_jobs 记账：起跑插 running 行，终态回写该项目最近一行 running
	//（同项目同时最多一个任务，见 produce.running 幂等锁）。
	StartRenderJob(projectID, kind string) error
	FinishRenderJob(projectID, status, outputPath, errMsg string) error
	ListRenderJobs(projectID string) ([]RenderJobRow, error)
	// 用户账号。UserByID 不需要：会话载荷自带角色（签名 cookie 无状态校验，零 DB 查询）。
	CreateUser(u *User) error                  // ID 为空自动生成；用户名冲突回 ErrDuplicateUser
	UserByName(username string) (*User, error) // 无此用户回 ErrNoUser
	ListUsers() ([]User, error)
	Kind() string
	// DB 暴露 GORM 句柄给同库共生的账本（stylelib 风格库 PG 后端共用连接池）；
	// 内存实现返回 nil（调用方据此判定「PG 未接」）。
	DB() *gorm.DB
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
	// users 表幂等自建（与 0003 assets 同款自管口径；migrations/0004_users.sql 留作运维对账）
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS users (
		id            UUID PRIMARY KEY,
		username      TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		role          TEXT NOT NULL DEFAULT 'user',
		created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
	)`).Error; err != nil {
		return nil, fmt.Errorf("建 users 表失败: %w", err)
	}
	// projects 账行加厚列（migrations/0006_project_meta.sql 留档；这里幂等补齐，
	// 老库不经 deploy 也能起）
	for _, stmt := range []string{
		`ALTER TABLE projects ADD COLUMN IF NOT EXISTS topic TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE projects ADD COLUMN IF NOT EXISTS storage_prefix TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE projects ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now()`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			return nil, fmt.Errorf("加厚 projects 表失败（%s）: %w", stmt, err)
		}
	}
	log.Println("[store] PG 已连接")
	return &pgStore{db: db}, nil
}

type pgStore struct{ db *gorm.DB }

func (s *pgStore) Kind() string { return "pg" }

func (s *pgStore) DB() *gorm.DB { return s.db }

func (s *pgStore) findUUID(diskID string) (string, error) {
	var row struct {
		UUID string `gorm:"column:id"`
	}
	err := s.db.Table("projects").Select("id").Where("disk_id = ?", diskID).Scan(&row).Error
	if err != nil {
		// 库报错与「无行」是两回事：抖动时把真错误吞成「无 DB 行」会把排查
		// 带进沟里（SaveMsg/ListMsgs 的报错全变误导）
		return "", fmt.Errorf("项目 %s 查 DB 失败: %w", diskID, err)
	}
	if row.UUID == "" {
		return "", fmt.Errorf("项目 %s 无 DB 行", diskID)
	}
	return row.UUID, nil
}

func (s *pgStore) CreateProject(diskID, name, userID string) error {
	// user_id 空 = 系统项目（机器 token 创建），存 NULL 而不是空串（UUID 列）
	return s.db.Exec(`INSERT INTO projects (name, status, disk_id, user_id) VALUES (?, 'created', ?, NULLIF(?, '')::uuid)`,
		name, diskID, userID).Error
}

func (s *pgStore) EnsureProject(diskID, name string) error {
	var n int64
	s.db.Table("projects").Where("disk_id = ?", diskID).Count(&n)
	if n > 0 {
		return nil
	}
	return s.CreateProject(diskID, name, "")
}

func (s *pgStore) UpdateStatus(diskID, status string) error {
	return s.db.Exec(`UPDATE projects SET status = ? WHERE disk_id = ?`, status, diskID).Error
}

// UpdateProjectMeta 幂等覆盖账行。磁盘是事实源，这里照抄盘上值——包括 status
// （自愈 UpdateStatus 可能漏掉的转移）。
func (s *pgStore) UpdateProjectMeta(diskID string, meta ProjectMeta) error {
	return s.db.Exec(`UPDATE projects SET
		topic = ?, aspect = ?, status = ?, storage_prefix = ?, updated_at = now()
		WHERE disk_id = ?`,
		meta.Topic, meta.Aspect, meta.Status, meta.StoragePrefix, diskID).Error
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

// listJobsCap 渲染任务列表上限（PG LIMIT 与内存实现同源）。
const listJobsCap = 50

func (s *pgStore) ListRenderJobs(projectID string) ([]RenderJobRow, error) {
	uuid, err := s.findUUID(projectID)
	if err != nil {
		return nil, err
	}
	var rows []RenderJobRow
	err = s.db.Table("render_jobs").
		Select("id, payload->>'kind' AS kind, status, output_path, error, started_at, finished_at").
		Where("project_id = ?", uuid).Order("id DESC").Limit(listJobsCap).Scan(&rows).Error
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
		// tool_args/tool_result 与内存实现同列：双实现形状漂移会让 /messages
		// 在 PG/内存两种存储下返回不同 JSON
		Select("id, role, type, content, refs, tool_name, tool_args, tool_result, created_at").
		Where("project_id = ? AND id > ? AND type IN ('text','tool_call','error')", uuid, afterID).
		Order("id ASC").Limit(limit).Scan(&rows).Error
	for i := range rows {
		rows[i].ProjectID = projectID
	}
	return rows, err
}

// ListMsgsTail 最近 limit 条消息：id 倒序取尾部再反转回正序（与 ListMsgs 同列同
// 过滤，双实现形状不漂移）。
func (s *pgStore) ListMsgsTail(projectID string, limit int) ([]Msg, error) {
	uuid, err := s.findUUID(projectID)
	if err != nil {
		return nil, err
	}
	var rows []Msg
	err = s.db.Table("chat_messages").
		Select("id, role, type, content, refs, tool_name, tool_args, tool_result, created_at").
		Where("project_id = ? AND type IN ('text','tool_call','error')", uuid).
		Order("id DESC").Limit(limit).Scan(&rows).Error
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 { // 反转回时间正序
		rows[i], rows[j] = rows[j], rows[i]
	}
	for i := range rows {
		rows[i].ProjectID = projectID
	}
	return rows, err
}

// ── 用户账号（PG 实现）────────────────────────────────────────

func (s *pgStore) CreateUser(u *User) error {
	if u.ID == "" {
		u.ID = uuid.NewString()
	}
	// 先查后插：唯一约束兜底并发，但固定账号场景预查给出可读错误足够
	switch _, err := s.UserByName(u.Username); {
	case errors.Is(err, ErrNoUser): // 可建
	case err != nil:
		return err
	default:
		return ErrDuplicateUser
	}
	return s.db.Exec(`INSERT INTO users (id, username, password_hash, role, created_at) VALUES (?, ?, ?, ?, now())`,
		u.ID, u.Username, u.PasswordHash, u.Role).Error
}

func (s *pgStore) UserByName(username string) (*User, error) {
	var u User
	err := s.db.Where("username = ?", username).Take(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNoUser
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *pgStore) ListUsers() ([]User, error) {
	var users []User
	err := s.db.Order("created_at ASC").Find(&users).Error
	return users, err
}

// ── 内存实现（PG 不可达时的降级）────────────────────────────

type memStore struct {
	mu       sync.Mutex
	projects []ProjectRow
	msgs     []Msg
	jobs     []RenderJobRow
	users    []User
	jobSeq   int64
	seq      int64
}

func newMem() *memStore { return &memStore{} }

func (s *memStore) Kind() string { return "mem" }

func (s *memStore) DB() *gorm.DB { return nil }

func (s *memStore) upsert(diskID, name, userID string) {
	for i := range s.projects {
		if s.projects[i].ID == diskID {
			return
		}
	}
	s.projects = append([]ProjectRow{{ID: diskID, Name: name, Status: "created", UserID: userID, CreatedAt: time.Now()}}, s.projects...)
}

func (s *memStore) CreateProject(diskID, name, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upsert(diskID, name, userID)
	return nil
}

func (s *memStore) EnsureProject(diskID, name string) error { return s.CreateProject(diskID, name, "") }

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

func (s *memStore) UpdateProjectMeta(diskID string, meta ProjectMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.projects {
		if s.projects[i].ID == diskID {
			p := &s.projects[i]
			p.Topic, p.Aspect = meta.Topic, meta.Aspect
			p.Status, p.StoragePrefix = meta.Status, meta.StoragePrefix
			p.UpdatedAt = time.Now()
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
			if len(out) >= listJobsCap { // 与 PG 的 LIMIT 50 同限：双实现不漂移
				break
			}
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

// ListMsgsTail 尾部倒序收集 limit 条再反转回正序（与 PG 实现同过滤同语义：
// 双实现形状漂移会让同一份历史在两种存储下喂给 LLM 不同的窗口）。
func (s *memStore) ListMsgsTail(projectID string, limit int) ([]Msg, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Msg
	for i := len(s.msgs) - 1; i >= 0 && len(out) < limit; i-- {
		m := s.msgs[i]
		if m.ProjectID == projectID && (m.Type == "text" || m.Type == "tool_call" || m.Type == "error") {
			out = append(out, m)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 { // 反转回时间正序
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// ── 用户账号（内存实现）────────────────────────────────────

func (s *memStore) CreateUser(u *User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.users {
		if s.users[i].Username == u.Username {
			return ErrDuplicateUser
		}
	}
	if u.ID == "" {
		u.ID = uuid.NewString()
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now()
	}
	s.users = append(s.users, *u)
	return nil
}

func (s *memStore) UserByName(username string) (*User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.users {
		if s.users[i].Username == username {
			u := s.users[i]
			return &u, nil
		}
	}
	return nil, ErrNoUser
}

func (s *memStore) ListUsers() ([]User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]User, len(s.users))
	copy(out, s.users)
	return out, nil
}
