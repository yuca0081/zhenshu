package stylelib

import (
	"os"
	"path/filepath"
	"testing"

	"webpage-video-agent/server/internal/contract"
)

// ── 词表（§4.2）────────────────────────────────────────────

func TestVocabCreateAndFind(t *testing.T) {
	l := New(t.TempDir())
	id, err := l.VocabCreate(VocabEntry{
		ID: "Claymation!", Name: "粘土定格 Vaporwave", Aliases: []string{"粘土动画", "clay"},
		Brief: "逐帧手作质感，指印与哑光陶土色", Tells: "边缘不规则+指纹压痕",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "claymation" { // id 净化：非法字符转 _，小写
		t.Fatalf("id 应净化为 claymation，得到 %q", id)
	}
	v, err := l.LoadVocab()
	if err != nil {
		t.Fatal(err)
	}
	// 归一化精确查重：name/别名 命中
	for _, w := range []string{"粘土定格 Vaporwave", " 粘土动画 ", "CLAY", "粘土定格vaporwave"} {
		if e, ok := v.Find(w); !ok || e.ID != id {
			t.Fatalf("查重 %q 应命中 %s：%v", w, id, e)
		}
	}
	if _, ok := v.Find("包豪斯"); ok {
		t.Fatal("不该命中")
	}
	// 重复申请同名词 → 幂等复用
	id2, err := l.VocabCreate(VocabEntry{Name: "粘土定格 Vaporwave", Brief: "x"})
	if err != nil || id2 != id {
		t.Fatalf("同名词应幂等复用 %s，得到 %s(%v)", id, id2, err)
	}
}

func TestVocabAttachAlias(t *testing.T) {
	l := New(t.TempDir())
	id, _ := l.VocabCreate(VocabEntry{ID: "bauhaus", Name: "包豪斯", Brief: "x"})
	if err := l.VocabAttachAlias(id, "Bauhaus"); err != nil {
		t.Fatal(err)
	}
	v, _ := l.LoadVocab()
	if _, ok := v.Find("BAUHAUS"); !ok {
		t.Fatal("别名归一化后应命中")
	}
	// 挂到已有别名 → 幂等不重复
	if err := l.VocabAttachAlias(id, "bauhaus"); err != nil {
		t.Fatal(err)
	}
	v, _ = l.LoadVocab()
	n := 0
	for _, a := range v.Entries[id].Aliases {
		if a == "Bauhaus" || a == "bauhaus" {
			n++
		}
	}
	if n > 1 {
		t.Fatalf("别名重复: %v", v.Entries[id].Aliases)
	}
}

// ── 条目 schema（§4.1）─────────────────────────────────────

func mustVocab(t *testing.T, l *Library, name, brief string) string {
	t.Helper()
	id, err := l.VocabCreate(VocabEntry{Name: name, Brief: brief})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestIngestVideoRefV3(t *testing.T) {
	l := New(t.TempDir())
	main := mustVocab(t, l, "粘土定格", "逐帧手作质感")
	aux := mustVocab(t, l, "蒸汽波", "80s 霓虹渐变")
	st, err := l.IngestVideoRef("Ref.2024", VideoStyleIn{
		Name:    "粘土微缩剧场",
		Subject: "粘土动画拍的面包店日常",
		Tags:    contract.StyleTags{Main: main, Aux: []string{aux}},
		TagEvidence: []contract.TagEvidence{
			{Tag: main, Note: "指印+哑光陶土色", Frames: []string{"frame-0.8.png"}},
		},
		Signatures: []contract.Signature{
			{Kind: contract.SigKindTransition, Name: "面团递接转场", Mechanism: "上一画面主体被推出画外，下一画面同位置跟进", Frames: []string{"frame-2.5.png"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.ID != "vid-ref-2024" {
		t.Fatalf("seed id 净化: %s", st.ID)
	}
	if !st.Published || st.OriginProject != "video:Ref.2024" {
		t.Fatalf("溯源错误: %+v", st)
	}
	if st.Signatures[0].ID != "sig1" {
		t.Fatalf("特色应统一编号 sig1: %+v", st.Signatures[0])
	}
	// 词表外的标签 → 拒收
	if _, err := l.IngestVideoRef("r2", VideoStyleIn{Name: "x", Tags: contract.StyleTags{Main: "不存在"}}); err == nil {
		t.Fatal("词表外标签应拒收")
	}
	// 超 6 条特色 → 拒收
	var sigs []contract.Signature
	for i := 0; i < 7; i++ {
		sigs = append(sigs, contract.Signature{Kind: "motif", Name: "m", Mechanism: "m"})
	}
	if _, err := l.IngestVideoRef("r3", VideoStyleIn{Name: "x", Tags: contract.StyleTags{Main: main}, Signatures: sigs}); err == nil {
		t.Fatal("特色超 6 条应拒收")
	}
	// kind 越界 → 拒收
	if _, err := l.IngestVideoRef("r4", VideoStyleIn{Name: "x", Tags: contract.StyleTags{Main: main},
		Signatures: []contract.Signature{{Kind: "vibe", Name: "n", Mechanism: "m"}}}); err == nil {
		t.Fatal("kind 越界应拒收")
	}
}

// 加载即校验：schema 不合格的条目跳过；证据帧文件缺失只剪枝不拒条目。
func TestListPrunesMissingFrames(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	main := mustVocab(t, l, "包豪斯", "x")
	// 手工造一条带失效帧引用的库
	b := `[{"id":"vid-x","name":"X","tags":{"main":"` + main + `"},
	  "tag_evidence":[{"tag":"` + main + `","note":"n","frames":["frame-gone.png","cover.png"]}],
	  "signatures":[{"id":"sig1","kind":"palette","name":"p","mechanism":"m","frames":["frame-gone.png"]}],
	  "published":true,"created_at":"2026-10-08T00:00:00Z"},
	 {"id":"vid-bad","name":"","tags":{"main":"nope"},"published":true,"created_at":"2026-10-08T00:00:00Z"}]`
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.path(), []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(l.MediaDir("vid-x"), 0o755)
	os.WriteFile(filepath.Join(l.MediaDir("vid-x"), "cover.png"), []byte("x"), 0o644)

	got, err := l.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "vid-x" {
		t.Fatalf("坏条目应被跳过、好条目保留: %+v", got)
	}
	if len(got[0].TagEvidence[0].Frames) != 1 || got[0].TagEvidence[0].Frames[0] != "cover.png" {
		t.Fatalf("失效帧应剪枝: %+v", got[0].TagEvidence)
	}
	if len(got[0].Signatures[0].Frames) != 0 {
		t.Fatalf("特色失效帧应剪枝: %+v", got[0].Signatures)
	}
}

// ── 简报（§4.3）────────────────────────────────────────────

func TestBuildBrief(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	main := mustVocab(t, l, "粘土定格", "逐帧手作质感")
	st, err := l.IngestVideoRef("r1", VideoStyleIn{
		Name: "粘土剧场", Subject: "面包店", Tags: contract.StyleTags{Main: main},
		TagEvidence: []contract.TagEvidence{{Tag: main, Note: "n", Frames: []string{"frame-1.png"}}},
		Signatures: []contract.Signature{
			{Kind: "palette", Name: "陶土色", Mechanism: "m1", Frames: []string{"frame-2.png"}},
			{Kind: "transition", Name: "递接", Mechanism: "m2", Frames: []string{"frame-3.png"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 媒体快照（证据帧落 style_media）
	mk := func(id string) {
		os.MkdirAll(l.MediaDir(st.ID), 0o755)
		os.WriteFile(filepath.Join(l.MediaDir(st.ID), id), []byte("x"), 0o644)
	}
	mk("frame-1.png")
	mk("frame-2.png")
	mk("frame-3.png")

	// 全开
	b, err := l.BuildBrief(st.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.EntryID != st.ID || b.EntryName != "粘土剧场" || b.Tags.Main != main {
		t.Fatalf("简报身份错: %+v", b)
	}
	if len(b.TagBriefs) != 1 || b.TagBriefs[0].Name != "粘土定格" {
		t.Fatalf("TagBriefs 应带词条人话: %+v", b.TagBriefs)
	}
	if len(b.Signatures) != 2 {
		t.Fatalf("默认全开: %+v", b.Signatures)
	}
	if len(b.Evidence) != 3 {
		t.Fatalf("证据 = 标签证据帧优先 + 特色帧，去重 ≤6: %v", b.Evidence)
	}
	for _, e := range b.Evidence {
		if !filepath.IsAbs(e) {
			t.Fatalf("证据须绝对路径: %s", e)
		}
	}
	// 关掉 sig1（palette）
	b2, err := l.BuildBrief(st.ID, map[string]bool{"sig1": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(b2.Signatures) != 1 || b2.Signatures[0].ID != "sig2" {
		t.Fatalf("关掉的特色不进简报: %+v", b2.Signatures)
	}
	if len(b2.Evidence) != 2 { // frame-1 + frame-3（frame-2 属 sig1）
		t.Fatalf("证据应只留开启特色的帧: %v", b2.Evidence)
	}
	// 条目不存在
	if _, err := l.BuildBrief("nope", nil); err == nil {
		t.Fatal("条目不存在应报错")
	}
}

// 落点覆盖校验（§4.5 第 4 步的机械数数）。
func TestCheckAbsorption(t *testing.T) {
	b := &contract.StyleBrief{Signatures: []contract.Signature{
		{ID: "sig1", Kind: "transition", Name: "a", Mechanism: "m"},
		{ID: "sig2", Kind: "palette", Name: "b", Mechanism: "m"},
	}}
	full := []contract.SigAbsorption{
		{SigID: "sig1", LandsIn: contract.LandsTransitionPolicy, How: "x"},
		{SigID: "sig2", LandsIn: contract.LandsSkins, How: "y"},
	}
	if err := b.CheckAbsorption(full); err != nil {
		t.Fatalf("全覆盖应过: %v", err)
	}
	if err := b.CheckAbsorption([]contract.SigAbsorption{
		{SigID: "sig1", LandsIn: contract.LandsMotion, How: "x"},
	}); err == nil {
		t.Fatal("漏登 sig2 应打回")
	}
	if err := b.CheckAbsorption([]contract.SigAbsorption{
		{SigID: "sig1", LandsIn: "vibes", How: "x"},
		{SigID: "sig2", LandsIn: contract.LandsSkins, How: "y"},
	}); err == nil {
		t.Fatal("lands_in 越枚举应打回")
	}
	if err := b.CheckAbsorption([]contract.SigAbsorption{
		{SigID: "sig1", LandsIn: contract.LandsMotion, How: "x"},
		{SigID: "sig2", LandsIn: contract.LandsSkins, How: "y"},
		{SigID: "sig9", LandsIn: contract.LandsMotion, How: "z"},
	}); err == nil {
		t.Fatal("自造特色 sig9 应打回")
	}
	// 无简报/无特色 = 恒过
	var nilBrief *contract.StyleBrief
	if err := nilBrief.CheckAbsorption(nil); err != nil {
		t.Fatal(err)
	}
	if err := (&contract.StyleBrief{}).CheckAbsorption(nil); err != nil {
		t.Fatal(err)
	}
}
