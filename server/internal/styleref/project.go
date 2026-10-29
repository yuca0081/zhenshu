package styleref

// 项目解析路（两路解析的打标分叉，2026-10-29）：出片项目 → 风格库条目。
//
// 依据：成片是设计系统（style/style_samples.json）的确定性渲染——视频里有的
// 视觉信息设计系统里都有且更精确（CSS 色值/字体名/动效毫秒/负面清单），所以
// 这条路不抽帧、不碰多模态，直接读设计系统走文本通道打标（RolePlan 纯文本脑）；
// 样张 poster（SaveProject 已拷进暂存目录）作证据帧。与上传视频解析共用
// 暂存区/修订/入库全流程（analyze.go runAgent 只在打标处按 Source 分叉）。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
)

// DesignDigest 项目设计系统人话摘要（项目解析路的打标原料）：direction/concept/
// 皮肤槽位/字体/动效/装置/版式/负面清单，机械拼接不起 LLM。
func DesignDigest(p *pipeline.Project) (string, error) {
	b, err := os.ReadFile(p.Artifact("style/style_samples.json"))
	if err != nil {
		return "", fmt.Errorf("项目无设计系统（style_samples.json 缺失）")
	}
	var ss contract.StyleSamples
	if err := json.Unmarshal(b, &ss); err != nil {
		return "", err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "- 方向：%s\n- 概念：%s\n", ss.Direction, ss.Concept)
	for i, sk := range ss.EffectiveSkins() {
		label := "皮肤"
		if i == 1 {
			label = "副皮肤"
		}
		fmt.Fprintf(&sb, "- %s「%s」：底 %s / 字 %s / 强调 %s；css %s\n",
			label, sk.Name, sk.Slots.BG, sk.Slots.TXT, sk.Slots.Accent, pipeline.FirstLine(sk.CSS, 200))
	}
	if ss.SkinRule != "" {
		fmt.Fprintf(&sb, "- 皮肤切换：%s\n", ss.SkinRule)
	}
	if f := ss.Fonts; f != nil {
		fmt.Fprintf(&sb, "- 字体：display %s / body %s / data %s\n", f.Display.Stack, f.Body.Stack, f.Data.Stack)
	}
	if m := ss.Motion; m != nil {
		fmt.Fprintf(&sb, "- 动效：%s，进场 %.2fs 落定 %.2fs 错峰 %.2fs——%s\n", m.Ease, m.DurIn, m.DurSettle, m.Stagger, m.Notes)
	}
	if d := ss.Device; d != nil {
		fmt.Fprintf(&sb, "- 贯穿装置「%s」：%s；逐段：%s\n", d.Name, d.Desc, d.PerSeg)
	}
	for _, l := range ss.Layouts {
		fmt.Fprintf(&sb, "- 版式 %s（%s）：%s\n", l.ID, l.Name, l.Desc)
	}
	if len(ss.AntiPatterns) > 0 {
		fmt.Fprintf(&sb, "- 负面清单：%s\n", strings.Join(ss.AntiPatterns, "、"))
	}
	return sb.String(), nil
}

// runProjectParse 项目解析的打标执行体（runAgent 按 Source 分叉进入；fail 由调用方
// 注入，与上传路同一状态回写纪律：解析只拥有 Status/Error/Style 字段）。
func (s *Store) runProjectParse(ctx context.Context, ref *Ref, fail func(error)) {
	proj := pipeline.NewProject(s.DataDir, ref.ProjectID)
	digest, err := DesignDigest(proj)
	if err != nil {
		fail(err)
		return
	}
	prov, err := llm.FromEnv(llm.StationRefTag)
	if err != nil {
		fail(fmt.Errorf("解析需要 API 模式（LLM_API_KEY）: %w", err))
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	d, err := DraftFromDesign(ctx, prov, s.Lib, ref.Name, digest)
	if err != nil {
		fail(err)
		return
	}
	// 样张帧 = 设计系统的视觉凭证：文本打标看不见图，主标签证据机械挂第一张；
	// 特色帧留空（哪帧演示哪个机制要人判断，确认页编辑可补挂）。
	if feats, ferr := readFeatures(s.dir(ref.ID)); ferr == nil && len(feats.Frames) > 0 && len(d.TagEvidence) > 0 {
		d.TagEvidence[0].Frames = []string{filepath.Base(feats.Frames[0].Path)}
	}
	if err := s.mutateRef(ref.ID, func(r *Ref) error {
		r.Style, r.Status, r.Error = d, "done", ""
		return nil
	}); err != nil {
		fail(err)
	}
}
