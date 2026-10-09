package styleref

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webpage-video-agent/server/internal/contract"
	"webpage-video-agent/server/internal/stylelib"
)

func testLib(t *testing.T) *stylelib.Library {
	t.Helper()
	return stylelib.New(t.TempDir())
}

// 打标作业必须带三重纪律：只收公认风格名词、特色写机制不写物件、题材只进 subject。
// 历史事故：城市宣传片解析出的条目连样张带描述全是城市物件，套到题材不同的新片上
// 整片画出城市元素——v3 特色清单是同样的口子，机制化写作是纪律线。
func TestSynthesizePromptDiscipline(t *testing.T) {
	p := synthesizeUserPrompt(testLib(t), "城市宣传片", videoFeatures{
		DurationS: 60, SpanS: [2]float64{0, 60}, NShots: 8,
		Frames: []featureFrame{{Path: `C:\x\frame-0.8.png`, T: 0.8}},
	}, "帧0: 主体居中", false, "")
	for _, want := range []string{
		"只收有公认视觉定义的风格名词", // 收录标准（§4.2）
		"拒收「高级感",          // 空话拒收
		"mechanism 写机制不写物件", // 特色纪律
		"换一个题材照做仍然成立",     // 机制化写作的判据
		"题材只进 subject",      // 题材隔离章节
		"除 subject 外任何字段禁写题材词", // 硬边界
		"风格词表",                 // 工具「查风格词表」注入
		"申请新词",                 // 工具「申请新词」
		"证据帧只能从这里挑",            // 帧坐标系
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("打标作业缺纪律 %q\n---\n%s", want, p)
		}
	}
	if strings.Contains(p, "原视频直读") {
		t.Fatalf("纯帧路径不该出现视频直读章节:\n%s", p)
	}
}

// 视频直读路径（PUBLIC_BASE_URL 配置后）：运镜/转场以视频实测为准，
// 颜色/构图仍钉死在测得事实——视频是时间维度的证据源，不是全盘放飞的许可。
func TestSynthesizePromptVideoDirectRead(t *testing.T) {
	p := synthesizeUserPrompt(testLib(t), "宣传片", videoFeatures{
		DurationS: 30, SpanS: [2]float64{0, 30}, NShots: 4,
	}, "帧0: 主体居中", true, "")
	for _, want := range []string{
		"原视频直读",            // 视频附随声明
		"**以视频实测为准**",      // 运镜/转场证据源切换
		"颜色仍以测得色板 hex 为准", // 颜色不随视频偏色漂移
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("带视频作业缺 %q\n---\n%s", want, p)
		}
	}
}

// 重喂轮的错误说明必须拼进作业（1 轮 + 结构校验失败重喂 ≤2 轮的回路；
// 「上一次输出被拒」标题由 synthesize/retag 调用方包装）。
func TestSynthesizePromptRetryNote(t *testing.T) {
	note := "\n\n## 上一次输出被拒（修正后重出）\ntags 数量 4 不合法"
	p := synthesizeUserPrompt(testLib(t), "x", videoFeatures{}, "", false, note)
	if !strings.Contains(p, "上一次输出被拒") || !strings.Contains(p, "tags 数量 4 不合法") {
		t.Fatalf("重喂说明未注入:\n%s", p)
	}
}

// subject 字段从 LLM 输出解码贯通（taggerOutput → StyleDraft）。
func TestTaggerSubjectDecoded(t *testing.T) {
	var d taggerOutput
	if err := json.Unmarshal([]byte(`{"subject":"等距视角的城市宣传动画","tags":[{"id":"iso","note":"x"}]}`), &d); err != nil {
		t.Fatal(err)
	}
	if d.Subject != "等距视角的城市宣传动画" {
		t.Fatalf("subject 未解码: %+v", d)
	}
}

// validateStyle：帧引用实存校验 + 特色缺证据帧打回（§4.1 结构自检，不带 LLM）。
func TestValidateStyleFrames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"frame-0.8.png", "frame-2.5.png"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	feats := videoFeatures{Frames: []featureFrame{
		{Path: filepath.Join(dir, "frame-0.8.png"), T: 0.8},
		{Path: filepath.Join(dir, "frame-2.5.png"), T: 2.5},
	}}
	good := &StyleDraft{
		Tags: contract.StyleTags{Main: "iso"},
		TagEvidence: []contract.TagEvidence{
			{Tag: "iso", Note: "指认", Frames: []string{"frame-0.8.png"}},
		},
		Signatures: []contract.Signature{
			{ID: "sig1", Kind: contract.SigKindPalette, Name: "撞色", Mechanism: "双色对撞", Frames: []string{"frame-2.5.png"}},
		},
	}
	if errs := validateStyle(good, feats); len(errs) > 0 {
		t.Fatalf("合格草稿被打回: %v", errs)
	}
	bad := &StyleDraft{
		Tags: contract.StyleTags{Main: "iso"},
		TagEvidence: []contract.TagEvidence{
			{Tag: "iso", Note: "指认", Frames: []string{"frame-99.png"}}, // 不存在的帧
		},
		Signatures: []contract.Signature{
			{ID: "sig1", Kind: contract.SigKindTransition, Name: "递接", Mechanism: "前段物件推出"}, // 缺证据帧
		},
	}
	errs := validateStyle(bad, feats)
	if len(errs) != 2 {
		t.Fatalf("应打回 2 处（坏帧引用+特色缺帧），得到 %d: %v", len(errs), errs)
	}
}
