package pipeline

// para 真片重拼验证（方案甲 §4.4 验证路径）：把指定项目的 transition 置 para，
// 重跑 concatSegs + stageStitch——纯程序零 token，段片不动。默认跳过，显式指定：
//
//	PARA_VERIFY=p20261023-110234 go test ./internal/pipeline -run TestParaVerifyRealFilm -v -timeout 30m
//
// 产出：renders/main_voice.mp4 / main.mp4 重拼为段落制分层转场版；
// timeline.json 写实际段起点。会覆盖原片——跑前备份 main_voice.mp4/main.mp4。
import (
	"os"
	"path/filepath"
	"testing"
)

func TestParaVerifyRealFilm(t *testing.T) {
	id := os.Getenv("PARA_VERIFY")
	if id == "" {
		t.Skip("未指定 PARA_VERIFY=<项目ID>，跳过真片重拼")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	SetRootDir(root)
	SetDataDir(filepath.Join(root, "data"))
	EnsureFFmpeg()
	p := NewProject(filepath.Join(root, "data"), id)
	sb, err := p.LoadStoryboard()
	if err != nil {
		t.Fatalf("storyboard 加载失败: %v", err)
	}
	if _, err := os.Stat(p.Artifact("renders/segs/" + sb.Segments[0].ID + ".mp4")); err != nil {
		t.Fatalf("段片不在（先回源或重渲）: %v", err)
	}
	if err := SetProjectField(p, "transition", "para"); err != nil {
		t.Fatalf("置 transition=para 失败: %v", err)
	}
	if err := concatSegs(p, sb, p.Artifact("renders/segs")); err != nil {
		t.Fatalf("concatSegs(para): %v", err)
	}
	if err := stageStitch(p); err != nil {
		t.Fatalf("stageStitch: %v", err)
	}
	tl := LoadTimeline(p)
	if len(tl) != len(sb.Segments) {
		t.Fatalf("timeline 行数 %d != 段数 %d", len(tl), len(sb.Segments))
	}
	// 总长 = Σ段片 − Σ窗口；每边界起点 = Σ_{k<i}d − Σ_{j<i}D（相对裸累计单调收缩）
	edges := paraEdges(p, sb)
	cum, sumD := 0.0, 0.0
	for i, seg := range sb.Segments {
		d := probeDuration(filepath.Join(p.Artifact("renders/segs"), seg.ID+".mp4"))
		if d <= 0 {
			t.Fatalf("段片 %s 时长探测失败", seg.ID)
		}
		if i > 0 {
			sumD += edges[i-1].Dur
		}
		if diff := tl[i].Start - (cum - sumD); diff > 0.05 || diff < -0.05 {
			t.Fatalf("%s 起点 %.3f 与 Σd−ΣD=%.3f 不符", seg.ID, tl[i].Start, cum-sumD)
		}
		cum += d
	}
	voice := probeDuration(p.Artifact("renders/main_voice.mp4"))
	if diff := voice - (cum - sumD); diff > 0.5 || diff < -0.5 {
		t.Fatalf("重拼总长 %.2fs 与期望 %.2fs 差超 0.5s", voice, cum-sumD)
	}
	t.Logf("para 重拼过：%d 段，裸总长 %.2fs → 成片 %.2fs（收缩 %.2fs）",
		len(sb.Segments), cum, voice, cum-voice)
}
