package contract

// v3 风格库共享数据形状（docs/style-library-v3.md §4.1/§4.3/§4.5）：
// 条目（stylelib.Style）、风格简报（项目 style/style_brief.json）、设计落点表
// （StyleSamples.signature_absorption）三方共用，放契约层防各包各持一份漂移。

import (
	"fmt"
	"strings"
)

// StyleTags 条目标签：主 1 + 辅 ≤2，值全部是词表 id（data/library/vocab.json）。
type StyleTags struct {
	Main string   `json:"main"`          // 必填，词表 id
	Aux  []string `json:"aux,omitempty"` // ≤2，均为词表 id
}

// TagEvidence 打标证据：每个标签至少一条人话指认 + 证据帧。
type TagEvidence struct {
	Tag    string   `json:"tag"`
	Note   string   `json:"note"`             // 「第2段：紫青撞色+网格地平线」
	Frames []string `json:"frames,omitempty"` // style_media/<id>/ 下 frame-*.png 文件名
}

// Signature 特色（参考片真正独有的手法）：六类枚举 + 机制化描述 + 证据帧。
// Mechanism 必须写机制不写物件（题材中毒后门），校验靠提示词纪律+人工过目+QA 反向判据，
// 机械不做词汇黑名单（误杀率高）。
type Signature struct {
	ID        string   `json:"id"`                 // sig1..sigN（条目内唯一）
	Kind      string   `json:"kind"`               // SignatureKind 枚举
	Name      string   `json:"name"`               // 人话短名
	Mechanism string   `json:"mechanism"`          // 机制描述 1-2 句，可执行
	Frames    []string `json:"frames,omitempty"`   // 证据帧 ≥1（项目解析路草稿允许空，人工在确认页补）
	UseHint   string   `json:"use_hint,omitempty"` // 什么节拍/场景适合用
}

// SignatureKind 特色六类：帧判（palette/motif/motion/material 落定态）与
// 片判（camera/transition 需要时间维度）的分档依据（QA 考核片段用）。
const (
	SigKindCamera     = "camera"     // 运镜
	SigKindTransition = "transition" // 转场
	SigKindMotif      = "motif"      // 母题
	SigKindMotion     = "motion"     // 动效
	SigKindMaterial   = "material"   // 素材/质感
	SigKindPalette    = "palette"    // 配色
)

var signatureKinds = map[string]bool{
	SigKindCamera: true, SigKindTransition: true, SigKindMotif: true,
	SigKindMotion: true, SigKindMaterial: true, SigKindPalette: true,
}

// ValidSignatureKind kind 是否合法枚举。
func ValidSignatureKind(kind string) bool { return signatureKinds[kind] }

// SignatureKindNames 枚举清单（提示词渲染用）。
func SignatureKindNames() string {
	return "camera|transition|motif|motion|material|palette"
}

// IsDynamicSignature 是否动态特色（需时间维度验证：运镜/转场）。
func IsDynamicSignature(kind string) bool {
	return kind == SigKindCamera || kind == SigKindTransition
}

// SigAbsorption 特色落点：设计交卷时每条开启的特色必须登记落在设计系统哪个组件。
type SigAbsorption struct {
	SigID   string `json:"sig_id"`
	LandsIn string `json:"lands_in"` // AbsorptionLands 枚举
	How     string `json:"how"`      // 一句话怎么落
}

// AbsorptionLands 落点枚举（designJSONShape / 落点校验共用）。
const (
	LandsDevice           = "device"
	LandsMotion           = "motion"
	LandsSkins            = "skins"
	LandsCameraPlan       = "camera_plan"
	LandsTransitionPolicy = "transition_policy"
	LandsMaterialPolicy   = "material_policy"
)

var absorptionLands = map[string]bool{
	LandsDevice: true, LandsMotion: true, LandsSkins: true,
	LandsCameraPlan: true, LandsTransitionPolicy: true, LandsMaterialPolicy: true,
}

// ValidAbsorptionLand lands_in 是否合法枚举。
func ValidAbsorptionLand(v string) bool { return absorptionLands[v] }

// AbsorptionLandNames 枚举清单（提示词渲染用）。
func AbsorptionLandNames() string {
	return "device|motion|skins|camera_plan|transition_policy|material_policy"
}

// TagBrief 词表词条的人话摘要（简报携带，供设计提示词直接引用，不再回查词表）。
type TagBrief struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Brief string `json:"brief"`
	Tells string `json:"tells,omitempty"` // 判别特征：帧里认什么——画面锚点，缺省兼容旧词条
}

// StyleBrief 风格简报（项目 style/style_brief.json，v3 §4.3）：选定条目在项目内的
// 本地拷贝，与库解耦——条目后来被删/改不影响已选项目。不选条目 = 文件不存在 =
// 设计阶段裸发明。写入者是 applyStyle API 与 agent 选定分支，同一文件同一格式。
type StyleBrief struct {
	EntryID    string      `json:"entry_id"`
	EntryName  string      `json:"entry_name"`
	Tags       StyleTags   `json:"tags"`
	TagBriefs  []TagBrief  `json:"tag_briefs"` // 从词表取：main+aux 的 {id,name,brief,tells}
	Signatures []Signature `json:"signatures"` // 开着（未关）的特色，含原文 mechanism
	Evidence   []string    `json:"evidence"`   // 证据帧绝对路径（style_media 取，≤6 张）
	// EvidenceNotes 与 Evidence 逐位对应的人话说明：标签证据=打标指认（「第2段：
	// 紫青撞色+网格地平线」），特色证据=「特色「名」的例证帧」。策展笔记随图走，
	// 附图时按帧配字幕——裸图只配一句泛泛说明等于让模型自己猜每张看什么。
	EvidenceNotes []string `json:"evidence_notes,omitempty"`
}

// CheckAbsorption 落点覆盖校验（stageStyleSamples 在草稿校验通过后追加执行）：
// brief 存在时 signature_absorption 必须恰好覆盖全部 sig_id 且 lands_in 合法。
// 多登不存在的 sig 也算错（模型自造特色=幻觉）。
func (b *StyleBrief) CheckAbsorption(got []SigAbsorption) error {
	if b == nil || len(b.Signatures) == 0 {
		return nil
	}
	want := map[string]bool{}
	for i := range b.Signatures {
		want[b.Signatures[i].ID] = true
	}
	seen := map[string]bool{}
	for _, a := range got {
		if !want[a.SigID] {
			return fmt.Errorf("signature_absorption 登记了简报之外的特色 %q（只能登记简报里的 sig_id）", a.SigID)
		}
		if seen[a.SigID] {
			return fmt.Errorf("signature_absorption 对 %q 重复登记", a.SigID)
		}
		seen[a.SigID] = true
		if !ValidAbsorptionLand(a.LandsIn) {
			return fmt.Errorf("signature_absorption.%s 的 lands_in=%q 越界（可选：%s）", a.SigID, a.LandsIn, AbsorptionLandNames())
		}
		if strings.TrimSpace(a.How) == "" {
			return fmt.Errorf("signature_absorption.%s 缺 how（一句话写清怎么落）", a.SigID)
		}
	}
	for id := range want {
		if !seen[id] {
			return fmt.Errorf("signature_absorption 漏了特色 %q（每条开启的特色都必须登记落点）", id)
		}
	}
	return nil
}

// CheckDynamicLanding 动态特色的「真实现」校验（CheckAbsorption 只查登记齐，
// 这里查落进动效语言没有）：转场/运镜/动效类特色承诺的手法必须在 motion 的
// 结构化字段里有实现——登记了 lands_in 却没落字段，特色就停在纸面上（实测：
// 「噪点吞没转场」登记丢失+无处可落，全片退化为黑场一切，观众感知=PPT）。
// brief 为 nil（未选库内风格）或 motion 缺失（旧设计）时不约束。
func (b *StyleBrief) CheckDynamicLanding(ss *StyleSamples) error {
	if b == nil || len(b.Signatures) == 0 || ss == nil || ss.Motion == nil {
		return nil
	}
	for _, sg := range b.Signatures {
		switch sg.Kind {
		case SigKindTransition:
			if strings.TrimSpace(ss.Motion.Transition) == "" {
				return fmt.Errorf("特色[%s]（transition）登记了落点但 motion.transition 为空——段间转场政策必须按该特色机制设计", sg.Name)
			}
		case SigKindCamera:
			if strings.TrimSpace(ss.Motion.Camera) == "" {
				return fmt.Errorf("特色[%s]（camera）登记了落点但 motion.camera 为空——运镜计划必须按该特色机制设计", sg.Name)
			}
		case SigKindMotion:
			if len(ss.Motion.Entrances) == 0 {
				return fmt.Errorf("特色[%s]（motion）登记了落点但 motion.entrances 为空——入场手法菜单必须按该特色机制设计", sg.Name)
			}
		}
	}
	return nil
}
