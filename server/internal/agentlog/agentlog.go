// Package agentlog —— 智能体任务的统一记账+留痕封皮（「过程」页的数据源）。
//
// 每个智能体任务（准备三工位/写画面/段审/边界审/终审/预审/机检/翻译）在开工时
// Begin 一次、收尾时 Done 一次：
//
//	at := agentlog.Begin(p, "seg.audit.seg03.r1", "段审 seg03（第 1 轮）")
//	loop.Rec = at.Rec()                       // ReAct 循环逐条喂对话
//	...干活...
//	at.Done(usage, calls, err)                // token 用量 + 状态落账
//
// 账走 manifest（agent.start/agent.done 两行，带 task/label/token 字段），
// 对话留痕走 llm/transcripts/<task>.jsonl。全部旁路：留痕/记账失败不挡主链路。
package agentlog

import (
	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
)

// Task 一个智能体任务的记账句柄。
type Task struct {
	p      *pipeline.Project
	task   string
	rec    *llm.Transcript
	prompt int
	compl  int
	cached int
	calls  int
}

// Begin 开工：开留痕文件 + manifest 记 agent.start。task 必须过 llm.ValidTask。
func Begin(p *pipeline.Project, task, label string) *Task {
	t := &Task{p: p, task: task}
	if rec := llm.OpenTranscript(p.Artifact("llm/transcripts"), task); rec != nil {
		t.rec = rec
	}
	extra := map[string]any{"task": task, "label": label}
	if t.rec != nil {
		extra["transcript"] = t.rec.Rel
	}
	p.ManifestFields("agent.start", label, extra)
	return t
}

// Rec 对话留痕接收端（挂到 ReAct 循环的 Rec 字段；未开出来时为 nil，循环需容 nil）。
func (t *Task) Rec() *llm.Transcript { return t.rec }

// Add 累加一次模型调用的用量（宿主在循环外自管调用时用；ReAct 循环内的由
// 循环累计、Done 直接收总量，不走这里）。
func (t *Task) Add(u openai.Usage) {
	t.prompt += u.PromptTokens
	t.compl += u.CompletionTokens
	t.cached += cachedOf(u)
	t.calls++
}

// cachedOf 平台前缀缓存命中（GLM usage.prompt_tokens_details.cached_tokens；
// 端点不回该字段时为 0——账上见 0 即「未上报」，别当「零命中」解读）。
func cachedOf(u openai.Usage) int {
	if u.PromptTokensDetails != nil {
		return u.PromptTokensDetails.CachedTokens
	}
	return 0
}

// Done 收尾：manifest 记 agent.done（状态 + token 总量 + 留痕路径）并关留痕文件。
// err 非空 = 任务失败（fail）；留痕里由调用方自行 Note 失败原因。
func (t *Task) Done(err error) {
	state, detail := "ok", "完成"
	if err != nil {
		state, detail = "fail", pipeline.FirstLine(err.Error(), 160)
	}
	extra := map[string]any{
		"task": t.task, "state": state,
		"prompt_tokens": t.prompt, "completion_tokens": t.compl, "calls": t.calls,
		"cached_tokens": t.cached,
	}
	if t.rec != nil {
		extra["transcript"] = t.rec.Rel
	}
	t.p.ManifestFields("agent.done", detail, extra)
	t.rec.Close()
}

// Total 把循环累计的总量灌进来（ReAct 循环宿主的收口方式：Done 前调一次）。
func (t *Task) Total(u openai.Usage, calls int) {
	t.prompt += u.PromptTokens
	t.compl += u.CompletionTokens
	t.cached += cachedOf(u)
	t.calls += calls
}
