// Package review：制作阶段审核工位（docs/production-stage-design.md 工作项⑯/⑮）——
//
//	一个判卷型 ReAct agent、三种调用档案（段审 / 边界审 / 全片终审）：
//	多模态、温度 ≈0、回合预算小、submit_verdict 唯一出口（submit-or-die）。
//	机制不进提示词：预算、缓存、schema 校验、打回判定、刹车判定都在框架侧；
//	LLM 只做判卷（取证 → 意见单）。
//
// 循环骨架照准备阶段三工位（server/internal/prep）的惯例落地：程序装配开场材料，
// 工具观察人话回流，校验失败错误清单回流循环内自修。与 prep 的差异：审核的
// submit 载荷是结构化意见单，程序按权威 schema + 跨字段规则校验。
package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

// Observation 工具执行结果（与 prep.Observation 同形）：Text 回喂模型；
// Images 非空以「系统附图」user 消息跟随观察（工具消息塞图端点不可靠，
// user 消息是实测可靠通道）；Done 仅 submit 工具校验通过后置位。
type Observation struct {
	Text   string
	Images []string
	Done   bool
}

// Tool 单个审核工具。
type Tool struct {
	Def openai.Tool
	Run func(ctx context.Context, args string) Observation
}

// ErrBudgetExhausted 回合预算耗尽仍未提交（调用方记账；审核不冷重试——
// 判卷预算烧尽说明取证收敛失败，按审查器故障降级）。
var ErrBudgetExhausted = errors.New("回合预算耗尽仍未提交")

// ChatClient 循环对模型的全部依赖：带温度的对话轮（判卷低温）。*llm.Provider 隐式满足。
type ChatClient interface {
	ChatTemp(ctx context.Context, msgs []openai.ChatCompletionMessage, tools []openai.Tool, temp float64) (openai.ChatCompletionMessage, openai.Usage, error)
}

// Sink 对话留痕接收端（*llm.Transcript 隐式满足）。可空：nil 照跑不留痕。
type Sink interface {
	Message(msg openai.ChatCompletionMessage)
	Note(format string, a ...any)
}

// Loop 单次审核会话。与 prep.Loop 同款 submit-or-die：无 ToolCalls 的文本回复
// 不落盘、nudge 后继续（回合照计）。
type Loop struct {
	Prov      ChatClient
	System    string
	Kickoff   string
	KickoffIm []string // 开场注入的图片（锚点帧/样张等，程序装配——跟 kickoff 同一条 user 消息）
	Tools     []Tool
	Submit    string
	MaxRounds int
	Temp      float64 // 判卷低温（≈0）
	LogPath   string  // 取证留痕 jsonl（非空时自动接 OnTool）
	// OnStep 每步记账（kind: action|observation|text）；OnTool 取证留痕（工具+参数+结果指纹由调用方算）。
	OnStep func(round int, kind, detail string)
	OnTool func(name, args, result string)
	// Rec 对话留痕（可空）；Usage/Calls 本轮累计（Run 返回后宿主读）。
	Rec Sink
	// TaskID/TaskLabel 非空时由宿主侧统一记账（runAudit 负责 agent.start/done）。
	TaskID    string
	TaskLabel string
	Usage     openai.Usage
	Calls     int
}

// Run 跑到 submit 校验通过，或预算耗尽 / 模型调用失败。
func (l *Loop) Run(ctx context.Context) error {
	kickParts := []openai.ChatMessagePart{{Type: openai.ChatMessagePartTypeText, Text: l.Kickoff}}
	for _, u := range l.KickoffIm {
		kickParts = append(kickParts, openai.ChatMessagePart{
			Type:     openai.ChatMessagePartTypeImageURL,
			ImageURL: &openai.ChatMessageImageURL{URL: u},
		})
	}
	kick := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser}
	if len(l.KickoffIm) > 0 {
		kick.MultiContent = kickParts // kickParts[0] 即 kickoff 正文；Content 与 MultiContent 互斥（openai 库序列化硬约束）
	} else {
		kick.Content = l.Kickoff
	}
	msgs := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: l.System},
		kick,
	}
	l.record(msgs...)
	schemas := make([]openai.Tool, 0, len(l.Tools))
	byName := map[string]int{}
	for _, t := range l.Tools {
		if _, dup := byName[t.Def.Function.Name]; dup {
			l.Tools[byName[t.Def.Function.Name]] = t
		} else {
			byName[t.Def.Function.Name] = len(schemas)
			schemas = append(schemas, t.Def)
		}
	}
	for round := 1; round <= l.MaxRounds; round++ {
		msg, u, err := l.Prov.ChatTemp(ctx, msgs, schemas, l.Temp)
		l.Usage.PromptTokens += u.PromptTokens
		l.Usage.CompletionTokens += u.CompletionTokens
		l.Calls++
		if err != nil {
			l.recNote("模型调用失败（第 %d 回合）: %v", round, err)
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if len(msg.ToolCalls) == 0 {
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
			if l.OnTool != nil {
				l.OnTool(call.Function.Name, call.Function.Arguments, obs.Text)
			}
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

func (l *Loop) exec(ctx context.Context, name, args string) Observation {
	for i := range l.Tools {
		if l.Tools[i].Def.Function.Name == name {
			return l.Tools[i].Run(ctx, args)
		}
	}
	names := make([]string, 0, len(l.Tools))
	for _, t := range l.Tools {
		names = append(names, t.Def.Function.Name)
	}
	return Observation{Text: "未知工具 " + name + "（可用：" + strings.Join(names, "、") + "）"}
}

func (l *Loop) step(round int, kind, detail string) {
	if l.OnStep != nil {
		l.OnStep(round, kind, detail)
	}
}

// imageFollowUp 多模态附图（与 prep 同通道）。
func imageFollowUp(images []string) openai.ChatCompletionMessage {
	parts := []openai.ChatMessagePart{{
		Type: openai.ChatMessagePartTypeText, Text: "［系统附图］上面工具取回的画面证据，供你判卷。",
	}}
	for _, u := range images {
		parts = append(parts, openai.ChatMessagePart{
			Type:     openai.ChatMessagePartTypeImageURL,
			ImageURL: &openai.ChatMessageImageURL{URL: u},
		})
	}
	return openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, MultiContent: parts}
}

func compactJSON(args string) string {
	var m map[string]any
	if json.Unmarshal([]byte(args), &m) == nil {
		b, _ := json.Marshal(m)
		return string(b)
	}
	return args
}

// decodeArgs 工具参数解析（人话错误回流；与 prep.decodeArgs 同款口径）。
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

// toolSchema 工具定义构造（结构化字段格式进 parameters，不进提示词）。
func toolSchema(name, desc string, params map[string]any) openai.Tool {
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
		Name: name, Description: desc, Parameters: params,
	}}
}
