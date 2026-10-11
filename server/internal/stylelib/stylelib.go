// Package stylelib：风格库 v3（docs/style-library-v3.md）——「策展目录 + 绝活收藏夹」。
//
// 一个条目 = 一套标签（引用模型固有知识，不提取教学）+ 特色清单（参考片真正独有
// 的手法：运镜/转场/母题/动效/素材/配色，机制化写作，制作时整包生效、单条可关）
// + 证据（style_media/<id>/ 原片与抽帧快照：打标证据、QA 考卷、回看素材）。
// Subject 是原片题材一句话，只供检索，永不进生成提示词（题材中毒的口子）。
//
// 条目存 data/library/styles.json（磁盘为事实源，空库零迁移）：
// 视频解析入库（published=true）或成片反哺起草（published=false，人工在库页确认）。
package stylelib

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"webpage-video-agent/server/internal/contract"
)

// Style 风格条目 v3：标签 + 特色 + 证据 + 溯源。
// 相对 v2 删除 direction/desc/genre/keywords/tokens/layouts/concept/fonts/motion/
// device/anti_patterns/keyframes/samples/skin——公认风格交给模型固有知识（标签），
// 独有手法交给特色，设计系统由各片设计阶段现发明（强参考走 style_brief.json）。
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
}

// ErrNoEntry 条目不存在（Publish/Delete 命中时返回；api 层据此回 404）。
var ErrNoEntry = errors.New("风格库没有该条目")

type Library struct {
	dir string // data/library
	mu  sync.Mutex
}

// 单实例注册表（library 目录绝对路径 → *Library）：同库处处同锁。styles.json 与
// vocab.json 同一把锁串行（打标入库会同时写两个文件）。文件永远是事实源，不做
// 内容缓存（外部手改即时可见）。
var instances sync.Map // key: library dir 绝对路径 → *Library

func New(dataDir string) *Library {
	key, err := filepath.Abs(filepath.Join(dataDir, "library"))
	if err != nil {
		key = filepath.Join(dataDir, "library")
	}
	if v, ok := instances.Load(key); ok {
		return v.(*Library)
	}
	v, _ := instances.LoadOrStore(key, &Library{dir: key})
	return v.(*Library)
}

func (l *Library) path() string { return filepath.Join(l.dir, "styles.json") }

// List 全部条目，新在前。加载即校验：schema 不合格的条目视为损坏，跳过并喊出来
// （写入侧已校验，能走到这里的通常是手改）；证据帧文件缺失只做剪枝不拒条目
// （快照部分失败不该让条目从列表里消失——那样连删都删不掉）。
func (l *Library) List() ([]Style, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.listLocked()
}

func (l *Library) listLocked() ([]Style, error) {
	b, err := os.ReadFile(l.path())
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
		styles[i].TagEvidence = pruneMissingFrames(l, styles[i].ID, styles[i].TagEvidence)
		styles[i].Signatures = pruneMissingSigFrames(l, styles[i].ID, styles[i].Signatures)
		out = append(out, styles[i])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// pruneMissingFrames 证据帧按 style_media/<id> 实存剪枝（不存在的引用静默剔除）。
func pruneMissingFrames(l *Library, id string, evs []contract.TagEvidence) []contract.TagEvidence {
	out := evs[:0:0]
	for _, ev := range evs {
		ev.Frames = keepExisting(l, id, ev.Frames)
		out = append(out, ev)
	}
	return out
}

func pruneMissingSigFrames(l *Library, id string, sigs []contract.Signature) []contract.Signature {
	out := sigs[:0:0]
	for _, sg := range sigs {
		sg.Frames = keepExisting(l, id, sg.Frames)
		out = append(out, sg)
	}
	return out
}

func keepExisting(l *Library, id string, names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(l.MediaDir(id), n)); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// validateEntry schema 校验（不查文件实存——那由写入方对着源目录做、加载时剪枝）：
// tags.main 必填且在词表；aux ≤2 且同上；signatures ≤6、kind 枚举、mechanism 非空。
// vocab 传 nil 时跳过词表核对（仅校验结构），词表核对由带词表的封装补上。
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
// frames 允许为空（成片反哺草稿，人工在库页补）。
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

// VideoStyleIn 参考视频风格入库载荷 v3（api 层从解析草稿变形；IngestVideoRef 消费）。
type VideoStyleIn struct {
	Name        string
	Subject     string // 原片题材一句话（浏览/检索用，不进生成提示词）
	Tags        contract.StyleTags
	TagEvidence []contract.TagEvidence
	Signatures  []contract.Signature // ID 允许缺省，入库时统一编 sig1..sigN
}

// IngestVideoRef 参考视频解析结果入库（upsert，published=true）。
// 校验 §4.1 全规则（含词表核对）；同 seed 重复入库覆盖式重写（v3 条目都是解析
// 产物，重解析=用户对同一视频再来一次，旧值不保——确认页已让人工过目）。
func (l *Library) IngestVideoRef(refID string, in VideoStyleIn) (*Style, error) {
	s := Style{
		ID:            videoStyleID(refID),
		Name:          in.Name,
		Tags:          in.Tags,
		TagEvidence:   in.TagEvidence,
		Subject:       in.Subject,
		OriginProject: "video:" + refID,
		Published:     true,
		CreatedAt:     time.Now(),
	}
	s.Signatures = renumberSigs(in.Signatures)
	if err := l.put(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ProjectDraftIn 成片反哺起草载荷（extract_stylepack，agent 侧 LLM 打标后组装）。
type ProjectDraftIn struct {
	Name        string
	Subject     string
	Tags        contract.StyleTags
	TagEvidence []contract.TagEvidence
	Signatures  []contract.Signature // frames 允许为空（本片无原片帧，人工在库页补）
}

// DraftFromProject 从已出片项目起草风格（Agent 自动，published=false 等用户确认）。
// 幂等：style_samples 带 style_id（本就出自库内风格）或同项目已起草过 → 返回既有条目。
func (l *Library) DraftFromProject(projectDir, projectID string, in ProjectDraftIn) (*Style, error) {
	var ss struct {
		StyleID string `json:"style_id"`
	}
	if b, err := os.ReadFile(filepath.Join(projectDir, "style", "style_samples.json")); err == nil {
		_ = json.Unmarshal(b, &ss)
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
		Name:          in.Name,
		Tags:          in.Tags,
		TagEvidence:   in.TagEvidence,
		Subject:       in.Subject,
		OriginProject: projectID,
		Published:     false,
		CreatedAt:     time.Now(),
	}
	s.Signatures = renumberSigs(in.Signatures)
	if err := l.putLocked(&s); err != nil {
		return nil, err
	}
	return &s, nil
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

// put 带词表核对的写入（ingest/起草共用）。
func (l *Library) put(s *Style) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.putLocked(s)
}

func (l *Library) putLocked(s *Style) error {
	vocab := l.loadVocabLocked()
	if errs := validateEntryVocab(s, vocab); len(errs) > 0 {
		return fmt.Errorf("风格条目不合格: %s", strings.Join(errs, "; "))
	}
	styles, err := l.listLocked()
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
		styles = append(styles, *s)
	}
	return l.save(styles)
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
	return nil, fmt.Errorf("%w: %s", ErrNoEntry, id)
}

// Delete 删除条目并清掉它的媒体快照目录。已出片项目不受影响——简报在选用时
// 早已拷进项目各自的 style/，删的只是「以后新建项目还能不能选它」。
func (l *Library) Delete(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	styles, err := l.listLocked()
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
	if err := l.save(out); err != nil {
		return err
	}
	_ = os.RemoveAll(l.MediaDir(id)) // 媒体是冗余快照，清不净不挡条目删除
	return nil
}

// Get 取单个条目（新建项目选用后查详情/复用）。库文件损坏时按不存在处理，
// 但必须把损坏喊出来——静默返回会让人以为条目丢了。
func (l *Library) Get(id string) (*Style, bool) {
	styles, err := l.List()
	if err != nil {
		log.Printf("[stylelib] styles.json 读取失败，查询按不存在处理: %v", err)
		return nil, false
	}
	for i := range styles {
		if styles[i].ID == id {
			return &styles[i], true
		}
	}
	return nil, false
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
func videoStyleID(refID string) string {
	sl := regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(strings.ToLower(refID), "-")
	return "vid-" + strings.Trim(sl, "-")
}
