package prep

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/pipeline"
	"webpage-video-agent/server/internal/react"
)

// mockProv 脚本化假模型：按序返回预设消息（工具调用/纯文本），耗尽报错。
type mockProv struct {
	scripted []openai.ChatCompletionMessage
	i        int
	seen     []int // 每轮收到的消息数（验证循环有在追加消息）
}

func (m *mockProv) Chat(_ context.Context, msgs []openai.ChatCompletionMessage, _ []openai.Tool) (openai.ChatCompletionMessage, openai.Usage, error) {
	m.seen = append(m.seen, len(msgs))
	if m.i >= len(m.scripted) {
		return openai.ChatCompletionMessage{}, openai.Usage{}, errors.New("脚本耗尽")
	}
	msg := m.scripted[m.i]
	m.i++
	return msg, openai.Usage{}, nil
}

func toolCall(name, args string) openai.ChatCompletionMessage {
	return openai.ChatCompletionMessage{
		ToolCalls: []openai.ToolCall{{
			ID: "call-1", Type: openai.ToolTypeFunction,
			Function: openai.FunctionCall{Name: name, Arguments: args},
		}},
	}
}

func testProject(t *testing.T) *pipeline.Project {
	t.Helper()
	dir := t.TempDir()
	p := pipeline.NewProject(dir, "p1")
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Artifact("project.json"), []byte(`{"id":"p1","name":"测试","topic":"蚊子为什么嗡嗡叫"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestSubmitOrDieValidScript 纯文本回复被 nudge 后，合法 submit 落盘即出口。
func TestSubmitOrDieValidScript(t *testing.T) {
	p := testProject(t)
	prov := &mockProv{scripted: []openai.ChatCompletionMessage{
		{Content: "我直接把稿子给你：第一句。"}, // 光说不做 → nudge
		toolCall("submit_script", `{"text":"蚊子为什么嗡嗡叫？其实那不是叫声。翅膀每秒能扇六百次。","fact_sheet":[{"claim":"翅膀每秒约600次","url":"https://example.com/a"}]}`),
	}}
	r := &scriptRun{p: p, d: Deps{}}
	loop := &Loop{
		Prov: prov, System: "s", Kickoff: "k",
		Tools: r.tools(), Submit: "submit_script", MaxRounds: scriptBudget,
	}
	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("循环失败: %v", err)
	}
	if _, err := LoadScript(p); err != nil {
		t.Fatalf("工件 A 未落盘: %v", err)
	}
	if _, err := os.Stat(p.Artifact("manuscripts/manuscript.json")); err != nil {
		t.Fatalf("legacy manuscript 未写: %v", err)
	}
	if len(prov.seen) < 2 {
		t.Fatalf("纯文本轮没被 nudge（只跑了 %d 轮）", len(prov.seen))
	}
}

// TestSubmitOrDieInvalidScript 校验拒绝回喂为 observation，修复发生在循环内。
func TestSubmitOrDieInvalidScript(t *testing.T) {
	p := testProject(t)
	prov := &mockProv{scripted: []openai.ChatCompletionMessage{
		toolCall("submit_script", `{"text":"# 标题\n正文"}`), // 结构标记 → 拒
		toolCall("submit_script", `{"text":"正文只有一句不完整"}`),
	}}
	r := &scriptRun{p: p, d: Deps{}}
	loop := &Loop{
		Prov: prov, System: "s", Kickoff: "k",
		Tools: r.tools(), Submit: "submit_script", MaxRounds: scriptBudget,
	}
	if err := loop.Run(context.Background()); err != nil {
		t.Fatalf("修复轮未走到合法提交: %v", err)
	}
	s, err := LoadScript(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Sentences) != 1 {
		t.Errorf("落盘句数 = %d, want 1", len(s.Sentences))
	}
}

// TestBudgetExhausted 预算烧完仍未提交 = 本步失败。
func TestBudgetExhausted(t *testing.T) {
	prov := &mockProv{scripted: []openai.ChatCompletionMessage{
		{Content: "让我再想想"}, {Content: "再想想"},
	}}
	loop := &Loop{
		Prov: prov, System: "s", Kickoff: "k",
		Tools: []Tool{{
			Def: toolSchema("noop", "", nil),
			Run: func(context.Context, string) Observation { return Observation{Text: "ok"} },
		}},
		Submit: "submit_never", MaxRounds: 2,
	}
	if err := loop.Run(context.Background()); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("err = %v, want ErrBudgetExhausted", err)
	}
}

// TestScriptSubmitRequiresFacts 用过检索未带 fact_sheet 被拒。
func TestScriptSubmitRequiresFacts(t *testing.T) {
	p := testProject(t)
	r := &scriptRun{p: p, d: Deps{}, used: true}
	obs := r.submit(`{"text":"正常正文。第二句。"}`, 120)
	if obs.Done {
		t.Fatalf("用过检索却不带 fact_sheet 应被拒: %s", obs.Text)
	}
	obs = r.submit(`{"text":"正常正文。第二句。","fact_sheet":[{"claim":"数字六百","url":"https://x.cn/a"}]}`, 120)
	if !obs.Done {
		t.Fatalf("带全 fact_sheet 应通过: %s", obs.Text)
	}
}

// TestLoopToolFollowUpImages 多模态观察以 user 附图消息跟随（协议安全通道）。
func TestLoopToolFollowUpImages(t *testing.T) {
	var counts []int
	prov := &captureProv{onChat: func(n int) { counts = append(counts, n) }}
	fakeImg := "data:image/png;base64,iVBORw0KGgo="
	prov.scripted = []openai.ChatCompletionMessage{
		toolCall("render_x", `{}`),
		toolCall("submit_never", `{}`),
	}
	loop := &Loop{
		Prov: prov, System: "s", Kickoff: "k",
		Tools: []Tool{
			{
				Def: toolSchema("render_x", "", nil),
				Run: func(context.Context, string) Observation {
					return Observation{Text: "渲染好了", Images: []string{fakeImg}}
				},
			},
			{
				Def: toolSchema("submit_never", "", nil),
				Run: func(context.Context, string) Observation { return Observation{Text: "not done"} },
			},
		},
		Submit: "submit_never", MaxRounds: 3,
	}
	_ = loop.Run(context.Background())
	// 第 2 轮消息 = 2(初) + 1(assistant+call) + 1(tool) + 1(附图 user) = 5
	if len(counts) < 2 || counts[1] != 5 {
		t.Fatalf("各轮消息数 = %v, want counts[1]==5（附图 user 消息未跟随）", counts)
	}
}

type captureProv struct {
	scripted []openai.ChatCompletionMessage
	i        int
	onChat   func(nMsgs int)
}

func (m *captureProv) Chat(_ context.Context, msgs []openai.ChatCompletionMessage, _ []openai.Tool) (openai.ChatCompletionMessage, openai.Usage, error) {
	if m.onChat != nil {
		m.onChat(len(msgs))
	}
	if m.i >= len(m.scripted) {
		return openai.ChatCompletionMessage{}, openai.Usage{}, errors.New("脚本耗尽")
	}
	msg := m.scripted[m.i]
	m.i++
	return msg, openai.Usage{}, nil
}

var _ react.ChatClient = (*mockProv)(nil)
var _ = filepath.Join // 占位引用（测试文件统一 import 面）

// TestNormalizeStyleArgs P2 宽容归一：数字字重/布尔叠层/"Npx" 圆角就地转换并出说明。
func TestNormalizeStyleArgs(t *testing.T) {
	in := `{"design_system":{"fonts":{"display":{"stack":"x","weight":700},"body":{"stack":"y","weight":"400"}},
"layouts":[{"id":"l1","layered":true},{"id":"l2","layered":false}],
"skins":[{"name":"s","slots":{"bg":"#111111","txt":"#eeeeee","accent":"#888888","font":"雅黑","radius":"12px","border_w":"2px"}}]}}`
	out, notes := normalizeStyleArgs(in)
	if len(notes) != 5 { // display.weight + 两处 layered + radius + border_w
		t.Fatalf("应归一 5 处，实得 %d：%v", len(notes), notes)
	}
	var m struct {
		DesignSystem struct {
			Fonts struct {
				Display struct {
					Weight string `json:"weight"`
				} `json:"display"`
			} `json:"fonts"`
			Layouts []struct {
				ID      string `json:"id"`
				Layered string `json:"layered"`
			} `json:"layouts"`
			Skins []struct {
				Slots struct {
					Radius  int `json:"radius"`
					BorderW int `json:"border_w"`
				} `json:"slots"`
			} `json:"skins"`
		} `json:"design_system"`
	}
	if err := decodeArgs(out, &m); err != nil {
		t.Fatalf("归一后应可解析: %v", err)
	}
	if m.DesignSystem.Fonts.Display.Weight != "700" {
		t.Fatalf("weight 应转字符串 \"700\"，实得 %q", m.DesignSystem.Fonts.Display.Weight)
	}
	if m.DesignSystem.Layouts[0].Layered != "yes" || m.DesignSystem.Layouts[1].Layered != "" {
		t.Fatalf("layered 应为 \"yes\"/空，实得 %q / %q", m.DesignSystem.Layouts[0].Layered, m.DesignSystem.Layouts[1].Layered)
	}
	if m.DesignSystem.Skins[0].Slots.Radius != 12 || m.DesignSystem.Skins[0].Slots.BorderW != 2 {
		t.Fatalf("radius/border_w 应转数字，实得 %d / %d", m.DesignSystem.Skins[0].Slots.Radius, m.DesignSystem.Skins[0].Slots.BorderW)
	}
	if _, changed := normalizeStyleArgs(`{"design_system":{"direction":"干净"}}`); changed != nil {
		t.Fatalf("无需归一应原样返回，实得说明 %v", changed)
	}
}

// TestDecodeArgsTypeMessage P2 报错人话：字段路径+期望类型+收到什么。
func TestDecodeArgsTypeMessage(t *testing.T) {
	var in struct {
		Weight string `json:"weight"`
	}
	err := decodeArgs(`{"weight":700}`, &in)
	if err == nil || !strings.Contains(err.Error(), "weight") || !strings.Contains(err.Error(), "字符串") || !strings.Contains(err.Error(), "数字") {
		t.Fatalf("类型错应报字段+期望+实收，实得: %v", err)
	}
}
