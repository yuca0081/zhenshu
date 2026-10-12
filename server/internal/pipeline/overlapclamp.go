package pipeline

// data-layout-allow-overlap 是「有意叠层」的声明属性（hyperframes check 的免检口子：
// 声明的文字叠压不升格为 content_overlap 错误）。口子只对设计真声明了叠层版式的片开放
// ——否则修复轮会学会「被报了就贴属性」的作弊路径，checker 形同虚设。
// 机械钳与 clampFontStack/applyContrastFixes 同模式：确定性操作，零 LLM 成本，幂等。
// 设计无叠层版式 → 全剥；有 → 单文件超上限的剥多余（上限防合法段里全屏乱贴）。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"webpage-video-agent/server/internal/contract"
)

// maxOverlapDeclsPerFrame 单帧声明上限：一个版式的叠层手法用不到更多；超过视为乱贴。
const maxOverlapDeclsPerFrame = 4

// reOverlapDecl 匹配 HTML 标签上的声明属性（裸布尔或带值）。要求前导空白——
// CSS 属性选择器 [data-layout-allow-overlap] 紧贴 '['，不会被误剥。
var reOverlapDecl = regexp.MustCompile(`(?i)\s+data-layout-allow-overlap(?:="[^"]*")?`)

// designHasLayeredLayout 设计系统里是否存在声明了叠层的版式。读不到设计按无叠层处理
// （老项目/设计未锁：声明一律剥，修复轮只能走重排）。
func designHasLayeredLayout(p *Project) bool {
	var ss contract.StyleSamples
	if readJSON(p.Artifact("style/style_samples.json"), &ss) != nil {
		return false
	}
	for _, l := range ss.Layouts {
		if strings.TrimSpace(l.Layered) != "" {
			return true
		}
	}
	return false
}

// clampOverlapAttr 单文件剥除：layeredOK=false 全剥；true 保留前 N 个、剥超限的。
// 返回剥后文本与剥除处数。从尾往前剥，前面的偏移量不受影响。
func clampOverlapAttr(html string, layeredOK bool) (string, int) {
	locs := reOverlapDecl.FindAllStringIndex(html, -1)
	if len(locs) == 0 {
		return html, 0
	}
	if !layeredOK {
		return reOverlapDecl.ReplaceAllString(html, ""), len(locs)
	}
	if len(locs) <= maxOverlapDeclsPerFrame {
		return html, 0
	}
	for i := len(locs) - 1; i >= maxOverlapDeclsPerFrame; i-- {
		html = html[:locs[i][0]] + html[locs[i][1]:]
	}
	return html, len(locs) - maxOverlapDeclsPerFrame
}

// clampOverlapDecls 全量扫场景帧剥声明（字幕无叠层手法，不扫 caps/）。返回剥除总处数。
func clampOverlapDecls(p *Project) int {
	entries, err := os.ReadDir(p.Artifact("compositions/frames"))
	if err != nil {
		return 0
	}
	layeredOK := designHasLayeredLayout(p)
	stripped := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		path := filepath.Join(p.Artifact("compositions/frames"), e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		fixed, n := clampOverlapAttr(string(b), layeredOK)
		if n > 0 {
			_ = writeFile(path, fixed)
			stripped += n
		}
	}
	return stripped
}
