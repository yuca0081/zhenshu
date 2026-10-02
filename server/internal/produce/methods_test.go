package produce

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"webpage-video-agent/server/internal/contract"
)

// 方法库守卫（data/library/methods/）：清单 methods.json 与文件必须一致，
// scene 域模板必须过确定性黑名单（模板自己违规就没资格给 LLM 仿写），
// comp 域公式必须短小可注入（进的是每段 spec prompt 的 token 预算）。
func TestMethodsLibraryManifest(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	ms := loadMethods(root)
	if len(ms) < 5 {
		t.Fatalf("方法库清单过薄（%d 条）：scene 3 + comp 3 起步", len(ms))
	}
	seen := map[string]bool{}
	domains := map[string]int{}
	for _, m := range ms {
		if m.Domain != "scene" && m.Domain != "comp" {
			t.Errorf("%s: 未知 domain %q（scene|comp）", m.File, m.Domain)
		}
		if m.Title == "" || len(m.Keywords) == 0 {
			t.Errorf("%s: 缺 title 或 keywords", m.File)
		}
		if seen[m.File] {
			t.Errorf("%s: 清单重复条目", m.File)
		}
		seen[m.File] = true
		b, err := os.ReadFile(filepath.Join(root, "data", "library", "methods", filepath.FromSlash(m.File)))
		if err != nil {
			t.Errorf("%s: 文件缺失: %v", m.File, err)
			continue
		}
		body := string(b)
		domains[m.Domain]++
		switch m.Domain {
		case "scene":
			if n := utf8.RuneCountInString(body); n > 3200 {
				t.Errorf("%s: %d 字超重（参考实现 ≤3.2k，注入的是 prompt token）", m.File, n)
			}
			if v := contract.SceneCodeViolation(body); v != "" {
				t.Errorf("%s: 模板含禁用模式 %q——确定性黑名单不过的模板教不出合规 scene", m.File, v)
			}
		case "comp":
			if n := utf8.RuneCountInString(body); n > 900 {
				t.Errorf("%s: %d 字超重（构图公式 ≤900 字，贵精不贵多）", m.File, n)
			}
			if !strings.Contains(body, "反模式") {
				t.Errorf("%s: 构图公式应有「反模式」段（负面示例是纪律公式的承重墙）", m.File)
			}
		}
	}
	if domains["scene"] < 3 || domains["comp"] < 3 {
		t.Errorf("域覆盖不足：scene %d / comp %d（各 ≥3）", domains["scene"], domains["comp"])
	}
}

// methodsFor 关键词命中：scene 命中弦图参考；comp 命中暗底浅字；两域独立；
// 关键词全不命中时零注入（老行为）。
func TestMethodsForInjection(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	sc, cp := methodsFor(root, contract.Segment{Key: "乘法表与圆环", VisualBrief: "深空底上粒子飞舞"})
	if sc == "" {
		t.Errorf("scene 未命中：期望弦图参考实现注入块，得到空串")
	}
	if !strings.Contains(sc, "## scene 参考实现") {
		t.Errorf("scene 注入块缺标准标题（prompt 契约）")
	}
	if !strings.Contains(cp, "暗底浅字纪律") {
		t.Errorf("comp 未命中：深空/粒子段应命中暗底浅字纪律")
	}
	if n := strings.Count(cp, "### "); n > 2 {
		t.Errorf("comp 注入 %d 篇超上限（≤2）", n)
	}

	sc2, cp2 := methodsFor(root, contract.Segment{Key: "品牌介绍", VisualBrief: "产品参数对比表"})
	if sc2 != "" || cp2 != "" {
		t.Errorf("无命中段不应注入方法（scene=%d 字 comp=%d 字）", len(sc2), len(cp2))
	}
}

// 相关性排序：两篇 comp 都命中时，命中词更多/更专一的排前面（非清单序）。
func TestMethodsForRanking(t *testing.T) {
	root := t.TempDir()
	mdir := filepath.Join(root, "data", "library", "methods")
	os.MkdirAll(filepath.Join(mdir, "comp"), 0o755)
	manifest := `[
	 {"domain":"comp","file":"comp/generic.md","title":"泛公式","keywords":["画面"]},
	 {"domain":"comp","file":"comp/specific.md","title":"专一公式","keywords":["深空","粒子","辉光"]}
	]`
	os.WriteFile(filepath.Join(mdir, "methods.json"), []byte(manifest), 0o644)
	os.WriteFile(filepath.Join(mdir, "comp", "generic.md"), []byte("泛公式正文"), 0o644)
	os.WriteFile(filepath.Join(mdir, "comp", "specific.md"), []byte("专一公式正文"), 0o644)
	_, cp := methodsFor(root, contract.Segment{Key: "深空粒子", VisualBrief: "画面辉光飞舞"})
	iSpec, iGen := strings.Index(cp, "专一公式"), strings.Index(cp, "泛公式")
	if iSpec < 0 || iGen < 0 {
		t.Fatalf("两篇都命中应都注入（≤2 上限内）: %q", cp)
	}
	if iSpec > iGen {
		t.Fatalf("高分（三词命中）应排在低分（单词命中）前面: %q", cp)
	}
	// 旁白也参与匹配（标题/简述没有、旁白里有的关键词要能命中）
	_, cp2 := methodsFor(root, contract.Segment{Key: "x", VisualBrief: "y", Narration: "背景是深空与粒子"})
	if !strings.Contains(cp2, "专一公式") {
		t.Fatalf("旁白关键词未参与匹配: %q", cp2)
	}
}
