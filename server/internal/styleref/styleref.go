// Package styleref：参考视频 → 风格条目（风格库「视频解析」后端，v3）。
//
// 链路：上传视频存 data/references/<id>/ → ai/extract_video_features.py 抽帧+取色
// （确定性，不碰 LLM）→ 解析智能体两阶段（构图观察 + 打标提炼，见 analyze.go）
// → 用户确认入库（api 层调 stylelib.IngestVideoRef 写 data/library/styles.json）。
//
// v3 作业 = 打标 + 特色提炼（一次轻解析，§4.4）：
//   - 公认风格只打标签：词表（vocab.json）里有的引用 id，没有的「申请新词」
//     （name+aliases+brief+tells）→ Go 侧查重（归一化精确 + LLM 语义）→ 落表；
//   - 参考片独有的手法提炼成特色清单（≤6，六类枚举，机制化写作 + 证据帧）；
//   - 不再产出五件套/tokens/HTML 样张/skin——设计系统由各片设计阶段现发明。
package styleref

import (
	"context"
	"encoding/base64"
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
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/stylelib"
)

type Ref struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`   // 原始文件名（含扩展名）
	Status    string      `json:"status"` // pending | running | done | error
	Error     string      `json:"error,omitempty"`
	Confirmed bool        `json:"confirmed"` // 已入库（styles.json）
	CreatedAt time.Time   `json:"created_at"`
	Style     *StyleDraft `json:"style,omitempty"` // 解析产出的草稿（一视频一条目）
}

// StyleDraft 解析产出的标签+特色草稿（入库前形态；确认页可增删改）。
// Tags 是已解析落表的词表 id；Signatures 的 ID 在入库时统一编 sig1..sigN。
type StyleDraft struct {
	Name        string                 `json:"name"`              // 条目名（缺省用主标签正式名）
	Subject     string                 `json:"subject,omitempty"` // 原片题材一句话（只供检索，永不进生成提示词）
	Tags        contract.StyleTags     `json:"tags"`
	TagEvidence []contract.TagEvidence `json:"tag_evidence,omitempty"`
	Signatures  []contract.Signature   `json:"signatures,omitempty"`
}

// taggerOutput 打标作业的 LLM 原始输出（词表 id 与新词申请混排，Go 侧解析落表）。
type taggerOutput struct {
	Subject    string               `json:"subject"`
	Name       string               `json:"name,omitempty"`
	Tags       []rawTag             `json:"tags"`
	Signatures []contract.Signature `json:"signatures"`
}

// rawTag 一个标签的原始形态：词表 id 或新词申请（二选一），带人话指认与证据帧。
type rawTag struct {
	ID     string   `json:"id"`               // 词表 id（词表里有的必须给这个）
	New    *newWord `json:"new,omitempty"`    // 词表没有 → 申请新词
	Note   string   `json:"note"`             // 人话指认：「第2段：紫青撞色+网格地平线」
	Frames []string `json:"frames,omitempty"` // 证据帧文件名（frame-*.png）
}

// newWord 申请新词（§4.2）：查重通过后写 vocab.json。
type newWord struct {
	ID      string   `json:"id"`                // 小写蛇形英文 id（如 claymation），可缺省
	Name    string   `json:"name"`              // 正式名（如 粘土定格）
	Aliases []string `json:"aliases,omitempty"` // 中英+俗称
	Brief   string   `json:"brief"`             // 一句机制描述：色域/质感/光/字体气质
	Tells   string   `json:"tells,omitempty"`   // 判别特征：帧里认什么
}

// videoFeatures features.json（ai/extract_video_features.py 产物）。
type videoFeatures struct {
	DurationS float64          `json:"duration_s"`
	SpanS     [2]float64       `json:"span_s"` // 本次抽取的时间范围（全片=[0,duration]）
	NShots    int              `json:"n_shots"`
	Cuts      []float64        `json:"cuts"`   // 镜头硬切点
	Sheets    []string         `json:"sheets"` // 关键帧网格图（相对引用目录）
	Frames    []featureFrame   `json:"frames"`
	Palette   []paletteCluster `json:"palette"`
}

type featureFrame struct {
	Path string  `json:"path"`
	T    float64 `json:"t"`
}

type paletteCluster struct {
	Hex   string  `json:"hex"`
	Share float64 `json:"share"`
	Lum   float64 `json:"lum"`
}

type Store struct {
	DataDir string // data/（引用库存 data/references/）
	RootDir string // 仓库根（ai/ 脚本）
	Lib     *stylelib.Library // 风格库（打标查词表/申请新词落表用；main 接线，解析前置）

	mu      sync.Mutex
	running map[string]bool
	sem     chan struct{} // 解析并发上限（批量上传排队，防 LLM/ffmpeg 抢资源）
}

func NewStore(dataDir, rootDir string) *Store {
	return &Store{DataDir: dataDir, RootDir: rootDir, running: map[string]bool{}, sem: make(chan struct{}, 2)}
}

func (s *Store) dir(id string) string { return filepath.Join(s.DataDir, "references", id) }

// Save 上传的视频落盘并登记（pending）。id 秒级时间戳：并发/连点上传会同秒
// 撞号——Stat 探测再 MkdirAll 有竞态窗口（两个请求双通过检查互写同一目录），
// 直接用 Mkdir 原子占号，撞号换下一个。
func (s *Store) Save(name string, save func(dst string) error) (*Ref, error) {
	if err := os.MkdirAll(filepath.Join(s.DataDir, "references"), 0o755); err != nil {
		return nil, err
	}
	var id string
	for n := 1; ; n++ {
		cand := "ref" + time.Now().Format("20060102-150405")
		if n > 1 {
			cand = fmt.Sprintf("ref%s-%d", time.Now().Format("20060102-150405"), n)
		}
		err := os.Mkdir(s.dir(cand), 0o755)
		if err == nil {
			id = cand
			break
		}
		if !os.IsExist(err) {
			return nil, err
		}
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" || len(ext) > 6 {
		ext = ".mp4"
	}
	// 目录由本次 Mkdir 原子占号新建，写盘失败时清掉它——不留孤儿占号目录
	// （List 按「ref* 目录里有 ref.json」过滤，孤儿目录会一直在列表里当坏档）。
	// 只清本次创建的 id，已存在的目录不可能走到这里（Mkdir 撞号会换下一个）。
	if err := save(filepath.Join(s.dir(id), "video"+ext)); err != nil {
		os.RemoveAll(s.dir(id))
		return nil, err
	}
	ref := &Ref{ID: id, Name: name, Status: "pending", CreatedAt: time.Now()}
	if err := s.saveRef(ref); err != nil {
		os.RemoveAll(s.dir(id))
		return nil, err
	}
	return ref, nil
}

// List 全部引用，新在前。
func (s *Store) List() ([]Ref, error) {
	entries, err := os.ReadDir(filepath.Join(s.DataDir, "references"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Ref
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "ref") {
			continue
		}
		if ref, err := s.loadRef(e.Name()); err == nil {
			out = append(out, *ref)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) loadRef(id string) (*Ref, error) {
	b, err := os.ReadFile(filepath.Join(s.dir(id), "ref.json"))
	if err != nil {
		return nil, err
	}
	var ref Ref
	if err := json.Unmarshal(b, &ref); err != nil {
		return nil, err
	}
	return &ref, nil
}

func (s *Store) saveRef(ref *Ref) error {
	b, _ := json.MarshalIndent(ref, "", "  ")
	// 原子写：List/Get/api 侧并发读 ref.json，裸 os.WriteFile 有撕裂窗口
	// （读到半截 JSON 的引用会从列表里静默消失）
	return pipeline.WriteFileAtomic(filepath.Join(s.dir(ref.ID), "ref.json"), b, 0o644)
}

// MarkConfirmed 入库完成标记（幂等）。
func (s *Store) MarkConfirmed(id string) error {
	ref, err := s.loadRef(id)
	if err != nil {
		return err
	}
	ref.Confirmed = true
	return s.saveRef(ref)
}

// UpdateDraft 确认页编辑草稿（§4.4：入库前可增删改特色）。
func (s *Store) UpdateDraft(id string, d *StyleDraft) error {
	ref, err := s.loadRef(id)
	if err != nil {
		return err
	}
	if ref.Status != "done" {
		return fmt.Errorf("解析未完成，不能编辑草稿")
	}
	if ref.Style == nil {
		return fmt.Errorf("该引用没有解析草稿")
	}
	ref.Style = d
	return s.saveRef(ref)
}

// Analyze 异步解析（同引用幂等：跑着就不重复起）。结果写 ref.json。
func (s *Store) Analyze(id string) (bool, error) {
	ref, err := s.loadRef(id)
	if err != nil {
		return false, fmt.Errorf("引用不存在: %w", err)
	}
	s.mu.Lock()
	if s.running[id] {
		s.mu.Unlock()
		return false, nil
	}
	s.running[id] = true
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.running, id)
			s.mu.Unlock()
		}()
		// goroutine panic 不在 gin Recovery 兜底范围（直接崩进程）；
		// 解析链涉及 JSON 解码与 LLM 响应，必须自带兜底并把失败落进 ref.json
		defer func() {
			if r := recover(); r != nil {
				ref.Status, ref.Error = "error", fmt.Sprintf("解析内部错误: %v", r)
				_ = s.saveRef(ref)
			}
		}()
		s.sem <- struct{}{} // 批量上传多支视频同时起解析：排队限流
		defer func() { <-s.sem }()
		s.runAgent(context.Background(), ref)
	}()
	return true, nil
}

// analyzeCompositions 分批把关键帧喂 vision，逐帧记录构图观察（打标的构图证据）。
func (s *Store) analyzeCompositions(ctx context.Context, prov *llm.Provider, frames []featureFrame) (string, error) {
	var batches [][]featureFrame
	for i := 0; i < len(frames) && i < 6; i += 3 {
		end := i + 3
		if end > len(frames) {
			end = len(frames)
		}
		batches = append(batches, frames[i:end])
	}
	var all strings.Builder
	for bi, batch := range batches {
		imgs, ts := make([]string, 0, len(batch)), make([]float64, 0, len(batch))
		for _, f := range batch {
			b, err := os.ReadFile(f.Path)
			if err != nil {
				continue
			}
			imgs = append(imgs, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(b))
			ts = append(ts, f.T)
		}
		if len(imgs) == 0 {
			continue
		}
		var out struct {
			Frames []struct {
				Index int    `json:"index"`
				Comp  string `json:"composition"`
			} `json:"frames"`
		}
		_, err := prov.GenerateJSONVisionNoThink(ctx,
			"你是视频画面分析师。只输出 JSON。",
			fmt.Sprintf("这是同一支参考视频的第 %d 批关键帧（按顺序编号 %d 起，时间点 %v 秒）。逐帧给出 composition（中文 ≤60 字）：主体在哪、画面结构（对称/三分/满幅/大留白）、图文关系、层次与视觉锚点、字幕条位置样式。输出：{\"frames\":[{\"index\":帧序号,\"composition\":\"…\"}]}",
				bi+1, bi*3, ts),
			imgs, 0.5, &out)
		if err != nil {
			return "", fmt.Errorf("构图分析失败: %w", err)
		}
		for _, o := range out.Frames {
			if o.Index >= 0 && o.Index < bi*3+len(batch) && o.Comp != "" {
				fmt.Fprintf(&all, "帧%d: %s\n", o.Index, o.Comp)
			}
		}
	}
	return all.String(), nil
}

// synthesize 打标 + 特色提炼（§4.4 一次轻解析；结构校验失败重喂 ≤2 轮）。
// videoURL 非空 = 附原视频剪辑直读（运镜/转场以视频实测为准）；空 = 纯帧推断。
func (s *Store) synthesize(ctx context.Context, prov *llm.Provider, name string,
	feats videoFeatures, notes string, videoURL string) (*StyleDraft, error) {
	if s.Lib == nil {
		return nil, fmt.Errorf("风格库未接线（Store.Lib 为空）")
	}
	lib := s.Lib
	var lastErr error
	for round := 0; round < 3; round++ {
		var retryNote string
		if lastErr != nil {
			retryNote = "\n\n## 上一次输出被拒（修正后重出）\n" + lastErr.Error()
		}
		raw, err := s.callTagger(ctx, prov, lib, name, feats, notes, videoURL, retryNote)
		if err != nil {
			return nil, err // 网络/生成错误不重喂（provider 自带重试）
		}
		d, rerr := s.resolveDraft(ctx, prov, lib, raw, feats)
		if rerr == nil {
			if errs := validateStyle(d, feats); len(errs) == 0 {
				return d, nil
			} else {
				rerr = fmt.Errorf("结构自检未过: %s", strings.Join(errs, "; "))
			}
		}
		lastErr = rerr
	}
	return nil, lastErr
}

// callTagger 一次打标 LLM 调用（词表快照随题注入 = 工具「查风格词表」）。
func (s *Store) callTagger(ctx context.Context, prov *llm.Provider, lib *stylelib.Library,
	name string, feats videoFeatures, notes, videoURL, retryNote string) (*taggerOutput, error) {
	rep := make([]string, 0, 3) // 代表帧：首/中/尾
	for _, idx := range []int{0, len(feats.Frames) / 2, len(feats.Frames) - 1} {
		if idx < 0 || idx >= len(feats.Frames) {
			continue
		}
		b, err := os.ReadFile(feats.Frames[idx].Path)
		if err != nil {
			continue
		}
		rep = append(rep, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(b))
	}
	system := "你是视频风格策展人：给参考视频打公认风格标签、提炼它真正独有的手法特色。只输出 JSON。"
	user := synthesizeUserPrompt(lib, name, feats, notes, videoURL != "", retryNote)
	var out taggerOutput
	var err error
	if videoURL != "" {
		_, err = prov.GenerateJSONVisionVideoNoThink(ctx, system, user, rep, videoURL, 0.5, &out)
	} else {
		_, err = prov.GenerateJSONVisionNoThink(ctx, system, user, rep, 0.5, &out)
	}
	if err != nil {
		return nil, fmt.Errorf("打标作业失败: %w", err)
	}
	return &out, nil
}

// synthesizeUserPrompt 打标作业正文（callTagger 与回归测试共用一份，防漂移）。
// 纪律：① 只收有公认视觉定义的风格名词（拒收「高级感」类空话）；② 特色写机制
// 不写物件（这份清单会被用来制作题材完全不同的新片）；③ 题材只进 subject。
func synthesizeUserPrompt(lib *stylelib.Library, name string, feats videoFeatures,
	notes string, withVideo bool, retryNote string) string {
	pal := make([]string, 0, len(feats.Palette))
	for _, c := range feats.Palette {
		pal = append(pal, fmt.Sprintf("%s(覆盖率%.0f%%,亮度%.2f)", c.Hex, c.Share*100, c.Lum))
	}
	spanTxt := "全片"
	if feats.SpanS[1]-feats.SpanS[0] < feats.DurationS-1 {
		spanTxt = fmt.Sprintf("%.0f-%.0f 秒段", feats.SpanS[0], feats.SpanS[1])
	}
	// 关键帧清单（时间点+文件名）：证据帧指认的坐标系
	frames := make([]string, 0, len(feats.Frames))
	for _, f := range feats.Frames {
		frames = append(frames, fmt.Sprintf("%s(t=%.1fs)", filepath.Base(f.Path), f.T))
	}
	// 工具「查风格词表」：词表快照随题注入（冷启动为空表，首批词条由申请新词长出）
	var vocabB strings.Builder
	if entries := lib.VocabList(); len(entries) > 0 {
		for _, e := range entries {
			line := "- " + e.ID + "（" + e.Name + "）：" + e.Brief
			if e.Tells != "" {
				line += "｜判别：" + e.Tells
			}
			vocabB.WriteString(line + "\n")
		}
	} else {
		vocabB.WriteString("（词表还是空的——你打的每个标签都会作为第一批新词申请入库）\n")
	}
	videoBlock := ""
	dynamicSrc := "看附上的连续关键帧与构图观察推断"
	if withVideo {
		videoBlock = `
## 原视频直读（消息里附有可播放的原视频，范围即上方抽取范围）
- 运镜/转场/动效节奏是静止帧给不了的信息，**以视频实测为准**：镜头有没有推拉摇移、
  元素怎么进场、镜头间怎么过渡；
- 颜色仍以测得色板 hex 为准（视频压缩会偏色），构图仍以逐帧观察为准。
`
		dynamicSrc = "以附上的原视频实测为准（你能直接看到运动）"
	}
	return fmt.Sprintf(`给参考视频「%s」（%s）打风格标签、提炼独有特色。产物会进风格库，供制作题材完全不同的新片时做「强参考」。

## 已测得事实（必须遵守）
- 抽取范围时长 %.1f 秒、%d 个镜头；色板 k-means 8 簇：
  %s
- 关键帧清单（证据帧只能从这里挑）：
  %s
- 视觉逐帧构图观察：
%s
## 风格词表（打标签先查这里）
%s
## 作业一：打标签（主 1 + 辅 ≤2）
- 只收有公认视觉定义的风格名词（设计史/影视行话立得住的，如 粘土定格/包豪斯/蒸汽波）；
  拒收「高级感/简洁大气/氛围感」类空话——词表里有的直接引用 id；
- 词表没有合适词条才「申请新词」：给 name（正式名）+ aliases（中英俗称）+ brief（一句
  机制描述：色域/质感/光/字体气质）+ tells（帧里认什么）+ id（小写蛇形英文，如 claymation）；
- 主标签定基调，辅标签只在真的并行存在第二种气质时给（没有就只给主标签）；
- 每个标签的 note 是人话指认：「第2段：紫青撞色+网格地平线」这种看得见说得出的证据，
  frames 从上面关键帧清单里挑 1–2 张。

## 作业二：提炼特色（≤6 条，只写这支片真正独有的手法）
- kind 六选一：%s（运镜/转场/母题/动效/素材质感/配色）；
- mechanism 写机制不写物件：只描述「怎么做到的」（节拍/方向/层次/比例/速度），1–2 句，
  换一个题材照做仍然成立——禁写原片画的具体东西（楼宇/人物/品牌字样等物件一律抽象成
  「色块/卡片/线条/图形」）；use_hint 写什么节拍/场景适合用；
- frames 每条 ≥1 张证据帧（从关键帧清单挑最能看清该手法的）；
- 公认风格自带的手法（粘土片的逐帧跳动、赛博朋克的霓虹）不算特色——特色是「这支片
  在该风格之上的独门绝活」，没有就少给甚至不给（signatures 可以为空数组）；
- camera/transition/motion 类手法的判断%s。

## 题材只进 subject
- subject：一句话记录原片画的是什么（≤30 字，如「等距视角的城市宣传动画」）——
  它只供人浏览与检索，下游不会注入生成模型；除 subject 外任何字段禁写题材词。%s

## 输出（只输出 JSON，无围栏）
{"name":"条目名（≤12 字，主标签正式名）","subject":"原片题材一句话（≤30字）",
"tags":[{"id":"词表id 或空","new":{"id":"..","name":"..","aliases":[".."],"brief":"..","tells":".."},"note":"人话指认","frames":["frame-xx.png"]}],
"signatures":[{"kind":"六类之一","name":"人话短名","mechanism":"机制描述","frames":["frame-xx.png"],"use_hint":"什么节拍适合用"}]}%s`,
		name, spanTxt, feats.SpanS[1]-feats.SpanS[0], feats.NShots, strings.Join(pal, "、"),
		strings.Join(frames, "、"), notes, vocabB.String(),
		contract.SignatureKindNames(), dynamicSrc, videoBlock, retryNote)
}

// resolveDraft LLM 原始输出 → 落表草稿（视频解析版）。词表落表机制与成片反哺的
// resolveDesignDraft 共用 resolveTaggerDraft——此前两处约 60 行逐字重复，改一处
// 漏一处即行为漂移。唯一差异是证据帧：解析版带 frames（存在性校验由调用方
// synthesize 的 validateStyle 做）。
func (s *Store) resolveDraft(ctx context.Context, prov *llm.Provider, lib *stylelib.Library,
	raw *taggerOutput, feats videoFeatures) (*StyleDraft, error) {
	return resolveTaggerDraft(ctx, prov, lib, raw, true)
}

// semanticDedupFn 语义查重实现（var 化供测试注入；真实实现打一次 LLM）。
type semanticDedupFn func(ctx context.Context, prov *llm.Provider, lib *stylelib.Library, words []string) (map[string]string, error)

var semanticDedup semanticDedupFn = semanticDedupLLM

// semanticDedupLLM 把候选词与现有词条列表给模型，问「有没有其实是同一个东西的
// 不同名字」。返回 word → 命中的词条 id（未命中不出现在结果里）。
func semanticDedupLLM(ctx context.Context, prov *llm.Provider, lib *stylelib.Library, words []string) (map[string]string, error) {
	entries := lib.VocabList()
	if len(entries) == 0 {
		return map[string]string{}, nil // 空表无语义查重可言
	}
	var eb strings.Builder
	for _, e := range entries {
		eb.WriteString("- " + e.ID + "（" + e.Name + (")：" + e.Brief + "\n"))
	}
	var out struct {
		Hits []struct {
			Word    string `json:"word"`
			EntryID string `json:"entry_id"`
		} `json:"hits"`
	}
	_, err := prov.GenerateJSONNoThink(ctx,
		"你是风格名词的策展查重员。只输出 JSON。",
		fmt.Sprintf(`候选新词与现有风格词表如下。判断每个候选词是否与某个现有词条其实是同一个风格的不同名字（同义/译名/俗称）。

## 现有词表
%s
## 候选词
%s

只报真命中（视觉定义一致的才合并；相近但不同的风格不合并）。
输出：{"hits":[{"word":"候选词","entry_id":"命中的词条id"}]}，没有命中给空数组。`,
			eb.String(), strings.Join(words, "、")),
		&out)
	if err != nil {
		return nil, err
	}
	hits := map[string]string{}
	for _, h := range out.Hits {
		if h.Word != "" && h.EntryID != "" {
			hits[h.Word] = h.EntryID
		}
	}
	return hits, nil
}

// validateStyle 入库前结构自检（§4.1 规则；词表核对在 resolveDraft 已做）：
// 证据帧/特色帧引用的文件必须真实存在于本段帧目录。
func validateStyle(st *StyleDraft, feats videoFeatures) []string {
	var errs []string
	if strings.TrimSpace(st.Tags.Main) == "" {
		errs = append(errs, "tags.main 为空")
	}
	if len(st.Tags.Aux) > 2 {
		errs = append(errs, "tags.aux 超 2 个")
	}
	valid := map[string]bool{}
	for _, f := range feats.Frames {
		valid[filepath.Base(f.Path)] = true
	}
	check := func(what string, names []string) {
		for _, n := range names {
			if !valid[n] {
				errs = append(errs, fmt.Sprintf("%s 引用了不存在的帧 %q", what, n))
			}
		}
	}
	for _, ev := range st.TagEvidence {
		if strings.TrimSpace(ev.Note) == "" {
			errs = append(errs, "tag_evidence 缺人话指认")
		}
		check("tag_evidence", ev.Frames)
	}
	for _, sg := range st.Signatures {
		if len(sg.Frames) == 0 {
			errs = append(errs, fmt.Sprintf("特色 %q 缺证据帧", sg.Name))
		}
		check("signature", sg.Frames)
	}
	return errs
}

// Get 单个引用。
func (s *Store) Get(id string) (*Ref, error) { return s.loadRef(id) }

var reTail = regexp.MustCompile(`\s+\n`)

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return reTail.ReplaceAllString(strings.Join(lines, "\n"), "\n")
}

func runPython(ctx context.Context, rootDir, script string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	argv := append([]string{filepath.Join(rootDir, script)}, args...)
	// pipeline.CommandContext：取消时整棵进程树击杀，python/ffmpeg 孙进程不残留
	cmd := pipeline.CommandContext(ctx, pipeline.PythonExe(), argv...)
	cmd.Dir = rootDir
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(out), ctx.Err()
	}
	return string(out), err
}
