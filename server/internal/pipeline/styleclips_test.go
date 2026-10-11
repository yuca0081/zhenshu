package pipeline

import (
	"os"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
)

// TestClipHTMLPlausible 机械校验三分支：形状完整放行；缺时间轴注册/含外链/超长拒绝。
func TestClipHTMLPlausible(t *testing.T) {
	good := "<template>" + strings.Repeat(`<div class="clip" data-start="0">占位内容凑长度</div>`, 8) +
		`<div data-composition-id="clip-0"><script>window.__timelines["clip-0"]=1</script></div></template>`
	if !clipHTMLPlausible(good) {
		t.Fatal("形状完整的片段应放行")
	}
	noTl := strings.Replace(good, "__timelines", "timelines", 1)
	if clipHTMLPlausible(noTl) {
		t.Fatal("缺 __timelines 应拒绝")
	}
	withLink := strings.Replace(good, `data-composition-id="clip-0"`,
		`data-composition-id="clip-0" data-x="https://cdn.example.com/x.js"`, 1)
	if !strings.Contains(withLink, "https://") {
		t.Fatal("测试构造失效：外链没注进去")
	}
	if clipHTMLPlausible(withLink) {
		t.Fatal("含外链应拒绝")
	}
	if clipHTMLPlausible("太短") {
		t.Fatal("过短应拒绝")
	}
	if clipHTMLPlausible(strings.Repeat("长", 80001)) {
		t.Fatal("超长应拒绝")
	}
}

// TestClipNarration 截句规则：取首句、去句读、超长截断加省略号；无分镜给空；
// desc 点名展示段时取该段旁白（画面/文案才对得上），没点名退回样张下标。
func TestClipNarration(t *testing.T) {
	sb := &contract.Storyboard{Segments: []contract.Segment{
		{ID: "seg01", Narration: "蜂巢为什么是六边形？因为它最省蜡。\n第二行不该出现"},
		{ID: "seg02", Narration: strings.Repeat("长", 40)},
		{ID: "seg06", Narration: "裁判所成立段的旁白。第二句"},
	}}
	if got := clipNarration(sb, 0, ""); got != "蜂巢为什么是六边形" {
		t.Fatalf("首句截取错: %q", got)
	}
	if got := clipNarration(sb, 1, ""); len([]rune(got)) != 32+2 { // 32 rune + ……
		t.Fatalf("超长截断错: %d rune %q", len([]rune(got)), got)
	}
	if got := clipNarration(sb, 2, "展示 seg06 裁判所成立章首页"); got != "裁判所成立段的旁白" {
		t.Fatalf("desc 点名段应取该段旁白: %q", got)
	}
	if got := clipNarration(sb, 0, "没有段引用的描述"); got != "蜂巢为什么是六边形" {
		t.Fatalf("desc 无段引用应退回样张下标: %q", got)
	}
	if got := clipNarration(nil, 0, ""); got != "" {
		t.Fatalf("无分镜应给空: %q", got)
	}
	// 样张下标越界收拢到最后一段
	if got := clipNarration(sb, 99, ""); got == "" {
		t.Fatal("越界应收拢到最后一段而非空")
	}
}

// TestProjectCanvas project.json 画幅 → CanvasFor 口径（缺省 16:9）。
func TestProjectCanvas(t *testing.T) {
	dir := t.TempDir()
	p := &Project{ID: "t", Dir: dir}
	if cv := projectCanvas(p); cv.Aspect != "16:9" || cv.W != 1920 {
		t.Fatalf("缺省应为 16:9: %+v", cv)
	}
	if err := os.WriteFile(p.Artifact("project.json"), []byte(`{"aspect":"9:16"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if cv := projectCanvas(p); cv.Aspect != "9:16" || cv.H != 1920 {
		t.Fatalf("9:16 应解析: %+v", cv)
	}
}

// TestClipUserPrompt 提示词自检：契约件/画幅/清单条数/设计系统注入都在。
func TestClipUserPrompt(t *testing.T) {
	ss := &contract.StyleSamples{
		Direction: "测试方向",
		Samples:   []contract.Sample{{Tag: "样张 A", Desc: "标题帧"}, {Tag: "样张 B", Desc: "数据页"}},
		Layouts:   []contract.DesignLayout{{ID: "l1", Name: "大字海报", Desc: "主体居中"}},
	}
	sb := &contract.Storyboard{Segments: []contract.Segment{{Narration: "第一段的旁白。后面还有"}, {Narration: "第二段"}}}
	prompt := clipUserPrompt(ss, sb, []int{0, 1}, contract.CanvasFor("9:16"))
	for _, want := range []string{
		"clip-0", "clip-1", "1080", "1920", "样张 A", "样张 B", "大字海报",
		"第一段的旁白", "data-composition-id", "__timelines", "测试方向",
		"标签本身不带任何属性", // 2026-10-10 实测模型把 id 写上 <template> 渲染必炸——契约必须点名
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("提示词缺 %q", want)
		}
	}
}

// TestNormalizeClipHTML 模型把组合物属性误写在 <template> 开标签上时搬正到内部
// 首个元素；形状本来对的原样返回。2026-10-10 三条小样全炸的根因。
func TestNormalizeClipHTML(t *testing.T) {
	bad := "<template data-composition-id='clip-0' data-width='1920' data-height='1080'>\n" +
		"<div id='root' class='skin-root'>x</div></template>"
	got := normalizeClipHTML(bad, "clip-0")
	if strings.Contains(got, "<template data-composition-id") {
		t.Fatalf("template 上的 id 没摘掉: %s", got)
	}
	if !strings.Contains(got, "<div data-composition-id=\"clip-0\" data-width=\"1920\" data-height=\"1080\" id='root'") {
		t.Fatalf("内部首元素没挂上组合物属性: %s", got)
	}
	good := "<template>\n<div data-composition-id=\"clip-1\" data-width=\"1080\">x</div></template>"
	if got := normalizeClipHTML(good, "clip-1"); got != good {
		t.Fatalf("形状本来对的原样返回: %s", got)
	}
	// template 后是注释（找不到元素开标签）→ 原样返回不误伤
	commented := "<template data-composition-id='clip-2'><!-- hi --></template>"
	if got := normalizeClipHTML(commented, "clip-2"); got != commented {
		t.Fatalf("无可挂元素应原样返回: %s", got)
	}
}

// TestEnsureSampleClipsNoLLM 无 LLM 环境：返回 false、写 style.clips.error 事实，不动样张。
func TestEnsureSampleClipsNoLLM(t *testing.T) {
	old := rootDir
	SetRootDir("../../..")
	defer SetRootDir(old)
	t.Setenv("LLM_API_KEY", "") // FromEnv 无 key 即不可用（双名都掐，防环境真 key 漏进来打真调用）
	t.Setenv("DEEPSEEK_API_KEY", "")
	dir := t.TempDir()
	p := &Project{ID: "t", Dir: dir}
	ss := &contract.StyleSamples{Samples: []contract.Sample{{Tag: "A", HTML: "x"}}}
	if EnsureSampleClips(p, ss, nil) {
		t.Fatal("无 LLM 应返回 false")
	}
	if ss.Samples[0].Video != "" || ss.Samples[0].ClipNote != "" {
		t.Fatal("无 LLM 不应改样张字段")
	}
	// manifest 落了 style.clips.error 事实
	b, err := os.ReadFile(p.Artifact("manifest.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "style.clips.error") {
		t.Fatalf("manifest 缺 style.clips.error: %s", b)
	}
}

// TestEnsureSampleClipsIdempotent 已有 mp4 的条目跳过（不再烧 LLM）。
func TestEnsureSampleClipsIdempotent(t *testing.T) {
	dir := t.TempDir()
	p := &Project{ID: "t", Dir: dir}
	ss := &contract.StyleSamples{Samples: []contract.Sample{{Tag: "A", Video: "style/clips/clip-0.mp4"}}}
	if EnsureSampleClips(p, ss, nil) {
		t.Fatal("全量有 mp4 应幂等返回 false")
	}
}
