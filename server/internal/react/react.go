// Package react：可复用的 ReAct 循环核心（想→做→看→再想），Go 侧驱动。
// 三条铁律与主管 Agent 同源：
// ① 工具受门禁——越界调用被拒，拒绝原因回喂；
// ② 循环步数上限——超限即停，绝不无限打转；
// ③ LLM 永不直接碰副作用——只发起工具调用，宿主（Go）校验后执行，观察结果回喂。
//
// 宿主分工：本包只管循环与工具调度的骨架；工具表、系统提示词、事件播报、
// 记账归各工位自己（主管把包级工具表适配成闭包注入，撰稿人带预算帽注入）。
package react

import (
	"context"

	openai "github.com/sashabaranov/go-openai"
)

// ChatClient 循环对模型的全部依赖。*llm.Provider 隐式满足；测试用 mock 顶上。
type ChatClient interface {
	Chat(ctx context.Context, msgs []openai.ChatCompletionMessage, tools []openai.Tool) (openai.ChatCompletionMessage, openai.Usage, error)
}

// Tool 单个工具：定义 + 门禁 + 执行体。
// Gate 返回空串 = 放行；否则为拒绝原因（回喂给模型）。
// Run 收循环 ctx（宿主的轮级时限由此穿透进工具，卡死的工具不再拖穿轮超时）
// 与原始 JSON 参数串，返回人话观察结果（回喂给模型，也交 Hooks 记账）。
type Tool struct {
	Def  openai.Tool
	Gate func() string
	Run  func(ctx context.Context, argsJSON string) string
}

// Hooks 工具执行过程的宿主钩子（播报/记账），全部可空。
type Hooks struct {
	// OnCall 门禁通过、即将执行（拒绝路径不触发，与主管原 execTool 行为一致）。
	OnCall func(name, args string)
	// OnFinish 每次执行终了（被拒或执行完）：result 是将回喂模型的观察原文。
	OnFinish func(name, args, result string, rejected bool)
}

// Core 一轮 ReAct 循环的骨架。每次对话轮新建一个（工具闭包按当前上下文注入）。
type Core struct {
	Prov     ChatClient
	MaxSteps int
	Hooks    Hooks
	tools    []Tool
	byName   map[string]int
}

// DefaultMaxSteps 宿主未指定时的步数上限。
const DefaultMaxSteps = 8

// New 构造核心；maxSteps ≤0 取 DefaultMaxSteps。工具重名后注册者胜出（不致静默丢 schema）。
func New(prov ChatClient, toolList []Tool, maxSteps int, hooks Hooks) *Core {
	if maxSteps <= 0 {
		maxSteps = DefaultMaxSteps
	}
	c := &Core{Prov: prov, MaxSteps: maxSteps, Hooks: hooks, byName: make(map[string]int, len(toolList))}
	for i := range toolList {
		name := toolList[i].Def.Function.Name
		if j, dup := c.byName[name]; dup {
			c.tools[j] = toolList[i]
			continue
		}
		c.byName[name] = len(c.tools)
		c.tools = append(c.tools, toolList[i])
	}
	return c
}

// Run 跑一轮 ReAct：msgs 由宿主组装好（system/历史/最新 user 消息）传入。
// 返回模型的最终文本回复；步数用尽仍未给文本时返回空串，兜底话术由宿主定。
func (c *Core) Run(ctx context.Context, msgs []openai.ChatCompletionMessage) (string, error) {
	for step := 0; step < c.MaxSteps; step++ {
		msg, _, err := c.Prov.Chat(ctx, msgs, c.schemas())
		if err != nil {
			return "", err
		}
		if len(msg.ToolCalls) == 0 {
			return msg.Content, nil
		}
		msgs = append(msgs, msg)
		for _, call := range msg.ToolCalls {
			obs := c.exec(ctx, call.Function.Name, call.Function.Arguments)
			msgs = append(msgs, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				Content:    obs,
				ToolCallID: call.ID,
				Name:       call.Function.Name,
			})
		}
	}
	return "", nil
}

func (c *Core) schemas() []openai.Tool {
	out := make([]openai.Tool, 0, len(c.tools))
	for i := range c.tools {
		out = append(out, c.tools[i].Def)
	}
	return out
}

// WithCallBudget 给工具套调用预算帽：帽内原样执行，用完后不再执行、回喂人话提示。
// 有限资源（搜索额度等）接进自主循环前的必备护栏。
func WithCallBudget(t Tool, budget int, exhausted string) Tool {
	left := budget
	base := t.Run
	t.Run = func(ctx context.Context, args string) string {
		if left <= 0 {
			return exhausted
		}
		left--
		return base(ctx, args)
	}
	return t
}

// exec 门禁 + 执行。未知工具直接回喂不记钩子——模型乱点名不该污染宿主案卷。
func (c *Core) exec(ctx context.Context, name, args string) string {
	i, ok := c.byName[name]
	if !ok {
		return "未知工具 " + name
	}
	t := &c.tools[i]
	if reason := t.Gate(); reason != "" {
		obs := "拒绝：" + reason
		if c.Hooks.OnFinish != nil {
			c.Hooks.OnFinish(name, args, obs, true)
		}
		return obs
	}
	if c.Hooks.OnCall != nil {
		c.Hooks.OnCall(name, args)
	}
	obs := t.Run(ctx, args)
	if c.Hooks.OnFinish != nil {
		c.Hooks.OnFinish(name, args, obs, false)
	}
	return obs
}
