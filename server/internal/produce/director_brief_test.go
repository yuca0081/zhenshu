package produce

// director_brief_test.go —— 准备工位分镜稿（结构化 brief）的邻段承诺注入
//（prep-agents-design §4 / #10）：无导演字段时 brief 在场仍注入，内容取自相邻段
// brief 的末拍/首拍；老项目（无导演字段无 brief）保持零注入。
import (
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
)

func segWithBrief(id string, idx int, scene, first, last string) contract.Segment {
	return contract.Segment{
		ID: id, Idx: idx, Key: "k" + id, Narration: "旁白。",
		VisualBrief:  scene + "\n首拍：" + first + "\n末拍：" + last,
		DurationHint: 8,
		Brief:        &contract.VisualBriefSpec{Scene: scene, FirstFrame: first, LastFrame: last},
	}
}

func TestDirectorNoteNeighborPromises(t *testing.T) {
	sb := &contract.Storyboard{TimingBasis: "narration", Segments: []contract.Segment{
		segWithBrief("seg01", 1, "场景一", "从零起手", "定格在城门"),
		segWithBrief("seg02", 2, "场景二", "接城门推近", "收在星空"),
		segWithBrief("seg03", 3, "场景三", "新场景显影", "收黑"),
	}}
	// 中段：注入上段末拍 + 下段首拍
	note := directorNoteFor(sb, sb.Segments[1])
	if !strings.Contains(note, "邻段承诺") {
		t.Fatalf("brief 在场却无邻段承诺块:\n%s", note)
	}
	if !strings.Contains(note, "定格在城门") || !strings.Contains(note, "新场景显影") {
		t.Errorf("邻段承诺未带相邻段末拍/首拍:\n%s", note)
	}
	if strings.Contains(note, "导演统筹") {
		t.Errorf("无导演字段不应有导演统筹头:\n%s", note)
	}
	// 老项目（无导演字段无 brief）：零注入
	old := &contract.Storyboard{TimingBasis: "narration", Segments: []contract.Segment{
		{ID: "seg01", Idx: 1, Key: "k", Narration: "旁白。", VisualBrief: "旧画面", DurationHint: 8},
	}}
	if got := directorNoteFor(old, old.Segments[0]); got != "" {
		t.Errorf("老项目应零注入，得到:\n%s", got)
	}
}
