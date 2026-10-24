package stylelib

// styleopt_test.go —— 风格库优化轮（2026-10-24）回归锁：
// ① Search 标签走词条名+别名展开（Tags 存词条 id，中文查询对 id 子串永远 miss）；
// ② Derive 衍生继承基风格 TagEvidence（简报「标签证据优先」不再对衍生条目落空）；
// ③ IngestVideoRef 落分面 + 陈列名消歧（撞名带题材词、upsert 覆盖不给自己加后缀）；
// ④ RefillFacets 只填三轴全空（人工值不被机械回填覆盖）。
import (
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
)

func seedEntry(t *testing.T, lib *Library) {
	t.Helper()
	if _, err := lib.VocabCreate(VocabEntry{
		ID: "claymation", Name: "粘土定格",
		Aliases: []string{"粘土动画", "Clay Animation"},
		Brief:   "逐帧手捏粘土质感，指印与哑光高光",
		Tells:   "帧里认指印纹理与粘土断口",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSearchMatchesVocabNameAndAlias(t *testing.T) {
	lib := New(t.TempDir())
	seedEntry(t, lib)
	if _, err := lib.IngestVideoRef("ref-a", VideoStyleIn{
		Name:    "某片",
		Subject: "测试题材",
		Tags:    contract.StyleTags{Main: "claymation"},
		Signatures: []contract.Signature{{
			ID: "sig1", Kind: contract.SigKindMotion, Name: "捏塑入场",
			Mechanism: "元素从粘土块捏出成型", Frames: []string{"frame-01.png"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"粘土定格", "粘土动画", "clay"} { // 正式名/别名/英文别名
		got, _, err := lib.Search(q, StyleFacets{}, 1, 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("查询 %q 应经词条展开命中 1 条，实际 %d 条", q, len(got))
		}
	}
	// 无关词仍不命中（展开不是放宽）
	if got, _, _ := lib.Search("赛博朋克", StyleFacets{}, 1, 20); len(got) != 0 {
		t.Fatalf("无关词不应命中，实际 %d 条", len(got))
	}
}

func TestDeriveInheritsTagEvidence(t *testing.T) {
	lib := New(t.TempDir())
	seedEntry(t, lib)
	base, err := lib.IngestVideoRef("ref-a", VideoStyleIn{
		Name:    "基风格",
		Subject: "测试题材",
		Tags:    contract.StyleTags{Main: "claymation"},
		TagEvidence: []contract.TagEvidence{{
			Tag: "claymation", Note: "第1段：指印纹理特写", Frames: []string{"frame-01.png"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := lib.Derive(DeriveInput{BaseID: base.ID, Name: "衍生款", Delta: "更哑光"})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.TagEvidence) != len(base.TagEvidence) {
		t.Fatalf("衍生条目应继承标签证据：基 %d 条 / 衍生 %d 条", len(base.TagEvidence), len(d.TagEvidence))
	}
	if d.TagEvidence[0].Tag != "claymation" || len(d.TagEvidence[0].Frames) != 1 {
		t.Fatalf("继承的证据内容漂移：%+v", d.TagEvidence[0])
	}
}

func TestIngestFacetsAndUniqueName(t *testing.T) {
	lib := New(t.TempDir())
	seedEntry(t, lib)
	in := VideoStyleIn{
		Name:    "冷晶仪象",
		Subject: "冰面科普参考片",
		Facets:  StyleFacets{Mood: "沉稳", Tone: "冷蓝", Density: "中密"},
		Tags:    contract.StyleTags{Main: "claymation"},
	}
	a, err := lib.IngestVideoRef("ref-a", in)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := lib.Get(a.ID); got.Facets != in.Facets {
		t.Fatalf("分面未落库：%+v", got.Facets)
	}
	// 同名第二条（不同视频）：带题材词消歧
	b, err := lib.IngestVideoRef("ref-b", in)
	if err != nil {
		t.Fatal(err)
	}
	if b.Name == a.Name || !strings.HasPrefix(b.Name, a.Name+"(") {
		t.Fatalf("撞名未消歧：a=%q b=%q", a.Name, b.Name)
	}
	// 同 seed 重复入库（upsert 覆盖）：旧条目是自身，不给自己加后缀
	c, err := lib.IngestVideoRef("ref-a", in)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != a.Name {
		t.Fatalf("upsert 覆盖被当撞名：a=%q c=%q", a.Name, c.Name)
	}
}

func TestRefillFacetsOnlyFillsEmpty(t *testing.T) {
	lib := New(t.TempDir())
	seedEntry(t, lib)
	a, err := lib.IngestVideoRef("ref-a", VideoStyleIn{
		Name: "甲", Subject: "测试", Tags: contract.StyleTags{Main: "claymation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := lib.RefillFacets(a.ID, StyleFacets{Mood: "明快", Tone: "黑金", Density: "高密"}); err != nil {
		t.Fatalf("三轴全空应可回填: %v", err)
	}
	if err := lib.RefillFacets(a.ID, StyleFacets{Mood: "悬疑"}); err == nil {
		t.Fatal("已有分面的条目应拒绝回填（人工值优先）")
	}
}
