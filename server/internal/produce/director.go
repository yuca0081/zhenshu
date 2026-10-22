// director.go —— 导演统筹注入（原 M1；M2 审片链已随 legacy 管线退役删除，
// 2026-10-22 拍板老项目不再重跑。prep 项目的审片由 segpipe 的段审/边界审/
// 全片终审承担）。directorNoteFor 把弧线定位/母题任务/邻段衔接/本段节拍注入
// 每段生成 prompt，directorFingerprint 让导演字段改动正确传导成段缓存过期。
package produce

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"webpage-video-agent/server/internal/contract"
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
