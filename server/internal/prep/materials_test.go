package prep

// 材料库与 read_source 集成（2026-10-30 主题页配套）：材料与原始稿同等进
// 材料主体；无稿无主题时退主题页想法文本。readSource 是纯函数式的装配逻辑，
// 直接打表验证，不烧 LLM。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/pipeline"
)

func newTestProject(t *testing.T) *pipeline.Project {
	t.Helper()
	return pipeline.NewProject(t.TempDir(), "mat-test")
}

func TestReadSourceMaterials(t *testing.T) {
	p := newTestProject(t)
	if _, err := SaveMaterial(p, "复利笔记.txt", "复利的核心是时间，72 法则：72/年化收益率≈翻倍年数。"); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveMaterial(p, "大纲.md", "# 大纲\n开头用咖啡钱举例。"); err != nil {
		t.Fatal(err)
	}
	got := (&scriptRun{p: p}).readSource(120)
	for _, want := range []string{"复利笔记.txt", "72 法则", "大纲.md", "咖啡钱", "用户上传材料"} {
		if !strings.Contains(got, want) {
			t.Fatalf("readSource 缺材料段 %q\n输出：%s", want, got)
		}
	}
}

// 无原始稿无主题：退主题页想法（新建项目不再填主题的兜底路径）。
func TestReadSourceIdeaFallback(t *testing.T) {
	p := newTestProject(t)
	if err := os.MkdirAll(filepath.Dir(p.Artifact(IdeaFile)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Artifact(IdeaFile), []byte("给上班族讲复利"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := (&scriptRun{p: p}).readSource(120)
	if !strings.Contains(got, "给上班族讲复利") {
		t.Fatalf("想法兜底未生效：%s", got)
	}
}

// 什么都没有：报错可读（不是空串）。
func TestReadSourceEmpty(t *testing.T) {
	p := newTestProject(t)
	got := (&scriptRun{p: p}).readSource(120)
	if !strings.Contains(got, "无法开工") {
		t.Fatalf("空项目应明确报无法开工：%s", got)
	}
}

func TestSaveMaterialGuards(t *testing.T) {
	p := newTestProject(t)
	if _, err := SaveMaterial(p, "../evil.txt", "x"); err == nil {
		t.Fatal("路径穿越名应被拒")
	}
	if _, err := SaveMaterial(p, ".hidden", "x"); err == nil {
		t.Fatal("隐藏文件名应被拒")
	}
	if _, err := SaveMaterial(p, "空内容.txt", "   "); err == nil {
		t.Fatal("空内容应被拒")
	}
	if _, err := SaveMaterial(p, strings.Repeat("长", 60_000), strings.Repeat("字", 51_000)); err == nil {
		t.Fatal("超长材料应被拒")
	}
	// 同名覆盖 = 更新
	if _, err := SaveMaterial(p, "a.txt", "第一版"); err != nil {
		t.Fatal(err)
	}
	list, err := SaveMaterial(p, "a.txt", "第二版")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Chars != len([]rune("第二版")) {
		t.Fatalf("同名覆盖语义破坏：%+v", list)
	}
	b, err := os.ReadFile(filepath.Join(p.Artifact(MaterialsDir), "a.txt"))
	if err != nil || string(b) != "第二版" {
		t.Fatalf("覆盖未落盘：%s %v", b, err)
	}
	// 删除
	list, err = DeleteMaterial(p, "a.txt")
	if err != nil || len(list) != 0 {
		t.Fatalf("删除失败：%v %+v", err, list)
	}
}
