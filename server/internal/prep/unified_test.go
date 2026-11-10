package prep

// unified_test.go —— 统一准备智能体 rev2 的落盘级测试：终态咬合检查
//（unifiedGapReport）、write_* 的保存即校验+失效提醒、finish_prep 出口。

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/pipeline"
)

// unifiedFixture 一个齐装项目：三工件 + 样张 png + 统一链会话状态。
func unifiedFixture(t *testing.T) (*pipeline.Project, *unifiedRun) {
	t.Helper()
	p := testProject(t)
	if err := os.MkdirAll(p.Artifact("style"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Artifact("style/sample-0.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	u := &unifiedRun{p: p, d: Deps{}, sr: &scriptRun{p: p, d: Deps{}}, st: &styleRun{p: p, d: Deps{}}, target: 120, writes: map[string]int{}}
	return p, u
}

// unifiedScript 写工件 A（两句稿）。
func unifiedScript(t *testing.T) *contract.Script {
	t.Helper()
	return contract.NewScript("第一句在这里。第二句在这里。", nil, 120)
}

// unifiedDraft 写工件 C（两段铺满两句、带内、零角色——吃 2026-11-09 放宽的
// 「角色 0–4 可选」）。
func unifiedDraft() *contract.StoryboardDraft {
	return contract.NewStoryboardDraft("一段到底", "测试世界", "",
		[]contract.DraftParagraph{{Title: "全片", Segs: []string{"seg01"}}},
		[]contract.DraftSegment{{
			ID: "seg01", Idx: 1, Key: "开场", NarrationRefs: "N1-N2",
			Brief:        contract.VisualBrief{Scene: "场景规划", FirstFrame: "首拍", LastFrame: "末拍"},
			DurationHint: 8,
			Shot:         &contract.ShotDirection{Size: "wide", Camera: "缓推", Stage: "舞台", Action: "这镜的戏"},
		}})
}

// seedUnified 三件全落盘（齐装）。
func seedUnified(t *testing.T, p *pipeline.Project) {
	t.Helper()
	if err := writeJSONFile(scriptPath(p), unifiedScript(t)); err != nil {
		t.Fatal(err)
	}
	ds := &contract.StyleSamples{
		Direction: "测试方向", Concept: "为什么配这部片",
		Skins:     []contract.Skin{{Name: "主", Slots: contract.SkinSlots{BG: "#101010", TXT: "#EDEDED", Accent: "#FFCC00", Font: "'Microsoft YaHei',sans-serif"}, CSS: ".skin-root{}"}},
		Fonts:     &contract.FontRoles{Display: contract.FontRole{Stack: "'SimHei',sans-serif"}, Body: contract.FontRole{Stack: "'KaiTi',serif"}},
		Motion:    &contract.MotionLang{Ease: "power2.out", DurIn: 0.5, DurSettle: 0.25, Stagger: 0.08, Entrances: []contract.MotionMove{{Name: "滑入", How: "x:40→0", UseFor: "标题"}, {Name: "点亮", How: "opacity:0→1", UseFor: "正文"}, {Name: "落定", How: "scale:1.05→1", UseFor: "数据"}}, Rhythm: "每 3 秒一个事件", Exits: "淡出", Camera: "微推", Transition: "直切", Notes: "克制"},
		Device:    &contract.DeviceSpec{Name: "卡片", Desc: "什么", PerSeg: "怎么变奏"},
		EntryExit: &contract.EntryExitSpec{Entry: "晕染显影", Exit: "淡出收黑", EntryDur: 0.4},
		Samples:   []contract.Sample{{Tag: "样张 A", Desc: "测试", HTML: "<!doctype html><html></html>"}},
	}
	pack := contract.NewStylePack("test-style", "测试风格：理由", contract.EditGrammar{Density: "中", BandMin: 5, BandMax: 15, Continuity: "弱"}, ds, []string{"样张 A"})
	if err := writeJSONFile(stylePackPath(p), pack); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(draftPath(p), unifiedDraft()); err != nil {
		t.Fatal(err)
	}
}

// TestUnifiedGapReport 终态咬合检查：空项目三缺口；齐装零缺口；改稿后分镜
// 引用悬空 → 缺口现形。
func TestUnifiedGapReport(t *testing.T) {
	p, _ := unifiedFixture(t)
	gaps := unifiedGapReport(p, Deps{})
	if len(gaps) != 3 {
		t.Fatalf("空项目应报三缺口, got %v", gaps)
	}
	seedUnified(t, p)
	if gaps = unifiedGapReport(p, Deps{}); len(gaps) != 0 {
		t.Fatalf("齐装应零缺口, got %v", gaps)
	}
	// 改稿（删掉第二句）：分镜 N1-N2 引用悬空 → 不咬合
	s := unifiedScript(t)
	s.Sentences = s.Sentences[:1]
	s.Text = "第一句在这里。"
	if err := writeJSONFile(scriptPath(p), s); err != nil {
		t.Fatal(err)
	}
	if gaps = unifiedGapReport(p, Deps{}); len(gaps) == 0 || !strings.Contains(gaps[0], "N2") {
		t.Fatalf("改稿后分镜引用悬空应现形, got %v", gaps)
	}
}

// TestWriteScriptExpiryReminder write_script 的失效提醒：盘上分镜引用 N3，
// 新稿只有两句 → 随保存返回悬空清单。
func TestWriteScriptExpiryReminder(t *testing.T) {
	p, u := unifiedFixture(t)
	old := contract.NewStoryboardDraft("", "世界", "",
		[]contract.DraftParagraph{{Title: "全片", Segs: []string{"seg01", "seg02"}}},
		[]contract.DraftSegment{
			{ID: "seg01", Idx: 1, Key: "a", NarrationRefs: "N1-N2", Brief: contract.VisualBrief{Scene: "s", FirstFrame: "f", LastFrame: "l"}, DurationHint: 8},
			{ID: "seg02", Idx: 2, Key: "b", NarrationRefs: "N3", Brief: contract.VisualBrief{Scene: "s", FirstFrame: "f", LastFrame: "l"}, DurationHint: 8},
		})
	if err := writeJSONFile(draftPath(p), old); err != nil {
		t.Fatal(err)
	}
	obs := u.writeScriptTool().Run(context.Background(), `{"text":"新稿第一句。新稿第二句。"}`)
	if strings.Contains(obs.Text, "被拒") {
		t.Fatalf("合法新稿不应被拒: %s", obs.Text)
	}
	if !strings.Contains(obs.Text, "seg02 引用 N3") || !strings.Contains(obs.Text, "不咬合") {
		t.Fatalf("应带下游失效提醒: %s", obs.Text)
	}
}

// TestWriteStoryboardGates write_storyboard 的依赖门与保存即校验：
// 缺上游拒收指路；齐装后合法稿落盘（含制作契约翻译）。
func TestWriteStoryboardGates(t *testing.T) {
	p, u := unifiedFixture(t)
	tool := u.writeStoryboardTool()
	args := func() string {
		b, _ := json.Marshal(map[string]any{"draft": unifiedDraft()})
		return string(b)
	}
	if obs := tool.Run(context.Background(), args()); !strings.Contains(obs.Text, "先 write_script") {
		t.Fatalf("缺文稿应指路: %s", obs.Text)
	}
	if err := writeJSONFile(scriptPath(p), unifiedScript(t)); err != nil {
		t.Fatal(err)
	}
	if obs := tool.Run(context.Background(), args()); !strings.Contains(obs.Text, "先 write_stylepack") {
		t.Fatalf("缺风格包应指路: %s", obs.Text)
	}
	seedUnified(t, p) // 补齐风格包
	obs := tool.Run(context.Background(), args())
	if !strings.Contains(obs.Text, "已落盘") {
		t.Fatalf("齐装合法稿应落盘: %s", obs.Text)
	}
	if _, err := os.Stat(p.Artifact("storyboards/storyboard.json")); err != nil {
		t.Fatal("制作契约 storyboard.json 应随写落盘")
	}
}

// TestFinishTool finish_prep：缺口拒收；齐装放行（Done 出口）+ 行为闸复核。
func TestFinishTool(t *testing.T) {
	p, u := unifiedFixture(t)
	obs := u.finishTool().Run(context.Background(), "")
	if obs.Done || !strings.Contains(obs.Text, "未放行") {
		t.Fatalf("缺口应拒收: %s", obs.Text)
	}
	seedUnified(t, p)
	obs = u.finishTool().Run(context.Background(), "")
	if !obs.Done {
		t.Fatalf("齐装应放行: %s", obs.Text)
	}
	// 行为闸：用过检索但盘上稿无 fact_sheet → 终检拦下
	u.sr.used = true
	obs = u.finishTool().Run(context.Background(), "")
	if obs.Done || !strings.Contains(obs.Text, "fact_sheet") {
		t.Fatalf("行为闸应复核: %s", obs.Text)
	}
}

// TestUnifiedWriteCap write_* 病理上限：只计成功落盘——参数错/被拒不烧额度
// （2026-11-09 首跑教训：deepseek-flash 大 JSON 截断连败 6 次差点烧光），
// 成功 10 次后拒收并提示复盘。
func TestUnifiedWriteCap(t *testing.T) {
	p, u := unifiedFixture(t)
	tool := u.writeScriptTool()
	// 参数错×多次：不计数，之后仍可正常写
	for i := 0; i < 5; i++ {
		obs := tool.Run(context.Background(), `{bad json`)
		if !strings.Contains(obs.Text, "参数") {
			t.Fatalf("坏参数应报参数错: %s", obs.Text)
		}
	}
	for i := 0; i < unifiedWriteMax; i++ {
		obs := tool.Run(context.Background(), `{"text":"第几稿都是这两句。第二句。"}`)
		if strings.Contains(obs.Text, "病理上限") {
			t.Fatalf("第 %d 次成功写不应触顶", i+1)
		}
	}
	obs := tool.Run(context.Background(), `{"text":"超限的一稿。第二句。"}`)
	if !strings.Contains(obs.Text, "病理上限") || !strings.Contains(obs.Text, "空转") {
		t.Fatalf("超限应拒收并提示复盘: %s", obs.Text)
	}
	_ = p
}

// TestUnifiedKickoffDiskState 开工语盘面注入：空项目三缺；齐装三件在盘。
func TestUnifiedKickoffDiskState(t *testing.T) {
	p, _ := unifiedFixture(t)
	k := unifiedKickoff(p, Deps{}, nil)
	for _, want := range []string{"文稿：缺", "风格包：缺", "分镜：缺", "缺哪件补哪件"} {
		if !strings.Contains(k, want) {
			t.Fatalf("开工语缺 %q:\n%s", want, k)
		}
	}
	seedUnified(t, p)
	k = unifiedKickoff(p, Deps{}, []string{StationScript, StationStyle, StationStoryboard})
	for _, want := range []string{"文稿已落盘", "风格包已落盘", "分镜已落盘", "要求重做"} {
		if !strings.Contains(k, want) {
			t.Fatalf("齐装开工语缺 %q:\n%s", want, k)
		}
	}
}

// TestUnifiedPrepFlag 开关读档。
func TestUnifiedPrepFlag(t *testing.T) {
	p, _ := unifiedFixture(t)
	if UnifiedPrep(p) {
		t.Fatal("默认不开")
	}
	if err := pipeline.SetProjectField(p, "unified_prep", "1"); err != nil {
		t.Fatal(err)
	}
	if !UnifiedPrep(p) {
		t.Fatal("unified_prep=1 应识别")
	}
}
