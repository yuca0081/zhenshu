package styleref

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/stylelib"
)

// stubDedup 注入假语义查重（不打网络）：按预设表回 word → entry_id。
func stubDedup(t *testing.T, hits map[string]string) {
	t.Helper()
	orig := semanticDedup
	semanticDedup = func(_ context.Context, _ *llm.Provider, _ *stylelib.Library, words []string) (map[string]string, error) {
		out := map[string]string{}
		for _, w := range words {
			if id, ok := hits[w]; ok {
				out[w] = id
			}
		}
		return out, nil
	}
	t.Cleanup(func() { semanticDedup = orig })
}

func mkFeats(t *testing.T, names ...string) videoFeatures {
	t.Helper()
	dir := t.TempDir()
	var fr []featureFrame
	for i, n := range names {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		fr = append(fr, featureFrame{Path: p, T: float64(i)})
	}
	return videoFeatures{Frames: fr}
}

// resolveDraft 的词表落表全链（LLM 全 mock）：
// 词表 id 直用 / 归一化精确撞车复用 / 语义撞车挂别名 / 全新词建词条。
func TestResolveDraftVocabFlow(t *testing.T) {
	s := NewStore(t.TempDir(), t.TempDir())
	lib := stylelib.New(s.DataDir)
	iso, err := lib.VocabCreate(stylelib.VocabEntry{ID: "iso", Name: "等距 2.5D", Brief: "轴测视角"})
	if err != nil {
		t.Fatal(err)
	}
	// 预置一个语义近邻词条（供语义查重命中）
	glow, err := lib.VocabCreate(stylelib.VocabEntry{ID: "glow", Name: "霓虹辉光", Brief: "暗底发光"})
	if err != nil {
		t.Fatal(err)
	}
	stubDedup(t, map[string]string{"霓虹发光风": glow})

	feats := mkFeats(t, "frame-0.8.png", "frame-2.5.png")
	raw := &taggerOutput{
		Subject: "等距视角的城市宣传动画",
		Tags: []rawTag{
			{ID: iso, Note: "轴测网格"},                                               // 词表 id 直用
			{New: &newWord{ID: "clay", Name: "粘土定格 Vaporwave", Aliases: []string{"clay"}, Brief: "逐帧手作", Tells: "指印"}}, // 全新词
			{New: &newWord{Name: "霓虹发光风", Brief: "x"}},                                    // 语义撞车 → 挂别名
		},
		Signatures: []contract.Signature{
			{Kind: contract.SigKindTransition, Name: "递接转场", Mechanism: "上段主体推出画外+下段同位跟进", Frames: []string{"frame-0.8.png"}},
		},
	}
	// 归一化精确撞车：重复申请已有词条名 → 复用（单独一轮，避免挤占 1+2 名额）
	dup, err := s.resolveDraft(context.Background(), nil, lib, &taggerOutput{
		Tags: []rawTag{{New: &newWord{Name: "等距 2.5D", Brief: "重复申请"}}},
	}, feats)
	if err != nil {
		t.Fatal(err)
	}
	if dup.Tags.Main != iso {
		t.Fatalf("精确撞车应复用 iso: %+v", dup.Tags)
	}

	d, err := s.resolveDraft(context.Background(), nil, lib, raw, feats)
	if err != nil {
		t.Fatal(err)
	}
	if d.Tags.Main != iso {
		t.Fatalf("主标签应直用词表 id: %+v", d.Tags)
	}
	if len(d.Tags.Aux) != 2 {
		t.Fatalf("辅标签 2 个: %+v", d.Tags)
	}
	if d.Tags.Aux[0] != "clay" { // 全新词落表后引用
		t.Fatalf("全新词应落表并被引用: %+v", d.Tags)
	}
	if d.Tags.Aux[1] != glow { // 「霓虹发光风」语义撞车 → 挂别名复用
		t.Fatalf("语义撞车应复用 glow: %+v", d.Tags)
	}
	if d.Name != "等距 2.5D" { // raw.Name 空 → 缺省用主标签正式名
		t.Fatalf("草稿名缺省应为主标签正式名: %q", d.Name)
	}
	// 全新词已落表，别名挂上
	v, _ := lib.LoadVocab()
	if e := v.Entries["clay"]; e.ID == "" || e.Name != "粘土定格 Vaporwave" {
		t.Fatalf("新词未落表: %+v", v.Entries["clay"])
	}
	found := false
	for _, a := range v.Entries[glow].Aliases {
		if a == "霓虹发光风" {
			found = true
		}
	}
	if !found {
		t.Fatalf("语义撞车应挂别名: %+v", v.Entries[glow])
	}
	// 无 id 无新词 → 拒收
	raw2 := &taggerOutput{Tags: []rawTag{{Note: "x"}}}
	if _, err := s.resolveDraft(context.Background(), nil, lib, raw2, feats); err == nil {
		t.Fatal("既无词表 id 又无新词申请应拒收")
	}
}
