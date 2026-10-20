package review

// loop_token_test.go —— 审核 Loop 省 token 三件套的行为单测（全部 mock，不碰网络）：
// 取证图历史裁剪（保留窗）、思考档位注入（effortClient 探测降级）、取证预算帽。

import (
	"context"
	"errors"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/llm"
)

// scriptedProv 按剧本回话的 mock：每次调用弹出一条预设消息，并记下收到的完整上下文。
// 单 goroutine 循环，无需加锁；剧本耗尽按错误浮出（测试断言调用次数时立刻暴露）。
type scriptedProv struct {
	script []openai.ChatCompletionMessage
	calls  [][]openai.ChatCompletionMessage // 每次调用收到的 msgs 快照
	plain  int                              // ChatTemp 调用次数
}

func (p *scriptedProv) pop(msgs []openai.ChatCompletionMessage) (openai.ChatCompletionMessage, error) {
	p.calls = append(p.calls, append([]openai.ChatCompletionMessage(nil), msgs...))
	if len(p.script) == 0 {
		return openai.ChatCompletionMessage{}, errors.New("剧本耗尽")
	}
	m := p.script[0]
	p.script = p.script[1:]
	return m, nil
}

func (p *scriptedProv) ChatTemp(ctx context.Context, msgs []openai.ChatCompletionMessage, tools []openai.Tool, temp float64) (openai.ChatCompletionMessage, openai.Usage, error) {
	p.plain++
	m, err := p.pop(msgs)
	return m, openai.Usage{}, err
}

// effortProv 在剧本 mock 上加档位通道：记录每次 ChatTempEffort 收到的档位。
type effortProv struct {
	scriptedProv
	efforts []llm.Effort
}

func (p *effortProv) ChatTempEffort(ctx context.Context, msgs []openai.ChatCompletionMessage, tools []openai.Tool, temp float64, e llm.Effort) (openai.ChatCompletionMessage, openai.Usage, error) {
	p.efforts = append(p.efforts, e)
	m, err := p.pop(msgs)
	return m, openai.Usage{}, err
}

// callMsg 一条带单个工具调用的 assistant 消息。
func callMsg(name, argsJSON, id string) openai.ChatCompletionMessage {
	return openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant,
		ToolCalls: []openai.ToolCall{{
			ID: id, Type: openai.ToolTypeFunction,
			Function: openai.FunctionCall{Name: name, Arguments: argsJSON},
		}},
	}
}

// countEvidenceImgs 统计上下文里「带图的用户消息 / 已裁占位消息」条数。
func countEvidenceImgs(msgs []openai.ChatCompletionMessage) (live, trimmed int) {
	for _, m := range msgs {
		if m.Role != openai.ChatMessageRoleUser {
			continue
		}
		if m.MultiContent != nil {
			for _, p := range m.MultiContent {
				if p.Type == openai.ChatMessagePartTypeImageURL {
					live++
					break
				}
			}
		} else if strings.Contains(m.Content, "已撤") {
			trimmed++
		}
	}
	return live, trimmed
}

// okVerdict 全 pass 意见单（过 profSegment 的 Validate，让 submit 一发入魂）。
const okVerdict = `{"dims":{"content":"pass","fact":"pass","style":"pass","first_last":"pass","device":"NA","subtitle":"pass"},"findings":[]}`

func submitTool() Tool {
	return Tool{Def: toolSchema("submit_verdict", "交卷", nil), Run: func(_ context.Context, args string) Observation {
		var v Verdict
		if err := decodeArgs(args, &v); err != nil {
			return Observation{Text: err.Error()}
		}
		if err := v.Validate(profSegment, nil); err != nil {
			return Observation{Text: "被拒：" + err.Error()}
		}
		return Observation{Text: "已收下", Done: true}
	}}
}

// TestLoopTrimsStaleEvidenceImages 保留窗=2：第 1 轮取的图活到第 3 轮请求，
// 第 4 轮起被换成占位；窗口内的图原样保留。
func TestLoopTrimsStaleEvidenceImages(t *testing.T) {
	t.Setenv("AUDIT_IMG_KEEP_ROUNDS", "2")
	prov := &scriptedProv{script: []openai.ChatCompletionMessage{
		callMsg("get_frame", `{"seg":"seg01","t":1}`, "c1"),
		callMsg("get_frame", `{"seg":"seg01","t":2}`, "c2"),
		callMsg("get_frame", `{"seg":"seg01","t":3}`, "c3"),
		callMsg("get_frame", `{"seg":"seg01","t":4}`, "c4"),
		callMsg("submit_verdict", okVerdict, "c5"),
	}}
	frame := Tool{Def: toolSchema("get_frame", "取帧", nil), Run: func(context.Context, string) Observation {
		return Observation{Text: "已取回帧", Images: []string{"data:image/jpeg;base64,AAAA"}}
	}}
	l := Loop{
		Prov: prov, System: "sys", Kickoff: "kick",
		Tools: []Tool{frame, submitTool()}, Submit: "submit_verdict",
		MaxRounds: 6, Temp: 0.1,
	}
	if err := l.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(prov.calls) != 5 {
		t.Fatalf("调用 %d 次，want 5", len(prov.calls))
	}
	// 第 3 轮请求（cut=0）：第 1 轮的图还在 → 2 live 0 trimmed
	if live, trimmed := countEvidenceImgs(prov.calls[2]); live != 2 || trimmed != 0 {
		t.Fatalf("r3: live=%d trimmed=%d，want 2/0", live, trimmed)
	}
	// 第 4 轮请求（cut=1）：第 1 轮图被撤，第 2/3 轮的还在
	if live, trimmed := countEvidenceImgs(prov.calls[3]); live != 2 || trimmed != 1 {
		t.Fatalf("r4: live=%d trimmed=%d，want 2/1", live, trimmed)
	}
	// 第 5 轮请求（cut=2）：第 1/2 轮图被撤，第 3/4 轮的还在
	if live, trimmed := countEvidenceImgs(prov.calls[4]); live != 2 || trimmed != 2 {
		t.Fatalf("r5: live=%d trimmed=%d，want 2/2", live, trimmed)
	}
	// 单调：第 4 轮被裁的消息在第 5 轮请求里保持已裁态（裁了不复原，前缀可续）
	live5, _ := countEvidenceImgs(prov.calls[4])
	if live5 != 2 {
		t.Fatalf("r5 live=%d，裁剪必须单调不复原", live5)
	}
}

// TestLoopKeepRoundsZero 窗口 0：取证图只活当轮，下一轮请求立即换占位。
func TestLoopKeepRoundsZero(t *testing.T) {
	t.Setenv("AUDIT_IMG_KEEP_ROUNDS", "0")
	prov := &scriptedProv{script: []openai.ChatCompletionMessage{
		callMsg("get_frame", `{"seg":"seg01","t":1}`, "c1"),
		callMsg("get_frame", `{"seg":"seg01","t":2}`, "c2"),
		callMsg("submit_verdict", okVerdict, "c3"),
	}}
	frame := Tool{Def: toolSchema("get_frame", "取帧", nil), Run: func(context.Context, string) Observation {
		return Observation{Text: "已取回帧", Images: []string{"data:image/jpeg;base64,AAAA"}}
	}}
	l := Loop{Prov: prov, System: "sys", Kickoff: "kick",
		Tools: []Tool{frame, submitTool()}, Submit: "submit_verdict", MaxRounds: 4, Temp: 0.1}
	if err := l.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 第 2 轮请求（cut=1）：第 1 轮图已被撤
	if live, trimmed := countEvidenceImgs(prov.calls[1]); live != 0 || trimmed != 1 {
		t.Fatalf("r2: live=%d trimmed=%d，want 0/1", live, trimmed)
	}
}

// TestLoopEffortDispatch 档位注入：底层支持 ChatTempEffort 时走它且档位如实；
// 只实现 ChatTemp 的 mock 降级不炸。
func TestLoopEffortDispatch(t *testing.T) {
	t.Run("effort path", func(t *testing.T) {
		prov := &effortProv{scriptedProv: scriptedProv{script: []openai.ChatCompletionMessage{
			callMsg("submit_verdict", okVerdict, "c1"),
		}}}
		l := Loop{Prov: prov, System: "sys", Kickoff: "kick",
			Tools: []Tool{submitTool()}, Submit: "submit_verdict",
			MaxRounds: 2, Temp: 0.1, Effort: llm.EffortLow}
		if err := l.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(prov.efforts) != 1 || prov.efforts[0] != llm.EffortLow {
			t.Fatalf("efforts=%v，want [low]", prov.efforts)
		}
		if prov.plain != 0 {
			t.Fatalf("ChatTemp 被调 %d 次，want 0（应走 effort 通道）", prov.plain)
		}
	})
	t.Run("fallback to ChatTemp", func(t *testing.T) {
		prov := &scriptedProv{script: []openai.ChatCompletionMessage{
			callMsg("submit_verdict", okVerdict, "c1"),
		}}
		l := Loop{Prov: prov, System: "sys", Kickoff: "kick",
			Tools: []Tool{submitTool()}, Submit: "submit_verdict",
			MaxRounds: 2, Temp: 0.1, Effort: llm.EffortLow}
		if err := l.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if prov.plain != 1 {
			t.Fatalf("ChatTemp 被调 %d 次，want 1（降级路径）", prov.plain)
		}
	})
	t.Run("empty effort uses ChatTemp", func(t *testing.T) {
		prov := &scriptedProv{script: []openai.ChatCompletionMessage{
			callMsg("submit_verdict", okVerdict, "c1"),
		}}
		l := Loop{Prov: prov, System: "sys", Kickoff: "kick",
			Tools: []Tool{submitTool()}, Submit: "submit_verdict", MaxRounds: 2, Temp: 0.1}
		if err := l.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if prov.plain != 1 {
			t.Fatalf("ChatTemp 被调 %d 次，want 1", prov.plain)
		}
	})
}

// TestAuditEffortEnv 环境变量解析：默认 low，high/disabled 直通，乱值回落 low。
func TestAuditEffortEnv(t *testing.T) {
	cases := []struct {
		env  string
		want llm.Effort
	}{
		{"", llm.EffortLow},
		{"low", llm.EffortLow},
		{"high", llm.EffortHigh},
		{"HIGH", llm.EffortHigh},
		{"disabled", llm.EffortDisabled},
		{"nonsense", llm.EffortLow},
	}
	for _, c := range cases {
		t.Setenv("LLM_AUDIT_EFFORT", c.env)
		if got := auditEffort(); got != c.want {
			t.Fatalf("LLM_AUDIT_EFFORT=%q → %q，want %q", c.env, got, c.want)
		}
	}
}

// TestEvidenceBudgetCap 帽内放行、用完拒绝且回喂人话提示；两个工具共享同一计数。
func TestEvidenceBudgetCap(t *testing.T) {
	hits := 0
	base := Tool{Def: toolSchema("get_frame", "取帧", nil), Run: func(context.Context, string) Observation {
		hits++
		return Observation{Text: "ok"}
	}}
	left := 2
	frame := withEvidenceBudget(base, &left, 2)
	clip := withEvidenceBudget(base, &left, 2)
	for i := 0; i < 2; i++ {
		if obs := frame.Run(context.Background(), "{}"); obs.Text != "ok" {
			t.Fatalf("帽内第 %d 次被拒: %s", i+1, obs.Text)
		}
	}
	obs := clip.Run(context.Background(), "{}") // 共享计数：另一个工具也吃同一顶帽
	if hits != 2 {
		t.Fatalf("base 执行 %d 次，want 2（用完不再执行）", hits)
	}
	if strings.Contains(obs.Text, "ok") || !strings.Contains(obs.Text, "取证额度已用完") {
		t.Fatalf("超帽回喂不对: %q", obs.Text)
	}
}

// TestAuditImgKeepRoundsEnv 保留窗解析：默认 2，0 合法，越界/乱值回落 2。
func TestAuditImgKeepRoundsEnv(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"", 2}, {"0", 0}, {"1", 1}, {"50", 50}, {"51", 2}, {"-1", 2}, {"abc", 2},
	}
	for _, c := range cases {
		t.Setenv("AUDIT_IMG_KEEP_ROUNDS", c.env)
		if got := auditImgKeepRounds(); got != c.want {
			t.Fatalf("AUDIT_IMG_KEEP_ROUNDS=%q → %d，want %d", c.env, got, c.want)
		}
	}
}
