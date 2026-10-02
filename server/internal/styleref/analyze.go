package styleref

// 解析智能体：参考视频 → 风格条目（一条主路径，两阶段）。
//
// 阶段1 理解：全片关键帧网格（sheet-00.png，每格带 #序号/时间戳）→ vision LLM
//   判断视频是单一风格还是多风格合集；多风格时给出每段的名称与起止边界。
//   边界由 Go 校验（排序/去重叠/最短时长），LLM 只提建议不碰时间轴。
// 阶段2 提炼：single = 全片一个风格包（老行为）；multi = 逐段抽帧（段内关键帧
//   + 段内取色）+ 逐段风格包，段名优先采用片中标题。
//
// 护栏：LLM 只看图和结构化事实；文件与进程全部由 Go 侧执行；某段解析失败
// 不拖垮整次解析（该段 Style 置空，其余段照常产出）。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"webpage-video-agent/server/internal/llm"
)

const minSegmentSec = 3.0 // 段最短时长：更短的「段」视为误判，并入邻段

// SegmentDraft 合集中的一个风格段（multi 时 ref.Segments）。
type SegmentDraft struct {
	Index  int            `json:"index"`         // 段序，1 起
	Name   string         `json:"name"`          // 段名（片中标题或 LLM 归纳）
	Start  float64        `json:"start"`         // 秒
	End    float64        `json:"end"`           // 秒
	Dir    string         `json:"dir,omitempty"` // 段帧目录名（ref 目录下，seg-<idx>）
	Frames []featureFrame `json:"frames,omitempty"`
	Style  *StyleDraft    `json:"style,omitempty"` // 解析失败为 nil
	Error  string         `json:"error,omitempty"`
}

// structure 阶段1 的 LLM 输出。
type structure struct {
	Kind     string `json:"kind"` // single | multi
	Reason   string `json:"reason,omitempty"`
	Segments []struct {
		Name  string  `json:"name"`
		Start float64 `json:"start"`
		End   float64 `json:"end"`
	} `json:"segments"`
}

// runAgent 解析编排（Analyze 的 goroutine 体）。
func (s *Store) runAgent(ctx context.Context, ref *Ref) {
	fail := func(err error) {
		ref.Status, ref.Error = "error", err.Error()
		_ = s.saveRef(ref)
	}
	ref.Status, ref.Error = "running", ""
	_ = s.saveRef(ref)

	// 1. 找视频
	var videos []string
	if es, rerr := os.ReadDir(s.dir(ref.ID)); rerr == nil {
		for _, e := range es {
			if strings.HasPrefix(e.Name(), "video.") {
				videos = append(videos, filepath.Join(s.dir(ref.ID), e.Name()))
			}
		}
	}
	if len(videos) == 0 {
		fail(fmt.Errorf("引用目录没有视频文件"))
		return
	}
	video := videos[0]

	// 2. 全片抽帧+取色（自适应 ≤36 帧 + sheet 网格）
	if out, err := runPython(ctx, s.RootDir, "ai/extract_video_features.py", video, s.dir(ref.ID)); err != nil {
		fail(fmt.Errorf("特征抽取失败: %w\n%s", err, tail(out, 8)))
		return
	}
	var feats videoFeatures
	if b, err := os.ReadFile(filepath.Join(s.dir(ref.ID), "features.json")); err != nil {
		fail(err)
		return
	} else if err := json.Unmarshal(b, &feats); err != nil {
		fail(err)
		return
	}

	prov, err := llm.FromEnv(llm.RoleVisual)
	if err != nil {
		fail(fmt.Errorf("解析需要 API 模式（LLM_API_KEY）: %w", err))
		return
	}

	// 3. 阶段1：理解视频结构
	st, err := s.detectStructure(ctx, prov, s.dir(ref.ID), feats, "全片")
	if err != nil {
		fail(err)
		return
	}

	// 4. 阶段2：提炼风格包
	if st.Kind != "multi" || len(st.Segments) < 2 {
		notes, err := s.analyzeCompositions(ctx, prov, feats.Frames)
		if err != nil {
			fail(err)
			return
		}
		style, err := s.synthesize(ctx, prov, ref.Name, "", feats, notes)
		if err != nil {
			fail(err)
			return
		}
		if errs := validateStyle(style); len(errs) > 0 {
			fail(fmt.Errorf("风格包自检未过: %s", strings.Join(errs, "; ")))
			return
		}
		ref.Kind, ref.Style, ref.Segments = "single", style, nil
	} else {
		segs, err := s.analyzeSegments(ctx, prov, video, ref, feats.DurationS, st)
		if err != nil {
			fail(err)
			return
		}
		ok := 0
		for _, seg := range segs {
			if seg.Style != nil {
				ok++
			}
		}
		if ok == 0 {
			fail(fmt.Errorf("全部 %d 段解析失败", len(segs)))
			return
		}
		ref.Kind = "multi"
		ref.Segments = segs
		ref.Style = nil
	}

	ref.Status = "done"
	if err := s.saveRef(ref); err != nil {
		fail(err)
	}
}

// segBound 段边界（纯边界，不含解析产物）。
type segBound struct {
	Name  string
	Start float64
	End   float64
}

const (
	refineThreshold = 24.0 // 粗段超过此时长 → 疑似多风格被合并，细拆
	refineCap       = 12   // 细拆扫描的段内帧数
	refineDepth     = 2    // 递归深度：粗→细→更细
	maxSegCount     = 40   // 段数兜底上限（防 LLM 发疯碎片化）
)

// detectStructure 阶段1：关键帧网格（≤2 张）+ 帧时间戳 + 镜头硬切点 → single/multi 与段边界。
// baseDir 是 features.json/sheet 所在目录；scope 描述帧覆盖范围（"全片" / "某段 55-120 秒"）。
func (s *Store) detectStructure(ctx context.Context, prov *llm.Provider, baseDir string,
	feats videoFeatures, scope string) (*structure, error) {
	var imgs []string
	for i, name := range feats.Sheets {
		if i >= 2 {
			break // vision 单次调用图片上限；60 帧 2 张网格足够
		}
		b, err := os.ReadFile(filepath.Join(baseDir, name))
		if err != nil {
			continue
		}
		imgs = append(imgs, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(b))
	}
	if len(imgs) == 0 {
		return nil, fmt.Errorf("关键帧网格缺失")
	}
	ts := make([]string, 0, len(feats.Frames))
	for _, f := range feats.Frames {
		ts = append(ts, fmt.Sprintf("%.1f", f.T))
	}
	cuts := make([]string, 0, len(feats.Cuts))
	for _, t := range feats.Cuts {
		cuts = append(cuts, fmt.Sprintf("%.1f", t))
	}
	spanEnd := feats.SpanS[1]
	if spanEnd <= 0 {
		spanEnd = feats.DurationS
	}
	var st structure
	_, err := prov.GenerateJSONVisionNoThink(ctx,
		"你是视频结构分析师。只输出 JSON。",
		fmt.Sprintf(`这是参考视频%s的关键帧网格（共 %d 格，按时间顺序分图展示，每格左上角标注 #序号 与时间戳 t=秒，时间为全片绝对时间）。

判断这批画面是「单一视觉风格」还是「多风格合集」：
- single：同一套视觉语言（同类配色/版式/材质/动画气质），哪怕镜头内容不同；
- multi：明显分段呈现多种不同风格（典型如风格盘点、混剪演示），段与段之间视觉语言切换。

## 已测得事实（必须遵守）
- 帧覆盖范围：%.1f-%.1f 秒；关键帧时间点（秒）：[%s]
- 镜头硬切时间点（秒）：[%s]
  多风格合集的段边界几乎总落在硬切点上；风格盘点类视频每个风格段通常 %.0f-%.0f 秒、
  段内带标题卡/信息卡、风格间硬切或叠化过渡。若画面出现「编号标题卡」（01/02/…
  递进编号），按编号数段——编号序列就是分段清单。
- 网格未必覆盖每一秒——段边界按帧趋势与硬切点推断，不必精确到帧。

## 输出（只输出 JSON，无围栏）
{"kind":"single|multi","reason":"≤40字判断依据","segments":[{"name":"段名","start":起,"end":止}]}
- single：segments 给空数组
- multi：segments 按时间排序、覆盖全部帧（首段 start=%.1f，末段 end=%.1f），每段 ≥%.0f 秒；
  尽量细分——多个段风格相近时仍按硬切边界与标题卡拆开；
  name 若画面里出现风格标题字样直接采用（中文 ≤12 字），否则按该段视觉语言归纳`,
			scope, len(feats.Frames), feats.SpanS[0], spanEnd,
			strings.Join(ts, ","), strings.Join(cuts, ","),
			minSegmentSec, minSegmentSec*4, feats.SpanS[0], spanEnd, minSegmentSec),
		imgs, 0.3, &st)
	if err != nil {
		return nil, fmt.Errorf("结构判断失败: %w", err)
	}
	return &st, nil
}

// refineBounds 粗段细拆：超过阈值的段单独抽帧再判断一遍，multi 则替换为子段（递归）。
// 只处理边界不合成风格包——先看清结构，再逐段提炼。
func (s *Store) refineBounds(ctx context.Context, prov *llm.Provider, video, refID string,
	segs []segBound, depth int) []segBound {
	if depth <= 0 {
		return segs
	}
	var out []segBound
	for _, seg := range segs {
		span := seg.End - seg.Start
		if span < refineThreshold || len(out)+len(segs) > maxSegCount {
			out = append(out, seg)
			continue
		}
		scanDir := filepath.Join(s.dir(refID), fmt.Sprintf("scan-%.0f", seg.Start))
		if _, err := runPython(ctx, s.RootDir, "ai/extract_video_features.py", video, scanDir,
			"--start", fmt.Sprintf("%.2f", seg.Start), "--end", fmt.Sprintf("%.2f", seg.End),
			"--cap", strconv.Itoa(refineCap)); err != nil {
			out = append(out, seg) // 扫描失败保守放行原段
			continue
		}
		b, err := os.ReadFile(filepath.Join(scanDir, "features.json"))
		if err != nil {
			out = append(out, seg)
			continue
		}
		var feats videoFeatures
		if json.Unmarshal(b, &feats) != nil || len(feats.Frames) < 4 {
			out = append(out, seg)
			continue
		}
		scope := fmt.Sprintf("的 %.0f-%.0f 秒片段", seg.Start, seg.End)
		st, err := s.detectStructure(ctx, prov, scanDir, feats, scope)
		if err != nil || st.Kind != "multi" || len(st.Segments) < 2 {
			out = append(out, seg) // 单风格或判断失败：保持原段
			continue
		}
		subs := sanitizeSegments(st, seg.Start, seg.End)
		if len(subs) < 2 {
			out = append(out, seg)
			continue
		}
		out = append(out, s.refineBounds(ctx, prov, video, refID, subs, depth-1)...)
	}
	return out
}

// analyzeSegments 阶段2（multi）：粗拆 → 递归细拆 → 逐段抽帧 + 逐段风格包。
func (s *Store) analyzeSegments(ctx context.Context, prov *llm.Provider, video string,
	ref *Ref, duration float64, st *structure) ([]SegmentDraft, error) {
	bounds := sanitizeSegments(st, 0, duration)
	bounds = s.refineBounds(ctx, prov, video, ref.ID, bounds, refineDepth)
	out := make([]SegmentDraft, 0, len(bounds))
	for i, seg := range bounds {
		idx := i + 1
		draft := SegmentDraft{Index: idx, Name: seg.Name, Start: seg.Start, End: seg.End}
		dirName := fmt.Sprintf("seg-%02d", idx)
		segDir := filepath.Join(s.dir(ref.ID), dirName)
		if out2, err := runPython(ctx, s.RootDir, "ai/extract_video_features.py",
			video, segDir, "--start", fmt.Sprintf("%.2f", seg.Start), "--end", fmt.Sprintf("%.2f", seg.End)); err != nil {
			draft.Error = fmt.Sprintf("段抽帧失败: %v\n%s", err, tail(out2, 4))
			out = append(out, draft)
			continue
		}
		var feats videoFeatures
		if b, err := os.ReadFile(filepath.Join(segDir, "features.json")); err != nil {
			draft.Error = err.Error()
			out = append(out, draft)
			continue
		} else if err := json.Unmarshal(b, &feats); err != nil {
			draft.Error = err.Error()
			out = append(out, draft)
			continue
		}
		notes, err := s.analyzeCompositions(ctx, prov, feats.Frames)
		if err != nil {
			draft.Error = err.Error()
			out = append(out, draft)
			continue
		}
		style, err := s.synthesize(ctx, prov, ref.Name, seg.Name, feats, notes)
		if err != nil {
			draft.Error = err.Error()
			out = append(out, draft)
			continue
		}
		if errs := validateStyle(style); len(errs) > 0 {
			draft.Error = "风格包自检未过: " + strings.Join(errs, "; ")
			out = append(out, draft)
			continue
		}
		draft.Dir = dirName
		draft.Frames = feats.Frames
		draft.Style = style
		out = append(out, draft)
	}
	return out, nil
}

// sanitizeSegments 段边界校验：排序、clamp 到 [lo,hi]、并掉过短的段。
func sanitizeSegments(st *structure, lo, hi float64) []segBound {
	var out []segBound
	for _, s := range st.Segments {
		start := clampSec(s.Start, lo, hi)
		end := clampSec(s.End, lo, hi)
		if end-start < minSegmentSec {
			continue
		}
		name := strings.TrimSpace(s.Name)
		if name == "" {
			name = fmt.Sprintf("第 %d 段", len(out)+1)
		}
		out = append(out, segBound{Name: name, Start: start, End: end})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	// 相邻段起点回退：后段起点早于前段终点 → 中点让位
	for i := 1; i < len(out); i++ {
		if out[i].Start < out[i-1].End {
			mid := (out[i].Start + out[i-1].End) / 2
			out[i-1].End, out[i].Start = mid, mid
		}
	}
	return out
}

func clampSec(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// readFeatures 读某目录 features.json（本包私有格式，extract_video_features.py 产物）。
func readFeatures(dir string) (videoFeatures, error) {
	b, err := os.ReadFile(filepath.Join(dir, "features.json"))
	if err != nil {
		return videoFeatures{}, err
	}
	var f videoFeatures
	if err := json.Unmarshal(b, &f); err != nil {
		return videoFeatures{}, err
	}
	return f, nil
}

// FeatureFrame 关键帧（Path 相对 features.json 所在目录）。
type FeatureFrame = featureFrame

// ReadFeatures 某目录 features.json 的帧清单（无文件/损坏返回 nil）。
// features.json 的格式属本包所有，外部（api 封面选帧）走这里，别再手写解析。
func ReadFeatures(dir string) []FeatureFrame {
	f, err := readFeatures(dir)
	if err != nil {
		return nil
	}
	return f.Frames
}
