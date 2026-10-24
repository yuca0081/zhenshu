package prep

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"webpage-video-agent/server/internal/pipeline"
)

// newFixtureProject 建一个齐装项目：三工件 + 设计/样张衍生品 + 制作期产物
// （journal/场景文件/帧）。返回项目与其全部已落盘文件的相对路径集合。
func newFixtureProject(t *testing.T) (*pipeline.Project, map[string]bool) {
	t.Helper()
	dir := t.TempDir()
	p := pipeline.NewProject(dir, "p1")
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := []string{
		scriptPath(p), stylePackPath(p), draftPath(p),
		p.Artifact("storyboards/storyboard.json"),
		p.Artifact("style/style_samples.json"), p.Artifact("style/skin.json"),
		p.Artifact("style/design.md"), p.Artifact("style/prep-draft.json"),
		p.Artifact("style/style_brief.json"), p.Artifact("llm/style_samples.json"),
		p.Artifact("style/sample-0.png"), p.Artifact("style/sample-1.png"),
		p.Artifact("style/sample-2.png"), filepath.Join(p.Artifact("style/clips"), "s.mp4"),
		p.Artifact(".hf-clip/x"), p.Artifact(".hf-sample/y"),
		p.Artifact("reviews/journal.jsonl"), p.Artifact("llm/comp-seg01.json"),
		p.Artifact("compositions/frames/seg01.html"), p.Artifact("audio/seg01.wav"),
		p.Artifact("renders/segs/seg01.mp4"), p.Artifact("renders/main.mp4"),
		p.Artifact("input/article.txt"),
	}
	for _, f := range files {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	set := map[string]bool{}
	for _, f := range files {
		set[f] = true
	}
	return p, set
}

func exists(f string) bool { _, err := os.Stat(f); return err == nil }

// TestInvalidateFromCascadeChecklist 反馈路由作废清单核对：三档各删什么、
// 留什么。同时固化两个「留」的事实——journal 与场景文件不在作废清单里，
// 这是 2026-10-23 全阶段测试 B4 的台账：改稿重做后二者都还在盘上，段级
// 流水线的恢复判定只认 journal 状态不认内容，隐患见 produce/staleness_test.go。
func TestInvalidateFromCascadeChecklist(t *testing.T) {
	t.Run("script", func(t *testing.T) {
		p, _ := newFixtureProject(t)
		InvalidateFrom(p, StationScript)
		for _, f := range []string{
			scriptPath(p), stylePackPath(p), draftPath(p),
			p.Artifact("storyboards/storyboard.json"),
			p.Artifact("style/style_samples.json"), p.Artifact("style/skin.json"),
			p.Artifact("style/design.md"), p.Artifact("style/prep-draft.json"),
			p.Artifact("style/style_brief.json"), p.Artifact("llm/style_samples.json"),
			p.Artifact("style/sample-0.png"), p.Artifact("style/sample-2.png"),
			p.Artifact("style/clips"), p.Artifact(".hf-clip"), p.Artifact(".hf-sample"),
		} {
			if exists(f) {
				t.Errorf("script 级联应作废 %s", rel(p, f))
			}
		}
		// 固化「留」：制作期产物不在 prep 作废半边（TTS/渲染指纹各自判旧，
		// 但 journal 与场景文件没有内容指纹闸——隐患台账）
		for _, f := range []string{
			p.Artifact("reviews/journal.jsonl"), p.Artifact("llm/comp-seg01.json"),
			p.Artifact("compositions/frames/seg01.html"), p.Artifact("audio/seg01.wav"),
			p.Artifact("renders/segs/seg01.mp4"), p.Artifact("renders/main.mp4"),
		} {
			if !exists(f) {
				t.Errorf("script 级联不应碰制作期产物 %s", rel(p, f))
			}
		}
	})
	t.Run("style", func(t *testing.T) {
		p, _ := newFixtureProject(t)
		InvalidateFrom(p, StationStyle)
		if !exists(scriptPath(p)) {
			t.Error("style 档不应动①文字稿")
		}
		if exists(stylePackPath(p)) || exists(draftPath(p)) {
			t.Error("style 档应作废②③")
		}
		if exists(p.Artifact("style/style_samples.json")) {
			t.Error("style 档应作废设计/样张衍生品")
		}
		if !exists(p.Artifact("reviews/journal.jsonl")) {
			t.Error("style 档不应碰 journal（台账事实）")
		}
	})
	t.Run("storyboard", func(t *testing.T) {
		p, _ := newFixtureProject(t)
		InvalidateFrom(p, StationStoryboard)
		if !exists(scriptPath(p)) || !exists(stylePackPath(p)) {
			t.Error("storyboard 档不应动①②")
		}
		if exists(draftPath(p)) || exists(p.Artifact("storyboards/storyboard.json")) {
			t.Error("storyboard 档应作废③与 legacy 分镜")
		}
		// ③失效不碰设计（设计已随打包终审锁定）
		if !exists(p.Artifact("style/style_samples.json")) {
			t.Error("storyboard 档不应动设计衍生品")
		}
	})
}

func rel(p *pipeline.Project, f string) string {
	if r, err := filepath.Rel(p.Dir, f); err == nil {
		return r
	}
	return f
}

// TestReplaceManuscriptCascade 改稿统一语义：真源更新 + 重放产物删除 +
// ①级联作废；无①工件的老项目只换真源不误报级联。
func TestReplaceManuscriptCascade(t *testing.T) {
	t.Run("prep project", func(t *testing.T) {
		p, _ := newFixtureProject(t)
		inv, err := ReplaceManuscript(p, "新稿正文")
		if err != nil || !inv {
			t.Fatalf("应触发级联: inv=%v err=%v", inv, err)
		}
		b, err := os.ReadFile(p.Artifact("input/article.txt"))
		if err != nil || string(b) != "新稿正文" {
			t.Fatalf("真源未更新: %s %v", b, err)
		}
		if exists(p.Artifact("manuscripts/manuscript.json")) {
			t.Error("重放产物 manuscript.json 未删（TTS 会拿旧稿）")
		}
		if exists(scriptPath(p)) || exists(stylePackPath(p)) || exists(draftPath(p)) {
			t.Error("三工件未级联作废")
		}
	})
	t.Run("no prep artifacts", func(t *testing.T) {
		p, _ := newFixtureProject(t)
		for _, f := range []string{scriptPath(p), stylePackPath(p), draftPath(p)} {
			if err := os.Remove(f); err != nil {
				t.Fatal(err)
			}
		}
		inv, err := ReplaceManuscript(p, "新稿")
		if err != nil || inv {
			t.Fatalf("无①工件不应报级联: inv=%v err=%v", inv, err)
		}
	})
}

// TestReplaySkipsAllStationsWhenArtifactsPresent 重放语义：三工件齐时
// RunStrict 一个工位都不跑（无 LLM 调用）直接就绪——缓存重放是省 token 的根。
func TestReplaySkipsAllStationsWhenArtifactsPresent(t *testing.T) {
	// 重放入口在 LLM 闸之后：先验 key 再逐站重放。塞假 key 只为过闸，
	// 三工件齐时不会发起任何真实调用。
	t.Setenv("LLM_API_KEY", "test-key-for-replay")
	t.Setenv("LLM_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("LLM_MODEL", "test-model")
	p, _ := newFixtureProject(t)
	// 全工件齐 → 逐站 HasArtifact 命中 → 不发起任何 LLM 会话。
	// Available() 环境无关地放行（即使未配 key 也不该走到 LLM）。
	if err := RunStrict(context.Background(), p, Deps{}); err != nil {
		t.Fatalf("三工件齐的重放不应报错（更不应发起 LLM 调用）: %v", err)
	}
	for _, st := range []string{StationScript, StationStyle, StationStoryboard} {
		if !HasArtifact(p, st) {
			t.Errorf("%s 工件不应被重放删除", st)
		}
	}
}
