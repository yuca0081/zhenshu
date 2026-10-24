// Package prep：准备阶段三工位（docs/prep-agents-design.md）——
//
//	① 文稿（纯文本模型）：研究 + 口语化改写 → 工件 A 文字稿
//	② 风格（多模态模型）：风格库探索 + 样张 → 工件 B 风格包
//	③ 分镜（纯文本模型）：段落 + 切段 + 场景规划 → 工件 C 分镜稿
//
// 循环框架（§5）：三个独立会话链式传工件（信号走工件，噪音留在过程里）；
// submit-or-die——每个工位唯一合法出口是它的 submit_* 工具，校验通过=落盘指纹，
// 不通过=错误清单作为 observation 回流，修复发生在循环内；回合预算是框架配置
// （①≤4 ②≤12 ③≤10），超限=本步失败+记账+至多一次冷重试。预算不进提示词。
// Go 拥有循环：派发、追加消息、计数、切断；manifest 记录每次 action/observation，
// 会话日志 llm/prep-*.jsonl 供 phase 级回放。
package prep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/llm"

	"webpage-video-agent/server/internal/react"
)

// Observation 工具执行结果。Text 回喂模型；Images 非空时以「系统附图」user 消息
// 跟随观察回喂（多模态自检——工具消息里塞图片块端点行为不可靠，user 消息是实测可靠通道）；
// Done=true 仅 submit 工具在落盘成功后置位，是循环唯一合法出口。
type Observation struct {
	Text   string
	Images []string
	Done   bool
}

// Tool 单个工位工具：schema + 执行体。门禁内化在执行体（预算帽/状态判断）——
// 准备工位的工具彼此独立，没有主管那种跨工具状态机。
type Tool struct {
	Def openai.Tool
	Run func(ctx context.Context, args string) Observation
}

// ErrBudgetExhausted 回合预算耗尽仍未提交（调用方记账 + 至多一次冷重试）。
var ErrBudgetExhausted = errors.New("回合预算耗尽仍未提交")

// Sink 对话留痕接收端（*llm.Transcript 隐式满足）。可空：nil 照跑不留痕。
type Sink interface {
	Message(msg openai.ChatCompletionMessage)
	Note(format string, a ...any)
}

// Loop 单工位一轮 ReAct 会话。每次 run 新建（工具闭包状态、消息序列都是会话私有）。
type Loop struct {
	Prov      react.ChatClient
	System    string
	Kickoff   string
	Tools     []Tool
	Submit    string // submit 工具名（唯一合法出口）
	MaxRounds int    // 回合预算（一次模型调用 = 一回合；nudge 也占回合，防无限光说不做）
	// OnStep 每步记账钩子（kind: action|observation|text|note）。nil = 只跑不记。
	OnStep func(round int, kind, detail string)
	// OnCall 每次模型调用返回后的用量钩子（成功失败都给——失败的调用有没有
	// 用量由端点定，有就记）。宿主接 agentlog.Task.Add 做增量落账（中-4）。
	OnCall func(u openai.Usage)
	// Rec 对话留痕（可空）：开场消息与每轮模型消息/工具观察逐条追加。
	Rec Sink
	// Usage/Calls 本轮累计 token 用量与模型调用次数（Run 返回后宿主读）。
	Usage openai.Usage
	Calls int
}

// Run 跑到 submit 落盘成功，或预算耗尽 / 模型调用失败。
func (l *Loop) Run(ctx context.Context) error {
	msgs := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: l.System},
		{Role: openai.ChatMessageRoleUser, Content: l.Kickoff},
	}
	l.record(msgs...)
	schemas := make([]openai.Tool, 0, len(l.Tools))
	byName := map[string]int{}
	for _, t := range l.Tools {
		if _, dup := byName[t.Def.Function.Name]; dup {
			l.Tools[byName[t.Def.Function.Name]] = t // 重名后注册者胜出（与 react.Core 同口径）
		} else {
			byName[t.Def.Function.Name] = len(schemas)
			schemas = append(schemas, t.Def)
		}
	}
	for round := 1; round <= l.MaxRounds; round++ {
		msg, u, err := l.Prov.Chat(ctx, msgs, schemas)
		llm.AccumulateUsage(&l.Usage, u)
		l.Calls++
		if l.OnCall != nil {
			l.OnCall(u) // 逐调用用量钩子（宿主做增量落账——中-4：进程死亡不再丢账）
		}
		if err != nil {
			l.recNote("模型调用失败（第 %d 回合）: %v", round, err)
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if len(msg.ToolCalls) == 0 {
			// 光说不做：文本回复不落盘，提醒后继续（回合照计，烧完预算自然失败）
			l.step(round, "text", msg.Content)
			nudge := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: fmt.Sprintf("文本回复不会落盘。任务完成的唯一方式是调用 %s 工具；继续。", l.Submit)}
			l.record(msg, nudge)
			msgs = append(msgs, msg, nudge)
			continue
		}
		l.record(msg)
		msgs = append(msgs, msg)
		for _, call := range msg.ToolCalls {
			l.step(round, "action", call.Function.Name+" "+compactJSON(call.Function.Arguments))
			obs := l.exec(ctx, call.Function.Name, call.Function.Arguments)
			l.step(round, "observation", obs.Text)
			tool := openai.ChatCompletionMessage{
				Role: openai.ChatMessageRoleTool, Content: obs.Text,
				ToolCallID: call.ID, Name: call.Function.Name,
			}
			msgs = append(msgs, tool)
			l.record(tool)
			if len(obs.Images) > 0 {
				im := imageFollowUp(obs.Images)
				msgs = append(msgs, im)
				l.record(im)
			}
			if call.Function.Name == l.Submit && obs.Done {
				return nil
			}
		}
	}
	return ErrBudgetExhausted
}

func (l *Loop) record(ms ...openai.ChatCompletionMessage) {
	if l.Rec == nil {
		return
	}
	for _, m := range ms {
		l.Rec.Message(m)
	}
}

func (l *Loop) recNote(format string, a ...any) {
	if l.Rec != nil {
		l.Rec.Note(format, a...)
	}
}

// exec 未知工具回喂人话（不记钩子——乱点名不污染案卷，与 react.Core 同口径）。
func (l *Loop) exec(ctx context.Context, name, args string) Observation {
	for i := range l.Tools {
		if l.Tools[i].Def.Function.Name == name {
			return l.Tools[i].Run(ctx, args)
		}
	}
	return Observation{Text: "未知工具 " + name + "（可用：" + strings.Join(l.toolNames(), "、") + "）"}
}

func (l *Loop) toolNames() []string {
	names := make([]string, 0, len(l.Tools))
	for _, t := range l.Tools {
		names = append(names, t.Def.Function.Name)
	}
	return names
}

func (l *Loop) step(round int, kind, detail string) {
	if l.OnStep != nil {
		l.OnStep(round, kind, detail)
	}
}

// imageFollowUp 多模态附图：user 角色 + image_url 块（GenerateJSONVision 同一通道，实测可靠）。
func imageFollowUp(images []string) openai.ChatCompletionMessage {
	parts := []openai.ChatMessagePart{{
		Type: openai.ChatMessagePartTypeText, Text: "［系统附图］上面工具产出的渲染结果，供你检查。",
	}}
	for _, u := range images {
		parts = append(parts, openai.ChatMessagePart{
			Type:     openai.ChatMessagePartTypeImageURL,
			ImageURL: &openai.ChatMessageImageURL{URL: u},
		})
	}
	return openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, MultiContent: parts}
}

// compactJSON 参数串规整成一行（记账可读）；非 JSON 原样截断。
func compactJSON(args string) string {
	var m map[string]any
	if json.Unmarshal([]byte(args), &m) == nil {
		b, _ := json.Marshal(m)
		return string(b)
	}
	return args
}

// decodeArgs 工具参数解析（失败返回可回喂的人话错误）。Go 原生 unmarshal 文案
// 模型难自查——实测 submit_style 连败 4 轮烧掉 4 个回合的教训：类型错报「字段
// 路径+期望类型+收到什么」，语法错明说去看引号逗号。
func decodeArgs(args string, out any) error {
	err := json.Unmarshal([]byte(args), out)
	if err == nil {
		return nil
	}
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		where := te.Field
		if where == "" {
			where = "顶层参数"
		}
		return fmt.Errorf("参数类型不对：字段 %s 需要%s，实际收到的是%s——按类型改好后重交", where, humanJSONType(te.Type), humanJSONVal(te.Value))
	}
	return fmt.Errorf("参数不是合法 JSON（多半是引号/逗号/括号问题）：%v", err)
}

// humanJSONVal JSON 侧收到的值类型名（UnmarshalTypeError.Value）→ 人话。
func humanJSONVal(v string) string {
	switch v {
	case "number":
		return "数字"
	case "string":
		return "字符串"
	case "boolean":
		return "布尔 true/false"
	case "array":
		return "数组"
	case "object":
		return "对象"
	case "null":
		return "null"
	}
	return v
}

// humanJSONType reflect 类型 → 人话类型名（回喂模型用）。
func humanJSONType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "字符串"
	case reflect.Bool:
		return "布尔 true/false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "数字"
	case reflect.Slice, reflect.Array:
		return "数组"
	case reflect.Struct, reflect.Map:
		return "对象"
	}
	return t.String()
}
