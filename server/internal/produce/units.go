package produce

// units.go —— 段落制渲染单元（2026-10-29 换骨，docs/para-transition-plan-2026-10-29.md §5）。
//
// 渲染单位从分镜换成段落：一个单元 = 一张连续 HTML + 一个段落 mp4，分镜退居
// 旁白/字幕/锚句层（TTS 仍按分镜、字幕仍按分镜、审片锚句仍按分镜句子）。
// 单元表落 storyboards/units.json + units-storyboard.json（contract.Storyboard 形，
// 供 pipeline 直读）；粒度规则（§5.1 半自由）：默认整段一个 HTML，超 5 分镜或
// 45 秒强制拆，拆点由方案甲的段内 fade 叠化兜底。
//
// 单元冒充分镜走既有管线：句匹配（segSentences 前缀游走）与事实（segFacts 交集）
// 吃合并旁白自动收齐全部分镜；frameQA/机检/渲染/判卷按单元 id 原样工作。

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/prep"
)

// 拆分上限（§5.1）：单单元最多 5 分镜 / 最长 45s。
const (
	unitMaxSegs = 5
	unitMaxDur  = 45.0
)

// unitPinned 项目渲染单位钉选（project.json "unit"）：para=段落制，seg=分镜制。
// 未钉的新项目首次制作时按「处女项目」判定钉 para；已钉永不静默改（老项目不迁移，
// 缓存不作废——plan §5.2 诚实账）。返回 "" = 未钉。
func unitPinned(p *pipeline.Project) string {
	return strings.ToLower(strings.TrimSpace(pipeline.ReadProjectField(p, "unit")))
}

// pinUnit 钉渲染单位（幂等）。
func pinUnit(p *pipeline.Project, unit string) {
	if unitPinned(p) == unit {
		return
	}
	if err := pipeline.SetProjectField(p, "unit", unit); err != nil {
		fmt.Printf("[units] 钉 unit=%s 失败: %v\n", unit, err)
	}
}

// virginProject 处女项目判定：从未产出过分镜制画面/段片（老项目钉 seg 永不迁移）。
func virginProject(p *pipeline.Project) bool {
	if es, err := os.ReadDir(p.Artifact("renders/segs")); err == nil {
		for _, e := range es {
			if strings.HasSuffix(e.Name(), ".mp4") {
				return false
			}
		}
	}
	if es, err := os.ReadDir(p.Artifact("llm")); err == nil {
		for _, e := range es {
			if strings.HasPrefix(e.Name(), "comp-seg") && strings.HasSuffix(e.Name(), ".json") {
				return false
			}
		}
	}
	return true
}

// BuildUnitsIfEligible 段落制就位判定 + 单元表构建落盘：
// 钉 seg → 不做；钉 para 或（未钉且处女项目）且草稿有段落层 → 构建并落盘，钉 para。
// 返回 nil = 本片走分镜制（老项目/无段落层）。
func BuildUnitsIfEligible(p *pipeline.Project, sb *contract.Storyboard, voices map[string]voiceMeta) []contract.RenderUnit {
	pin := unitPinned(p)
	if pin == "seg" {
		return nil
	}
	if pin == "" && !virginProject(p) {
		pinUnit(p, "seg") // 老项目：钉住分镜制，永不静默迁移
		return nil
	}
	draft, err := prep.LoadDraft(p)
	if err != nil || len(draft.Paragraphs) == 0 {
		if pin == "para" {
			pinUnit(p, "seg") // 钉了段落制但段落层没了（重跑准备丢了草稿）→ 退分镜制
		}
		return nil
	}
	units := BuildRenderUnits(draft.Paragraphs, sb, voices)
	if len(units) == 0 {
		return nil
	}
	pinUnit(p, "para")
	if err := writeUnitsPlan(p, units); err != nil {
		fmt.Printf("[units] 单元表落盘失败（退分镜制）: %v\n", err)
		return nil
	}
	return units
}

// BuildRenderUnits 段落表 → 渲染单元表（纯函数）。分镜顺序以分镜表为准；段落里
// 引用不到的分镜（草稿与分镜表脱节）按序归前一段落兜底，不静默丢段。
func BuildRenderUnits(paras []contract.DraftParagraph, sb *contract.Storyboard, voices map[string]voiceMeta) []contract.RenderUnit {
	if sb == nil || len(sb.Segments) == 0 {
		return nil
	}
	segByID := map[string]contract.Segment{}
	for _, s := range sb.Segments {
		segByID[s.ID] = s
	}
	// 段落归属：草稿段落层为准；引用不到的分镜按序挂到最后一个段落（不丢段）
	owner := map[string]int{}
	orphans := []string{}
	for pi, para := range paras {
		for _, id := range para.Segs {
			if _, ok := segByID[id]; ok {
				owner[id] = pi
			}
		}
	}
	last := 0
	for _, s := range sb.Segments {
		if pi, ok := owner[s.ID]; ok {
			last = pi
		} else {
			owner[s.ID] = last
			orphans = append(orphans, s.ID)
		}
	}
	// 按段落序聚合（保持分镜表顺序）
	idxs := make(map[int][]contract.Segment)
	var order []int
	for _, s := range sb.Segments {
		pi := owner[s.ID]
		if _, seen := idxs[pi]; !seen {
			order = append(order, pi)
		}
		idxs[pi] = append(idxs[pi], s)
	}
	sort.Ints(order)
	var units []contract.RenderUnit
	for _, pi := range order {
		segs := idxs[pi]
		title := paras[pi].Title
		if pi >= len(paras) || title == "" {
			title = fmt.Sprintf("段落 %d", pi+1)
		}
		// 拆分：贪心装箱，每箱 ≤5 分镜且 ≤45s（单分镜超 45s 独占一箱——分镜本身超长是旁白问题）
		for _, chunk := range splitChunk(segs, voices) {
			u := contract.RenderUnit{
				ParaIdx: pi,
				Title:   title,
				Chapter: segs[0].Chapter,
				Segs:    make([]contract.UnitSeg, 0, len(chunk)),
			}
			at := 0.0
			for _, s := range chunk {
				d := voices[s.ID].DurationS
				if d <= 0 {
					d = 3.0 // 无配音时长（理论不该发生，TTS 先于单元构建）：给保守占位
				}
				u.Segs = append(u.Segs, contract.UnitSeg{
					ID: s.ID, Start: at, End: at + d + contract.SegTail, NarrDur: d,
					Key: s.Key, Narration: s.Narration, Brief: s.VisualBrief,
				})
				at += d + contract.SegTail
			}
			u.Narration = joinUnitNarration(chunk)
			u.Dur = at
			units = append(units, u)
		}
	}
	// 编号：段落序 1 起 + 拆分后缀（未拆无后缀）
	nParts := map[int]int{}
	for i := range units {
		nParts[units[i].ParaIdx]++
	}
	seen := map[int]int{}
	for i := range units {
		pi := units[i].ParaIdx
		seen[pi]++
		id := fmt.Sprintf("para%02d", pi+1)
		title := units[i].Title
		if nParts[pi] > 1 {
			suffix := string(rune('a' + seen[pi] - 1))
			id += suffix
			title += fmt.Sprintf("（%d/%d）", seen[pi], nParts[pi])
		}
		units[i].ID = id
		units[i].Part = seen[pi]
		units[i].Parts = nParts[pi]
		units[i].Title = title
	}
	return units
}

// joinUnitNarration 单元旁白 = 分镜旁白按序拼接（句匹配 segSentences 按前缀游走，
// 与 script.Sentences 全片顺序天然对齐）。
func joinUnitNarration(segs []contract.Segment) string {
	parts := make([]string, len(segs))
	for i, s := range segs {
		parts[i] = s.Narration
	}
	return strings.Join(parts, "")
}

// splitChunk 贪心拆箱：≤5 分镜且 ≤45s；返回分镜组列表。
func splitChunk(segs []contract.Segment, voices map[string]voiceMeta) [][]contract.Segment {
	var out [][]contract.Segment
	var cur []contract.Segment
	dur := 0.0
	flush := func() {
		if len(cur) > 0 {
			out = append(out, cur)
			cur, dur = nil, 0
		}
	}
	for _, s := range segs {
		d := voices[s.ID].DurationS + contract.SegTail
		if len(cur) > 0 && (len(cur) >= unitMaxSegs || dur+d > unitMaxDur) {
			flush()
		}
		cur = append(cur, s)
		dur += d
	}
	flush()
	return out
}

// writeUnitsPlan 落盘：units.json（单元表）+ units-storyboard.json（单元分镜表，
// contract.Storyboard 形——pipeline/produce 的 WorkStoryboard 读它）。
func writeUnitsPlan(p *pipeline.Project, units []contract.RenderUnit) error {
	sb, err := p.LoadStoryboard()
	if err != nil {
		return err
	}
	sbU := UnitStoryboard(sb, units)
	ub, err := json.MarshalIndent(units, "", "  ")
	if err != nil {
		return err
	}
	if err := pipeline.WriteFileAtomic(p.Artifact("storyboards/units.json"), ub, 0o644); err != nil {
		return err
	}
	sbB, err := json.MarshalIndent(sbU, "", "  ")
	if err != nil {
		return err
	}
	return pipeline.WriteFileAtomic(p.Artifact("storyboards/units-storyboard.json"), sbB, 0o644)
}

// UnitStoryboard 单元分镜表：真实分镜表的结构复制品，Segments 换成单元（合并旁白/
// 主题作 Key；VisualBrief 带格子坐标）。导演统筹（Arc/Motifs）与 Overlay 原样保留。
func UnitStoryboard(sb *contract.Storyboard, units []contract.RenderUnit) *contract.Storyboard {
	out := *sb
	segs := make([]contract.Segment, len(units))
	for i, u := range units {
		briefs := make([]string, len(u.Segs))
		segBriefs := map[string]string{}
		for _, s := range sb.Segments {
			segBriefs[s.ID] = s.VisualBrief
		}
		for j, c := range u.Segs {
			briefs[j] = fmt.Sprintf("%s（格子 %.2f–%.2fs）：%s", c.ID, c.Start, c.End, segBriefs[c.ID])
		}
		segs[i] = contract.Segment{
			ID:           u.ID,
			Idx:          i + 1,
			Key:          u.Title,
			Narration:    u.Narration,
			VisualBrief:  strings.Join(briefs, "\n"),
			Chapter:      u.Chapter,
			DurationHint: u.Dur,
		}
	}
	out.Segments = segs
	return &out
}

// LoadUnits 单元表（无/解析失败返回 nil）。配 workOn 判段落制。
func LoadUnits(p *pipeline.Project) []contract.RenderUnit {
	b, err := os.ReadFile(p.Artifact("storyboards/units.json"))
	if err != nil {
		return nil
	}
	var us []contract.RenderUnit
	if json.Unmarshal(b, &us) != nil {
		return nil
	}
	return us
}

// MergedVoice 单元合并配音（词级时间戳换算成单元绝对时间；字幕带与词序表用）。
// phrases 由 assemble.py 侧按词偏移重排（python 持有 phrases 原文）。
func MergedVoice(u contract.RenderUnit, voices map[string]voiceMeta) voiceMeta {
	mv := voiceMeta{ID: u.ID, DurationS: u.Dur - contract.SegTail}
	for _, c := range u.Segs {
		off := c.Start
		for _, w := range voices[c.ID].Words {
			mv.Words = append(mv.Words, wordTiming{Text: w.Text, Start: w.Start + off, End: w.End + off})
		}
	}
	return mv
}
