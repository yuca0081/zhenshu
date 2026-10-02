// Package stylelib：风格库（唯一风格事实源，前称模板库/StylePack/风格注册表）。
//
// 一个条目 = 一个风格，五个面：身份（id/name/desc/genre/keywords）、外观（tokens 色板
// + layouts 版式）、证据（samples 样张 1–3 张）、复用件（skin 风格文件）、溯源（origin）。
// 全链路一条锚线：style_samples.json 的 style_id 精确指到条目——场景生成拿 tokens/版式、
// 拼接拿转场/后期特效、复用拷 skin，都按 id 查这里，不再 direction 模糊 join。
//
// 条目存 data/library/styles.json（磁盘为事实源）：
// 成片出片 Agent 起草（published=false）或视频解析入库（published=true）
// → 新建项目选用 → 样张+skin 拷进项目，style_id 随 style_samples.json 落盘。
// 首次访问自动迁移合并旧双库（templates.json × registry/styles.json，读旧写新一次完成）。
package stylelib

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"webpage-video-agent/server/internal/contract"
)

// Style 风格条目：身份 + 外观 + 证据 + 复用件。
// 设计系统五件套（concept/fonts/motion/device/anti_patterns）视频解析条目才有，
// 与项目 design 阶段同构（contract.StyleSamples 同款类型）；老条目全空 → 走 v1 兜底注入。
type Style struct {
	ID            string               `json:"id"`
	Name          string               `json:"name"`               // 人话名「深空渐变（高饱和·宇宙系）」
	Direction     string               `json:"direction"`          // 方向描述（与项目 style_samples.direction 同源；迁移前=Name）
	Desc          string               `json:"desc,omitempty"`     // 一句话气质/来源
	Genre         string               `json:"genre,omitempty"`    // 适用题材
	Keywords      []string             `json:"keywords,omitempty"` // 检索/匹配词
	Tokens        map[string]any       `json:"tokens,omitempty"`   // 色板/字体/转场/后期（渲染与场景注入直读）
	Layouts       []Layout             `json:"layouts,omitempty"`  // 标准构图版式（场景生成按段轮换注入）
	Concept       string               `json:"concept,omitempty"`
	Fonts         *contract.FontRoles  `json:"fonts,omitempty"`         // 字体三角色
	Motion        *contract.MotionLang `json:"motion,omitempty"`        // 动效语言
	Device        *contract.DeviceSpec `json:"device,omitempty"`        // 贯穿装置
	AntiPatterns  []string             `json:"anti_patterns,omitempty"` // 负面清单
	Keyframes     []string             `json:"keyframes,omitempty"`     // 真迹关键帧文件名（style_media/<id>/ 下 frame-*.png 子集）
	Samples       []Sample             `json:"samples,omitempty"`       // 样张 1–3 张（所见即所得）
	Skin          *contract.Skin       `json:"skin,omitempty"`          // 风格文件（项目复用时落盘 skin.json）
	OriginProject string               `json:"origin_project,omitempty"`
	Published     bool                 `json:"published"`
	CreatedAt     time.Time            `json:"created_at"`
}

// Layout 标准构图版式（场景生成的版式纪律，轮换防每段同构）。
type Layout struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Desc string `json:"desc"`
}

// Sample 样张（与成片同一套 token，所见即所得）。
type Sample struct {
	Tag  string `json:"tag"`
	Desc string `json:"desc,omitempty"`
	HTML string `json:"html"`
}

type Library struct {
	dir string // data/library
	mu  sync.Mutex
}

func New(dataDir string) *Library {
	return &Library{dir: filepath.Join(dataDir, "library")}
}

func (l *Library) path() string { return filepath.Join(l.dir, "styles.json") }

// List 全部条目，新在前。首次访问自动完成旧双库（templates.json × registry/styles.json，
// 更早的 stylepacks.json 也认）→ styles.json 的迁移合并。
func (l *Library) List() ([]Style, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.listLocked()
}

func (l *Library) listLocked() ([]Style, error) {
	b, err := os.ReadFile(l.path())
	if os.IsNotExist(err) {
		styles := l.migrate()
		if styles != nil {
			if serr := l.save(styles); serr != nil {
				return nil, serr
			}
			return styles, nil
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var styles []Style
	if err := json.Unmarshal(b, &styles); err != nil {
		return nil, fmt.Errorf("styles.json 损坏: %w", err)
	}
	sort.Slice(styles, func(i, j int) bool { return styles[i].CreatedAt.After(styles[j].CreatedAt) })
	return styles, nil
}

// migrate 旧双库合并：registry 条目为骨（id/tokens/layouts/genre/keywords，id 被老项目
// style_id 引用必须保留），pack 为肉（desc/samples/skin/published）。同 direction 归并
// 为一条；各自落单的也进库（tokens 空的靠样张气质，samples 空的靠 tokens）。
func (l *Library) migrate() []Style {
	packs := l.legacyPacks()
	if packs == nil {
		if reg := l.legacyRegistry(); len(reg) == 0 {
			return nil // 两处皆空 = 全新环境，不动笔
		}
	}
	byDirection := map[string]int{}
	var styles []Style
	for _, reg := range l.legacyRegistry() {
		byDirection[reg.Direction] = len(styles)
		reg.Published = true // 注册表存在 = 已入卷
		if reg.CreatedAt.IsZero() {
			reg.CreatedAt = time.Now()
		}
		styles = append(styles, reg)
	}
	for _, p := range packs {
		if i, ok := byDirection[p.Direction]; ok {
			s := &styles[i] // 注册表为骨，pack 补肉
			if p.Desc != "" {
				s.Desc = p.Desc
			}
			if len(p.Samples) > 0 {
				s.Samples = p.Samples
			}
			if p.Skin != nil {
				s.Skin = p.Skin
			}
			s.Published = s.Published || p.Published
			if !p.CreatedAt.IsZero() {
				s.CreatedAt = p.CreatedAt
			}
			if s.OriginProject == "" {
				s.OriginProject = p.OriginProject
			}
			continue
		}
		// 无注册表对应：项目提炼包自成条目（tokens 空，靠样张气质）
		styles = append(styles, p)
	}
	return styles
}

// legacyPacks 旧模板包（templates.json，更早 stylepacks.json 兜底）→ 新条目。
// example_specs/assets 是 v1 死字段，不迁移。
func (l *Library) legacyPacks() []Style {
	for _, name := range []string{"templates.json", "stylepacks.json"} {
		b, err := os.ReadFile(filepath.Join(l.dir, name))
		if err != nil {
			continue
		}
		var old []struct {
			ID            string         `json:"id"`
			Name          string         `json:"name"`
			Direction     string         `json:"direction"`
			Desc          string         `json:"desc"`
			SampleHTML    string         `json:"sample_html"`
			SampleTag     string         `json:"sample_tag"`
			OriginProject string         `json:"origin_project"`
			Published     bool           `json:"published"`
			CreatedAt     time.Time      `json:"created_at"`
			Skin          *contract.Skin `json:"skin"`
		}
		if json.Unmarshal(b, &old) != nil {
			continue
		}
		var out []Style
		for _, p := range old {
			s := Style{
				ID: p.ID, Name: p.Name, Direction: p.Direction, Desc: p.Desc,
				OriginProject: p.OriginProject, Published: p.Published, CreatedAt: p.CreatedAt,
				Skin: p.Skin,
			}
			if s.Direction == "" {
				s.Direction = s.Name
			}
			if p.SampleHTML != "" {
				s.Samples = []Sample{{Tag: p.SampleTag, HTML: p.SampleHTML}}
			}
			out = append(out, s)
		}
		return out
	}
	return nil
}

// legacyRegistry 旧风格注册表（registry/styles.json）→ 新条目（photo 是 v1 配图概念，丢弃）。
func (l *Library) legacyRegistry() []Style {
	b, err := os.ReadFile(filepath.Join(l.dir, "registry", "styles.json"))
	if err != nil {
		return nil
	}
	var old []struct {
		ID        string         `json:"id"`
		Direction string         `json:"direction"`
		Genre     string         `json:"genre"`
		Keywords  []string       `json:"keywords"`
		Tokens    map[string]any `json:"tokens"`
		Layouts   []Layout       `json:"layouts"`
	}
	if json.Unmarshal(b, &old) != nil {
		return nil
	}
	var out []Style
	for _, e := range old {
		out = append(out, Style{
			ID: e.ID, Name: e.Direction, Direction: e.Direction, Genre: e.Genre,
			Keywords: e.Keywords, Tokens: e.Tokens, Layouts: e.Layouts,
		})
	}
	return out
}

// DraftFromProject 从已出片项目起草风格（Agent 自动，published=false 等用户确认）。
// 随包带走 skin + 全部样张（≤3）；style_samples 带 style_id 说明本就出自库内风格，
// 幂等返回该条目（复用链路不再重复起草）。同一项目重复起草同样幂等。
func (l *Library) DraftFromProject(projectDir, projectID, projectName string) (*Style, error) {
	var ss contract.StyleSamples
	b, err := os.ReadFile(filepath.Join(projectDir, "style", "style_samples.json"))
	if err != nil {
		return nil, fmt.Errorf("项目无风格样张: %w", err)
	}
	if err := json.Unmarshal(b, &ss); err != nil {
		return nil, err
	}
	if ss.Direction == "" || len(ss.Samples) == 0 {
		return nil, fmt.Errorf("风格样张不完整（direction/样本缺失）")
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	styles, err := l.listLocked()
	if err != nil {
		// 读失败绝不当空表处理：随后 save 会用单条新数据覆盖全库（清库事故）
		return nil, fmt.Errorf("风格库读取失败（不写入，保护现有库）: %w", err)
	}
	if ss.StyleID != "" {
		for i := range styles {
			if styles[i].ID == ss.StyleID {
				return &styles[i], nil // 库内风格出的片，无需再起草
			}
		}
	}
	for i := range styles {
		if styles[i].OriginProject == projectID {
			return &styles[i], nil // 已起草过
		}
	}

	s := Style{
		ID:            newID(styles),
		Name:          ss.Direction,
		Direction:     ss.Direction,
		Desc:          fmt.Sprintf("从「%s」成片提炼；含风格文件与全套样张", projectName),
		Skin:          ss.Skin,
		OriginProject: projectID,
		Published:     false,
		CreatedAt:     time.Now(),
	}
	for _, smp := range ss.Samples {
		if len(s.Samples) >= 3 {
			break
		}
		s.Samples = append(s.Samples, Sample{Tag: smp.Tag, Desc: smp.Desc, HTML: smp.HTML})
	}
	// skin 是样张阶段 LLM 同产、用户确认时锁定的（§12.2）；样张里有但 skin 缺位则不带
	if s.Skin != nil && s.Skin.Slots.BG == "" {
		s.Skin = nil
	}
	styles = append(styles, s)
	if err := l.save(styles); err != nil {
		return nil, err
	}
	return &s, nil
}

// VideoStyleIn 参考视频风格入库载荷（api 层从解析草稿变形；IngestVideoRef 消费）。
type VideoStyleIn struct {
	Name         string // 展示名（合集分段用段名）；空则与 direction 同源
	Direction    string
	Genre        string
	Keywords     []string
	Desc         string
	Tokens       map[string]any
	Layouts      []Layout
	Concept      string
	Fonts        *contract.FontRoles
	Motion       *contract.MotionLang
	Device       *contract.DeviceSpec
	AntiPatterns []string
	Keyframes    []string // 真迹关键帧文件名（style_media/<id>/ 下的 frame-*.png 子集）
	Samples      []Sample
}

// IngestVideoRef 参考视频解析结果入库（upsert，published=true）：
// 旧流程的 AppendRegistry + AddVideoPack 双写双库在此合为一次写入。
func (l *Library) IngestVideoRef(refID string, in VideoStyleIn) (*Style, error) {
	if in.Direction == "" || len(in.Samples) == 0 {
		return nil, fmt.Errorf("风格不完整（direction/样张缺失）")
	}
	name := in.Name
	if name == "" {
		name = in.Direction
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	styles, err := l.listLocked()
	if err != nil {
		return nil, fmt.Errorf("风格库读取失败（不写入，保护现有库）: %w", err)
	}
	id := videoStyleID(refID)
	for i := range styles {
		if styles[i].ID == id {
			// 已入库：不整条重写（用户可能改过），只补齐缺失面——老版本入库的条目
			// 没有五件套/真迹关键帧，重新解析确认同一个视频时在这里升级。
			up := &styles[i]
			if up.Concept == "" {
				up.Concept = in.Concept
			}
			if up.Fonts == nil {
				up.Fonts = in.Fonts
			}
			if up.Motion == nil {
				up.Motion = in.Motion
			}
			if up.Device == nil {
				up.Device = in.Device
			}
			if len(up.AntiPatterns) == 0 {
				up.AntiPatterns = in.AntiPatterns
			}
			if len(up.Keyframes) == 0 {
				up.Keyframes = in.Keyframes
			}
			if len(in.Tokens) > 0 && len(up.Tokens) == 0 {
				up.Tokens = in.Tokens
			}
			if len(in.Layouts) > 0 && len(up.Layouts) == 0 {
				up.Layouts = in.Layouts
			}
			err := l.save(styles)
			if err != nil {
				return nil, err
			}
			return up, nil
		}
	}
	s := Style{
		ID: id, Name: name, Direction: in.Direction, Desc: in.Desc,
		Genre: in.Genre, Keywords: in.Keywords, Tokens: in.Tokens, Layouts: in.Layouts,
		Concept: in.Concept, Fonts: in.Fonts, Motion: in.Motion, Device: in.Device,
		AntiPatterns: in.AntiPatterns, Keyframes: in.Keyframes,
		Samples: in.Samples, OriginProject: "video:" + refID,
		Published: true, CreatedAt: time.Now(),
	}
	styles = append(styles, s)
	if err := l.save(styles); err != nil {
		return nil, err
	}
	return &s, nil
}

// Publish 用户确认入库。
func (l *Library) Publish(id string) (*Style, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	styles, err := l.listLocked()
	if err != nil {
		return nil, err
	}
	for i := range styles {
		if styles[i].ID == id {
			styles[i].Published = true
			return &styles[i], l.save(styles)
		}
	}
	return nil, fmt.Errorf("风格库没有 %s", id)
}

// UpdateEntry 单条修改（回填/升级类作业用）：读失败绝不落盘（防损坏清库），
// fn 里只改该条目，成功才整库原子写回。
func (l *Library) UpdateEntry(id string, fn func(*Style) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	styles, err := l.listLocked()
	if err != nil {
		return fmt.Errorf("风格库读取失败（不写入，保护现有库）: %w", err)
	}
	for i := range styles {
		if styles[i].ID == id {
			if err := fn(&styles[i]); err != nil {
				return err
			}
			return l.save(styles)
		}
	}
	return fmt.Errorf("风格库没有 %s", id)
}

// Get 取单个条目（新建项目选用后查详情/复用）。
func (l *Library) Get(id string) (*Style, bool) {
	styles, _ := l.List()
	for i := range styles {
		if styles[i].ID == id {
			return &styles[i], true
		}
	}
	return nil, false
}

// ByDirection 方向名精确匹配（style_id 缺位的旧项目/默认包兜底锚定）。
func (l *Library) ByDirection(direction string) (*Style, bool) {
	if strings.TrimSpace(direction) == "" {
		return nil, false
	}
	styles, _ := l.List()
	for i := range styles {
		if styles[i].Direction == direction {
			return &styles[i], true
		}
	}
	return nil, false
}

// StyleSamplesJSON 把风格变成本项目可用的 style_samples.json 内容
// （style_id 随行——后续场景生成/拼接/再提炼都靠它精确锚定本条目；
// skin 一并携带——确认时落盘 skin.json；confirmed 由调用方改写）。
// 设计系统五件套 + 版式一并带出：fonts/motion/device 任一非空即让项目端
// hasDesignSystem 成立，DesignDoc 注入/版式轮换/画面质检全套走 v2 通道。
func (s *Style) StyleSamplesJSON() []byte {
	skin := s.Skin
	if skin == nil {
		// 条目没存皮肤（视频解析/老项目起草路径）→ 从 tokens 机械派生。
		// v2 制作闸硬性要求皮肤，库内包不能在这里断粮；派生是纯函数，确定性与 tokens 一致。
		skin = SkinFromTokens(s.Direction, s.Tokens)
	}
	out := contract.StyleSamples{
		StyleID:      s.ID,
		Direction:    s.Direction,
		Desc:         s.Desc,
		Concept:      s.Concept,
		Fonts:        s.Fonts,
		Motion:       s.Motion,
		Device:       s.Device,
		AntiPatterns: s.AntiPatterns,
		Samples:      make([]contract.Sample, 0, len(s.Samples)),
		Skin:         skin,
		Confirmed:    false,
	}
	for _, l := range s.Layouts {
		out.Layouts = append(out.Layouts, contract.DesignLayout{ID: l.ID, Name: l.Name, Desc: l.Desc})
	}
	for _, smp := range s.Samples {
		d := smp.Desc
		if d == "" {
			d = "风格库复用样张"
		}
		out.Samples = append(out.Samples, contract.Sample{Tag: smp.Tag, Desc: d, HTML: smp.HTML})
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return b
}

func (l *Library) save(styles []Style) error {
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(styles, "", "  ")
	tmp := l.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, l.path())
}

// newID 秒级时间戳 id；同秒并发撞号则追加序号。
func newID(styles []Style) string {
	id := "sp" + time.Now().Format("20060102-150405")
	taken := map[string]bool{}
	for _, s := range styles {
		taken[s.ID] = true
	}
	for n := 2; taken[id]; n++ {
		id = fmt.Sprintf("sp%s-%d", time.Now().Format("20060102-150405"), n)
	}
	return id
}

// videoStyleID 参考视频风格 id（vid- 前缀，文件名安全化；原 styleref.RegistryID 语义）。
// VideoStyleID 是它的导出版：api 侧封面样张的 <img src> 需要预知入库后的条目 id，
// 与 IngestVideoRef 的赋 id 逻辑同源，防两处手工复算漂移。
func videoStyleID(refID string) string {
	sl := regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(strings.ToLower(refID), "-")
	return "vid-" + strings.Trim(sl, "-")
}

// VideoStyleID 参考视频风格条目 id（IngestVideoRef 对同一 seed 赋的正是这个 id）。
func VideoStyleID(refID string) string { return videoStyleID(refID) }
