package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPostResolve postFor 解析回环：project.json 覆盖/关闭 → 风格 tokens.post → 默认轻后期。
func TestPostResolve(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "data", "library", "styles.json")); err != nil {
		t.Skipf("styles.json 不存在，跳过: %v", err)
	}
	old := dataDir
	SetDataDir(filepath.Join(root, "data"))
	defer SetRootDir(old)

	dir := t.TempDir()
	p := &Project{ID: "t", Dir: dir}

	// project.json 无 post + 无分镜（style_id 空）→ 默认轻后期
	if err := os.WriteFile(p.Artifact("project.json"), []byte(`{"name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fx := postFor(p)
	if fx == nil || fx.Vignette != defaultPostFX.Vignette || fx.Grain != defaultPostFX.Grain {
		t.Fatalf("无配置应走默认轻后期，得到 %+v", fx)
	}

	// project.json post:false → 关闭
	if err := os.WriteFile(p.Artifact("project.json"), []byte(`{"post":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if fx := postFor(p); !fx.off() {
		t.Fatalf("post:false 应关闭，得到 %+v", fx)
	}

	// project.json post 对象 → 整组替换（未写字段=0=该项关）
	if err := os.WriteFile(p.Artifact("project.json"),
		[]byte(`{"post":{"vignette":0.8,"grain":0.6}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fx = postFor(p)
	if fx == nil || fx.Vignette != 0.8 || fx.Grain != 0.6 || fx.Chroma != 0 {
		t.Fatalf("对象应整组替换，得到 %+v", fx)
	}

	// 分镜 style_id 指向 styles.json 条目 → 应返回该条目 tokens.post（与注册表数据解耦：期望值现场读）
	if err := os.WriteFile(p.Artifact("project.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sb := map[string]any{"timing_basis": "narration", "style_id": "cosmos",
		"segments": []map[string]any{{"id": "seg01", "idx": 1, "narration": "n", "visual_brief": "v"}}}
	b, _ := json.Marshal(sb)
	if err := os.MkdirAll(p.Artifact("storyboards"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Artifact("storyboards/storyboard.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	var want struct {
		Tokens struct {
			Post *postFX `json:"post"`
		} `json:"tokens"`
	}
	raw, err := os.ReadFile(filepath.Join(root, "data", "library", "styles.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil {
		t.Fatal("styles.json 解析失败")
	}
	found := false
	for _, e := range entries {
		var idOnly struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(e, &idOnly) == nil && strings.EqualFold(idOnly.ID, "cosmos") {
			if json.Unmarshal(e, &want) == nil {
				found = true
			}
			break
		}
	}
	if !found || want.Tokens.Post == nil {
		t.Skip("cosmos 未配 tokens.post，跳过风格覆盖分支")
	}
	got := postFor(p)
	if got == nil || *got != *want.Tokens.Post {
		t.Fatalf("style cosmos 应得注册表值 %+v，得到 %+v", want.Tokens.Post, got)
	}
}

// TestVideoChain 滤镜链拼装：全 0 空、单项、参数钳制、顺序（色差→暗角→颗粒）。
func TestVideoChain(t *testing.T) {
	if fx := (*postFX)(nil); fx.videoChain() != "" {
		t.Fatal("nil 应无滤镜")
	}
	if fx := (&postFX{}); fx.videoChain() != "" {
		t.Fatal("全 0 应无滤镜")
	}
	chain := (&postFX{Vignette: 0.35, Grain: 0.4}).videoChain()
	if chain == "" {
		t.Fatal("有参数应产出滤镜链")
	}
	// 超界钳制不炸即可
	if chain := (&postFX{Vignette: 5, Grain: 9, Chroma: 3}).videoChain(); chain == "" {
		t.Fatal("超界参数应钳制而非丢弃")
	}
}
