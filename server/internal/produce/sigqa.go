package produce

// 特色验收（v3 §4.6）：三层机制的第二层——QA 逐条判据。
// frameqa 读简报 + 落点表构造「特色判据清单」：每条特色由 mechanism+use_hint 套
// 模板生成一句是非题，视觉模型逐条判 absorbed/drifted/missing；
// missing|drifted → 判据原句进修复要求 → 段级重做 → 复验（复用 frameQA 修复循环）。
//
// 第三层分档（帧判/片判）：palette/motif/motion/material 落定态静帧直接验；
// transition=段边界前后帧序列（边界前后各 3 帧，覆盖 ±3s）；camera 静帧只能验有无
// （回放人兜底）。video_url 通道（模型直读考核片段）落地前按帧序列实现——
// extractBoundaryFrames 的返回形状已按「证据图序列」设计，通道落地后替换为片段即可。

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
)

// sigCriterion 一条特色判据（简报特色 × 设计落点）。
type sigCriterion struct {
	Sig     contract.Signature
	LandsIn string // 落点组件（交卷登记；空=未登记，如重做后设计未重走落点校验）
	How     string
}

// signatureCriteria 特色判据清单：简报开启的特色按落点表配对（吸收了哪条、落在哪）。
func signatureCriteria(brief *contract.StyleBrief, ss *contract.StyleSamples) []sigCriterion {
	if brief == nil {
		return nil
	}
	landed := map[string]contract.SigAbsorption{}
	if ss != nil {
		for _, a := range ss.SignatureAbsorption {
			landed[a.SigID] = a
		}
	}
	out := make([]sigCriterion, 0, len(brief.Signatures))
	for _, sg := range brief.Signatures {
		c := sigCriterion{Sig: sg}
		if a := landed[sg.ID]; a.SigID != "" {
			c.LandsIn, c.How = a.LandsIn, a.How
		}
		out = append(out, c)
	}
	return out
}

// kindScope 特色六类的判据视角（人话前缀）。
func kindScope(kind string) string {
	switch kind {
	case contract.SigKindTransition:
		return "段间切换"
	case contract.SigKindCamera:
		return "镜头运动"
	case contract.SigKindMotion:
		return "动效节奏"
	case contract.SigKindMotif:
		return "母题复现"
	case contract.SigKindMaterial:
		return "材质质感"
	case contract.SigKindPalette:
		return "配色"
	}
	return "手法"
}

// Question 判据句（是非题；判据原句会原样进修复要求）。
func (c sigCriterion) Question() string {
	q := fmt.Sprintf("检查【%s·%s】：%s 是否实现了「%s」——%s",
		kindScope(c.Sig.Kind), c.Sig.Name, kindScope(c.Sig.Kind), c.Sig.Name, c.Sig.Mechanism)
	if c.LandsIn != "" {
		q += "（设计落点：" + c.LandsIn
		if c.How != "" {
			q += "：" + c.How
		}
		q += "）"
	}
	if c.Sig.UseHint != "" {
		q += "（适用：" + c.Sig.UseHint + "）"
	}
	q += "？"
	if c.Sig.Kind == contract.SigKindCamera {
		q += "静态帧只能粗判运镜痕迹，拿不准判 absorbed（回放人兜底，不误杀）。"
	}
	return q
}

// hasDynamicCrit 是否含动态特色（transition/camera）——决定要不要追加分段边界帧。
func hasDynamicCrit(crits []sigCriterion) bool {
	for _, c := range crits {
		if contract.IsDynamicSignature(c.Sig.Kind) {
			return true
		}
	}
	return false
}

// boundaryFrameTimes 边界前后取帧时刻：前段尾（-3/-1.5/-0.3s）+ 本段头（+0.4/+1.6/+3s）。
var boundaryFrameTimes = []struct {
	fromTail bool
	off      string
}{
	{true, "-3.0"}, {true, "-1.6"}, {true, "-0.3"},
	{false, "0.4"}, {false, "1.6"}, {false, "3.0"},
}

// extractBoundaryFrames 段边界前后帧序列（transition 判据的考核证据，§4.6 第三层
// 帧通道实现：边界前后各 3 帧覆盖 ±3s）。首段无上边界返回 nil；抽帧失败容忍缺张。
func extractBoundaryFrames(ctx context.Context, p *pipeline.Project, segIdx int) []string {
	if segIdx < 2 {
		return nil
	}
	prevID := fmt.Sprintf("seg%02d", segIdx-1)
	curID := fmt.Sprintf("seg%02d", segIdx)
	prevMP4 := p.Artifact("renders/segs/" + prevID + ".mp4")
	curMP4 := p.Artifact("renders/segs/" + curID + ".mp4")
	if !existsFile(prevMP4) || !existsFile(curMP4) {
		return nil
	}
	ff, err := pipeline.FFmpegPath()
	if err != nil {
		return nil
	}
	dir := p.Artifact("reports/frameqa")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	var urls []string
	for i, bt := range boundaryFrameTimes {
		mp4, out := curMP4, filepath.Join(dir, fmt.Sprintf("%s.b%d.jpg", curID, i))
		var seek []string
		if bt.fromTail {
			mp4, out = prevMP4, filepath.Join(dir, fmt.Sprintf("%s.b%d.jpg", prevID, i))
			seek = []string{"-sseof", bt.off}
		} else {
			seek = []string{"-ss", bt.off}
		}
		if u := grabJPEG(ctx, ff, mp4, out, seek); u != "" {
			urls = append(urls, u)
		}
	}
	return urls
}

// grabJPEG 抽一帧成 1280 宽 JPEG 并返回 data URL（失败返回 ""）。
// 先删旧图再抽——与 frameqa 四帧审查同纪律：修复轮/重做后段片已变，
// 命中旧缓存会把陈旧画面当证据送审。
func grabJPEG(ctx context.Context, ff, mp4, out string, seek []string) string {
	_ = os.Remove(out)
	args := append([]string{"-y", "-v", "error"}, seek...)
	args = append(args, "-i", mp4, "-frames:v", "1", "-vf", "scale=1280:-2", "-q:v", "4", out)
	cmd := exec.CommandContext(ctx, ff, args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil || !existsFile(out) {
		_ = os.Remove(out)
		return ""
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return ""
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b)
}

// sigVerdictFrame QA 输出的特色判定行。
type sigVerdictRow struct {
	SigID   string `json:"sig_id"`
	Verdict string `json:"verdict"` // absorbed | drifted | missing
	Note    string `json:"note"`
}

// sigFailed 特色判定是否存在 missing|drifted（→ 该段进修复轮）。
func sigFailed(rows []sigVerdictRow) bool {
	for _, r := range rows {
		if r.Verdict == "missing" || r.Verdict == "drifted" {
			return true
		}
	}
	return false
}

// sigFeedback 特色判据原句 → 回喂 sceneGen 的修正要求（判定+判据+落点提示）。
func sigFeedback(rows []sigVerdictRow, crits []sigCriterion) string {
	byID := map[string]sigCriterion{}
	for _, c := range crits {
		byID[c.Sig.ID] = c
	}
	var b bytes.Buffer
	b.WriteString("强参考特色判定未过（重出时必须按判据实现该手法，机制照搬、物件用本段内容）：\n")
	for _, r := range rows {
		if r.Verdict != "missing" && r.Verdict != "drifted" {
			continue
		}
		c := byID[r.SigID]
		fmt.Fprintf(&b, "- [特色·%s] %s：%s", verdictLabel(r.Verdict), c.Sig.Name, c.Question())
		if r.Note != "" {
			fmt.Fprintf(&b, "（审查备注：%s）", r.Note)
		}
		if c.LandsIn != "" {
			fmt.Fprintf(&b, "→ 落点 %s", c.LandsIn)
			if c.How != "" {
				fmt.Fprintf(&b, "：%s", c.How)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func verdictLabel(v string) string {
	if v == "missing" {
		return "缺失"
	}
	return "走样"
}

// criteriaBlock 审查提示里的特色判据章节（无判据返回 ""）。
// boundaryN 是本段附带的边界帧序列张数（>0 时告知模型看图判定 transition）。
func criteriaBlock(crits []sigCriterion, boundaryN int) string {
	if len(crits) == 0 {
		return ""
	}
	var b bytes.Buffer
	b.WriteString("\n## 特色判据（用户选定的强参考，逐条判定 absorbed/drifted/missing）\n")
	for i, c := range crits {
		fmt.Fprintf(&b, "%d. %s\n", i+1, c.Question())
	}
	b.WriteString("- 判定口径：画面实现了该手法=absorbed；做了但明显走样=drifted；完全没体现=missing\n")
	if boundaryN > 0 {
		fmt.Fprintf(&b, "- 转场类判据看消息末尾附的 %d 张段边界前后帧序列（前 3 张=上一段结尾、后 3 张=本段开头）\n", boundaryN)
	}
	b.WriteString("- 判的是手法不是内容：本段画的内容与参考片不同是应该的，只看机制在不在\n")
	b.WriteString(`- 输出进 "signatures": [{"sig_id":"sig1","verdict":"absorbed|drifted|missing","note":"一句话依据"}]
`)
	return b.String()
}
