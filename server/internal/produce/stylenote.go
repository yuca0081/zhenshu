package produce

import (
	"encoding/json"
	"os"
	"strings"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/stylelib"
)

// designNoteFor 段级风格注入（v2 笼子二入口，plan-v2 §4）：style_samples.json 含设计系统块
// （fonts/motion/device 任一）→ DesignDoc 机械渲染——皮肤（含双皮肤切换规则）/字体三角色/
// 动效语言/贯穿装置/版式轮换/负面清单一次给全，本段版式指派按段号内联；
// 否则退 v1 兜底（方向+皮肤槽位+注册表版式轮换——库内老包/旧项目）。
func designNoteFor(p *pipeline.Project, dataDir string, segIdx int) string {
	var ss contract.StyleSamples
	if b, err := os.ReadFile(p.Artifact("style/style_samples.json")); err == nil && json.Unmarshal(b, &ss) == nil && hasDesignSystem(&ss) {
		return "\n" + pipeline.DesignDoc(&ss, segIdx)
	}
	return styleNoteFor(p, dataDir) + layoutNote(projectLayouts(p, dataDir), segIdx)
}

// hasDesignSystem 是否 v2 设计系统（区别于 v1 纯皮肤文件——升级换代的判别器）。
func hasDesignSystem(ss *contract.StyleSamples) bool {
	return ss.Fonts != nil || ss.Motion != nil || ss.Device != nil
}

// projectStyle 项目锚定的风格库条目：style_samples.style_id 精确查（选库/冷启动落盘），
// direction 精确匹配兜底（旧项目/手写样张）。未命中返回 nil（纯靠样张截图注入）。
func projectStyle(p *pipeline.Project, dataDir string) *stylelib.Style {
	lib := stylelib.New(dataDir)
	if id := pipeline.ProjectStyleID(p); id != "" {
		if s, ok := lib.Get(id); ok {
			return s
		}
	}
	if s, ok := lib.ByDirection(projectDirection(p)); ok {
		return s
	}
	return nil
}

// projectLayouts 项目锚定风格的标准版式（场景生成按段轮换，治每段同构）。
func projectLayouts(p *pipeline.Project, dataDir string) []stylelib.Layout {
	if st := projectStyle(p, dataDir); st != nil {
		return st.Layouts
	}
	return nil
}

// styleNoteFor 项目风格 → 场景生成提示里的风格说明：方向 + 皮肤槽位（§12.2，锁定后
// 以项目为准）+ 风格库条目补题材/关键词/色板 tokens（LLM 写场景代码直接拿 hex；
// v1 的配图调性随配图槽位一起退役——v2 场景是程序化图形，无配图）。
func styleNoteFor(p *pipeline.Project, dataDir string) string {
	direction := projectDirection(p)
	if direction == "" {
		return ""
	}
	note := "\n## 本片风格\n- 方向：" + direction
	note += skinNote(p)
	st := projectStyle(p, dataDir)
	if st == nil {
		return note
	}
	if st.Genre != "" {
		note += "\n- 适用题材：" + st.Genre
	}
	if len(st.Keywords) > 0 {
		note += "\n- 关键词：" + strings.Join(st.Keywords, "、")
	}
	if pal := PaletteNote(st.Tokens); pal != "" && !strings.Contains(note, "皮肤槽位") {
		// 项目没锁皮肤时库色板是唯一配色事实源；锁了皮肤则槽位优先，不重复注入
		note += "\n- 色板（画面配色以此为准）：" + pal
	}
	return note
}

// PaletteNote 风格库 tokens → 一行人话色板（底/主字/墨/accent/面板/字体）。
// tokens 形状：bg 可能是 hex 字符串或 {from,to,...} 渐变对象；accent 可能是单串或数组。
func PaletteNote(tokens map[string]any) string {
	if len(tokens) == 0 {
		return ""
	}
	get := func(k string) string { s, _ := tokens[k].(string); return strings.TrimSpace(s) }
	var parts []string
	if bg := get("bg"); bg != "" {
		parts = append(parts, "底 "+bg)
	} else if m, ok := tokens["bg"].(map[string]any); ok {
		from, _ := m["from"].(string)
		to, _ := m["to"].(string)
		from, to = strings.TrimSpace(from), strings.TrimSpace(to)
		switch {
		case from != "" && to != "":
			parts = append(parts, "底 "+from+"→"+to)
		case from != "":
			parts = append(parts, "底 "+from)
		}
	}
	if v := get("txt"); v != "" {
		parts = append(parts, "主字 "+v)
	}
	if v := get("ink"); v != "" {
		parts = append(parts, "墨/强调 "+v)
	}
	if v := get("accent"); v != "" {
		parts = append(parts, "accent "+v)
	} else if arr, ok := tokens["accent"].([]any); ok {
		var hexes []string
		for _, c := range arr {
			if s, _ := c.(string); s != "" {
				hexes = append(hexes, s)
			}
		}
		if len(hexes) > 0 {
			parts = append(parts, "accent "+strings.Join(hexes, "/"))
		}
	}
	if v := get("surface"); v != "" {
		parts = append(parts, "面板底 "+v)
	}
	if v := get("surface_txt"); v != "" {
		parts = append(parts, "面板字 "+v)
	}
	if v := get("font"); v != "" {
		parts = append(parts, "字体 "+v)
	}
	return strings.Join(parts, " · ")
}
