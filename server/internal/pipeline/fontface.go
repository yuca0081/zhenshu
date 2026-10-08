package pipeline

// 场景 HTML 用了系统字体却忘写 @font-face 声明，是 lint 硬错误里最高频的一类
// （font_family_without_font_face），且 LLM 修复轮反复不收敛——模型每次重画都可能
// 换一批字体忘声明。落盘侧机械补齐：font-family 里出现过、系统白名单内、且尚未
// 声明的字体族，逐个补 src:local 声明。确定性操作，零 LLM 成本，幂等。

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// osFontWhitelist 渲染端（Windows/镜像）实际可用的系统字体——scene 提示字体白名单
// 的超集；不在白名单里的字体族不动（web 字体必须有真字体文件，src:local 是骗检）。
// 与 ai/assemble.py 的 _OS_CJK_FONTS 是同一份集合的双端实现（中文别名/西文全收），
// constants_drift_test.go 强制两边一致，改这里必须同步那边。
var osFontWhitelist = map[string]bool{
	"microsoft yahei": true, "微软雅黑": true,
	"simhei": true, "黑体": true,
	"simsun": true, "宋体": true,
	"nsimsun": true, "新宋体": true,
	"kaiti": true, "楷体": true,
	"stkaiti": true, "stsong": true, "stzhongsong": true,
	"fangsong": true, "仿宋": true,
	"noto serif sc": true, "noto sans sc": true,
	"consolas": true, "segoe ui": true, "georgia": true, "cambria": true,
	"arial narrow": true, "arial": true, "courier new": true,
	"times new roman": true, "impact": true,
}

var (
	reFontFaceFamily = regexp.MustCompile(`(?is)@font-face\s*\{[^}]*?font-family\s*:\s*['"]?([^'";}]+)`)
	// 单引号/裸字体栈：值到 ; } > 为止（' 在值内合法，拆完剥引号）
	reFontFamilyUse = regexp.MustCompile(`(?i)font-family\s*:\s*['"]?([^;}">]+)`)
	// 双引号字体栈："首族"后的剩余段（"Arial Narrow", Georgia, serif → ", Georgia, serif"）
	reFontFamilyUseDQ = regexp.MustCompile(`(?i)font-family\s*:\s*"[^"]+"\s*,([^;}]+)`)
)

// ensureFontFaces 补齐缺失的系统字体 @font-face 声明，返回原文或补齐后的文本。
func ensureFontFaces(html string) string {
	declared := map[string]bool{}
	for _, m := range reFontFaceFamily.FindAllStringSubmatch(html, -1) {
		declared[strings.ToLower(strings.TrimSpace(m[1]))] = true
	}
	used := map[string]bool{}
	collect := func(values []string) {
		for _, stack := range values {
			for _, name := range strings.Split(stack, ",") {
				name = strings.ToLower(strings.TrimSpace(strings.Trim(name, "'\"")))
				if osFontWhitelist[name] {
					used[name] = true
				}
			}
		}
	}
	for _, m := range reFontFamilyUse.FindAllStringSubmatch(html, -1) {
		collect(m[1:2])
	}
	for _, m := range reFontFamilyUseDQ.FindAllStringSubmatch(html, -1) {
		collect(m[1:2])
	}
	var missing []string
	for name := range used {
		if !declared[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return html
	}
	sort.Strings(missing)
	var decl strings.Builder
	for _, name := range missing {
		display := titleCaseFont(name)
		fmt.Fprintf(&decl, "@font-face{font-family:'%s';src:local('%s');}", display, display)
	}
	if i := strings.Index(html, "<style"); i >= 0 {
		if j := strings.IndexByte(html[i:], '>'); j >= 0 {
			at := i + j + 1
			return html[:at] + decl.String() + html[at:]
		}
	}
	return "<style>" + decl.String() + "</style>" + html
}

// titleCaseFont 白名单小写键 → 规范展示名（local('Microsoft YaHei') 的大小写要跟系统一致）。
func titleCaseFont(lower string) string {
	for _, canonical := range []string{"Microsoft YaHei", "SimHei", "SimSun", "KaiTi", "STKaiti",
		"FangSong", "Consolas", "Segoe UI", "Georgia", "Cambria", "Arial Narrow", "Arial",
		"Courier New", "Times New Roman", "Impact"} {
		if strings.ToLower(canonical) == lower {
			return canonical
		}
	}
	return lower
}

// fixupFrameFonts 对已落盘的合成物 HTML 补字体声明（幂等，内容没变不重写）。
// 场景文件被上游缓存复用时 write 位不会执行，全量扫一遍兜住存量。
func fixupFrameFonts(p *Project) {
	entries, err := os.ReadDir(p.Artifact("compositions/frames"))
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		path := filepath.Join(p.Artifact("compositions/frames"), e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		fixed := ensureFontFaces(string(b))
		if fixed != string(b) {
			_ = writeFile(path, fixed)
		}
	}
}
