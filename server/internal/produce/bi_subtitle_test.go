package produce

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/events"
	"webpage-video-agent/server/internal/pipeline"
)

// TestTranslateSubtitlesGate 双语字幕翻译步的开关/幂等路径（不调真 LLM）。
func TestTranslateSubtitlesGate(t *testing.T) {
	dir := t.TempDir()
	p := &pipeline.Project{ID: "t", Dir: dir}
	r := &Runner{Hub: events.NewHub()}
	write := func(rel string, v any) {
		b, _ := json.Marshal(v)
		if err := os.WriteFile(filepath.Join(dir, rel), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	meta := func(bi bool, withEN bool) map[string]any {
		v := map[string]any{"id": "seg01", "duration_s": 2.0,
			"words":   []map[string]any{{"text": "群", "start": 0.2}, {"text": "论", "start": 0.6}},
			"phrases": [][]int{{0, 1}}}
		if withEN {
			v["phrases_en"] = []string{"group theory"}
		}
		return map[string]any{"voices": []any{v}, "bi_subtitle": bi}
	}

	// 1. 开关关闭 → nil（正常路径）
	write("project.json", map[string]any{"name": "x"})
	write("audio_meta.json", meta(false, false))
	if err := r.translateSubtitles(context.Background(), p); err != nil {
		t.Fatalf("开关关闭应返回 nil: %v", err)
	}

	// 2. 已翻译 → nil（幂等跳过）
	write("project.json", map[string]any{"bi_subtitle": true})
	write("audio_meta.json", meta(true, true))
	if err := r.translateSubtitles(context.Background(), p); err != nil {
		t.Fatalf("已翻译应幂等返回 nil: %v", err)
	}

	// 3. 开启 + 未翻译 + 无 LLM key → 错误（上层 warn 不挡片）
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	write("audio_meta.json", meta(true, false))
	err := r.translateSubtitles(context.Background(), p)
	if err == nil || !strings.Contains(err.Error(), "LLM_API_KEY") {
		t.Fatalf("无 key 应报 LLM 配置错误，得到: %v", err)
	}
}
