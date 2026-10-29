package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
)

// buildXfadeChain 泛化的回归闸：均一 fade/0.4 边界下，视频链与旧 buildXfadeGraph
// 的 D·i 公式产物必须逐字节一致（v1b xfade 令牌语义不漂）。方案甲 §4.1 的承诺。
func TestBuildXfadeChainUniformMatchesLegacy(t *testing.T) {
	durs := []float64{5.0, 4.0, 6.0, 3.5}
	edges := []xfadeEdge{{Kind: "fade", Dur: xfadeDur}, {Kind: "fade", Dur: xfadeDur}, {Kind: "fade", Dur: xfadeDur}}
	vcChain, startsChain := buildXfadeChain(durs, edges)
	vcLegacy, _, startsLegacy, err := buildXfadeGraph(durs)
	if err != nil {
		t.Fatal(err)
	}
	if vcChain != vcLegacy {
		t.Fatalf("均一窗口下视频链应与旧图逐字节一致:\nchain:  %s\nlegacy: %s", vcChain, vcLegacy)
	}
	for i := range startsChain {
		if d := startsChain[i] - startsLegacy[i]; d > 1e-9 || d < -1e-9 {
			t.Fatalf("starts[%d] 漂移: %v vs %v", i, startsChain[i], startsLegacy[i])
		}
	}
}

// buildParaGraph 分层图的数学与形态：段内 fade 0.4 / 段界 smoothleft 0.5，
// offset = Σd − ΣD；音频裁尾对接（atrim+concat，禁 acrossfade——旁白开头不吃淡入）。
func TestBuildParaGraph(t *testing.T) {
	durs := []float64{5.0, 4.0, 6.0}
	edges := []xfadeEdge{{Kind: "fade", Dur: paraInDur}, {Kind: paraEdgeKind, Dur: paraEdgeDur}}
	vc, ac, starts, err := buildParaGraph(durs, edges)
	if err != nil {
		t.Fatal(err)
	}
	// 起点：0、5−0.4=4.6、(5+4)−(0.4+0.5)=8.1
	want := []float64{0, 4.6, 8.1}
	for i, w := range want {
		if d := starts[i] - w; d > 0.01 || d < -0.01 {
			t.Fatalf("start[%d]=%v want %v", i, starts[i], w)
		}
	}
	if !strings.Contains(vc, "transition=fade:duration=0.40:offset=4.600") {
		t.Fatalf("段内边界应 fade 0.40 offset 4.600: %s", vc)
	}
	if !strings.Contains(vc, "transition=smoothleft:duration=0.50:offset=8.100[vout]") {
		t.Fatalf("段界边界应 %s 0.50 offset 8.100: %s", paraEdgeKind, vc)
	}
	// 音频：前两段按各自边界窗口裁尾（5−0.4=4.6、4−0.5=3.5），末段不裁，concat 顶接
	if !strings.Contains(ac, "[0:a]atrim=duration=4.600,asetpts=PTS-STARTPTS[a0]") ||
		!strings.Contains(ac, "[1:a]atrim=duration=3.500,asetpts=PTS-STARTPTS[a1]") {
		t.Fatalf("裁尾长度应按各自边界窗口: %s", ac)
	}
	if !strings.Contains(ac, "[a0][a1][2:a]concat=n=3:v=0:a=1[aout]") {
		t.Fatalf("末段不裁直接 concat 顶接: %s", ac)
	}
	if strings.Contains(ac, "acrossfade") {
		t.Fatalf("para 音频不得用 acrossfade（叠化窗内 incoming 淡入伤旁白开头）: %s", ac)
	}
}

// 段短于其相邻边界窗口：拒绝并回退 concat（与 xfade 同口径，不能硬叠）。
func TestBuildParaGraphShortSeg(t *testing.T) {
	edges := []xfadeEdge{{Kind: "fade", Dur: paraInDur}, {Kind: paraEdgeKind, Dur: paraEdgeDur}}
	if _, _, _, err := buildParaGraph([]float64{5.0, 0.44, 6.0}, edges); err == nil {
		t.Fatal("0.44s 段装不下 0.5s 入边窗口，应报 errXfadeShort")
	}
	if _, _, _, err := buildParaGraph([]float64{5.0}, nil); err == nil {
		t.Fatal("单段应报 errXfadeShort")
	}
}

// paraEdges 边界分类：同段落相邻 → 段内 fade；异段落/无段落层/认不出 → 段界
// （保守：宁可翻页，不把异段落错认成同段顺接）。
func TestParaEdges(t *testing.T) {
	p := NewProject(t.TempDir(), "ptest")
	sb := &contract.Storyboard{Segments: []contract.Segment{
		{ID: "seg01"}, {ID: "seg02"}, {ID: "seg03"}, {ID: "seg04"},
	}}
	// 无段落层（老项目/直建）：全段界
	for _, e := range paraEdges(p, sb) {
		if e.Kind != paraEdgeKind || e.Dur != paraEdgeDur {
			t.Fatalf("无段落层应全段界: %+v", e)
		}
	}
	// 段落层：seg01–03 同段、seg04 异段 → 段内×2 + 段界×1
	os.MkdirAll(filepath.Dir(p.Artifact("storyboards/draft.json")), 0o755)
	os.WriteFile(p.Artifact("storyboards/draft.json"),
		[]byte(`{"paragraphs":[{"title":"甲","segs":["seg01","seg02","seg03"]},{"title":"乙","segs":["seg04"]}]}`), 0o644)
	edges := paraEdges(p, sb)
	if len(edges) != 3 {
		t.Fatalf("4 段应有 3 边界，得到 %d", len(edges))
	}
	if edges[0].Kind != "fade" || edges[1].Kind != "fade" || edges[2].Kind != paraEdgeKind {
		t.Fatalf("分类应为 段内/段内/段界: %+v", edges)
	}
	// 草稿段落里缺 seg04（认不出）：该边界 → 段界
	os.WriteFile(p.Artifact("storyboards/draft.json"),
		[]byte(`{"paragraphs":[{"title":"甲","segs":["seg01","seg02","seg03"]}]}`), 0o644)
	edges = paraEdges(p, sb)
	if edges[2].Kind != paraEdgeKind {
		t.Fatalf("认不出的边界应段界（保守）: %+v", edges)
	}
}
