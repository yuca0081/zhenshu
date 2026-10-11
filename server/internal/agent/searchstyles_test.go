package agent

import (
	"context"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/stylelib"
)

// search_styles 曾把原始 args JSON 串当查询词做子串匹配，带 query 调用必全部
// 过滤、永远回「没有命中」（2026-10-12 深审抓获，此前零测试覆盖）。锁死：
// 参数解析 + 命中/未命中/空库三态。
func TestSearchStylesParsesQuery(t *testing.T) {
	l := stylelib.New(t.TempDir())
	main, err := l.VocabCreate(stylelib.VocabEntry{Name: "粘土定格", Brief: "逐帧手作质感"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.IngestVideoRef("Ref.2024", stylelib.VideoStyleIn{
		Name:    "粘土微缩剧场",
		Subject: "粘土动画拍的面包店日常",
		Tags:    contract.StyleTags{Main: main},
	}); err != nil {
		t.Fatal(err)
	}
	a := &Agent{Lib: l}
	p := pipeline.NewProject(t.TempDir(), "p-test")
	tl := findTool("search_styles")

	if out := tl.run(context.Background(), a, p, `{"query":"粘土"}`); !strings.Contains(out, "粘土微缩剧场") {
		t.Fatalf("应命中已入库条目: %s", out)
	}
	if out := tl.run(context.Background(), a, p, `{"query":"蒸汽波赛博朋克"}`); !strings.Contains(out, "没有命中") {
		t.Fatalf("未命中应如实说: %s", out)
	}
	if out := tl.run(context.Background(), a, p, `{}`); !strings.Contains(out, "粘土微缩剧场") {
		t.Fatalf("空 query 应列全库: %s", out)
	}

	empty := &Agent{Lib: stylelib.New(t.TempDir())}
	if out := tl.run(context.Background(), empty, p, `{"query":"粘土"}`); !strings.Contains(out, "风格库为空") {
		t.Fatalf("空库应明说: %s", out)
	}
}
