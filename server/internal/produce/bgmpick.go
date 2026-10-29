// bgmpick.go —— 配乐工位（段级流水线 TTS 之后）：读分镜弧线 + 实测语速 + 曲库
// 标签，LLM 一次轻量调用选曲，机械执法纠偏，规则兜底。写 project.json bgm 字段
// 后，拼接混音（mixBGM sidechain 闪避）零改动自动生效。
//
// 幂等与主权：project.json 已有 bgm 字段（用户 set_bgm 拍板过/上轮已选）→ 永不
// 覆盖——自动选曲只填空，不推翻人。
// 非阻塞哲学（同 translateSubtitles/fonts）：曲库缺失、LLM 不可用、预算闸刹车、
// 校验不过 → 规则兜底或跳过，永不挡片。
package produce

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/agentlog"
	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
)

// bgmPick LLM 选曲输出（只输出 JSON）。
type bgmPick struct {
	Track  string `json:"track"`
	Reason string `json:"reason"`
}

// tempoForPace 实测语速（字/秒）→ 偏好节奏。阈值放宽到只抓极端错配（快稿慢垫/
// 慢稿快曲才干预，中速稳态不设限）——对比度回填的教训：机械执法范围越窄越稳。
func tempoForPace(cps float64) string {
	switch {
	case cps <= 0:
		return "" // 无实测数据（老项目缺 audio_meta）：不设限
	case cps >= 5.6:
		return "fast"
	case cps <= 3.6:
		return "slow"
	default:
		return "mid"
	}
}

// speechPace 全片实测语速：旁白字数（去空白）/ 配音总秒数。
func speechPace(sb *contract.Storyboard, voices map[string]voiceMeta) (float64, bool) {
	if sb == nil || len(voices) == 0 {
		return 0, false
	}
	chars, secs := 0, 0.0
	for _, seg := range sb.Segments {
		v, ok := voices[seg.ID]
		if !ok || v.DurationS <= 0 {
			continue
		}
		chars += len([]rune(stripSpaces(seg.Narration)))
		secs += v.DurationS
	}
	if chars == 0 || secs <= 0 {
		return 0, false
	}
	return float64(chars) / secs, true
}

// wantEnergy 期望能量：章节张力均值（同 1-5 标度直接映射）；无弧线 → 3（中庸）。
func wantEnergy(sb *contract.Storyboard) int {
	if sb == nil || sb.Arc == nil || len(sb.Arc.Chapters) == 0 {
		return 3
	}
	sum := 0
	for _, ch := range sb.Arc.Chapters {
		sum += ch.Tension
	}
	avg := float64(sum) / float64(len(sb.Arc.Chapters))
	e := int(avg + 0.5)
	if e < 1 {
		e = 1
	}
	if e > 5 {
		e = 5
	}
	return e
}

// tempoConflict 极端错配判据：快稿慢垫 / 慢稿快曲 才算冲突；mid 与未标注不设限。
func tempoConflict(track pipeline.BGMChoice, want string) bool {
	if want == "" || track.Tempo == "" {
		return false
	}
	return (want == "fast" && track.Tempo == "slow") ||
		(want == "slow" && track.Tempo == "fast")
}

// pickByRule 规则兜底选曲： tempo 合规优先（错配 +100）> 能量贴近（未标注 +2，
// 让有标签的优先）> 曲库顺序（稳定）。全部无标签时自然落在库序第一首（calm）。
func pickByRule(choices []pipeline.BGMChoice, wantTempo string, wantEnergy int) pipeline.BGMChoice {
	best := choices[0]
	bestScore := 1 << 30
	for _, c := range choices {
		s := 0
		if tempoConflict(c, wantTempo) {
			s += 100
		}
		if c.Energy > 0 {
			s += absInt(c.Energy - wantEnergy)
		} else {
			s += 2
		}
		if s < bestScore {
			best, bestScore = c, s
		}
	}
	return best
}

// overridePick 机械改判：在 tempo 硬合规的候选里按能量贴近挑（改判不降级——
// 原选曲的能量偏好保留到合规曲目上）。
func overridePick(choices []pipeline.BGMChoice, wantTempo string, energyAnchor int) pipeline.BGMChoice {
	best := pipeline.BGMChoice{}
	bestScore := 1 << 30
	for _, c := range choices {
		if tempoConflict(c, wantTempo) {
			continue
		}
		s := 0
		if c.Energy > 0 {
			s += absInt(c.Energy - energyAnchor)
		} else {
			s += 2
		}
		if s < bestScore {
			best, bestScore = c, s
		}
	}
	if best.Name == "" {
		return pickByRule(choices, wantTempo, energyAnchor)
	}
	return best
}

func tempoLabel(t string) string {
	switch t {
	case "fast":
		return "快"
	case "slow":
		return "慢"
	case "mid":
		return "中速"
	default:
		return "不限"
	}
}

// bgmPromptChapters 弧线 → 逐行描述（含每章实测秒数，让模型知道哪章最重）。
func bgmPromptChapters(sb *contract.Storyboard, voices map[string]voiceMeta) string {
	if sb.Arc == nil || len(sb.Arc.Chapters) == 0 {
		return "（无章节弧线，共 " + fmt.Sprintf("%d", len(sb.Segments)) + " 段）"
	}
	lines := make([]string, 0, len(sb.Arc.Chapters))
	for _, ch := range sb.Arc.Chapters {
		secs := 0.0
		for _, id := range ch.Segs {
			if v, ok := voices[id]; ok {
				secs += v.DurationS
			}
		}
		mood := ch.Mood
		if mood == "" {
			mood = "未标注"
		}
		lines = append(lines, fmt.Sprintf("· %s｜情绪 %s｜张力 %d/5｜约 %.0f 秒",
			ch.Title, mood, ch.Tension, secs))
	}
	return strings.Join(lines, "\n")
}

// bgmScriptExcerpt 旁白节选（每段首 60 字、全片封顶 1200 字）：情绪判断要闻到
// 稿子的味道，光靠章节标题不够；封顶防长稿把一次轻量调用喂成大调用。
func bgmScriptExcerpt(sb *contract.Storyboard) string {
	var b strings.Builder
	budget := 1200
	for _, seg := range sb.Segments {
		if budget <= 0 {
			break
		}
		r := []rune(stripSpaces(seg.Narration))
		if len(r) > 60 {
			r = r[:60]
		}
		if len(r) > budget {
			r = r[:budget]
		}
		b.WriteString("· ")
		b.WriteString(string(r))
		b.WriteString("…\n")
		budget -= len(r)
	}
	return b.String()
}

// buildBGMPrompt 选曲提示词。题材中立红线：只依据稿子本身与弧线数据判断，
// 提示词不含任何题材预设。
func buildBGMPrompt(choices []pipeline.BGMChoice, sb *contract.Storyboard,
	voices map[string]voiceMeta, cps float64, wantTempo string) (system, user string) {
	system = "你是视频配乐师。背景音乐垫在人声之下（混音时会自动闪避压低），" +
		"只负责氛围：跟全片情绪对味、跟叙事弧线的能量对位、跟语速的快慢不打架。" +
		"只依据给出的稿子节选、章节弧线与语速数据判断，不预设任何题材。" +
		"只输出 JSON：{\"track\":\"曲名\",\"reason\":\"一句话理由\"}，" +
		"track 必须逐字取自曲库清单中的曲名。"
	var lib strings.Builder
	for _, c := range choices {
		lib.WriteString("· " + c.Name + "｜" + c.Mood)
		if c.Tempo != "" {
			lib.WriteString(fmt.Sprintf("｜%d BPM %s", c.Bpm, tempoLabel(c.Tempo)))
		}
		if c.Energy > 0 {
			lib.WriteString(fmt.Sprintf("｜能量 %d/5", c.Energy))
		}
		lib.WriteString("\n")
	}
	pace := "无实测数据"
	if cps > 0 {
		pace = fmt.Sprintf("%.1f 字/秒（%s）", cps, tempoLabel(wantTempo))
	}
	user = "## 曲库（只准从这里选）\n" + lib.String() +
		"\n## 全片主线\n" + arcSpine(sb) +
		"\n## 章节弧线\n" + bgmPromptChapters(sb, voices) +
		"\n## 实测语速\n" + pace +
		"\n## 稿子节选\n" + bgmScriptExcerpt(sb)
	return system, user
}

func arcSpine(sb *contract.Storyboard) string {
	if sb.Arc != nil && strings.TrimSpace(sb.Arc.Spine) != "" {
		return strings.TrimSpace(sb.Arc.Spine)
	}
	return "（未提供）"
}

// pickBGM 配乐工位入口：填空不推翻，失败兜底，永不挡片。
func (r *Runner) pickBGM(ctx context.Context, p *pipeline.Project,
	sb *contract.Storyboard, voices map[string]voiceMeta) {
	var cur struct {
		BGM string `json:"bgm"`
	}
	if b, err := os.ReadFile(p.Artifact("project.json")); err == nil &&
		json.Unmarshal(b, &cur) == nil && strings.TrimSpace(cur.BGM) != "" {
		// "off" 是用户拍板关闭的哨兵值（SetBGM 写入）：关闭也是拍板，照样不推翻
		if strings.EqualFold(strings.TrimSpace(cur.BGM), "off") {
			r.Manifest(p, "produce.bgm", "配乐已被用户关闭（set_bgm off），跳过自动选曲")
			return
		}
		r.Manifest(p, "produce.bgm", "保留已有配乐 "+strings.TrimSpace(cur.BGM))
		return
	}
	choices := pipeline.ListBGMChoices()
	if len(choices) == 0 {
		r.Manifest(p, "produce.bgm", "曲库为空，跳过配乐")
		return
	}
	cps, _ := speechPace(sb, voices)
	wantTempo := tempoForPace(cps)
	wantE := wantEnergy(sb)

	prov, perr := llm.FromEnv(llm.StationBgmPick)
	if perr != nil {
		prov = nil
	} else if err := llm.CheckBudget(ctx); err != nil {
		prov = nil // 预算闸=刹车：不烧 token，规则兜底
	}
	name, reason := "", ""
	if prov != nil {
		name, reason = r.llmPickBGM(ctx, p, prov, choices, sb, voices, cps, wantTempo)
	}
	if name == "" {
		fixed := pickByRule(choices, wantTempo, wantE)
		name, reason = fixed.Name, fmt.Sprintf(
			"规则兜底：弧线张力均值≈能量%d、语速 %.1f 字/秒偏好%s节奏",
			wantE, cps, tempoLabel(wantTempo))
	}
	// 机械执法：极端错配改判（LLM 选了慢垫配快稿这类），能量偏好保留到合规曲目
	if choice, ok := choiceByName(choices, name); ok && tempoConflict(choice, wantTempo) {
		fixed := overridePick(choices, wantTempo, energyOf(choices, name))
		reason = fmt.Sprintf("机械改判 %s→%s：语速 %.1f 字/秒不宜配%s垫（原由：%s）",
			name, fixed.Name, cps, tempoLabel(choice.Tempo), reason)
		name = fixed.Name
	}

	if err := pipeline.SetBGM(p, name); err != nil {
		r.Manifest(p, "produce.bgm", "选曲落库失败，跳过配乐")
		return
	}
	_ = pipeline.SetProjectField(p, "bgm_reason", reason)
	r.Manifest(p, "produce.bgm", "自动选曲 "+name+"："+reason)
	r.emit(p.ID, "progress", "bgm", "配乐："+name)
}

func choiceByName(choices []pipeline.BGMChoice, name string) (pipeline.BGMChoice, bool) {
	for _, c := range choices {
		if strings.EqualFold(c.Name, name) {
			return c, true
		}
	}
	return pipeline.BGMChoice{}, false
}

// energyOf 能量锚点（改判时保留原选的能量偏好）；未标注回 3。
func energyOf(choices []pipeline.BGMChoice, name string) int {
	if c, ok := choiceByName(choices, name); ok && c.Energy > 0 {
		return c.Energy
	}
	return 3
}

// llmPickBGM 一次轻量调用选曲（low effort），无效输出回喂重试一次，仍败回空
// 交规则兜底。记账同 translate：请求/响应留痕「过程」页。
func (r *Runner) llmPickBGM(ctx context.Context, p *pipeline.Project,
	prov *llm.Provider, choices []pipeline.BGMChoice, sb *contract.Storyboard,
	voices map[string]voiceMeta, cps float64, wantTempo string) (string, string) {
	system, user := buildBGMPrompt(choices, sb, voices, cps, wantTempo)
	at := agentlog.Begin(p, "bgm_pick", "自动配乐选曲")
	if rec := at.Rec(); rec != nil {
		rec.Message(openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: system})
		rec.Message(openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: user})
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		var out bgmPick
		u, err := prov.GenerateJSONEffort(ctx, system, user, &out, llm.EffortLow)
		at.Add(u)
		if rec := at.Rec(); rec != nil {
			content := pipeline.MustJSON(out)
			if err != nil {
				content = "ERROR: " + err.Error()
			}
			rec.Message(openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: content})
		}
		if err == nil {
			if c, ok := choiceByName(choices, strings.TrimSpace(out.Track)); ok {
				at.Done(nil)
				return c.Name, strings.TrimSpace(out.Reason)
			}
			lastErr = fmt.Errorf("track %q 不在曲库", out.Track)
		} else {
			lastErr = err
		}
		user += "\n\n上次输出无效（" + lastErr.Error() + "）。只输出曲库清单中的曲名。"
	}
	at.Done(lastErr)
	return "", ""
}
