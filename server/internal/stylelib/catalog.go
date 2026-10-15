package stylelib

// catalog：条目/词表账本后端（docs/style-library-cloud-storage.md §3）。
// 两种实现：pgCatalog（PostgreSQL，0005 styles/style_vocab 表）与 fileCatalog
// （data/library/*.json 文件回退——demo 容器、没配 PG 的环境用）。
//
// 接口只给原语（全量读 / upsert / 单列改 / 删）；查重、编号、schema 校验等复合
// 逻辑全在 Library（校验职责在 Go，PG 只管存取与行级唯一——数据库不当校验器）。

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"webpage-video-agent/server/internal/contract"
)

type catalog interface {
	ListStyles() ([]Style, error)
	// PutStyle upsert：同 ID 覆盖但保留首建 created_at（回填到 s，列表排序稳定）。
	PutStyle(s *Style) error
	// MarkPublished 用户确认入库。ErrNoEntry。
	MarkPublished(id string) (*Style, error)
	// DeleteStyle 删行。ErrNoEntry；媒体前缀由调用方清（先账后货）。
	DeleteStyle(id string) error
	// AllVocab 词表全量。损坏报错（申请流程须区分空表与坏表）。
	AllVocab() ([]VocabEntry, error)
	// PutVocab upsert by id（新建词条与化石导入共用）。
	PutVocab(e VocabEntry) error
	// SetAliases 整体改写词条别名（词条不存在报错；归一化查重在 Library，
	// 这里只负责写——新增别名由调用方算好全量列表传入）。
	SetAliases(id string, aliases []string) error
	// Empty 表/文件是否为空（化石导入的「表空才导」判据）。
	Empty() (bool, error)
}

// jsonBytes JSONB 列的极简载体（Value/Scan 走 JSON 文本，与 store.Refs 同款）。
type jsonBytes []byte

// facetPtr / usagePtr 零值结构体不落 payload（omitempty 对值结构体不生效，手动判零）。
func facetPtr(f StyleFacets) *StyleFacets {
	if f == (StyleFacets{}) {
		return nil
	}
	return &f
}

func usagePtr(u UsageStats) *UsageStats {
	if u == (UsageStats{}) {
		return nil
	}
	return &u
}

func (j jsonBytes) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	return string(j), nil
}

func (j *jsonBytes) Scan(src any) error {
	if src == nil {
		*j = nil
		return nil
	}
	switch v := src.(type) {
	case []byte:
		*j = append((*j)[:0], v...)
	case string:
		*j = []byte(v)
	default:
		return fmt.Errorf("jsonb: 不支持的扫描类型 %T", src)
	}
	return nil
}

// strList JSONB 数组列的 Go 载体（tags_aux / aliases）。
// 为什么不用 TEXT[]：pgx 会把空切片编成 NULL（撞 NOT NULL），gorm schema 解析器
// 又不认裸 []string——JSONB 由 json 同机制承载，中文别名也没有转义坑。数组内查询
// 目前无需求，真要做可用 jsonb contains。
type strList []string

func (s strList) Value() (driver.Value, error) {
	if s == nil {
		s = strList{}
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func (s *strList) Scan(src any) error {
	if src == nil {
		*s = strList{}
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("jsonb 数组: 不支持的扫描类型 %T", src)
	}
	var out []string
	if err := json.Unmarshal(b, &out); err != nil {
		return fmt.Errorf("jsonb 数组损坏: %w", err)
	}
	*s = out
	return nil
}

// ── PG 实现（0005 表）──────────────────────────────────────

// styleRow styles 表行。可筛选字段列化，嵌套数据整包进 payload（§3 不拆表的取舍）。
type styleRow struct {
	ID            string    `gorm:"column:id"`
	Name          string    `gorm:"column:name"`
	TagsMain      string    `gorm:"column:tags_main"`
	TagsAux       strList   `gorm:"column:tags_aux"`
	Subject       string    `gorm:"column:subject"`
	OriginProject string    `gorm:"column:origin_project"`
	Published     bool      `gorm:"column:published"`
	Payload       jsonBytes `gorm:"column:payload"`
	CreatedAt     time.Time `gorm:"column:created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at"`
}

// stylePayload payload JSONB 形状：契约层嵌套数据整包（v3.1 起，分面/治理/使用
// 统计也住这里——列结构不动，schema 演进零迁移）。
type stylePayload struct {
	TagEvidence []contract.TagEvidence `json:"tag_evidence,omitempty"`
	Signatures  []contract.Signature   `json:"signatures,omitempty"`
	Facets      *StyleFacets           `json:"facets,omitempty"`
	DerivedFrom string                 `json:"derived_from,omitempty"`
	Unverified  bool                   `json:"unverified,omitempty"`
	Usage       *UsageStats            `json:"usage,omitempty"`
}

func (r *styleRow) toStyle() (Style, error) {
	var p stylePayload
	if err := json.Unmarshal(r.Payload, &p); err != nil {
		return Style{}, fmt.Errorf("条目 %s payload 损坏: %w", r.ID, err)
	}
	s := Style{
		ID:            r.ID,
		Name:          r.Name,
		Tags:          contract.StyleTags{Main: r.TagsMain, Aux: []string(r.TagsAux)},
		TagEvidence:   p.TagEvidence,
		Signatures:    p.Signatures,
		Subject:       r.Subject,
		OriginProject: r.OriginProject,
		Published:     r.Published,
		CreatedAt:     r.CreatedAt,
		DerivedFrom:   p.DerivedFrom,
		Unverified:    p.Unverified,
	}
	if p.Facets != nil {
		s.Facets = *p.Facets
	}
	if p.Usage != nil {
		s.Usage = *p.Usage
	}
	return s, nil
}

// vocabRow style_vocab 表行。
type vocabRow struct {
	ID        string    `gorm:"column:id"`
	Name      string    `gorm:"column:name"`
	Aliases   strList   `gorm:"column:aliases"`
	Brief     string    `gorm:"column:brief"`
	Tells     string    `gorm:"column:tells"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (r *vocabRow) toEntry() VocabEntry {
	return VocabEntry{ID: r.ID, Name: r.Name, Aliases: []string(r.Aliases), Brief: r.Brief, Tells: r.Tells, CreatedAt: r.CreatedAt}
}

// pgCatalog 账本 PG 后端。ensureSchema 幂等自建表（与 users 同款自管口径，
// 0005 留作运维对账），开发机没 psql 也能起。
type pgCatalog struct{ db *gorm.DB }

func (c *pgCatalog) ensureSchema() error {
	// tags_aux/aliases 用 JSONB 不用 TEXT[]：pgx 把空切片编成 NULL（撞 NOT NULL）、
	// gorm 不认裸 []string（见 strList 注释）；别名都是短字符串，无数组内查询需求。
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS styles (
			id             TEXT PRIMARY KEY,
			name           TEXT NOT NULL,
			tags_main      TEXT NOT NULL,
			tags_aux       JSONB NOT NULL DEFAULT '[]',
			subject        TEXT NOT NULL DEFAULT '',
			origin_project TEXT NOT NULL DEFAULT '',
			published      BOOLEAN NOT NULL DEFAULT false,
			payload        JSONB NOT NULL,
			created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_styles_published ON styles(published)`,
		`CREATE INDEX IF NOT EXISTS idx_styles_tags_main ON styles(tags_main)`,
		`CREATE TABLE IF NOT EXISTS style_vocab (
			id         TEXT PRIMARY KEY,
			name       TEXT NOT NULL,
			aliases    JSONB NOT NULL DEFAULT '[]',
			brief      TEXT NOT NULL DEFAULT '',
			tells      TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
	} {
		if err := c.db.Exec(ddl).Error; err != nil {
			return fmt.Errorf("风格库建表失败（检查 0005 迁移）: %w", err)
		}
	}
	return nil
}

func (c *pgCatalog) Empty() (bool, error) {
	var n int64
	if err := c.db.Table("styles").Count(&n).Error; err != nil {
		return false, err
	}
	return n == 0, nil
}

func (c *pgCatalog) ListStyles() ([]Style, error) {
	var rows []styleRow
	if err := c.db.Table("styles").Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]Style, 0, len(rows))
	for i := range rows {
		s, err := rows[i].toStyle()
		if err != nil {
			log.Printf("[stylelib] %v（跳过）", err) // payload 损坏：跳过不拖垮全库列表
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// PutStyle upsert。ON CONFLICT 不碰 created_at = 保首建值；回查一次把实际值
// 写回 s（重解析覆盖路径的返回值与旧文件实现同形）。
func (c *pgCatalog) PutStyle(s *Style) error {
	payload, err := json.Marshal(stylePayload{
		TagEvidence: s.TagEvidence, Signatures: s.Signatures,
		Facets: facetPtr(s.Facets), DerivedFrom: s.DerivedFrom, Unverified: s.Unverified,
		Usage: usagePtr(s.Usage),
	})
	if err != nil {
		return err
	}
	created := s.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	err = c.db.Exec(`INSERT INTO styles
		(id, name, tags_main, tags_aux, subject, origin_project, published, payload, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, now())
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name, tags_main = EXCLUDED.tags_main, tags_aux = EXCLUDED.tags_aux,
			subject = EXCLUDED.subject, origin_project = EXCLUDED.origin_project,
			published = EXCLUDED.published, payload = EXCLUDED.payload, updated_at = now()`,
		s.ID, s.Name, s.Tags.Main, strList(s.Tags.Aux), s.Subject, s.OriginProject, s.Published, jsonBytes(payload), created).Error
	if err != nil {
		return err
	}
	var row struct {
		CreatedAt time.Time `gorm:"column:created_at"`
	}
	if err := c.db.Table("styles").Select("created_at").Where("id = ?", s.ID).Take(&row).Error; err == nil {
		s.CreatedAt = row.CreatedAt
	}
	return nil
}

func (c *pgCatalog) MarkPublished(id string) (*Style, error) {
	res := c.db.Exec(`UPDATE styles SET published = true, updated_at = now() WHERE id = ?`, id)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoEntry, id)
	}
	var row styleRow
	if err := c.db.Table("styles").Where("id = ?", id).Take(&row).Error; err != nil {
		return nil, err
	}
	s, err := row.toStyle()
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (c *pgCatalog) DeleteStyle(id string) error {
	res := c.db.Exec(`DELETE FROM styles WHERE id = ?`, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("%w: %s", ErrNoEntry, id)
	}
	return nil
}

func (c *pgCatalog) AllVocab() ([]VocabEntry, error) {
	var rows []vocabRow
	if err := c.db.Table("style_vocab").Order("name ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]VocabEntry, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].toEntry())
	}
	return out, nil
}

func (c *pgCatalog) PutVocab(e VocabEntry) error {
	created := e.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	return c.db.Exec(`INSERT INTO style_vocab (id, name, aliases, brief, tells, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name, aliases = EXCLUDED.aliases, brief = EXCLUDED.brief, tells = EXCLUDED.tells`,
		e.ID, e.Name, strList(e.Aliases), e.Brief, e.Tells, created).Error
}

func (c *pgCatalog) SetAliases(id string, aliases []string) error {
	res := c.db.Exec(`UPDATE style_vocab SET aliases = ? WHERE id = ?`, strList(aliases), id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("词条 %s 不存在", id)
	}
	return nil
}

// ── 文件实现（现状行为，文件回退）──────────────────────────

// fileCatalog 账本文件后端：styles.json / vocab.json，原子替换、单锁串行
// （两个文件同一把锁——打标入库会同时写两个文件）。schema 不合格的条目加载时
// 跳过并喊出来（写入侧已校验，能走到这里的通常是手改）。
type fileCatalog struct {
	dir string
	mu  sync.Mutex
}

func (c *fileCatalog) stylesPath() string { return filepath.Join(c.dir, "styles.json") }

func (c *fileCatalog) vocabPath() string { return filepath.Join(c.dir, "vocab.json") }

func (c *fileCatalog) Empty() (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	styles, err := c.listLocked()
	if err != nil {
		return false, err
	}
	return len(styles) == 0, nil
}

func (c *fileCatalog) ListStyles() ([]Style, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.listLocked()
}

func (c *fileCatalog) listLocked() ([]Style, error) {
	b, err := os.ReadFile(c.stylesPath())
	if os.IsNotExist(err) {
		return nil, nil // 空库（白板是合法状态）
	}
	if err != nil {
		return nil, err
	}
	var styles []Style
	if err := json.Unmarshal(b, &styles); err != nil {
		return nil, fmt.Errorf("styles.json 损坏: %w", err)
	}
	out := make([]Style, 0, len(styles))
	for i := range styles {
		if errs := validateEntry(&styles[i]); len(errs) > 0 {
			log.Printf("[stylelib] 条目 %s 校验不合格，加载时跳过: %s", styles[i].ID, strings.Join(errs, "; "))
			continue
		}
		out = append(out, styles[i])
	}
	return out, nil
}

func (c *fileCatalog) PutStyle(s *Style) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	styles, err := c.listLocked()
	if err != nil {
		return fmt.Errorf("风格库读取失败（不写入，保护现有库）: %w", err)
	}
	replaced := false
	for i := range styles {
		if styles[i].ID == s.ID {
			// 同 seed 重解析：保留首建时间（列表排序稳定），其余以新解析为准
			s.CreatedAt = styles[i].CreatedAt
			styles[i] = *s
			replaced = true
			break
		}
	}
	if !replaced {
		if s.CreatedAt.IsZero() {
			s.CreatedAt = time.Now()
		}
		styles = append(styles, *s)
	}
	return c.saveStyles(styles)
}

func (c *fileCatalog) MarkPublished(id string) (*Style, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	styles, err := c.listLocked()
	if err != nil {
		return nil, err
	}
	for i := range styles {
		if styles[i].ID == id {
			styles[i].Published = true
			return &styles[i], c.saveStyles(styles)
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrNoEntry, id)
}

func (c *fileCatalog) DeleteStyle(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	styles, err := c.listLocked()
	if err != nil {
		return fmt.Errorf("风格库读取失败（不写入，保护现有库）: %w", err)
	}
	out := make([]Style, 0, len(styles))
	found := false
	for i := range styles {
		if styles[i].ID == id {
			found = true
			continue
		}
		out = append(out, styles[i])
	}
	if !found {
		return fmt.Errorf("%w: %s", ErrNoEntry, id)
	}
	return c.saveStyles(out)
}

// saveStyles 原子替换（tmp+rename）。文件是本地模式的正本，与旧实现同纪律。
func (c *fileCatalog) saveStyles(styles []Style) error {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(styles, "", "  ")
	tmp := c.stylesPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.stylesPath())
}

func (c *fileCatalog) AllVocab() ([]VocabEntry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, err := c.loadVocabLocked()
	if err != nil {
		return nil, err
	}
	out := make([]VocabEntry, 0, len(v.Entries))
	for _, e := range v.Entries {
		out = append(out, e)
	}
	return out, nil
}

// loadVocabLocked 读词表（须持 c.mu）。文件不存在返回空表；损坏返回错误。
func (c *fileCatalog) loadVocabLocked() (*Vocab, error) {
	v := &Vocab{Entries: map[string]VocabEntry{}}
	b, err := os.ReadFile(c.vocabPath())
	if os.IsNotExist(err) {
		return v, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return nil, fmt.Errorf("vocab.json 损坏: %w", err)
	}
	if v.Entries == nil {
		v.Entries = map[string]VocabEntry{}
	}
	return v, nil
}

func (c *fileCatalog) PutVocab(e VocabEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, err := c.loadVocabLocked()
	if err != nil {
		return err
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	v.Entries[e.ID] = e
	return c.saveVocabLocked(v)
}

func (c *fileCatalog) SetAliases(id string, aliases []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, err := c.loadVocabLocked()
	if err != nil {
		return err
	}
	e, ok := v.Entries[id]
	if !ok {
		return fmt.Errorf("词条 %s 不存在", id)
	}
	e.Aliases = aliases
	v.Entries[id] = e
	return c.saveVocabLocked(v)
}

func (c *fileCatalog) saveVocabLocked(v *Vocab) error {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	tmp := c.vocabPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.vocabPath())
}
