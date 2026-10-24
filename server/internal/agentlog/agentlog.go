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
	"encoding/json"
	"os"
	"strings"

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
	// 增量落账基线（中-4）：上次 Flush 时的累计值，差值进下一行 agent.usage。
	flushedP      int
	flushedC      int
	flushedCached int
	flushedCalls  int
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

// flushThreshold 增量落账阈值（2026-10-23 中-4）：未落账增量超过这么多 token
// 就 Flush 一次——进程死亡（重启/崩溃）时已烧的钱最多丢一个阈值档。
const flushThreshold = 20000

// Add 累加一次模型调用的用量（宿主在循环外自管调用时用；ReAct 循环内的由
// 循环累计、Done 直接收总量，不走这里）。未落账增量过阈值自动 Flush——
// 长任务（②风格工位一轮 60 万级）进程死亡时不再整段丢账。
func (t *Task) Add(u openai.Usage) {
	t.prompt += u.PromptTokens
	t.compl += u.CompletionTokens
	t.cached += cachedOf(u)
	t.calls++
	if (t.prompt-t.flushedP)+(t.compl-t.flushedC) >= flushThreshold {
		t.Flush()
	}
}

// Flush 增量落账：把上次落账以来的用量差值写一行 manifest（agent.usage）。
// 差值记账：任务正常收尾时 agent.done 有全量，ProjectSpend 会跳过它的增量行，
// 不重复计数；进程死亡的任务没有 done 行，增量行合计即真实消耗。
func (t *Task) Flush() {
	dP, dC := t.prompt-t.flushedP, t.compl-t.flushedC
	if dP+dC <= 0 {
		return
	}
	t.p.ManifestFields("agent.usage", "增量落账", map[string]any{
		"task":              t.task,
		"prompt_tokens":     dP,
		"completion_tokens": dC,
		"cached_tokens":     t.cached - t.flushedCached,
		"calls":             t.calls - t.flushedCalls,
	})
	t.flushedP, t.flushedC, t.flushedCached, t.flushedCalls = t.prompt, t.compl, t.cached, t.calls
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

// ProjectSpend 从 manifest.jsonl 的账行汇总项目历史用量——预算闸
// （llm.Account）的播种数据：重启/重跑不归零，超限判定看项目全生命周期。
// 两类账行：agent.done（任务终局全量，逐行累加——同任务重试各自一行，
// 各自是独立尝试的真实消耗）+ agent.usage（增量行，2026-10-23 中-4）。
// 增量行只对「没有任何 done 行的任务」生效：有 done 的任务其增量已被 done
// 全量覆盖，再加就重复计数；没 done 的任务 = 进程死亡，增量合计才是真实消耗。
// 读不到/解析不了的行跳过（账是旁路，不许它挡主链路）。
func ProjectSpend(p *pipeline.Project) (prompt, completion, calls int) {
	b, err := os.ReadFile(p.Artifact("manifest.jsonl"))
	if err != nil {
		return 0, 0, 0
	}
	type usageRow struct {
		Event         string `json:"event"`
		Task          string `json:"task"`
		PromptTokens  int    `json:"prompt_tokens"`
		CompletionTok int    `json:"completion_tokens"`
		Calls         int    `json:"calls"`
	}
	doneTasks := map[string]bool{}
	orphan := map[string]*usageRow{}
	for _, line := range strings.Split(string(b), "\n") {
		var e usageRow
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		switch e.Event {
		case "agent.done":
			doneTasks[e.Task] = true
			prompt += e.PromptTokens
			completion += e.CompletionTok
			calls += e.Calls
		case "agent.usage":
			o := orphan[e.Task]
			if o == nil {
				o = &usageRow{Task: e.Task}
				orphan[e.Task] = o
			}
			o.PromptTokens += e.PromptTokens
			o.CompletionTok += e.CompletionTok
			o.Calls += e.Calls
		}
	}
	for task, o := range orphan {
		if doneTasks[task] {
			continue
		}
		prompt += o.PromptTokens
		completion += o.CompletionTok
		calls += o.Calls
	}
	return prompt, completion, calls
}
