package main

// facetbackfill.go —— 分面回填子命令（离线跑，不占 8080）：
//
//	server -backfill-facets           dry-run：列出三轴全空的条目与 LLM 建议值，不写库
//	server -backfill-facets --apply   真写：建议值落库（只填三轴全空的，已有一轴的跳过）
//
// 背景（2026-10-24）：解析链此前整层不产分面（2026-10-24 起入库自动带），存量
// 条目 2/3 分面全空——agent 的 search_styles 按情绪/色调/密度筛时这些条目隐身。
// 本命令是存量修补口；材料用条目自述（标签词条名+特色机制+题材），纯文本调用，
// 每条几千 token。

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"webpage-video-agent/server/internal/config"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/store"
	"webpage-video-agent/server/internal/stylelib"
)

const facetBackfillSystem = `你是风格库的策展助理。给一条风格条目归纳分面三轴（检索用短语）。只输出 JSON。`

func runBackfillFacets(args []string) {
	apply := false
	for _, a := range args {
		if a == "--apply" {
			apply = true
		}
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	st, err := store.Open(cfg.PGDSN)
	if err != nil {
		fmt.Println("存储打开失败：", err)
		os.Exit(1)
	}
	lib := stylelib.NewWith(cfg.DataDir, st.DB(), nil) // 账本按 PG/文件与主服务同源；分面回填不碰媒体
	prov, err := llm.FromEnv(llm.RolePlan)
	if err != nil {
		fmt.Println("分面归纳需要 API 模式（LLM_API_KEY）：", err)
		os.Exit(1)
	}

	styles, err := lib.List()
	if err != nil {
		fmt.Println("风格库读取失败：", err)
		os.Exit(1)
	}
	empty := 0
	for _, s := range styles {
		if s.Facets != (stylelib.StyleFacets{}) {
			continue
		}
		empty++
		vocab := lib.VocabBriefs(s.Tags.Main)
		vocab = append(vocab, lib.VocabBriefs(s.Tags.Aux...)...)
		var tagsB strings.Builder
		for _, tb := range vocab {
			tagsB.WriteString("- " + tb.Name + "：" + tb.Brief + "\n")
		}
		var sigB strings.Builder
		for _, sg := range s.Signatures {
			sigB.WriteString("- " + sg.Name + "：" + sg.Mechanism + "\n")
		}
		user := fmt.Sprintf(`风格条目「%s」
题材：%s
标签：
%s特色机制：
%s
从上述材料归纳分面三轴，各 ≤6 字短语，措辞贴近示例便于筛选命中：
mood 情绪（如 沉稳/明快/悬疑/热血/治愈）、tone 色调（如 黑金/低饱和暖色/冷蓝/高对比黑白）、density 密度（如 疏朗/中密/高密）。
输出 {"mood":"..","tone":"..","density":".."}`,
			s.Name, s.Subject, tagsB.String(), sigB.String())

		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		var out stylelib.StyleFacets
		_, gerr := prov.GenerateJSONNoThink(ctx, facetBackfillSystem, user, &out)
		cancel()
		if gerr != nil {
			fmt.Printf("  %s「%s」归纳失败（跳过）：%v\n", s.ID, s.Name, gerr)
			continue
		}
		fmt.Printf("  %s「%s」→ 情绪:%s 色调:%s 密度:%s", s.ID, s.Name, out.Mood, out.Tone, out.Density)
		if apply {
			if rerr := lib.RefillFacets(s.ID, out); rerr != nil {
				fmt.Printf("（写库失败：%v）\n", rerr)
				continue
			}
			fmt.Print("（已写）\n")
		} else {
			fmt.Print("\n")
		}
	}
	mode := "dry-run（要真写加 --apply）"
	if apply {
		mode = "--apply"
	}
	fmt.Printf("分面回填完成：%d 条三轴全空，模式 %s\n", empty, mode)
}
