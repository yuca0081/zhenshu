package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 漂移门禁：SegTail 是 Go/Py/TS 三端隐式契约（MP4 段长 = 配音时长 + 尾垫），
// 任何一端单方面改动都会让「引用此刻」的段内偏移错 0.35×N 秒。
// 这里强制 ai/assemble.py 与 web/src/segtime.ts 的字面值与 contract.SegTail 一致。
func TestSegTailMatchesTwinImplementations(t *testing.T) {
	py, err := os.ReadFile(filepath.Join("..", "..", "..", "ai", "assemble.py"))
	if err != nil {
		t.Fatalf("assemble.py 不可读: %v", err)
	}
	ts, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "src", "segtime.ts"))
	if err != nil {
		t.Fatalf("segtime.ts 不可读: %v", err)
	}
	tail := regexp.QuoteMeta(strconv.FormatFloat(SegTail, 'f', -1, 64))
	if !regexp.MustCompile(`\+\s*` + tail).Match(py) {
		t.Errorf("ai/assemble.py 中未找到「+ %s」尾垫字面值：若改了 contract.SegTail，须同步 scene = duration_s + %s", tail, tail)
	}
	if !regexp.MustCompile(`SEG_TAIL\s*=\s*` + tail).Match(ts) {
		t.Errorf("web/src/segtime.ts 中 SEG_TAIL 与 contract.SegTail（%s）不一致", tail)
	}
}

// 漂移门禁：风格库事实源是 data/library/styles.json（Go 侧 pipeline.styleTokens 直读）。
// 曾踩坑：assemble.py 残留 registry/ 旧路径，try/except 吞错后 overlay 风格 token
// 与转场模式静默走默认值——这里禁止该旧路径再出现。
func TestAssembleStylesPathNoRegistry(t *testing.T) {
	py, err := os.ReadFile(filepath.Join("..", "..", "..", "ai", "assemble.py"))
	if err != nil {
		t.Fatalf("assemble.py 不可读: %v", err)
	}
	if regexp.MustCompile(`registry`).Match(py) {
		t.Errorf("ai/assemble.py 引用了 registry/ 旧路径——现行事实源是 data/library/styles.json")
	}
	if !regexp.MustCompile(`'styles\.json'`).Match(py) {
		t.Errorf("ai/assemble.py 未引用 styles.json（overlay 取 token 的路径丢了？）")
	}
}

// 漂移门禁：系统字体白名单是 Go/Py 双端隐式契约——scene HTML 补声明走 pipeline
// osFontWhitelist，字幕 overlay 走 assemble.py _OS_CJK_FONTS。两边集合不一致时，
// 同一字体名一侧补声明另一侧不补，lint 修复轮就会单侧反复失败。
func TestFontWhitelistMatchesTwinImplementation(t *testing.T) {
	go_, err := os.ReadFile(filepath.Join("..", "..", "..", "server", "internal", "pipeline", "fontface.go"))
	if err != nil {
		t.Fatalf("fontface.go 不可读: %v", err)
	}
	py, err := os.ReadFile(filepath.Join("..", "..", "..", "ai", "assemble.py"))
	if err != nil {
		t.Fatalf("assemble.py 不可读: %v", err)
	}
	extract := func(re, src string) map[string]bool {
		out := map[string]bool{}
		for _, m := range regexp.MustCompile(re).FindAllStringSubmatch(src, -1) {
			out[strings.TrimSpace(m[1])] = true
		}
		return out
	}
	goSet := extract(`"([^"]+)":\s*true`, string(go_))
	// py 集合以「'xxx', 'yyy',\n」字面量书写，逐对引号截取
	pySet := extract(`'([^']+)'`, regexp.MustCompile(`_OS_CJK_FONTS = \{([^}]*)\}`).
		FindStringSubmatch(string(py))[1])
	if len(goSet) == 0 {
		t.Fatal("fontface.go 里没提取到任何白名单条目（结构变了？同步本测试）")
	}
	if len(pySet) == 0 {
		t.Fatal("assemble.py 里没提取到 _OS_CJK_FONTS 条目（结构变了？同步本测试）")
	}
	for name := range goSet {
		if !pySet[name] {
			t.Errorf("白名单漂移：%q 在 fontface.go 有、assemble.py 没有（两边必须同一集合）", name)
		}
	}
	for name := range pySet {
		if !goSet[name] {
			t.Errorf("白名单漂移：%q 在 assemble.py 有、fontface.go 没有（两边必须同一集合）", name)
		}
	}
}
