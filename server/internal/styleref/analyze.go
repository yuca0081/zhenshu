package styleref

// 解析智能体：参考视频 → 风格条目草稿（一条主路径，两阶段）。
//
// 阶段1 理解：全片自适应抽帧（sheet 网格 + 取色）→ 逐帧构图观察。
// 阶段2 打标+提炼（v3）：全片一份「标签+特色」草稿，一次轻解析。
//
// 一个视频 = 一个条目。曾经的「单/多风格判断 + 自动拆段」已退役
// （2026-10-09）：单风格深度展示视频会被按场景硬切成十几段重复条目
// （实测卡带未来主义 12 段 11 段同标签），真遇到多风格合集，手动剪出
// 目标片段单独上传，拆得比模型自动拆更准。
//
// 护栏：LLM 只看图/视频和结构化事实；文件与进程全部由 Go 侧执行。
// 视频直读（2026-10-08）：PUBLIC_BASE_URL 配置后，阶段2 附 480p 无声剪辑
// 的签名 URL 供模型直看（运镜/转场/节奏实测）；未配置或剪辑失败自动退
// 纯帧。见 video.go。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"webpage-video-agent/server/internal/llm"
)

// runAgent 解析编排（Analyze 的 goroutine 体）。
func (s *Store) runAgent(ctx context.Context, ref *Ref) {
	// 状态回写走 mutateRef 字段合并（2026-10-19 中-26）：confirm/草稿编辑可能在
	// 解析期间落盘，整体回写旧副本会把它们抹掉——解析只拥有 Status/Error/Style。
	fail := func(err error) {
		_ = s.mutateRef(ref.ID, func(r *Ref) error { r.Status, r.Error = "error", err.Error(); return nil })
	}
	_ = s.mutateRef(ref.ID, func(r *Ref) error { r.Status, r.Error = "running", ""; return nil })

	// 两路解析分叉（2026-10-29）：project = 项目解析（读设计系统文本打标，不抽帧，
	// 见 project.go）；其余（空/"upload"）= 上传视频解析（抽帧+多模态，本函数主路径）。
	if ref.Source == SourceProject {
		s.runProjectParse(ctx, ref, fail)
		return
	}

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

	prov, err := llm.FromEnv(llm.StationRefAnalyze)
	if err != nil {
		fail(fmt.Errorf("解析需要 API 模式（LLM_API_KEY）: %w", err))
		return
	}

	// 3. 构图观察 + 打标提炼（附原视频剪辑直读——运镜/转场以视频实测为准；
	//    剪辑/签名任一步不成自动退纯帧，视频是增强项不挡解析；结构自检失败
	//    重喂 ≤2 轮在 synthesize 内）
	notes, err := s.analyzeCompositions(ctx, prov, feats.Frames)
	if err != nil {
		fail(err)
		return
	}
	clipURL := s.videoURLFor(ctx, ref.ID, video, 0, 0, "full")
	style, err := s.synthesize(ctx, prov, ref.Name, feats, notes, clipURL)
	if err != nil && clipURL != "" { // 视频拉取/解析失败（模型侧取媒体失败等）：降级纯帧再来一次
		style, err = s.synthesize(ctx, prov, ref.Name, feats, notes, "")
	}
	if err != nil {
		fail(err)
		return
	}
	if err := s.mutateRef(ref.ID, func(r *Ref) error {
		r.Style = style
		r.Status = "done"
		r.Error = ""
		return nil
	}); err != nil {
		fail(err)
	}
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

// FeatureFrame 关键帧（Path 是 ai/extract_video_features.py 写入的绝对路径——
// 那边存 str(png.resolve())，别当相对路径拼接）。
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
