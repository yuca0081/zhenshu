package produce

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
)

// 段落定位注（production-architecture-discussion §4 收益①：生成侧注入段落主题+
// 段内位置）——锁三件事：注的格式、draft 缺失（老项目）全链回退旧行为、提示词
// 行只出现在带注时（前缀缓存布局不被动）。
func TestParaNote(t *testing.T) {
	dir := t.TempDir()
	p := pipeline.NewProject(dir, "p1")
	if got := paraNote(p, "seg02"); got != "" {
		t.Fatalf("无 draft 应得空注，实得 %q", got)
	}
	if got := paraNoteFingerprint(p, "seg02"); got != "" {
		t.Fatalf("无 draft 指纹应不带段落项，实得 %q", got)
	}

	d := &contract.StoryboardDraft{
		Paragraphs: []contract.DraftParagraph{
			{Title: "开场", Segs: []string{"seg01"}},
			{Title: "训练阶段", Segs: []string{"seg02", "seg03"}},
		},
		Segments: []contract.DraftSegment{
			{ID: "seg01", Idx: 1}, {ID: "seg02", Idx: 2}, {ID: "seg03", Idx: 3},
		},
	}
	if err := os.MkdirAll(p.Artifact("storyboards"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Artifact("storyboards/draft.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := paraNote(p, "seg02"); got != "第 2/2 段落「训练阶段」·段内第 1/2 段" {
		t.Fatalf("段内定位注不符，实得 %q", got)
	}
	if got := paraNote(p, "seg99"); got != "" {
		t.Fatalf("未知段应得空注（分镜与 draft 漂移时回退），实得 %q", got)
	}
	if got := paraNoteFingerprint(p, "seg02"); len(got) != 9 || got[0] != ':' {
		t.Fatalf("带注指纹应 \":xxxxxxxx\"，实得 %q", got)
	}

	got := sceneUserPrompt(contract.Segment{ID: "seg02", Key: "k", Narration: "n"},
		voiceMeta{}, "", "", contract.Canvas{W: 1080, H: 1920, Aspect: "9:16"},
		"", "", "", "", "", false, "", paraNote(p, "seg02"))
	if !strings.Contains(got, "- 段落定位：第 2/2 段落「训练阶段」·段内第 1/2 段") {
		t.Fatalf("提示词缺段落定位行")
	}
	tail := got[strings.Index(got, "## 段信息"):]
	if strings.Index(tail, "- 段落定位") > strings.Index(tail, "- 旁白") {
		t.Fatalf("段落定位行应紧跟标题、在旁白之前（结构信息前置）")
	}
	empty := sceneUserPrompt(contract.Segment{ID: "seg01", Key: "k", Narration: "n"},
		voiceMeta{}, "", "", contract.Canvas{W: 1080, H: 1920, Aspect: "9:16"},
		"", "", "", "", "", false, "", "")
	if strings.Contains(empty, "段落定位") {
		t.Fatalf("空注不应加段落定位行（老项目提示词保持旧样）")
	}
}
