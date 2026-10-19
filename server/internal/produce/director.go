// director.go —— 导演统筹两层落点。
//
// ⚠ 架构状态（2026-10-19 标注；docs/production-architecture-discussion.md 已
// 裁定「导演角色退役、预期考卷并入审核工位」）：
//   - M1 仍是活代码：directorNoteFor 把弧线定位/母题任务/邻段衔接/本段节拍
//     注入每段生成 prompt（prep 段级流水线与 legacy run() 两条路径共用），
//     directorFingerprint 让导演字段改动正确传导成段缓存过期。
//   - M2 审片（directorReview 及其配套）只服务 legacy run() 路径：prep 新项目
//     走 segpipe 的段审/边界审/全片终审，永不触达本文件下半部分。非阻塞哲学
//     同 prereview/frameQA（审片故障降级放行）；legacy 项目清零后 M2 可整删。
package produce

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
)

// directorNoteFor 导演统筹注入块：本段在弧线中的位置（章/张力/章内角色）+
// 母题任务 + 邻段衔接参照 + 本段节拍表。无导演字段且无结构化 brief 返回空串
// （老项目零感知）。段定位用数组位置而非 idx 算术——idx 唯一性有校验、与数组
// 序的一致性没有。
// 结构化 brief（准备工位分镜稿翻译产物）在场时，追加「邻段承诺」块——即使没有
// 导演字段也注入（prep-agents-design §4：邻段两行承诺对所有段白送注入，防层），
// 内容取自相邻段 brief 的末拍/首拍，本段首末拍重申。
func directorNoteFor(sb *contract.Storyboard, seg contract.Segment) string {
	hasDirector := sb.Arc != nil || len(seg.Beats) > 0 || len(sb.Motifs) > 0
	if !hasDirector && seg.Brief == nil {
		return ""
	}
	pos := -1
	for i, s := range sb.Segments {
		if s.ID == seg.ID {
			pos = i
			break
		}
	}
	var b strings.Builder
	if hasDirector {
		b.WriteString("## 导演统筹（全片视角——本段不是孤立的一段）\n")
		if sb.Arc != nil && pos >= 0 {
			for ci, ch := range sb.Arc.Chapters {
				cpos := -1
				for i, id := range ch.Segs {
					if id == seg.ID {
						cpos = i
						break
					}
				}
				if cpos < 0 {
					continue
				}
				role := "开篇"
				switch {
				case cpos > 0 && cpos < len(ch.Segs)-1:
					role = "推进"
				case cpos == len(ch.Segs)-1 && len(ch.Segs) > 1:
					role = "收尾"
				}
				fmt.Fprintf(&b, "- 本段是第 %d 章「%s」（%s，张力 %d/5）的第 %d/%d 段，承担「%s」\n",
					ci+1, ch.Title, ch.Mood, ch.Tension, cpos+1, len(ch.Segs), role)
				break
			}
		}
		for _, m := range sb.Motifs {
			for _, ap := range m.Appearances {
				if ap.SegID != seg.ID {
					continue
				}
				roleZh := map[string]string{
					"establish": "首次立起（这一段定它的视觉形态，后续段照此复现）",
					"callback":  "中途复现（沿用已立起的形态做变奏，观众要能认出是同一个意象）",
					"echo":      "结尾呼应（收束全片的记忆点，回到开篇的意象）",
				}[ap.Role]
				fmt.Fprintf(&b, "- 母题「%s」（%s）在本段出场：%s\n", m.Name, m.Desc, roleZh)
			}
		}
		if pos > 0 && sb.Segments[pos-1].Brief == nil {
			prev := sb.Segments[pos-1]
			fmt.Fprintf(&b, "- 上一段（%s）画面：%s——本段开场与它的收尾要接得住\n", prev.ID, prev.VisualBrief)
		}
		if pos >= 0 && pos+1 < len(sb.Segments) && sb.Segments[pos+1].Brief == nil {
			next := sb.Segments[pos+1]
			fmt.Fprintf(&b, "- 下一段（%s）画面：%s——本段收尾给它留好衔接\n", next.ID, next.VisualBrief)
		}
		if len(seg.Beats) > 0 {
			b.WriteString("## 本段节拍（旁白句 → 此刻画面该有什么；元素揭示与内容按节拍排，不前载不拖尾）\n")
			for _, bt := range seg.Beats {
				fmt.Fprintf(&b, "- 「%s」→ %s\n", bt.Quote, bt.Expect)
			}
		}
		b.WriteString("\n")
	}
	// 邻段承诺（结构化 brief 在场时注入，准备工位项目的主衔接通道）
	if seg.Brief != nil {
		b.WriteString("## 邻段承诺（分镜稿首末拍——相邻段衔接的全部依据，照做不另创）\n")
		fmt.Fprintf(&b, "- 本段首拍：%s\n- 本段末拍：%s\n", seg.Brief.FirstFrame, seg.Brief.LastFrame)
		if pos > 0 && sb.Segments[pos-1].Brief != nil {
			prev := sb.Segments[pos-1]
			fmt.Fprintf(&b, "- 上段（%s）末拍收在：%s——本段首拍若写延续，就从这里接\n", prev.ID, prev.Brief.LastFrame)
		}
		if pos >= 0 && pos+1 < len(sb.Segments) && sb.Segments[pos+1].Brief != nil {
			next := sb.Segments[pos+1]
			fmt.Fprintf(&b, "- 下段（%s）首拍起于：%s——本段末拍要落到它接得住的地方\n", next.ID, next.Brief.FirstFrame)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// directorFingerprint 分镜层导演字段内容指纹（arc+motifs；逐段 beats 不在此——
// 它在 sceneFingerprint 的段级分量里，改哪段哪段过期）。无导演字段返回空串：
// 指纹拼接结果与旧旁车格式完全一致，老项目段缓存照常命中，不触发全量重生成。
func directorFingerprint(sb *contract.Storyboard) string {
	if sb.Arc == nil && len(sb.Motifs) == 0 {
		return ""
	}
	b, err := json.Marshal(struct {
		Arc    *contract.Arc    `json:"arc,omitempty"`
		Motifs []contract.Motif `json:"motifs,omitempty"`
	}{sb.Arc, sb.Motifs})
	if err != nil {
		return ":err"
	}
	sum := sha256.Sum256(b)
	return ":" + hex.EncodeToString(sum[:4])
}

// ── M2 审片 ──────────────────────────────────────────────────────

// directorReviewOn 审片开关：project.json "director_review"（*bool；缺省=开，
// 与 pre_review 同款显式落值语义）。
func directorReviewOn(p *pipeline.Project) bool {
	b, err := os.ReadFile(p.Artifact("project.json"))
	if err != nil {
		return true
	}
	var meta struct {
		DirectorReview *bool `json:"director_review"`
	}
	if json.Unmarshal(b, &meta) != nil || meta.DirectorReview == nil {
		return true
	}
	return *meta.DirectorReview
}

// directorIssue 审片意见单条目。SegID 必须是时刻表里出现过的段；kind 四查枚举。
type directorIssue struct {
	SegID      string `json:"seg_id"`
	Kind       string `json:"kind"` // expectation_miss|motif_missing|pacing|deadair
	Severity   string `json:"severity"`
	Note       string `json:"note"`
	Suggestion string `json:"suggestion"`
}

type directorVerdict struct {
	Pass   bool            `json:"pass"`
	Issues []directorIssue `json:"issues"`
}

// directorShot 审片抽帧计划：节拍锚句换算出的全局时刻 + 考卷标签。
type directorShot struct {
	At      float64       `json:"at"` // 全局时刻（snapshot --at）
	SegID   string        `json:"seg_id"`
	Key     string        `json:"key"`
	BeatIdx int           `json:"beat_idx"`
	Beat    contract.Beat `json:"beat"`
	Chapter string        `json:"chapter,omitempty"`
	Motifs  string        `json:"motifs,omitempty"` // 此段应可见的母题（顿号分隔）
	First   bool          `json:"first,omitempty"`  // 段首拍（章开篇视角）
}

// directorMaxShots 连环画帧数上限（多模态请求体积与全局视野的平衡点）。
const directorMaxShots = 24

// nospace 去全部空白（含全角空格）——锚句匹配与契约校验（contract.containsIgnoreSpaces）
// 的口径对齐，只是这里还要对词流做。
func nospace(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r', '　':
			return -1
		}
		return r
	}, s)
}

// beatAnchor 节拍锚句 → 段内时刻（纯机械，无 LLM）：锚句（去空白）在词流拼接
// （去空白）里找首位置，取起始词的 start——词级时间戳是平台对 agent 工作流
// 最直接的优势（拉普拉斯妖手工版 70 个锚点全靠人肉估）。词流与旁白存在 ASR
// 差异（数字写法/吞字）找不到时按节拍序在段内均分兜底：锚点偏一点不伤考卷
// 语义，预期写的是画面内容不是精确 choreography。
func beatAnchor(seg contract.Segment, v voiceMeta, beatIdx, beatN int, dur float64) float64 {
	q := []rune(nospace(seg.Beats[beatIdx].Quote))
	if len(q) > 0 && len(v.Words) > 0 {
		var flat []rune
		var widx []int
		for wi, w := range v.Words {
			for _, r := range nospace(w.Text) {
				flat = append(flat, r)
				widx = append(widx, wi)
			}
		}
		for i := 0; i+len(q) <= len(flat); i++ {
			match := true
			for j := range q {
				if flat[i+j] != q[j] {
					match = false
					break
				}
			}
			if match {
				return v.Words[widx[i]].Start
			}
		}
	}
	if beatN <= 1 {
		return dur / 2
	}
	return dur * float64(beatIdx+1) / float64(beatN+1)
}

// directorShots 抽帧计划：每段首拍+尾拍（章开篇天然由各章首段的首拍帧覆盖，
// 不另花帧数），预算三档优先级——母题段全部帧 > 其余段首拍 > 其余段尾拍，
// 上限 24 帧后截断。缺配音/缺场景文件的段跳过（时刻轴照累计，口径同 preReviewTimes）。
func directorShots(p *pipeline.Project, sb *contract.Storyboard, voices map[string]voiceMeta) []directorShot {
	chapterOf := map[string]string{}
	if sb.Arc != nil {
		for _, ch := range sb.Arc.Chapters {
			for _, id := range ch.Segs {
				chapterOf[id] = ch.Title
			}
		}
	}
	motifsOf := map[string][]string{}
	for _, m := range sb.Motifs {
		for _, ap := range m.Appearances {
			motifsOf[ap.SegID] = append(motifsOf[ap.SegID], m.Name)
		}
	}
	segAt := map[string]float64{}
	acc := 0.0
	type cand struct {
		seg     contract.Segment
		beatIdx int
		first   bool
	}
	var cands []cand
	for _, seg := range sb.Segments {
		segAt[seg.ID] = acc
		acc += voices[seg.ID].DurationS + contract.SegTail
		n := len(seg.Beats)
		if n == 0 || voices[seg.ID].DurationS <= 0 || !existsFile(p.Artifact("compositions/frames/"+seg.ID+".html")) {
			continue
		}
		cands = append(cands, cand{seg, 0, true})
		if n > 1 {
			cands = append(cands, cand{seg, n - 1, false})
		}
	}
	var picked []cand
	taken := func(c cand) bool {
		for _, pk := range picked {
			if pk.seg.ID == c.seg.ID && pk.beatIdx == c.beatIdx {
				return true
			}
		}
		return false
	}
	for _, tier := range []func(cand) bool{
		func(c cand) bool { return len(motifsOf[c.seg.ID]) > 0 }, // 母题段全帧
		func(c cand) bool { return c.first },                     // 其余段首拍
		func(c cand) bool { return true },                        // 其余段尾拍
	} {
		for _, c := range cands {
			if len(picked) >= directorMaxShots {
				break
			}
			if !tier(c) || taken(c) {
				continue
			}
			picked = append(picked, c)
		}
	}
	shots := make([]directorShot, 0, len(picked))
	for _, c := range picked {
		dur := voices[c.seg.ID].DurationS + contract.SegTail
		t := beatAnchor(c.seg, voices[c.seg.ID], c.beatIdx, len(c.seg.Beats), dur)
		if t < 0.5 { // 段首 0.25s 黑场渐变内截帧无意义（口径同 preReviewTimes）
			t = 0.5
		}
		if m := dur - 0.5; t > m {
			t = m
		}
		if t < 0.25 { // 极短段防御
			t = 0.25
		}
		sh := directorShot{
			At:      round2(segAt[c.seg.ID] + t),
			SegID:   c.seg.ID,
			Key:     c.seg.Key,
			BeatIdx: c.beatIdx,
			Beat:    c.seg.Beats[c.beatIdx],
			Chapter: chapterOf[c.seg.ID],
			First:   c.first,
		}
		if ms := motifsOf[c.seg.ID]; len(ms) > 0 {
			sh.Motifs = strings.Join(ms, "、")
		}
		shots = append(shots, sh)
	}
	sort.Slice(shots, func(i, j int) bool { return shots[i].At < shots[j].At })
	dedup := shots[:0] // 极短段钳位后两拍可能撞同一时刻，--at 不喂重复值
	seen := map[float64]bool{}
	for _, sh := range shots {
		if !seen[sh.At] {
			seen[sh.At] = true
			dedup = append(dedup, sh)
		}
	}
	return dedup
}

// directorReview 导演审片（run() 3.5 步：assemble 后、check 前——意见 high 段
// 重出画面后由下一步 checkWithRepair 统一兜机械问题）。err 只在 ctx 取消或
// 结构性失败（compositions/assemble——后续阶段必然同样失败，如实上抛）时非
// nil；审片类故障（snapshot/视觉调用/重出失败）一律降级放行。
func (r *Runner) directorReview(ctx context.Context, p *pipeline.Project) error {
	if !directorReviewOn(p) {
		r.Manifest(p, "director.review_off", "")
		return nil
	}
	sb, err := p.LoadStoryboard()
	if err != nil {
		r.Manifest(p, "director.review_skip", "无分镜: "+err.Error())
		return nil
	}
	hasBeats := false
	total := 0.0
	voices, verr := loadAudioMeta(p)
	if verr == nil {
		for _, seg := range sb.Segments {
			hasBeats = hasBeats || len(seg.Beats) > 0
			total += voices[seg.ID].DurationS + contract.SegTail
		}
	}
	if sb.Arc == nil && !hasBeats {
		r.Manifest(p, "director.skipped_no_plan", "分镜无导演统筹字段（老项目/定调降级）")
		return nil
	}
	if verr != nil {
		r.Manifest(p, "director.review_skip", "缺 audio_meta.json: "+verr.Error())
		return nil
	}
	if len(sb.Segments) < 8 && total < 120 {
		r.Manifest(p, "director.skipped_short",
			fmt.Sprintf("%d 段 / %.0fs——短片全局性弱，审片收益覆盖不了成本", len(sb.Segments), total))
		return nil
	}
	shots := directorShots(p, sb, voices)
	if len(shots) == 0 {
		r.Manifest(p, "director.review_skip", "无可审帧（无节拍或无场景文件）")
		return nil
	}

	id := p.ID
	r.emit(id, "stage", "director", "running")
	r.Manifest(p, "director.review.start", fmt.Sprintf("%d 帧连环画", len(shots)))
	atArg := make([]string, len(shots))
	for i, sh := range shots {
		atArg[i] = fmt.Sprintf("%.2f", sh.At)
	}
	frames, serr := r.snapFrames(p, strings.Join(atArg, ","), "reports/director/review")
	if serr != nil {
		r.emit(id, "stage", "director", "snapshot 失败，审片降级放行")
		r.Manifest(p, "director.snapshot_error", pipeline.FirstLine(serr.Error(), 160))
		return nil
	}
	// 帧文件名 → 时刻 → 考卷序（时间正序即 shots 序；snapshot 缺某帧就少一张图，
	// 时刻表同步剔除，图号与时刻表永不错位——错位比缺帧更糟）
	nameAt := map[string]float64{}
	for _, name := range frames {
		if m := reSnapAt.FindStringSubmatch(name); m != nil {
			if t, perr := strconv.ParseFloat(m[1], 64); perr == nil {
				nameAt[name] = round2(t)
			}
		}
	}
	ordered := make([]directorShot, 0, len(shots))
	imgs := make([]string, 0, len(shots))
	for _, sh := range shots {
		var name string
		for n, t := range nameAt {
			if t == sh.At {
				name = n
				break
			}
		}
		if name == "" {
			r.Manifest(p, "director.frame_missing", fmt.Sprintf("%.2fs", sh.At))
			continue
		}
		jpg, jerr := shrinkFrame(ctx, filepath.Join(p.Artifact("reports/director/review"), name))
		if jerr != nil {
			r.Manifest(p, "director.frame_missing", fmt.Sprintf("%.2fs 缩水失败", sh.At))
			continue
		}
		ordered = append(ordered, sh)
		imgs = append(imgs, dataURL(jpg))
	}
	if len(imgs) == 0 {
		r.emit(id, "stage", "director", "无可用帧，审片降级放行")
		r.Manifest(p, "director.snapshot_error", "帧全部缺失/缩水失败")
		return nil
	}
	prov, perr := llm.FromEnv(llm.RoleVisual)
	if perr != nil {
		r.Manifest(p, "director.review_error", "无视觉模型配置: "+perr.Error())
		return nil
	}
	var vd directorVerdict
	if _, rerr := prov.GenerateJSONVisionEffort(ctx, "你是这部视频的导演，只输出 JSON。",
		directorReviewPrompt(sb, ordered), imgs, 0.2, &vd, llm.EffortHigh); rerr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.emit(id, "stage", "director", "审查调用失败，审片降级放行")
		r.Manifest(p, "director.review_error", pipeline.FirstLine(rerr.Error(), 160))
		return nil
	}
	// 意见单落盘（账本：用户「引用此刻返工」与 M3 仲裁的输入）
	report := struct {
		TS       string          `json:"ts"`
		Shots    []directorShot  `json:"shots"`
		Pass     bool            `json:"pass"`
		Issues   []directorIssue `json:"issues"`
		Repaired []string        `json:"repaired"`
	}{time.Now().Format(time.RFC3339), ordered, vd.Pass, vd.Issues, nil}
	if b, merr := json.MarshalIndent(report, "", "  "); merr == nil {
		_ = pipeline.WriteFileAtomic(p.Artifact("reports/director-review.json"), b, 0o644)
	}
	for _, is := range vd.Issues {
		r.Manifest(p, "director.issue", fmt.Sprintf("%s [%s/%s] %s", is.SegID, is.Kind, is.Severity, pipeline.FirstLine(is.Note, 100)))
	}
	// 处置：high 且段合法（出现在时刻表里——导演不许指点没看见的段）→ 重出 ≤1 轮
	seenSeg := map[string]bool{}
	for _, sh := range ordered {
		seenSeg[sh.SegID] = true
	}
	inStoryboard := map[string]contract.Segment{}
	for _, seg := range sb.Segments {
		inStoryboard[seg.ID] = seg
	}
	bySeg := map[string][]directorIssue{}
	for _, is := range vd.Issues {
		if is.Severity == "high" && seenSeg[is.SegID] {
			if _, ok := inStoryboard[is.SegID]; ok {
				bySeg[is.SegID] = append(bySeg[is.SegID], is)
			}
		}
	}
	if len(bySeg) == 0 {
		r.emit(id, "stage", "director", "done")
		if len(vd.Issues) == 0 {
			r.Manifest(p, "director.pass", fmt.Sprintf("%d 帧全过", len(ordered)))
		} else {
			r.Manifest(p, "director.pass", fmt.Sprintf("无 high；%d 条 medium 备注见 reports/director-review.json", len(vd.Issues)))
		}
		return nil
	}
	r.emit(id, "progress", "director", fmt.Sprintf("%d 段按导演意见重出画面", len(bySeg)))
	var repaired []string
	for segID, issues := range bySeg {
		r.Manifest(p, "director.repair", segID+": "+pipeline.FirstLine(issues[0].Note, 80))
		// 重出失败≠拦制作：旧画面已过 lint 可渲染，保留继续（宁放过不误杀，同 prereview）
		if gerr := r.sceneGen(ctx, p, "", directorFeedback(inStoryboard[segID], issues), segID); gerr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			r.Manifest(p, "director.regen_error", segID+": "+pipeline.FirstLine(gerr.Error(), 120))
			continue
		}
		repaired = append(repaired, segID)
		r.clearFrames(p, segID)
	}
	if len(repaired) > 0 {
		if err := pipeline.Run(ctx, p, "compositions"); err != nil {
			return fmt.Errorf("合成物落盘失败: %w", err)
		}
		if err := r.assemble(ctx, p); err != nil {
			return fmt.Errorf("组装失败: %w", err)
		}
	}
	r.emit(id, "stage", "director", fmt.Sprintf("done（%d/%d 段按意见重出，不二审——机械门禁与渲后复查兜底）", len(repaired), len(bySeg)))
	r.Manifest(p, "director.issues", fmt.Sprintf("high %d 段，重出 %d 段（%s）",
		len(bySeg), len(repaired), strings.Join(repaired, ",")))
	report.Repaired = repaired
	if b, merr := json.MarshalIndent(report, "", "  "); merr == nil {
		_ = pipeline.WriteFileAtomic(p.Artifact("reports/director-review.json"), b, 0o644)
	}
	return nil
}

// directorReviewPrompt 审片考卷：全片连环画一次一调（区别于 frameQA/prereview 的
// 逐段判分）。每帧自带「此刻应该是什么」——预期考卷正是拉普拉斯妖对照实验里
// 唯一能抓内容缺失型真 bug 的手段（计数器死掉/画面空洞，静态与运行时检查全盲）。
func directorReviewPrompt(sb *contract.Storyboard, shots []directorShot) string {
	var b strings.Builder
	fmt.Fprintf(&b, `你是这部视频的导演，正在看粗剪：%d 帧全片关键帧连环画，按时间正序（帧身份见时刻表）。
这不是查单帧瑕疵（另有质检员管像素级问题）——你的任务是以全片视角审：这些段连起来，像不像一部按弧线讲的片子。

`, len(shots))
	if sb.Arc != nil {
		fmt.Fprintf(&b, "## 全片弧线（对照实际观感）\n主线：%s\n弧线：", sb.Arc.Spine)
		for i, ch := range sb.Arc.Chapters {
			if i > 0 {
				b.WriteString(" → ")
			}
			fmt.Fprintf(&b, "「%s」%s（张力 %d/5，%s–%s）", ch.Title, ch.Mood, ch.Tension, ch.Segs[0], ch.Segs[len(ch.Segs)-1])
		}
		b.WriteString("\n\n")
	}
	if len(sb.Motifs) > 0 {
		b.WriteString("## 母题注册表（复现要能认出是同一意象）\n")
		for _, m := range sb.Motifs {
			apps := make([]string, len(m.Appearances))
			for i, ap := range m.Appearances {
				apps[i] = ap.SegID + "(" + ap.Role + ")"
			}
			fmt.Fprintf(&b, "- 「%s」：%s——出场 %s\n", m.Name, m.Desc, strings.Join(apps, "、"))
		}
		b.WriteString("\n")
	}
	b.WriteString("## 帧身份时刻表（定位键；图号从 1 起）\n")
	for i, sh := range shots {
		fmt.Fprintf(&b, "图%d = %.1fs「%s」%s 第 %d 拍「%s」——此刻画面应有：%s", i+1, sh.At, sh.SegID, sh.Key, sh.BeatIdx+1, sh.Beat.Quote, sh.Beat.Expect)
		if sh.Chapter != "" {
			if sh.First {
				fmt.Fprintf(&b, "（章「%s」开篇）", sh.Chapter)
			} else {
				fmt.Fprintf(&b, "（章「%s」）", sh.Chapter)
			}
		}
		if sh.Motifs != "" {
			fmt.Fprintf(&b, "；母题「%s」应在此可见", sh.Motifs)
		}
		b.WriteString("\n")
	}
	b.WriteString(`
## 四查（只报帧上真实可见的问题；动效中间态、深底留白、scene 画布构图是设计不是问题）
1. 预期对照（expectation_miss）：逐帧对照时刻表的「此刻画面应有」——该出现的主体没出现或明显货不对板 = 内容缺失，最严重一类
2. 母题复现（motif_missing）：标了母题的帧里找该意象——认不出是同一个东西的复现即报
3. 节奏（pacing）：对照弧线张力——连续多帧高密度堆砌无喘息 / 连续多帧空淡如水，与所在章的张力定位明显不符
4. 衔接与空洞（deadair）：相邻章的画面接不接得住（风格/主体突然断裂）；单帧画面明显空转（旁白讲着内容画面没响应）

## 输出 JSON
{"pass": true/false, "issues": [{"seg_id": "segNN", "kind": "expectation_miss|motif_missing|pacing|deadair", "severity": "high|medium", "note": "证据（哪帧什么现象）", "suggestion": "这一段该怎么改"}]}
pass 标准：无 high 即 true；medium 只记录不判失败。seg_id 必须取时刻表里出现过的段 id。`)
	return b.String()
}

// directorFeedback 导演意见 → sceneGen 的 feedback 位：意见 + 本段节拍表重申 +
// 设计系统不动的边界（导演有权说演错了，无权换布景）。
func directorFeedback(seg contract.Segment, issues []directorIssue) string {
	var b strings.Builder
	b.WriteString("导演审片意见（全片视角发现的问题，必须落实；设计系统/皮肤/字体不动，只改指出的问题）：\n")
	for _, is := range issues {
		fmt.Fprintf(&b, "- [%s] %s → %s\n", is.Kind, is.Note, is.Suggestion)
	}
	if len(seg.Beats) > 0 {
		b.WriteString("本段节拍表（重申——元素揭示与画面内容按节拍排，不前载不拖尾）：\n")
		for _, bt := range seg.Beats {
			fmt.Fprintf(&b, "- 「%s」→ %s\n", bt.Quote, bt.Expect)
		}
	}
	return b.String()
}
