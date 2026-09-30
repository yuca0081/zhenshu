// Package methodlib：模板库 v2（asset-library.md §12.3，前称方法库/StylePack）。
//
// 模板 = 入库的风格（published）+ 配套参考包：风格文件（skin：槽位+自由CSS）+
// 样张 + 示例 spec（1–2 份，spec 阶段注入仿写）+ 配套素材 id 清单。
// 「皮肤定死、编排仿照、内容自由」——用同一模板出的片子同一套皮肤、相似气质、
// 完全不同的内容编排。
//
// 条目存 data/library/templates.json（磁盘为事实源）：
// 成片出片时 Agent 起草（published=false）→ 用户确认入库（published=true）
// → 新建项目选用 → 风格文件拷进项目 + spec 阶段示例仿写。
// 旧 stylepacks.json 读取时自动迁移（读旧写新一次完成）。
package methodlib

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"webpage-video-agent/server/internal/contract"
)

// StylePack 模板条目。v1 字段保留（样张/溯源），v2 增风格文件与参考包。
type StylePack struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Direction     string          `json:"direction"` // 与 style_samples.direction 同源
	Desc          string          `json:"desc"`
	SampleHTML    string          `json:"sample_html"` // 首张样张（所见即所得）
	SampleTag     string          `json:"sample_tag"`
	OriginProject string          `json:"origin_project"`
	Published     bool            `json:"published"`
	CreatedAt     time.Time       `json:"created_at"`
	Skin          *contract.Skin  `json:"skin,omitempty"`        // 风格文件（槽位+自由CSS；项目自有皮肤走 §12.2 主链路）
	ExampleSpecs  []string        `json:"example_specs,omitempty"` // 示例 spec JSON（≤2 份，spec 阶段注入仿写）
	Assets        []string        `json:"assets,omitempty"`       // 配套素材 id（lib: 引用清单，新项目可复用）
}

type Library struct {
	dir string // data/library
}

func New(dataDir string) *Library {
	return &Library{dir: filepath.Join(dataDir, "library")}
}

func (l *Library) path() string { return filepath.Join(l.dir, "templates.json") }

// legacyPath 旧文件（P2 前称 stylepacks）；templates.json 缺失时读取并迁移。
func (l *Library) legacyPath() string { return filepath.Join(l.dir, "stylepacks.json") }

// List 全部条目，新在前。首次访问自动完成 stylepacks.json → templates.json 迁移。
func (l *Library) List() ([]StylePack, error) {
	var packs []StylePack
	b, err := os.ReadFile(l.path())
	if os.IsNotExist(err) {
		if lb, lerr := os.ReadFile(l.legacyPath()); lerr == nil { // 一次性迁移：读旧写新
			if json.Unmarshal(lb, &packs) == nil {
				_ = l.save(packs)
				return packs, nil
			}
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &packs); err != nil {
		return nil, fmt.Errorf("templates.json 损坏: %w", err)
	}
	sort.Slice(packs, func(i, j int) bool { return packs[i].CreatedAt.After(packs[j].CreatedAt) })
	return packs, nil
}

// DraftFromProject 从已出片项目起草模板（Agent 自动，published=false 等用户确认）。
// v2：随包带走风格文件（style/skin.json）+ 示例 spec + 配套素材清单——
// 有 skin 的模板在渲染层走项目自有皮肤（§12.2），未命中注册表也不会静默回退。
// 同一项目重复起草 = 幂等返回已有条目。
func (l *Library) DraftFromProject(projectDir, projectID, projectName string) (*StylePack, error) {
	var ss struct {
		Direction string `json:"direction"`
		Samples   []struct {
			Tag  string `json:"tag"`
			HTML string `json:"html"`
		} `json:"samples"`
	}
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

	packs, _ := l.List()
	for i := range packs {
		if packs[i].OriginProject == projectID {
			return &packs[i], nil // 已起草过
		}
	}

	pack := StylePack{
		ID:            newPackID(packs),
		Name:          ss.Direction,
		Direction:     ss.Direction,
		Desc:          fmt.Sprintf("从「%s」成片提炼；含风格文件/示例 spec/配套素材清单", projectName),
		SampleHTML:    ss.Samples[0].HTML,
		SampleTag:     ss.Samples[0].Tag,
		OriginProject: projectID,
		Published:     false,
		CreatedAt:     time.Now(),
	}
	// 风格文件：项目确认时锁定的 skin.json（§12.2 主链路产物）
	if sb, serr := os.ReadFile(filepath.Join(projectDir, "style", "skin.json")); serr == nil {
		var skin contract.Skin
		if json.Unmarshal(sb, &skin) == nil && skin.Slots.BG != "" {
			pack.Skin = &skin
		}
	}
	// 示例 spec：挑最紧凑的 2 份（信息密度高、token 省），单份截 3200 字符
	type se struct {
		path string
		size int
	}
	var specs []se
	if files, gerr := filepath.Glob(filepath.Join(projectDir, "llm", "comp-*.spec.json")); gerr == nil {
		for _, f := range files {
			if fi, serr := os.Stat(f); serr == nil {
				specs = append(specs, se{f, int(fi.Size())})
			}
		}
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].size < specs[j].size })
	for _, s := range specs[:min(2, len(specs))] {
		if sb, serr := os.ReadFile(s.path); serr == nil {
			t := string(sb)
			if len(t) > 3200 {
				t = t[:3200]
			}
			pack.ExampleSpecs = append(pack.ExampleSpecs, t)
		}
	}
	// 配套素材清单：全片 spec 里引用的 lib: id（去重，≤40）
	seen := map[string]bool{}
	if files, _ := filepath.Glob(filepath.Join(projectDir, "llm", "comp-*.spec.json")); files != nil {
		for _, f := range files {
			fb, _ := os.ReadFile(f)
			for _, m := range strings.Split(string(fb), "\"") {
				if id, ok := strings.CutPrefix(m, "lib:"); ok && !seen[id] {
					seen[id] = true
					pack.Assets = append(pack.Assets, id)
				}
			}
		}
	}
	if len(pack.Assets) > 40 {
		pack.Assets = pack.Assets[:40]
	}
	packs = append(packs, pack)
	if err := l.save(packs); err != nil {
		return nil, err
	}
	return &pack, nil
}

// newPackID 秒级时间戳 id：两个起草入口（成片提炼/视频解析）同秒并发会撞号
// （templates.json 出现重复主键，后续按 id 定位错乱）——已有同号则追加序号。
func newPackID(packs []StylePack) string {
	id := "sp" + time.Now().Format("20060102-150405")
	taken := map[string]bool{}
	for _, p := range packs {
		taken[p.ID] = true
	}
	for n := 2; taken[id]; n++ {
		id = fmt.Sprintf("sp%s-%d", time.Now().Format("20060102-150405"), n)
	}
	return id
}

// AddVideoPack 从参考视频解析结果起草模板（published=false 等用户确认）。
// 同一引用重复确认 = 幂等返回已有条目。视频解析风格的渲染走注册表 tokens
// （AppendRegistry 同步入 styles.json），故此处不带 skin。
func (l *Library) AddVideoPack(refID, name, direction, desc, sampleHTML, sampleTag string) (*StylePack, error) {
	if direction == "" || sampleHTML == "" {
		return nil, fmt.Errorf("模板不完整（direction/样张缺失）")
	}
	packs, _ := l.List()
	origin := "video:" + refID
	for i := range packs {
		if packs[i].OriginProject == origin {
			return &packs[i], nil
		}
	}
	pack := StylePack{
		ID:            newPackID(packs),
		Name:          name,
		Direction:     direction,
		Desc:          desc,
		SampleHTML:    sampleHTML,
		SampleTag:     sampleTag,
		OriginProject: origin,
		Published:     false,
		CreatedAt:     time.Now(),
	}
	packs = append(packs, pack)
	if err := l.save(packs); err != nil {
		return nil, err
	}
	return &pack, nil
}

// Publish 用户确认入库。
func (l *Library) Publish(id string) (*StylePack, error) {
	packs, err := l.List()
	if err != nil {
		return nil, err
	}
	for i := range packs {
		if packs[i].ID == id {
			packs[i].Published = true
			return &packs[i], l.save(packs)
		}
	}
	return nil, fmt.Errorf("模板库没有 %s", id)
}

// Get 取单个条目（新建项目选用后查详情）。
func (l *Library) Get(id string) (*StylePack, bool) {
	packs, _ := l.List()
	for i := range packs {
		if packs[i].ID == id {
			return &packs[i], true
		}
	}
	return nil, false
}

// StyleSamplesJSON 把模板变成本项目可用的 style_samples.json 内容
//（confirmed=false 等用户点头；skin 一并携带——applyStyle 落盘 skin.json）。
func (p *StylePack) StyleSamplesJSON() []byte {
	type sample struct {
		Tag  string `json:"tag"`
		Desc string `json:"desc"`
		HTML string `json:"html"`
	}
	out := struct {
		Direction string           `json:"direction"`
		Desc      string           `json:"desc"`
		Samples   []sample         `json:"samples"`
		Skin      *contract.Skin   `json:"skin,omitempty"`
		Confirmed bool             `json:"confirmed"`
	}{
		Direction: p.Direction,
		Desc:      p.Desc,
		Samples:   []sample{{Tag: p.SampleTag, Desc: "模板复用样张", HTML: p.SampleHTML}},
		Skin:      p.Skin,
		Confirmed: false,
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return b
}

func (l *Library) save(packs []StylePack) error {
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(packs, "", "  ")
	tmp := l.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, l.path())
}
