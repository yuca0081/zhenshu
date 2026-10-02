// Package styleref：参考视频 → 风格（风格库「视频解析」后端）。
//
// 链路：上传视频存 data/references/<id>/ → ai/extract_video_features.py 抽帧+取色
// （确定性，不碰 LLM）→ 解析智能体两阶段（结构理解 + 逐段提炼，见 analyze.go）
// → 用户确认入库（api 层调 stylelib.IngestVideoRef 写 data/library/styles.json）。
package styleref

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
)

type Ref struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`   // 原始文件名（含扩展名）
	Status    string         `json:"status"` // pending | running | done | error
	Error     string         `json:"error,omitempty"`
	Confirmed bool           `json:"confirmed"` // 已入库（styles.json + 方法库）
	CreatedAt time.Time      `json:"created_at"`
	Kind      string         `json:"kind,omitempty"`     // single | multi（解析智能体判定）
	Style     *StyleDraft    `json:"style,omitempty"`    // single 时有（老 ref.json 兼容）
	Segments  []SegmentDraft `json:"segments,omitempty"` // multi 时有，每段一个风格草稿
}

// StyleDraft 解析产出的风格包（入库前草稿；tokens/layouts 直接进注册表）。
// 设计系统五件套（concept/fonts/motion/device/anti_patterns）与项目 design 阶段
// 同构（contract.StyleSamples 同款类型）——入库后随 StyleSamplesJSON 带进项目，
// hasDesignSystem 成立，DesignDoc 注入/版式轮换/画面质检全套点亮。
type StyleDraft struct {
	Direction    string               `json:"direction"`
	Genre        string               `json:"genre"`
	Keywords     []string             `json:"keywords"`
	Photo        string               `json:"photo"`
	Concept      string               `json:"concept,omitempty"`
	Fonts        *contract.FontRoles  `json:"fonts,omitempty"`
	Motion       *contract.MotionLang `json:"motion,omitempty"`
	Device       *contract.DeviceSpec `json:"device,omitempty"`
	AntiPatterns []string             `json:"anti_patterns,omitempty"`
	Layouts      []StyleLayout        `json:"layouts"`
	Tokens       json.RawMessage      `json:"tokens"` // 原样透传给 styles.json（引擎按字段取）
	Samples      []struct {
		Tag  string `json:"tag"`
		Desc string `json:"desc"`
		HTML string `json:"html"`
	} `json:"samples"`
}

type StyleLayout struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Desc string `json:"desc"`
}

// videoFeatures features.json（ai/extract_video_features.py 产物）。
type videoFeatures struct {
	DurationS float64          `json:"duration_s"`
	SpanS     [2]float64       `json:"span_s"` // 本次抽取的时间范围（全片=[0,duration]）
	NShots    int              `json:"n_shots"`
	Cuts      []float64        `json:"cuts"`   // 镜头硬切点（拆段的边界候选信号）
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

	mu      sync.Mutex
	running map[string]bool
}

func NewStore(dataDir, rootDir string) *Store {
	return &Store{DataDir: dataDir, RootDir: rootDir, running: map[string]bool{}}
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
	if err := save(filepath.Join(s.dir(id), "video"+ext)); err != nil {
		return nil, err
	}
	ref := &Ref{ID: id, Name: name, Status: "pending", CreatedAt: time.Now()}
	return ref, s.saveRef(ref)
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
	return os.WriteFile(filepath.Join(s.dir(ref.ID), "ref.json"), b, 0o644)
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
		s.runAgent(context.Background(), ref)
	}()
	return true, nil
}

// analyzeCompositions 分批把关键帧喂 vision，逐帧记录构图观察。
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

// synthesize 色板+构图观察+节奏 → 完整风格包 JSON（tokens 用给定 hex，样张纯 CSS 仿帧）。
// segName 非空表示合集分段模式：direction 优先采用段名（片中标题），事实用段内数据。
func (s *Store) synthesize(ctx context.Context, prov *llm.Provider, name, segName string,
	feats videoFeatures, notes string) (*StyleDraft, error) {
	pal := make([]string, 0, len(feats.Palette))
	for _, c := range feats.Palette {
		pal = append(pal, fmt.Sprintf("%s(覆盖率%.0f%%,亮度%.2f)", c.Hex, c.Share*100, c.Lum))
	}
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
	spanTxt := "全片"
	if feats.SpanS[1]-feats.SpanS[0] < feats.DurationS-1 {
		spanTxt = fmt.Sprintf("%.0f-%.0f 秒段", feats.SpanS[0], feats.SpanS[1])
	}
	nameHint := ""
	if segName != "" {
		nameHint = fmt.Sprintf("\n- 已知该段风格名「%s」（解析阶段从片中标题读出）——direction 优先直接采用它，可在 ≤6 字内微调；name 参数也以它为准", segName)
	}
	system := "你是视频视觉分析师，把参考视频提炼成可复用的风格包。只输出 JSON。"
	user := fmt.Sprintf(`把参考视频「%s」（%s）提炼成一个风格包（挂到网页渲染视频管线上，与现有 token 引擎同一套 schema）。

## 已测得事实（必须遵守）
- 抽取范围时长 %.1f 秒、%d 个镜头；色板 k-means 8 簇：
  %s
- 视觉逐帧构图观察：
%s%s
- font 只能用 Windows 系统字体栈：'Segoe UI'/'Microsoft YaHei'/'Georgia'/'KaiTi'/'STKaiti'/'Cambria'/'Arial Narrow'/'SimHei' 组合（示例：'Georgia','SimHei',serif），禁止 Noto/思源等未装字体
- tokens 里的颜色必须从上面色板 hex 里选（可按角色微调明度但保持色相；覆盖率最高的暗色通常做 bg.from，最亮簇做 txt）；颜色之外的字段按画面气质定。
- direction 是风格名（≤12 字，含一个括号副标）；**禁止出现「手绘」「扁平」二字**（引擎按名字路由）。
- layouts 给 5 个该视频的标准构图版式（id 用 ASCII，name 中文 ≤6 字，desc ≤70 字写清主体位置/比例/留白/元素数；画布 1920×1080，安全区 x∈[160,1760] y∈[120,850]，底部 896 起字幕带禁放，每版式 ≥3 元素且不重叠）。
- samples 给 2 张纯 CSS 样张（tag/desc/html），html 是完整自包含文档，画布固定 960×540：html,body{width:960px;height:540px;overflow:hidden}，内联一个 <style>，每张 ≤2000 字符，不要图片/网络资源，气质尽量贴近附上的关键帧。

## 设计系统（神似的关键，逐项给全；下游与项目设计阶段同一套注入机制）
- concept：一句话（≤40字）——这套风格的视觉概念，它为什么像这部片。
- fonts：字体三角色 {"display":{"stack":"..","weight":"..","tracking":".."},"body":{...},"data":{...}}，display/body 必填；栈只能用上面列的 Windows 系统字体且必须含中文字体（中文会豆腐块）。
- motion：动效语言 {"ease":"缓动族","dur_in":进场时长,"dur_settle":落定时长,"stagger":同组错峰,"notes":"性格一句话"}。看附上的连续关键帧推断：元素怎么进场（淡入/位移方向/缩放）、大概多快、错峰节奏；notes 写什么怎么动、禁什么动。时长档范围 dur_in 0.1–1.5 秒、dur_settle 0.05–1.0 秒、stagger 0–0.5 秒。
- device：贯穿装置 {"name":"装置名","desc":"是什么、观众为什么记住它","per_seg":"逐段怎么重复/变奏"}——全片反复出现的签名画面元素或构图机制（如「放射线强调」「红色印章盖角」）；没有显式装置就从构图语法归纳一个。
- anti_patterns：负面清单 3–6 条——这部片绝不会出现的手法（如「彩虹渐变」「emoji 贴纸」「圆角卡片堆砌」），出现即返工的俗手。

## tokens schema（缺字段引擎走缺省）
{"font":"系统字体栈","bg":{"type":"solid|linear","from":"#..","to":"#..","angle":0,"fx":["stars|dots|grid|glow|noise 子集"],"glow":[{"color":"#..","x":50,"y":50,"r":50,"alpha":"33"}]},"ink":"#..","txt":"#..","surface":"#..|rgba(..)","surface_txt":"#..","accent":["#..×6"],"primary":"#..","arrow":"#..","border_w":0,"radius":12,"shadow":"css 或 none","title":{"weight":800,"spacing":2},"cap":{"mode":"bar|pill|plain","bg":"rgba(..)","color":"#.."},"img":{"frame":"polaroid|clean|glass","shadow":"css"}}

## 输出（只输出 JSON，无围栏）
{"direction":"…","genre":"适用题材（从画面推断，≤30字）","keywords":["…×4-8"],"photo":"配图 query 调性（≤30字）","concept":"…","fonts":{…},"motion":{…},"device":{…},"anti_patterns":["…×3-6"],"layouts":[…5],"tokens":{…},"samples":[…2]}
`, name, spanTxt, feats.SpanS[1]-feats.SpanS[0], feats.NShots, strings.Join(pal, "、"), notes, nameHint)
	var style StyleDraft
	_, err := prov.GenerateJSONVisionNoThink(ctx, system, user, rep, 0.5, &style)
	if err != nil {
		return nil, fmt.Errorf("风格合成失败: %w", err)
	}
	return &style, nil
}

// validateStyle 入库前自检（够注册表吃、引擎能配）。
func validateStyle(st *StyleDraft) []string {
	var errs []string
	if st.Direction == "" || strings.Contains(st.Direction, "手绘") || strings.Contains(st.Direction, "扁平") {
		errs = append(errs, "direction 缺失或含引擎保留词")
	}
	if len(st.Layouts) < 3 {
		errs = append(errs, "layouts 不足 3 个")
	}
	if len(st.Samples) == 0 || st.Samples[0].HTML == "" {
		errs = append(errs, "样张缺失")
	}
	var tk struct {
		BG     *json.RawMessage `json:"bg"`
		Ink    string           `json:"ink"`
		TXT    string           `json:"txt"`
		Accent []string         `json:"accent"`
	}
	if json.Unmarshal(st.Tokens, &tk) != nil || tk.BG == nil || tk.Ink == "" || tk.TXT == "" || len(tk.Accent) < 4 {
		errs = append(errs, "tokens 不完整（bg/ink/txt/accent≥4）")
	}
	// 设计系统五件套与项目 design 阶段同纪律（contract.ValidateStyleSamplesDraft 同款阈值）：
	// 没有它们风格条目退化回 v1 色板卡——神似的缺口，宁打回重析不放行
	errs = append(errs, ValidateDesignSystem(st)...)
	return errs
}

// ValidateDesignSystem 设计系统五件套自检（concept/fonts/motion/device/anti_patterns），
// 阈值与 contract.ValidateStyleSamplesDraft 同款。入库自检与老条目回填工具共用一套，
// 阈值只放这一处。
func ValidateDesignSystem(st *StyleDraft) []string {
	var errs []string
	if strings.TrimSpace(st.Concept) == "" {
		errs = append(errs, "concept 缺失")
	}
	if st.Fonts == nil || strings.TrimSpace(st.Fonts.Display.Stack) == "" || strings.TrimSpace(st.Fonts.Body.Stack) == "" ||
		!contract.HasCJKFont(st.Fonts.Display.Stack) || !contract.HasCJKFont(st.Fonts.Body.Stack) {
		errs = append(errs, "fonts 不完整或字体栈缺中文字体")
	}
	if st.Motion == nil || strings.TrimSpace(st.Motion.Ease) == "" || strings.TrimSpace(st.Motion.Notes) == "" ||
		st.Motion.DurIn < 0.1 || st.Motion.DurIn > 1.5 ||
		st.Motion.DurSettle < 0.05 || st.Motion.DurSettle > 1.0 ||
		st.Motion.Stagger < 0 || st.Motion.Stagger > 0.5 {
		errs = append(errs, "motion 缺失或时长档越界（dur_in 0.1–1.5 / dur_settle 0.05–1.0 / stagger 0–0.5）")
	}
	if st.Device == nil || strings.TrimSpace(st.Device.Name) == "" ||
		strings.TrimSpace(st.Device.Desc) == "" || strings.TrimSpace(st.Device.PerSeg) == "" {
		errs = append(errs, "device 缺失（名称/是什么/逐段运用三件齐）")
	}
	if len(st.AntiPatterns) > 8 {
		errs = append(errs, "anti_patterns 超 8 条")
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
	py, err := exec.LookPath("python")
	if err != nil {
		return "", fmt.Errorf("python 不在 PATH: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	argv := append([]string{filepath.Join(rootDir, script)}, args...)
	cmd := exec.CommandContext(ctx, py, argv...)
	cmd.Dir = rootDir
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(out), ctx.Err()
	}
	return string(out), err
}
