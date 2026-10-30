package produce

// ir_smoke_manual_test.go —— SceneIR 真机冒烟（手动，IR_SMOKE=1 才跑）。
// 目的：在整条制作链开跑前，验证当前模型能按提示词产出可校验、可编译、可过
// scene_lint 的 IR。花费一次小调用（~5k token），失败时直接给出模型原文。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
)

func TestSceneIRSmoke(t *testing.T) {
	if os.Getenv("IR_SMOKE") != "1" {
		t.Skip("IR_SMOKE=1 才跑（真实 LLM 调用）")
	}
	root, _ := filepath.Abs("../../..")
	_ = godotenv.Load(filepath.Join(root, ".env"))
	// 手动冒烟不落盘缓存：go test 的 CWD 是包目录，默认 cache 会在
	// server/internal/produce/data 下生成垃圾（2026-10-30 实测）。
	_ = os.Setenv("LLM_DISK_CACHE", "off")
	prov, err := llm.FromEnv(llm.StationSceneGen)
	if err != nil {
		t.Fatalf("LLM 未配置: %v", err)
	}
	seg := contract.Segment{
		ID: "seg01", Idx: 1, Key: "三角角色登场",
		Narration:    "一个三角形从纸面上升起来，它第一次看见这个世界。",
		VisualBrief:  "米色纸底，蓝色三角从下方升起，只用一个圆点和一条短横线做表情；大量留白。",
		DurationHint: 6,
	}
	v := voiceMeta{DurationS: 6, Words: []wordTiming{
		{Text: "一个", Start: 0.2, End: 0.6}, {Text: "三角形", Start: 0.6, End: 1.3},
		{Text: "从纸面上", Start: 1.3, End: 2.2}, {Text: "升起来", Start: 2.2, End: 2.9},
	}}
	cv := contract.CanvasFor("16:9")
	prompt := sceneIRUserPrompt(seg, v, "", "", cv, "（测试：无设计系统，保持克制的中性版式）", "", "")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	var out struct {
		Note string            `json:"note"`
		IR   *contract.SceneIR `json:"ir"`
	}
	u, err := prov.GenerateJSONNoThink(ctx, sceneIRSystem(), prompt, &out)
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	t.Logf("usage: prompt=%d completion=%d calls=1", u.PromptTokens, u.CompletionTokens)
	if out.IR == nil {
		b, _ := json.Marshal(out)
		t.Fatalf("模型没给 ir 字段；原文前 1200 字：%s", string(b)[:min(1200, len(b))])
	}
	if verrs := contract.ValidateSceneIR(out.IR, seg.DurationHint, cv.W, cv.H); len(verrs) > 0 {
		t.Logf("首版 IR 校验未过（%d 项），走改动单修复：%s", len(verrs), strings.Join(verrs, "；"))
		next, u2, e := repairSceneIR(ctx, prov, out.IR, verrs, seg.DurationHint, nil)
		t.Logf("repair usage: prompt=%d completion=%d", u2.PromptTokens, u2.CompletionTokens)
		if e != nil {
			b, _ := json.MarshalIndent(out.IR, "", "  ")
			t.Fatalf("IR 修复失败: %v\n--- IR ---\n%s", e, b)
		}
		out.IR = next
		if verrs2 := contract.ValidateSceneIR(out.IR, seg.DurationHint, cv.W, cv.H); len(verrs2) > 0 {
			b, _ := json.MarshalIndent(out.IR, "", "  ")
			t.Fatalf("修复后 IR 仍不过：\n- %s\n--- IR ---\n%s", strings.Join(verrs2, "\n- "), b)
		}
	}
	html, err := CompileSceneIR(out.IR, seg.ID, cv, seg.DurationHint)
	if err != nil {
		t.Fatalf("编译失败: %v", err)
	}
	tmpDir := filepath.Join(root, "tmp")
	_ = os.MkdirAll(tmpDir, 0o755)
	_ = os.WriteFile(filepath.Join(tmpDir, "ir-smoke.html"), []byte(html), 0o644)
	if b, merr := json.MarshalIndent(out.IR, "", "  "); merr == nil {
		_ = os.WriteFile(filepath.Join(tmpDir, "ir-smoke.json"), b, 0o644)
	}
	r := &Runner{RootDir: root}
	if errs := r.sceneLintRun(ctx, html); len(errs) > 0 {
		t.Fatalf("编译产物没过 scene_lint：\n- %s\n（现场文件：%s）", strings.Join(errs, "\n- "), filepath.Join(tmpDir, "ir-smoke.html"))
	}
	t.Logf("IR 冒烟通过：elements=%d cues=%d html=%d bytes", len(out.IR.Elements), len(out.IR.Cues), len(html))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
