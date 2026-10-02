// stylebackfill 一次性回填工具：视频解析入库的老条目缺设计系统五件套
// （concept/fonts/motion/device/anti_patterns——入库早于五件套代码），而重解析
// 整条链要 40+ 次视觉调用且分段边界靠 LLM 重猜、可能跟老条目错位。这里逐条
// 拿条目自己的真迹关键帧（style_media/<id>/，缺失回退 references 对应段目录）
// 喂视觉 LLM 只补五件套，自检阈值与入库自检同一套（styleref.ValidateDesignSystem），
// 不过带错重喂（≤3 轮）。顺带补媒体快照与真迹关键帧登记。
//
// 用法（在仓库根或 server/ 下跑，.env 同 server）：
//
//	go run ./cmd/stylebackfill                 # 干跑：生成但不写库
//	go run ./cmd/stylebackfill -write          # 写库
//	go run ./cmd/stylebackfill -write -only vid-ref20261001-232713-s03
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"webpage-video-agent/server/internal/config"
	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/stylelib"
	"webpage-video-agent/server/internal/styleref"
)

var reSeg = regexp.MustCompile(`-s(\d{2})$`)

func main() {
	write := flag.Bool("write", false, "真正写库（缺省干跑：生成并打印，不落盘）")
	only := flag.String("only", "", "只处理指定条目 id（单条重试用）")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Println("配置加载失败:", err)
		os.Exit(1)
	}
	prov, err := llm.FromEnv(llm.RoleVisual)
	if err != nil {
		fmt.Println("视觉 LLM 不可用（.env 配 LLM_API_KEY）:", err)
		os.Exit(1)
	}
	lib := stylelib.New(cfg.DataDir)
	styles, err := lib.List()
	if err != nil {
		fmt.Println("风格库读取失败:", err)
		os.Exit(1)
	}

	var todo []stylelib.Style
	for _, st := range styles {
		if *only != "" && st.ID != *only {
			continue
		}
		if !st.Published || !strings.HasPrefix(st.OriginProject, "video:") {
			continue
		}
		if hasFivePiece(&st) {
			continue
		}
		todo = append(todo, st)
	}
	fmt.Printf("待回填 %d 条（库内共 %d 条；mode=%s）\n", len(todo), len(styles), map[bool]string{true: "write", false: "dry"}[*write])

	fail := 0
	for i := range todo {
		st := &todo[i]
		fmt.Printf("\n== [%d/%d] %s「%s」\n", i+1, len(todo), st.ID, st.Name)
		if err := backfillOne(lib, prov, st, *write); err != nil {
			fail++
			fmt.Printf("   ✗ %v\n", err)
			continue
		}
	}
	fmt.Printf("\n完成：%d 条待回填，%d 条失败\n", len(todo), fail)
	if fail > 0 {
		os.Exit(1)
	}
}

func hasFivePiece(st *stylelib.Style) bool {
	return st.Concept != "" && st.Fonts != nil && st.Motion != nil && st.Device != nil
}

func backfillOne(lib *stylelib.Library, prov *llm.Provider, st *stylelib.Style, write bool) error {
	paths, segDir := framePaths(lib, st)
	if write && len(paths) == 0 && segDir != "" {
		// 媒体目录还没快照：先补（快照是幂等跳过已存在文件）
		if err := lib.SnapshotMedia(st.ID, segDir); err != nil {
			fmt.Println("   · 媒体快照失败（不挡回填）:", err)
		} else {
			fmt.Println("   · 媒体快照已补", segDir)
		}
		paths, _ = framePaths(lib, st)
	}
	if len(paths) == 0 {
		return fmt.Errorf("找不到真迹帧（媒体目录与 references 段目录都空）")
	}
	var imgs []string
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		imgs = append(imgs, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(b))
	}
	if len(imgs) == 0 {
		return fmt.Errorf("真迹帧全读不了")
	}

	tokens, _ := json.Marshal(st.Tokens)
	layouts := make([]string, 0, len(st.Layouts))
	for _, l := range st.Layouts {
		layouts = append(layouts, l.ID+"("+l.Name+")")
	}
	system := "你是视频视觉分析师。只输出 JSON。"
	user := fmt.Sprintf(`给参考视频风格「%s」补齐设计系统五件套（条目入库于旧版本，缺这一页）。附 %d 张该风格的真实关键帧，气质判断以图为准。

## 已知事实（必须遵守）
- 条目说明：%s
- 适用题材：%s；关键词：%s
- 色板 tokens：%s
- 标准版式：%s

## 输出五件套（只输出 JSON，无围栏、无注释）
{"concept":"一句话（≤40字）：这套风格的视觉概念，它为什么像这部片",
 "fonts":{"display":{"stack":"..","weight":"..","tracking":".."},"body":{...},"data":{...}},
 "motion":{"ease":"缓动族","dur_in":0.5,"dur_settle":0.25,"stagger":0.08,"notes":"什么怎么动、禁什么动"},
 "device":{"name":"装置名","desc":"是什么、观众为什么记住它","per_seg":"逐段怎么重复/变奏"},
 "anti_patterns":["…×3-6，这部片绝不会出现的俗手"]}
- fonts 栈只能用 Windows 系统字体（'Segoe UI'/'Microsoft YaHei'/'Georgia'/'KaiTi'/'STKaiti'/'Cambria'/'Arial Narrow'/'SimHei' 组合），display/body 必填且必须含中文字体
- motion 看关键帧推断元素进场/节奏；时长档 dur_in 0.1–1.5、dur_settle 0.05–1.0、stagger 0–0.5
- device 没有显式装置就从构图语法归纳一个（如「放射线强调」「红色印章盖角」）`,
		st.Direction, len(imgs), st.Desc, st.Genre, strings.Join(st.Keywords, "、"),
		string(tokens), strings.Join(layouts, "、"))

	var d styleref.StyleDraft
	var lastErr error
	for round := 0; round < 3; round++ {
		prompt := user
		if lastErr != nil {
			prompt = "上一版自检未过：" + lastErr.Error() + "——修正后重新输出完整五件套 JSON。\n\n" + user
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		_, gerr := prov.GenerateJSONVisionNoThink(ctx, system, prompt, imgs, 0.5, &d)
		cancel()
		if gerr != nil {
			return fmt.Errorf("视觉生成失败: %w", gerr)
		}
		if errs := styleref.ValidateDesignSystem(&d); len(errs) > 0 {
			lastErr = fmt.Errorf("%s", strings.Join(errs, "; "))
			fmt.Printf("   · 第%d轮自检未过：%s\n", round+1, lastErr)
			continue
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		return fmt.Errorf("三轮自检都未过: %w", lastErr)
	}

	fmt.Printf("   concept: %s\n", d.Concept)
	fmt.Printf("   device: %s — %s\n", d.Device.Name, d.Device.Desc)
	fmt.Printf("   motion: %s（%.2fs/%.2fs/%.2fs）\n", d.Motion.Ease, d.Motion.DurIn, d.Motion.DurSettle, d.Motion.Stagger)
	fmt.Printf("   anti: %s\n", strings.Join(d.AntiPatterns, "、"))
	if !write {
		fmt.Println("   · 干跑不写库（-write 落盘）")
		return nil
	}
	return lib.UpdateEntry(st.ID, func(up *stylelib.Style) error {
		// 只补空位：条目可能被用户手工改过，不整条覆盖
		if up.Concept == "" {
			up.Concept = d.Concept
		}
		if up.Fonts == nil {
			up.Fonts = d.Fonts
		}
		if up.Motion == nil {
			up.Motion = d.Motion
		}
		if up.Device == nil {
			up.Device = d.Device
		}
		if len(up.AntiPatterns) == 0 {
			up.AntiPatterns = d.AntiPatterns
		}
		if len(up.Keyframes) == 0 {
			up.Keyframes = stylelib.PickKeyframes(frameNames(lib, st.ID), 5)
		}
		return nil
	})
}

// framePaths 真迹帧路径：媒体目录（快照过）→ references 原始段目录。≤4 张均匀挑。
// 第二返回值是找到的 references 段目录（媒体缺失时供快照补齐），找不到为空。
func framePaths(lib *stylelib.Library, st *stylelib.Style) ([]string, string) {
	if names := frameNames(lib, st.ID); len(names) > 0 {
		dir := lib.MediaDir(st.ID)
		var out []string
		for _, p := range stylelib.PickKeyframes(names, 4) {
			out = append(out, filepath.Join(dir, p))
		}
		return out, ""
	}
	// references/<refID>[-sNN → /seg-NN]
	refID := strings.TrimPrefix(st.OriginProject, "video:")
	// MediaDir = <data>/library/style_media/<id>，向上三级到 data/ 再拼 references
	refsRoot := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(lib.MediaDir(st.ID)))), "references")
	segDir := filepath.Join(refsRoot, refID)
	if m := reSeg.FindStringSubmatch(refID); m != nil {
		segDir = filepath.Join(refsRoot, refID[:len(refID)-len(m[0])], "seg-"+m[1])
	}
	if ff := styleref.ReadFeatures(segDir); len(ff) > 0 {
		var all []string
		for _, f := range ff {
			all = append(all, f.Path)
		}
		return stylelib.PickKeyframes(all, 4), segDir
	}
	return nil, segDir
}

func frameNames(lib *stylelib.Library, id string) []string {
	es, err := os.ReadDir(lib.MediaDir(id))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range es {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "frame-") && strings.HasSuffix(e.Name(), ".png") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // frame-NN 按名即按时间序
	return names
}
