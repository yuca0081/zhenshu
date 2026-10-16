package prep

// run.go —— 准备阶段编排器（§5.1）：三个独立会话链式传工件，Go 拥有循环。
//
//	Run        管线入口：缺哪站跑哪站（重放语义）；LLM 未配置 = 降级返回 nil，
//	           legacy 单发路径照常接管（无 key 老路径零行为变化）。
//	RunStrict  反馈重跑入口（agent rerun_prep 工具 / api）：失败上抛。
//
// 预算耗尽/模型失败 → 至多一次冷重试（全新会话，工具状态归零）；重试仍败才报失败。
// 反馈路由（终审后）：改文字→重①（②③级联）/ 改风格→重②③ / 改结构场景→重③，
// 由 InvalidateFrom + Run 组合实现——工位内部没有「重跑」概念，失效即重放。

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
)

// Available LLM 是否可用（不可用时管线入口降级 legacy，反馈重跑入口报错）。
func Available() bool {
	_, err := llm.FromEnv(llm.RolePlan)
	return err == nil
}

// LegacyProject 老项目判定：没有文字稿工件、但 legacy 分镜已在——prep 不碰它
// （旧项目升级靠显式反馈重跑，不靠管线偷偷重写）。
func LegacyProject(p *pipeline.Project) bool {
	if HasArtifact(p, StationScript) {
		return false
	}
	_, err := p.LoadStoryboard()
	return err == nil
}

// Run 管线入口（stagePrep 调用）。
func Run(ctx context.Context, p *pipeline.Project, d Deps) error {
	return run(ctx, p, d, false)
}

// RunStrict 严格入口（反馈重跑）：LLM 不可用/失败都上抛。
func RunStrict(ctx context.Context, p *pipeline.Project, d Deps) error {
	return run(ctx, p, d, true)
}

func run(ctx context.Context, p *pipeline.Project, d Deps, strict bool) error {
	if !Available() {
		if strict {
			return errors.New("LLM 未配置（LLM_API_KEY），准备工位无法重跑")
		}
		p.Manifest("prep.skipped", "LLM 未配置，准备阶段降级 legacy 单发路径")
		return nil
	}
	type station struct {
		name  string
		run   func(context.Context) error
		after func()
	}
	stations := []station{
		{StationScript, func(c context.Context) error { return runScriptStation(c, p, d) }, nil},
		{StationStyle, func(c context.Context) error { return runStyleStation(c, p, d) }, nil},
		{StationStoryboard, func(c context.Context) error { return runStoryboardStation(c, p, d) }, nil},
	}
	d.emit(p.ID, "stage", "prep", "running")
	for _, st := range stations {
		if HasArtifact(p, st.name) {
			p.Manifest("prep.skip_cached", st.name)
			continue
		}
		if err := runWithColdRetry(ctx, p, st.name, st.run); err != nil {
			p.Manifest("prep.error", st.name+": "+pipeline.FirstLine(err.Error(), 160))
			d.emit(p.ID, "stage", "prep", "error: "+pipeline.FirstLine(err.Error(), 120))
			return err
		}
	}
	p.Manifest("prep.done", "三工件就绪，等待打包终审")
	d.emit(p.ID, "stage", "prep", "done")
	return nil
}

// runWithColdRetry 预算耗尽/模型失败 → 记账 + 至多一次冷重试（§5.2）。
// 冷重试是全新会话：Loop 与工具闭包都在 station.run 内新建，重调即归零。
func runWithColdRetry(ctx context.Context, p *pipeline.Project, name string, f func(context.Context) error) error {
	err := f(ctx)
	if err == nil || ctx.Err() != nil {
		return err
	}
	p.Manifest("prep.cold_retry", name+": "+pipeline.FirstLine(err.Error(), 160))
	return f(ctx)
}

// Summary 三工件就绪态一行账（agent/终审 UI 用）。
func Summary(p *pipeline.Project) string {
	s := ""
	if HasArtifact(p, StationScript) {
		if sc, err := LoadScript(p); err == nil {
			s += fmt.Sprintf("文字稿 %d 句/%d 字；", len(sc.Sentences), sc.WordCount)
		}
	} else {
		s += "文字稿缺；"
	}
	if HasArtifact(p, StationStyle) {
		if sp, err := LoadStylePack(p); err == nil && sp.DesignSystem != nil {
			s += fmt.Sprintf("风格包「%s」；", sp.DesignSystem.Direction)
		}
	} else {
		s += "风格包缺；"
	}
	if HasArtifact(p, StationStoryboard) {
		if dr, err := LoadDraft(p); err == nil {
			s += fmt.Sprintf("分镜稿 %d 段；", len(dr.Segments))
		}
	} else {
		s += "分镜稿缺；"
	}
	return strings.TrimSuffix(s, "；")
}
