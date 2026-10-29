package styleref

// 项目解析路的打标提示词（2026-10-29，原「成片反哺」直写库路径已退役——项目解析
// 现与上传视频解析同走暂存区/修订/确认入库流程）：把项目已确认的设计系统起草成
// 「标签+特色」草稿。样张帧由 SaveProject 拷进暂存目录，但打标走文本通道（LLM
// 看不见图）：设计系统 digest → 词表比对 + 申请新词，frames 留空由 Go 侧机械挂
// 样张；与上传视频解析共用 resolveTaggerDraft 的词表查重/落表机制。

import (
	"context"
	"fmt"
	"strings"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/stylelib"
)

// DraftFromDesign 从项目设计系统起草标签+特色（项目解析路 runProjectParse 调用）。
// designDigest 是设计系统的人话摘要（direction/concept/皮肤槽位/字体/动效/装置/
// 版式/负面清单），lib 是风格库（词表查重落表用）。
func DraftFromDesign(ctx context.Context, prov *llm.Provider, lib *stylelib.Library, projectName, designDigest string) (*StyleDraft, error) {
	vocabList := lib.VocabList()
	var vocabB strings.Builder
	if len(vocabList) > 0 {
		for _, e := range vocabList {
			vocabB.WriteString("- " + e.ID + "（" + e.Name + "）：" + e.Brief + "\n")
		}
	} else {
		vocabB.WriteString("（词表还是空的——你打的每个标签都会作为第一批新词申请入库）\n")
	}
	var lastErr error
	for round := 0; round < 3; round++ {
		var retryNote string
		if lastErr != nil {
			retryNote = "\n\n## 上一次输出被拒（修正后重出）\n" + lastErr.Error()
		}
		var raw taggerOutput
		_, err := prov.GenerateJSONNoThink(ctx,
			"你是视频风格策展人：给一套自制设计系统打公认风格标签、提炼它真正独有的手法特色。只输出 JSON。",
			fmt.Sprintf(`给成片「%s」的设计系统打风格标签、提炼特色，产物进风格库供后续新片复用。

## 风格词表（打标签先查这里）
%s
## 这套设计系统（本片已确认、全片遵守的规矩）
%s
## 打标签（主 1 + 辅 ≤2）
- 只收有公认视觉定义的风格名词（设计史/影视行话立得住的）；词表里有的直接引用 id，
  没有合适词条才「申请新词」（name+aliases+brief+tells+小写蛇形英文 id）；
- note 写人话指认（这套系统里哪个决定支撑了这个判断）；frames 一律留空
  （样张证据帧由系统挂，人工可在确认页调整）。

## 提炼特色（≤6 条，只写这套系统真正独有的手法）
- kind 六选一：%s；mechanism 写机制不写物件（换题材照做仍成立），use_hint 写什么节拍适合用；
- 从 device/motion/skins/layouts/transition 习惯里挑有记号的机制；公认风格自带的手法不算特色；
- frames 一律留空（同上，由系统挂样张、人工可调）。

## 条目名
- name 是这套系统在库里的陈列名（≤12 字）：概括它独有气质的独特名（如「冷晶仪象」），
  不要写主标签家族名（同标签多条目会同名撞车）。

## 分面三轴（检索用，各 ≤6 字短语）
- mood 情绪（如 沉稳/明快/悬疑/热血/治愈）、tone 色调（如 黑金/低饱和暖色/冷蓝/高对比黑白）、
  density 密度（如 疏朗/中密/高密）——从这套系统的实际观感归纳，措辞贴近示例便于筛选命中。

## 题材只进 subject
- subject 一句话记录本片画的是什么（≤30 字）——只供检索，永不进生成提示词。

## 输出（只输出 JSON，无围栏）
{"name":"条目名（≤12 字，独特名）","subject":"本片题材一句话",
"facets":{"mood":"..","tone":"..","density":".."},
"tags":[{"id":"..","new":{..},"note":".."}],
"signatures":[{"kind":"..","name":"..","mechanism":"..","use_hint":".."}]}%s`,
				projectName, vocabB.String(), designDigest, contract.SignatureKindNames(), retryNote),
			&raw)
		if err != nil {
			return nil, fmt.Errorf("起草作业失败: %w", err)
		}
		d, rerr := resolveDesignDraft(ctx, prov, lib, &raw)
		if rerr == nil {
			return d, nil
		}
		lastErr = rerr
	}
	return nil, lastErr
}

// resolveDesignDraft 视频版 resolveDraft 的无帧变体：与 resolveDraft 共用
// resolveTaggerDraft 的词表落表纪律，只是不做帧证据处理与存在性校验
// （项目解析路打标看不见图，草稿帧由 Go 侧机械挂样张、人工可在确认页补）。
func resolveDesignDraft(ctx context.Context, prov *llm.Provider, lib *stylelib.Library, raw *taggerOutput) (*StyleDraft, error) {
	return resolveTaggerDraft(ctx, prov, lib, raw, false)
}

// resolveTaggerDraft 打标原始输出 → 落表草稿的共用执行体（resolveDraft 上传视频解析 /
// resolveDesignDraft 项目解析两路唯一实现）：词表 id 直用；新词申请走查重（归一化
// 精确匹配 → LLM 语义查重）后建词条/挂别名。这是「申请新词」工具的 Go 侧执行体。
// withFrames=false 时不带证据帧。
func resolveTaggerDraft(ctx context.Context, prov *llm.Provider, lib *stylelib.Library,
	raw *taggerOutput, withFrames bool) (*StyleDraft, error) {
	if len(raw.Tags) < 1 || len(raw.Tags) > 3 {
		return nil, fmt.Errorf("tags 数量 %d 不合法（主 1 + 辅 ≤2）", len(raw.Tags))
	}
	vocab, err := lib.LoadVocab()
	if err != nil {
		return nil, fmt.Errorf("词表读取失败: %w", err)
	}
	// 先集中归一化精确查重 + 收集语义查重候选
	var pend []*newWord
	ids := make([]string, 0, len(raw.Tags))
	names := make([]string, 0, len(raw.Tags)) // 各标签正式名（草稿名缺省回退用）
	for i, t := range raw.Tags {
		switch {
		case t.ID != "" && vocab.Has(t.ID):
			ids = append(ids, t.ID)
			names = append(names, vocab.Entries[t.ID].Name)
		case t.New != nil && strings.TrimSpace(t.New.Name) != "":
			if hit, ok := vocab.Find(t.New.Name); ok {
				ids = append(ids, hit.ID) // 精确撞车：复用词条
				names = append(names, hit.Name)
			} else {
				ids = append(ids, "") // 占位：语义查重后落表
				names = append(names, strings.TrimSpace(t.New.Name))
				pend = append(pend, t.New)
			}
		default:
			return nil, fmt.Errorf("第 %d 个标签既不在词表也没申请新词", i+1)
		}
	}
	// 语义查重（一次 LLM 调用批处理全部候选；命中挂别名复用，未命中建词条）。
	// blanks 预先记下占位下标——循环里逐个回填后空白位会变，边数边填会错位。
	if len(pend) > 0 {
		blanks := make([]int, 0, len(pend))
		for i, id := range ids {
			if id == "" {
				blanks = append(blanks, i)
			}
		}
		cands := make([]string, 0, len(pend))
		for _, w := range pend {
			cands = append(cands, w.Name)
		}
		hits, derr := semanticDedup(ctx, prov, lib, cands)
		if derr != nil {
			return nil, fmt.Errorf("新词语义查重失败: %w", derr)
		}
		for pi, w := range pend {
			if exID := hits[w.Name]; exID != "" && vocab.Has(exID) {
				if aerr := lib.VocabAttachAlias(exID, w.Name); aerr != nil {
					return nil, aerr
				}
				ids[blanks[pi]] = exID
				continue
			}
			id, cerr := lib.VocabCreate(stylelib.VocabEntry{
				ID: w.ID, Name: w.Name, Aliases: w.Aliases, Brief: w.Brief, Tells: w.Tells,
			})
			if cerr != nil {
				return nil, fmt.Errorf("新词 %q 入表失败: %w", w.Name, cerr)
			}
			ids[blanks[pi]] = id
		}
	}
	d := &StyleDraft{
		Name:    strings.TrimSpace(raw.Name),
		Subject: strings.TrimSpace(raw.Subject),
		Facets: stylelib.StyleFacets{
			Mood:    strings.TrimSpace(raw.Facets.Mood),
			Tone:    strings.TrimSpace(raw.Facets.Tone),
			Density: strings.TrimSpace(raw.Facets.Density),
		},
		Tags: contract.StyleTags{Main: ids[0], Aux: ids[1:]},
	}
	if d.Name == "" {
		d.Name = names[0] // 缺省用主标签正式名
	}
	for i, t := range raw.Tags {
		ev := contract.TagEvidence{Tag: ids[i], Note: strings.TrimSpace(t.Note)}
		if withFrames {
			ev.Frames = t.Frames
		}
		d.TagEvidence = append(d.TagEvidence, ev)
	}
	d.Signatures = append(d.Signatures, raw.Signatures...)
	return d, stylelib.ValidateSignatures(d.Signatures)
}
