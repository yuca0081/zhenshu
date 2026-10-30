// Package store：元数据持久层（PostgreSQL，GORM 只作查询层不迁移表）。
// PG_DSN 已配置却连不上 = 直接报错拒绝启动（fail-fast）：静默降级内存会丢全部
// 聊天记录，且用户无感知。开发机临时没库：不配 PG_DSN（内存），或显式
// STORE_FALLBACK=1 接受降级。
package store

import (
	"context"
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
	StoragePrefix string    `gorm:"column:storage_prefix" json:"-"` // MinIO 桶内相对路径前缀（<user_id>/<项目ID>/，账本为准）
	UpdatedAt     time.Time `gorm:"column:updated_at" json:"-"`
	// 0008 列表事实源字段（docs/storage-rework-2026-10-28.md Q4/Q1）：mode 建项落账；
	// last_event/last_event_ts 制作/重做终态处落账（NotifyResult 读 manifest 终态写入），
	// 列表的「上次制作结果」由此直出，不再回放磁盘 manifest。
	Mode        string    `gorm:"column:mode" json:"-"`
	LastEvent   string    `gorm:"column:last_event" json:"-"`
	LastEventTs time.Time `gorm:"column:last_event_ts" json:"-"`
}

func (ProjectRow) TableName() string { return "projects" }

// ProjectMeta 项目账行的可同步字段。PG 行是项目元数据账本（方案甲，2026-10-28 拍板）：
// 可检索字段以 PG 为准（写点在建项/状态转移处），磁盘 project.json 降级为随货快照。
// StoragePrefix 是 MinIO 桶内相对路径——「URL 永不入库」原则不受影响：入账的是 key，
// 不是带域名/桶名的 URL，换端点零迁移。
type ProjectMeta struct {
	Topic         string
	Aspect        string
	Status        string
	StoragePrefix string
	Mode          string // script（文稿驱动，缺省）| music（音乐驱动 PV）
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

// Store 聊天案卷 + 项目元数据。chat_messages.project_id 在 DDL 里是 UUID 外键，
// PG 实现按 disk_id 换写；内存实现直接存。
type Store interface {
	CreateProject(diskID, name, userID string) error
	EnsureProject(diskID, name string) error // 已存在则跳过
	UpdateStatus(diskID, status string) error
	// UpdateProjectMeta 加厚账行（topic/画幅/模式/状态/对象路径）。幂等覆盖写。
	UpdateProjectMeta(diskID string, meta ProjectMeta) error
	// ListProjects / GetProject 项目账本读取（listProjects 列表事实源、云端项目
	// 首触归属判定）。GetProject 无行回 ErrNoProjectRow。
	ListProjects() ([]ProjectRow, error)
	GetProject(diskID string) (ProjectRow, error)
	// SetLastEvent 制作/重做终态落账（last_event/last_event_ts + updated_at）。
	SetLastEvent(diskID, event string, ts time.Time) error
	DeleteProject(diskID string) error // 行+聊天全删（磁盘目录由调用方删）
	SaveMsg(m *Msg) error
	ListMsgs(projectID string, afterID int64, limit int) ([]Msg, error)
	// ListMsgsTail 取该项目最近 limit 条消息，按时间正序返回。对话历史窗口用：
	// ListMsgs 恒从最旧往后取，消息一超量最新对话永远进不了 LLM 上下文。
	ListMsgsTail(projectID string, limit int) ([]Msg, error)
	// TryProductionLease 制作租约（串行换机，Q5 拍板）：PG advisory lock 按项目占坑，
	// 同一项目同时只允许一台机器开工。返回幂等 release；ok=false = 租约被占
	// （另一台机器在开）。内存实现退化为进程内互斥（跨机无从谈起）。
	TryProductionLease(diskID string) (release func(), ok bool, err error)
	// 用户账号。UserByID 不需要：会话载荷自带角色（签名 cookie 无状态校验，零 DB 查询）。
	CreateUser(u *User) error                  // ID 为空自动生成；用户名冲突回 ErrDuplicateUser
	UserByName(username string) (*User, error) // 无此用户回 ErrNoUser
	ListUsers() ([]User, error)
	// 通用设置 KV（app_settings 表，migrations/0009）：LLM 工位设置远程事实源等
	// 低频整存整取场景。GetKV 无行回 ok=false；updatedAt 为该行最近写入时间。
	GetKV(key string) (val []byte, updatedAt time.Time, ok bool, err error)
	PutKV(key string, val []byte) error
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
	// app_settings 通用设置 KV（migrations/0009 留档；同自管口径，老库不经 deploy 也能起）
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS app_settings (
		key        TEXT PRIMARY KEY,
		value      JSONB NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`).Error; err != nil {
		return nil, fmt.Errorf("建 app_settings 表失败: %w", err)
	}
	// projects 账行加厚列（migrations/0006、0008 留档；这里幂等补齐，
	// 老库不经 deploy 也能起）
	for _, stmt := range []string{
		`ALTER TABLE projects ADD COLUMN IF NOT EXISTS topic TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE projects ADD COLUMN IF NOT EXISTS storage_prefix TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE projects ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now()`,
		`ALTER TABLE projects ADD COLUMN IF NOT EXISTS mode TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE projects ADD COLUMN IF NOT EXISTS last_event TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE projects ADD COLUMN IF NOT EXISTS last_event_ts TIMESTAMPTZ`,
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

// ErrNoProjectRow 该 disk_id 在 projects 表无行（磁盘才是事实源，属正常态）。
var ErrNoProjectRow = errors.New("项目无 DB 行")

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
		return "", fmt.Errorf("项目 %s: %w", diskID, ErrNoProjectRow)
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
	return s.db.Exec(`UPDATE projects SET status = ?, updated_at = now() WHERE disk_id = ?`, status, diskID).Error
}

// UpdateProjectMeta 幂等覆盖账行（含状态——状态转移点可能整批同步盘上快照）。
func (s *pgStore) UpdateProjectMeta(diskID string, meta ProjectMeta) error {
	return s.db.Exec(`UPDATE projects SET
		topic = ?, aspect = ?, status = ?, storage_prefix = ?, mode = ?, updated_at = now()
		WHERE disk_id = ?`,
		meta.Topic, meta.Aspect, meta.Status, meta.StoragePrefix, meta.Mode, diskID).Error
}

func (s *pgStore) ListProjects() ([]ProjectRow, error) {
	var rows []ProjectRow
	err := s.db.Order("created_at DESC").Find(&rows).Error
	return rows, err
}

func (s *pgStore) GetProject(diskID string) (ProjectRow, error) {
	var row ProjectRow
	err := s.db.Where("disk_id = ?", diskID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ProjectRow{}, fmt.Errorf("项目 %s: %w", diskID, ErrNoProjectRow)
	}
	if err != nil {
		return ProjectRow{}, fmt.Errorf("项目 %s 查 DB 失败: %w", diskID, err)
	}
	return row, nil
}

func (s *pgStore) SetLastEvent(diskID, event string, ts time.Time) error {
	return s.db.Exec(`UPDATE projects SET last_event = ?, last_event_ts = ?, updated_at = now() WHERE disk_id = ?`,
		event, ts, diskID).Error
}

// TryProductionLease pg_try_advisory_lock 会话级占坑：锁绑定在专用连接上，
// 制作跑多久占多久（连接池 MaxOpenConns=4，同机并发制作通常 ≤2，可承受）。
// release 解锁并还连接，幂等（Once 包裹由调用方负责——这里自身幂等安全：
// 重复 unlock 只返回 false）。进程崩溃连接断开锁自动释放，无死租约。
func (s *pgStore) TryProductionLease(diskID string) (func(), bool, error) {
	sqlDB, err := s.db.DB()
	if err != nil {
		return nil, false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	var got bool
	if err := conn.QueryRowContext(ctx,
		`SELECT pg_try_advisory_lock(hashtextextended('zhenshu-produce:' || $1, 0))`,
		diskID).Scan(&got); err != nil {
		conn.Close()
		return nil, false, err
	}
	if !got {
		conn.Close()
		return nil, false, nil
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = conn.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtextextended('zhenshu-produce:' || $1, 0))`, diskID)
			conn.Close() // 还池（锁随会话释放，双保险）
		})
	}, true, nil
}

// DeleteProject 删项目镜像：聊天、任务、行。无行不报错（磁盘才是事实源）；
// 但「DB 查询/删除失败」必须原样上抛（2026-10-19 中-32）——瞬时抖动被吞成
// 「删除成功」时镜像行变孤儿且无从排查，与 findUUID 自己注释的纪律一致。
func (s *pgStore) DeleteProject(diskID string) error {
	uuid, err := s.findUUID(diskID)
	if err != nil {
		if errors.Is(err, ErrNoProjectRow) {
			return nil // 磁盘已删、DB 本就无行：干净
		}
		return err // 查库失败：上抛，由调用方决定降级提示
	}
	if err := s.db.Exec(`DELETE FROM chat_messages WHERE project_id = ?`, uuid).Error; err != nil {
		return err
	}
	return s.db.Exec(`DELETE FROM projects WHERE id = ?`, uuid).Error
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

func (s *pgStore) GetKV(key string) (val []byte, updatedAt time.Time, ok bool, err error) {
	var row struct {
		Value     []byte    `gorm:"column:value"`
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	err = s.db.Table("app_settings").Select("value, updated_at").Where("key = ?", key).Scan(&row).Error
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("app_settings 读 %s 失败: %w", key, err)
	}
	if row.Value == nil { // 无行 = 未设置（合法态），与库报错分开
		return nil, time.Time{}, false, nil
	}
	return row.Value, row.UpdatedAt, true, nil
}

func (s *pgStore) PutKV(key string, val []byte) error {
	return s.db.Exec(`INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?::jsonb, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		key, string(val)).Error
}

// ── 内存实现（PG 不可达时的降级）────────────────────────────

type memStore struct {
	mu       sync.Mutex
	projects []ProjectRow
	msgs     []Msg
	users    []User
	seq      int64
	leases   map[string]bool // 制作租约占坑（进程内语义）
	kv       map[string][]byte
	kvTS     map[string]time.Time
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
			s.projects[i].UpdatedAt = time.Now()
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
			p.Mode = meta.Mode
			p.UpdatedAt = time.Now()
		}
	}
	return nil
}

func (s *memStore) ListProjects() ([]ProjectRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ProjectRow, len(s.projects))
	copy(out, s.projects)
	return out, nil
}

func (s *memStore) GetProject(diskID string) (ProjectRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.projects {
		if s.projects[i].ID == diskID {
			return s.projects[i], nil
		}
	}
	return ProjectRow{}, fmt.Errorf("项目 %s: %w", diskID, ErrNoProjectRow)
}

func (s *memStore) SetLastEvent(diskID, event string, ts time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.projects {
		if s.projects[i].ID == diskID {
			s.projects[i].LastEvent, s.projects[i].LastEventTs = event, ts
			s.projects[i].UpdatedAt = time.Now()
		}
	}
	return nil
}

// TryProductionLease 内存实现退化为进程内互斥：跨机串行本就以 PG 为前提。
func (s *memStore) TryProductionLease(diskID string) (func(), bool, error) {
	s.mu.Lock()
	if s.leases == nil {
		s.leases = map[string]bool{}
	}
	if s.leases[diskID] {
		s.mu.Unlock()
		return nil, false, nil
	}
	s.leases[diskID] = true
	s.mu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.leases, diskID)
			s.mu.Unlock()
		})
	}
	return release, true, nil
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

func (s *memStore) GetKV(key string) (val []byte, updatedAt time.Time, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.kv[key]
	if !ok {
		return nil, time.Time{}, false, nil
	}
	return b, s.kvTS[key], true, nil
}

func (s *memStore) PutKV(key string, val []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.kv == nil {
		s.kv = map[string][]byte{}
		s.kvTS = map[string]time.Time{}
	}
	s.kv[key] = val
	s.kvTS[key] = time.Now()
	return nil
}
