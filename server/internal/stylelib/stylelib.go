// Package stylelib：风格库 v3（docs/style-library-v3.md）——「策展目录 + 绝活收藏夹」，
// 云存储见 docs/style-library-cloud-storage.md。
//
// 一个条目 = 一套标签（引用模型固有知识，不提取教学）+ 特色清单（参考片真正独有
// 的手法：运镜/转场/母题/动效/素材/配色，机制化写作，制作时整包生效、单条可关）
// + 证据（媒体快照：打标证据、QA 考卷、回看素材）。
// Subject 是原片题材一句话，只供检索，永不进生成提示词（题材中毒的口子）。
//
// 存储：账（条目+词表）进 catalog 后端（PG 0005 表或 data/library/*.json 文件回退），
// 货（媒体）进 MediaStore 后端（MinIO 或本地目录）。后端按 .env 配置自由组合，
// 化石导入见 Migrate。URL 永不入库——payload 里只存文件名，前缀拼装收在后端。
package stylelib

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"webpage-video-agent/server/internal/contract"
)

// Style 风格条目 v3：标签 + 特色 + 证据 + 溯源。
// 相对 v2 删除 direction/desc/genre/keywords/tokens/layouts/concept/fonts/motion/
// device/anti_patterns/keyframes/samples/skin——公认风格交给模型固有知识（标签），
// 独有手法交给特色，设计系统由各片设计阶段现发明（强参考走 style_brief.json）。
// v3.1（prep-agents-design #8）新增：分面标签（风格工位检索轴）、衍生出处与
// 未验证标签（治理：用过它的片出片才转正）、使用统计（数据驱动选型+⑤记账闭环）。
type Style struct {
	ID            string                 `json:"id"` // vid-<refID>[-sNN] | sp<ts>
	Name          string                 `json:"name"`
	Tags          contract.StyleTags     `json:"tags"`
	TagEvidence   []contract.TagEvidence `json:"tag_evidence,omitempty"`   // 每个 tag 至少一条
	Signatures    []contract.Signature   `json:"signatures,omitempty"`     // 0–6 条
	Subject       string                 `json:"subject,omitempty"`        // 原片题材一句话，只供检索，永不进生成提示词
	OriginProject string                 `json:"origin_project,omitempty"` // "video:<refID>" 或项目 id
	Published     bool                   `json:"published"`
	CreatedAt     time.Time              `json:"created_at"`

	Facets      StyleFacets `json:"facets,omitempty"`       // 分面：情绪/色调/密度（题材=Subject）
	DerivedFrom string      `json:"derived_from,omitempty"` // 衍生出处（register_style 自动带）
	Unverified  bool        `json:"unverified,omitempty"`   // 未验证：用过它的片出片后才转正
	Usage       UsageStats  `json:"usage,omitempty"`        // 使用统计（出片记账；分数来自审核）

	// DistillPending 待蒸馏标记（蒸馏方案 §3.1 懒触发）：解析入库时置位，提示
	// 该条目有真迹可开蒸馏单；不自动触发——风格快常用了才值得花这笔探索成本。
	// kit 落库（AttachKit）时清除；重解析覆盖式重写会重新置位。
	DistillPending bool `json:"distill_pending,omitempty"`

	// Kit 可执行画材包（docs/style-kit-distillation-plan §6）：蒸馏工位产物。
	// nil = 未蒸馏（现状全部条目），新老互不干扰；payload JSONB 加字段零迁移。
	// 制作期画面工位注入 CheatSheet（小抄）替代整篇画法描述；Files 只存文件名，
	// 正本住 MediaStore style_media/<id>/kit/，URL 永不入库。
	Kit *StyleKit `json:"kit,omitempty"`
}

// StyleKit 画材包：还原过关才进库的可执行代码资产，不是提取文本（防 v2 覆辙）。
type StyleKit struct {
	Framework  string        `json:"framework"`             // 用了什么框架 + 一句话为什么（选型本身是风格知识）
	Deps       []StyleKitDep `json:"deps,omitempty"`        // 依赖（精确版本；零依赖为空）
	Files      []string      `json:"files,omitempty"`       // 画材包代码文件名（kit.js/scenes.js/...，正本在媒体后端）
	CheatSheet string        `json:"cheat_sheet,omitempty"` // API 小抄（≤~2k token）——画面工位提示词真正注入的东西
	Score      StyleKitScore `json:"score,omitempty"`       // 判卷记录：入库门槛依据，带伤必标分
	Notes      string        `json:"notes,omitempty"`       // 配方笔记（路线 + 已知弱点，≤14 行）
}

// StyleKitDep 依赖清单条目：版本钉死（@latest 不许进，毕业检查第①条）。
type StyleKitDep struct {
	Name    string `json:"name"`              // 包名（零依赖纯 Canvas 时为空表）
	Version string `json:"version,omitempty"` // 精确版本
	License string `json:"license,omitempty"` // 许可证（GPL 类商用小心，毕业检查第③条）
}

// StyleKitScore 蒸馏判卷分：reproduce=还原分（真迹对照四维）、neutral=换题分
// （防描红）、rounds=迭代轮数（预算证据）。分制由判卷官口径带出（如 40 分制）。
type StyleKitScore struct {
	Reproduce string  `json:"reproduce,omitempty"` // 如 "A 34/40 · B 31.5/40 · C 31/40（终审）"
	Neutral   float64 `json:"neutral,omitempty"`   // 换题样图风格保持度 0-100
	Rounds    int     `json:"rounds,omitempty"`    // 判卷迭代轮数
}

// StyleFacets 分面标签：风格工位 search_styles 的检索轴（prep-agents-design §3.2）。
// 自由短语而非枚举——分面是检索线索，不是校验词表。
type StyleFacets struct {
	Mood    string `json:"mood,omitempty"`    // 情绪（如 沉稳/明快/悬疑）
	Tone    string `json:"tone,omitempty"`    // 色调（如 低饱和暖色/黑金/冷蓝）
	Density string `json:"density,omitempty"` // 密度（如 疏朗/中密/高密）
}

// UsageStats 使用统计：本片风格包选型时的数据驱动依据；⑤审核通过记账回写，
// 形成「②探索 ← ⑤记账」闭环。分数 0–5（0 = 未评）。
type UsageStats struct {
	Count      int       `json:"count,omitempty"`      // 被项目选用的次数
	Passes     int       `json:"passes,omitempty"`     // 产出成片（通过制作全部门禁）次数
	LastScore  float64   `json:"last_score,omitempty"` // 最近一次审核分（0–5）
	LastUsedAt time.Time `json:"last_used_at,omitempty"`
}

// ErrNoEntry 条目不存在（Publish/Delete 命中时返回；api 层据此回 404）。
var ErrNoEntry = errors.New("风格库没有该条目")

// Library 风格库：账本后端（cat）+ 媒体后端（media）的组合。
// mu 串行化「读-查重-写」复合操作（词表查重、解析入库幂等）——行级 upsert 管单行
// 原子性，管不了查重序列；跨进程的同名并发由单用户场景兜底。
type Library struct {
	cat   catalog
	media MediaStore
	dir   string // data/library：fileCatalog 的家、化石路径、references 回退定位基准
	mu    sync.Mutex
}

// New 纯本地后端（现状行为）：账在 data/library/*.json、媒体在 style_media/ 本地目录。
// 测试、demo 容器、没配 PG 的环境用。
func New(dataDir string) *Library {
	return NewWith(dataDir, nil, nil)
}

// NewWith 组合后端（main 接线）：db 非 nil → 账走 PG（0005 表，Migrate 幂等自建），
// 否则文件回退；media nil → 本地目录。两维各自独立选型，化石导入按各维配置生效。
func NewWith(dataDir string, db *gorm.DB, media MediaStore) *Library {
	dir := filepath.Join(dataDir, "library")
	l := &Library{dir: dir, media: media}
	if l.media == nil {
		l.media = newDirMedia(dir)
	}
	if db != nil {
		l.cat = &pgCatalog{db: db}
	} else {
		l.cat = &fileCatalog{dir: dir}
	}
	return l
}

// MediaDir 风格媒体本地目录（本地后端=正本；MinIO 后端=Localize 读穿缓存根）。
// 测试与排查用；业务读取一律走 OpenMedia/Localize。
func (l *Library) MediaDir(id string) string {
	return filepath.Join(l.dir, "style_media", id)
}

// List 全部条目，新在前。文件后端加载即校验：schema 不合格的条目跳过并喊出来
// （写入侧已校验，能走到这里的通常是手改）。缺帧剪枝已随云存储退役——引用缺失
// 由前端单帧 404 兜底，不再逐帧探测拖慢列表。
func (l *Library) List() ([]Style, error) {
	styles, err := l.cat.ListStyles()
	if err != nil {
		return nil, err
	}
	sort.Slice(styles, func(i, j int) bool { return styles[i].CreatedAt.After(styles[j].CreatedAt) })
	return styles, nil
}

// Get 取单个条目（新建项目选用后查详情/复用）。走 catalog 单条读（2026-10-19
// 中-29：旧版全量 List，PG 后端每查一条 = 整表 SELECT + 逐行反序列化）。
// 账本读失败时按不存在处理，但必须把失败喊出来——静默返回会让人以为条目丢了。
func (l *Library) Get(id string) (*Style, bool) {
	if l.cat == nil {
		return nil, false
	}
	s, err := l.cat.GetStyle(id)
	if err != nil {
		if !errors.Is(err, ErrNoEntry) {
			log.Printf("[stylelib] 账本读取失败，查询按不存在处理: %v", err)
		}
		return nil, false
	}
	return s, true
}

// VideoStyleIn 参考解析风格入库载荷 v3（api 层从解析草稿变形；IngestVideoRef 消费）。
type VideoStyleIn struct {
	Name        string
	Subject     string // 原片题材一句话（浏览/检索用，不进生成提示词）
	Facets      StyleFacets
	Tags        contract.StyleTags
	TagEvidence []contract.TagEvidence
	Signatures  []contract.Signature // ID 允许缺省，入库时统一编 sig1..sigN
	Origin      string               // OriginProject 出处：空 = "video:"+refID（上传视频解析路）；项目解析路传项目 ID
}

// IngestVideoRef 参考解析风格入库（upsert，published=true）。两路解析共用：
// 上传视频路（seed=refID）与项目解析路（api 层在 Origin 传真实项目 ID，条目可
// 追溯出处）。校验 §4.1 全规则（含词表核对）；同 seed 重复入库覆盖式重写（v3
// 条目都是解析产物，重解析=用户对同一来源再来一次，旧值不保——确认页已让人工过目）。
func (l *Library) IngestVideoRef(refID string, in VideoStyleIn) (*Style, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	styles, err := l.cat.ListStyles()
	if err != nil {
		return nil, err
	}
	origin := in.Origin
	if origin == "" {
		origin = "video:" + refID
	}
	s := Style{
		ID:             videoStyleID(refID),
		Name:           uniqueName(styles, videoStyleID(refID), strings.TrimSpace(in.Name), in.Subject),
		Tags:           in.Tags,
		TagEvidence:    in.TagEvidence,
		Subject:        in.Subject,
		Facets:         in.Facets,
		OriginProject:  origin,
		Published:      true,
		CreatedAt:      time.Now(),
		DistillPending: true,
	}
	// 覆盖式重写不烧画材包：重解析换的是标签/特色（人的新过目），Kit 是蒸馏工位
	// 另一条生产线攒下的可执行资产，跟重解析无关——留着，且不再挂待蒸馏标记。
	if old, err := l.cat.GetStyle(s.ID); err == nil && old != nil && old.Kit != nil {
		s.Kit = old.Kit
		s.DistillPending = false
	}
	s.Signatures = renumberSigs(in.Signatures)
	if err := l.validateWrite(&s); err != nil {
		return nil, err
	}
	if err := l.cat.PutStyle(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

// uniqueName 陈列名消歧（2026-10-24）：条目名不再绑定主标签家族名/视频标题，
// LLM 起的独特名同名撞车真实存在——撞名时先带题材词消歧，仍撞追加序号。
// selfID 是 upsert 覆盖场景的自身条目（重入库同名不该给自己加后缀），新建传空。
func uniqueName(styles []Style, selfID, name, subject string) string {
	taken := map[string]bool{}
	for _, st := range styles {
		if st.ID != selfID {
			taken[st.Name] = true
		}
	}
	if name == "" || !taken[name] {
		return name
	}
	base := name + "(" + contract.FirstRunes(subject, 8) + ")"
	if !taken[base] {
		return base
	}
	for n := 2; ; n++ {
		cand := fmt.Sprintf("%s %d", base, n)
		if !taken[cand] {
			return cand
		}
	}
}

// validateWrite 写入校验（§4.1）：tags 在词表、signatures ≤6/kind 枚举/mechanism 非空。
// 词表从账本现读——校验职责在 Go，词表演进不改表约束。
func (l *Library) validateWrite(s *Style) error {
	v, err := l.LoadVocab()
	if err != nil {
		return fmt.Errorf("词表读取失败: %w", err)
	}
	if errs := validateEntryVocab(s, v); len(errs) > 0 {
		return fmt.Errorf("风格条目不合格: %s", strings.Join(errs, "; "))
	}
	return nil
}

// renumberSigs 特色统一编号 sig1..sigN（条目内唯一由 ValidateSignatures 把关）。
func renumberSigs(sigs []contract.Signature) []contract.Signature {
	out := make([]contract.Signature, len(sigs))
	copy(out, sigs)
	for i := range out {
		out[i].ID = fmt.Sprintf("sig%d", i+1)
	}
	return out
}

// Publish 用户确认入库。
func (l *Library) Publish(id string) (*Style, error) {
	return l.cat.MarkPublished(id)
}

// AttachKit 蒸馏工位回写画材包（docs/style-kit-distillation-plan §6）：账本只记
// 清单（Files 是文件名，正本已先经 MediaStore 落位），同时摘掉待蒸馏标记。
// kit 文件本体由调用方（prep 蒸馏监工）走 PutMedia 存进 style_media/<id>/kit/。
func (l *Library) AttachKit(id string, kit *StyleKit) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, err := l.cat.GetStyle(id)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrNoEntry, id)
	}
	st.Kit = kit
	st.DistillPending = false
	return l.cat.PutStyle(st)
}

// PutMedia 媒体写入的通用口子（蒸馏工位专用扩展名走这里；参考快照仍走
// SnapshotMedia）。name 必须过 ValidMediaName（含 kit/ 子树与 repro/neutral 样图）。
func (l *Library) PutMedia(styleID, name, srcPath string) error {
	if !ValidMediaName(name) {
		return fmt.Errorf("媒体名 %q 不在白名单", name)
	}
	return l.media.Put(styleID, name, srcPath)
}

// DistillDir 蒸馏任务目录根（data/library/distill/<entryID>-<ts>/）：
// 考卷袋 in/、交付 out/、判卷与留痕都住这里，与媒体后端无关（宿主本地盘）。
func (l *Library) DistillDir() string { return filepath.Join(l.dir, "distill") }

// Delete 删除条目并清掉它的媒体。已出片项目不受影响——简报在选用时早已拷进
// 项目各自的 style/，删的只是「以后新建项目还能不能选它」。先账后货：删账是
// 提交点，媒体前缀清不净只是孤儿对象（桶 versioning 是后悔药）。
func (l *Library) Delete(id string) error {
	if err := l.cat.DeleteStyle(id); err != nil {
		return err
	}
	_ = l.media.DeletePrefix(id)
	return nil
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
func videoStyleID(refID string) string {
	sl := regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(strings.ToLower(refID), "-")
	return "vid-" + strings.Trim(sl, "-")
}

// validateEntry schema 校验（不查文件实存——写入方对着源目录做）：tags.main 必填；
// aux ≤2；signatures ≤6、kind 枚举、mechanism 非空。词表核对由带词表的封装补上。
func validateEntry(s *Style) []string {
	return validateEntryVocab(s, nil)
}

func validateEntryVocab(s *Style, vocab *Vocab) []string {
	var errs []string
	if strings.TrimSpace(s.Name) == "" {
		errs = append(errs, "name 为空")
	}
	if strings.TrimSpace(s.Tags.Main) == "" {
		errs = append(errs, "tags.main 为空")
	} else if vocab != nil && !vocab.Has(s.Tags.Main) {
		errs = append(errs, "tags.main="+s.Tags.Main+" 不在词表")
	}
	if len(s.Tags.Aux) > 2 {
		errs = append(errs, "tags.aux 超 2 个")
	}
	for _, a := range s.Tags.Aux {
		if vocab != nil && a != "" && !vocab.Has(a) {
			errs = append(errs, "tags.aux="+a+" 不在词表")
		}
	}
	if err := ValidateSignatures(s.Signatures); err != nil {
		errs = append(errs, err.Error())
	}
	return errs
}

// ValidateSignatures 特色清单校验：≤6、id 条目内唯一、kind 枚举、name/mechanism 非空。
// frames 允许为空（项目解析路草稿，样张帧由系统挂/人工在确认页补）。
func ValidateSignatures(sigs []contract.Signature) error {
	if len(sigs) > 6 {
		return fmt.Errorf("signatures %d 条超上限（≤6）", len(sigs))
	}
	seen := map[string]bool{}
	for i := range sigs {
		sg := &sigs[i]
		if strings.TrimSpace(sg.ID) == "" {
			sg.ID = fmt.Sprintf("sig%d", i+1) // 兜底补号（入库路径正常都会带）
		}
		if seen[sg.ID] {
			return fmt.Errorf("signature id %q 重复", sg.ID)
		}
		seen[sg.ID] = true
		if !contract.ValidSignatureKind(sg.Kind) {
			return fmt.Errorf("signature %q 的 kind=%q 越界（可选：%s）", sg.ID, sg.Kind, contract.SignatureKindNames())
		}
		if strings.TrimSpace(sg.Name) == "" {
			return fmt.Errorf("signature %q 缺 name", sg.ID)
		}
		if strings.TrimSpace(sg.Mechanism) == "" {
			return fmt.Errorf("signature %q 缺 mechanism（机制描述）", sg.ID)
		}
	}
	return nil
}

// ── 媒体（api 回源 + 快照）─────────────────────────────────

// SnapshotMedia 把 sourceDir（参考视频目录：video.mp4 + frame-*.png）快照进风格媒体
// 后端。部分成功不回滚：媒体是冗余不是事实源，缺哪张下次重新确认/手动补即可。
// 幂等：已存在的跳过（MinIO 免重传）；cover 例外——代表帧随重入库变化，每次覆盖。
func (l *Library) SnapshotMedia(styleID, sourceDir string) error {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return fmt.Errorf("读参考视频目录: %w", err)
	}
	var firstErr error
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !ValidMediaName(name) {
			continue
		}
		if name != "cover.png" && l.media.Stat(styleID, name) {
			continue // 已快照过
		}
		if err := l.media.Put(styleID, name, filepath.Join(sourceDir, name)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// MediaNames 风格媒体抽帧清单 + 是否带原视频（库页详情抽屉）。
// 空（老条目/快照失败）回退 origin 指向的参考视频目录。
func (l *Library) MediaNames(id string) (frames []string, hasVideo bool) {
	if !isSlugName(id) {
		return nil, false
	}
	if names, err := l.media.List(id); err == nil {
		for _, name := range names {
			switch {
			case ValidMediaName(name) && strings.HasPrefix(name, "frame-"):
				frames = append(frames, name)
			case name == "video.mp4":
				hasVideo = true
			}
		}
	}
	if len(frames) == 0 && !hasVideo { // 抽帧缺失就回退（快照只带 video 时也要能列出参考抽帧）
		if dir, ok := l.refMediaDir(id); ok {
			frames2, hv2 := scanDirNames(dir)
			frames = frames2
			hasVideo = hasVideo || hv2
		}
	}
	return frames, hasVideo
}

// OpenMedia 媒体本体（库页回看/封面/证据）。ok=false → 调用方回 404。
// MinIO 后端直读对象流（不经本地缓存——回看是低频大文件）；缺文件回退参考目录。
func (l *Library) OpenMedia(id, name string) (rc io.ReadSeekCloser, size int64, modTime time.Time, ok bool) {
	if !isSlugName(id) || !ValidMediaName(name) {
		return nil, 0, time.Time{}, false
	}
	if r, n, mt, err := l.media.Open(id, name); err == nil {
		return r, n, mt, true
	}
	if dir, ok := l.refMediaDir(id); ok {
		f, err := os.Open(filepath.Join(dir, name))
		if err == nil {
			st, err := f.Stat()
			if err == nil && !st.IsDir() {
				return f, st.Size(), st.ModTime(), true
			}
			f.Close()
		}
	}
	return nil, 0, time.Time{}, false
}

// refMediaDir 老条目的参考视频目录回退（origin_project="video:<refID>"，
// media 里没有快照时直接看 references）。返回目录路径。
func (l *Library) refMediaDir(id string) (string, bool) {
	st, ok := l.Get(id)
	if !ok || !strings.HasPrefix(st.OriginProject, "video:") {
		return "", false
	}
	return filepath.Join(l.dir, "..", "references", strings.TrimPrefix(st.OriginProject, "video:")), true
}

// scanDirNames 目录文件名清单（参考视频目录恒在本地盘，与媒体后端无关）。
func scanDirNames(dir string) (frames []string, hasVideo bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch name := e.Name(); {
		case ValidMediaName(name) && strings.HasPrefix(name, "frame-"):
			frames = append(frames, name)
		case name == "video.mp4":
			hasVideo = true
		}
	}
	return frames, hasVideo
}
