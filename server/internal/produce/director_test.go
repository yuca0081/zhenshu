package produce

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
)

func directorNoteStoryboard() *contract.Storyboard {
	return &contract.Storyboard{TimingBasis: "narration", Segments: []contract.Segment{
		{ID: "seg01", Idx: 1, Key: "钩子", Narration: "你此刻的念头，可能早已被写定。", VisualBrief: "粒子汇聚", DurationHint: 10,
			Beats: []contract.Beat{
				{Quote: "你此刻的念头", Expect: "粒子散点开始汇聚"},
				{Quote: "可能早已被写定", Expect: "金眼睁开的定格"},
			}},
		{ID: "seg02", Idx: 2, Key: "诞生", Narration: "一八一四年，拉普拉斯写下一段话。", VisualBrief: "文献引文", DurationHint: 12},
		{ID: "seg03", Idx: 3, Key: "终章", Narration: "而宇宙没有回答。", VisualBrief: "落版收束", DurationHint: 9},
	}}
}

// 导演统筹注入块：弧线定位/母题/邻段衔接/节拍逐块出现；首段无上文、末段无下文；
// 无导演字段的老分镜零占位。
func TestDirectorNoteFor(t *testing.T) {
	sb := directorNoteStoryboard()
	sb.Arc = &contract.Arc{Spine: "从立到破", Chapters: []contract.ArcChapter{
		{Title: "立", Mood: "挑衅", Tension: 3, Segs: []string{"seg01"}},
		{Title: "破", Mood: "收束", Tension: 4, Segs: []string{"seg02", "seg03"}},
	}}
	sb.Motifs = []contract.Motif{{Name: "全知之眼", Desc: "金色眼睛", Appearances: []contract.MotifApp{
		{SegID: "seg01", Role: "establish"}, {SegID: "seg03", Role: "echo"},
	}}}

	n1 := directorNoteFor(sb, sb.Segments[0])
	for _, want := range []string{
		"第 1 章「立」（挑衅，张力 3/5）的第 1/1 段，承担「开篇」",
		"母题「全知之眼」（金色眼睛）在本段出场：首次立起",
		"下一段（seg02）画面：文献引文", // 首段只有下文
		"「你此刻的念头」→ 粒子散点开始汇聚",
	} {
		if !strings.Contains(n1, want) {
			t.Fatalf("seg01 注入块缺 %q\n---\n%s", want, n1)
		}
	}
	if strings.Contains(n1, "上一段") {
		t.Fatal("首段不该有上一段参照")
	}

	n3 := directorNoteFor(sb, sb.Segments[2])
	for _, want := range []string{
		"第 2 章「破」（收束，张力 4/5）的第 2/2 段，承担「收尾」",
		"结尾呼应",
		"上一段（seg02）画面：文献引文",
	} {
		if !strings.Contains(n3, want) {
			t.Fatalf("seg03 注入块缺 %q\n---\n%s", want, n3)
		}
	}
	if strings.Contains(n3, "本段节拍") {
		t.Fatal("seg03 无节拍不该出现节拍表")
	}

	n2 := directorNoteFor(sb, sb.Segments[1])
	for _, want := range []string{
		"第 2 章「破」", "承担「开篇」", // 章2 的 1/2 段
		"上一段（seg01）画面：粒子汇聚",
		"下一段（seg03）画面：落版收束", // 中间段双向衔接
	} {
		if !strings.Contains(n2, want) {
			t.Fatalf("seg02 注入块缺 %q\n---\n%s", want, n2)
		}
	}

	// 老分镜：零占位
	plain := &contract.Storyboard{TimingBasis: "narration", Segments: []contract.Segment{
		{ID: "seg01", Idx: 1, Key: "k", Narration: "n", DurationHint: 9},
	}}
	if directorNoteFor(plain, plain.Segments[0]) != "" {
		t.Fatal("无导演字段应空串")
	}
}

// 指纹传导：节拍改动只影响该段指纹；无节拍保持旧格式（老项目段缓存不冲）；
// 弧线/母题改动走分镜层指纹（有导演字段才追加第三段）。
func TestDirectorFingerprints(t *testing.T) {
	seg := contract.Segment{ID: "seg01", Idx: 1, Key: "k", Narration: "n", VisualBrief: "v", DurationHint: 9}

	// 无节拍：与旧格式逐字节一致
	legacy := sha256.Sum256([]byte(seg.Key + "\x00" + seg.Narration + "\x00" + seg.VisualBrief + "\x00" + strconv.Itoa(int(seg.DurationHint))))
	if got := sceneFingerprint(seg); got != hex.EncodeToString(legacy[:8]) {
		t.Fatalf("无节拍指纹应与旧格式一致: %s", got)
	}

	// 加节拍：指纹必须变
	seg.Beats = []contract.Beat{{Quote: "n", Expect: "画面"}}
	if sceneFingerprint(seg) == hex.EncodeToString(legacy[:8]) {
		t.Fatal("节拍入键后指纹没变")
	}

	// 分镜层指纹：无导演字段空串（旁车两段格式不变）；有则 ":xxxx" 且随弧线变
	plain := &contract.Storyboard{TimingBasis: "narration", Segments: []contract.Segment{seg}}
	if directorFingerprint(plain) != "" {
		t.Fatal("无导演字段应空串")
	}
	plain.Arc = &contract.Arc{Spine: "s", Chapters: []contract.ArcChapter{{Title: "a", Tension: 1, Segs: []string{"seg01"}}}}
	fp1 := directorFingerprint(plain)
	if !strings.HasPrefix(fp1, ":") || len(fp1) != 9 {
		t.Fatalf("导演指纹格式应为 :+8hex，得到 %q", fp1)
	}
	plain.Arc.Spine = "s2"
	if fp2 := directorFingerprint(plain); fp2 == fp1 {
		t.Fatal("弧线变了分镜层指纹没变")
	}
}
