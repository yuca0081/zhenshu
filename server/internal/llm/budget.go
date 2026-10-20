// budget.go —— 项目级 token 预算闸（2026-10-20 成本诊断 P0-1）。
//
// 诊断账本：一次失控制作可烧 5.4M token，其中大头是「同一段被反复重跑/重审」，
// 且 429/取消/重启会整批重来。预算闸是最后一条止血带：项目累计用量（含重启前，
// 从 manifest agent.done 账行播种）超过上限后，所有 LLM 调用入口直接拒发新请求，
// 让管线如实失败可重跑，而不是无限「再修一轮」。
//
// 用法：运行入口造一个 Account（produce 的 runWithAutoRetry / prep 的 run），
// WithAccount 塞进 ctx——下游全部 LLM 调用自动过闸、自动记账，调用方零改动。
// 上限 LLM_PROJECT_TOKEN_BUDGET（默认 8_000_000；≤0 = 不限）。
package llm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"

	openai "github.com/sashabaranov/go-openai"
)

// ErrBudgetExceeded 预算耗尽哨兵错误（errors.Is 可判）；包装层应停止自动重跑。
var ErrBudgetExceeded = errors.New("项目 token 预算已耗尽")

// ProjectTokenBudget 项目 token 上限（LLM_PROJECT_TOKEN_BUDGET，默认 800 万）。
// 8M ≈ 诊断期最贵项目（5.45M）的 1.5 倍：健康项目打不满，失控项目烧不到第二轮。
// 设 0 或负数 = 关闸。
func ProjectTokenBudget() int64 {
	if v := os.Getenv("LLM_PROJECT_TOKEN_BUDGET"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return 8_000_000
}

// Account 一次运行（或一个项目生命周期）的 token 账本。并发安全。
type Account struct {
	mu     sync.Mutex
	prompt int64
	compl  int64
	calls  int64
	budget int64  // ≤0 = 不限
	trip   string // 非空 = 已超限（记触发时的账面快照）
}

// NewAccount 造账本。seed 三元组来自历史账（agentlog.ProjectSpend）——重启不归零。
func NewAccount(prompt, completion, calls, budget int64) *Account {
	a := &Account{prompt: prompt, compl: completion, calls: calls, budget: budget}
	if a.over() {
		a.trip = a.snapshot()
	}
	return a
}

func (a *Account) over() bool { return a.budget > 0 && a.prompt+a.compl >= a.budget }

func (a *Account) snapshot() string {
	return fmt.Sprintf("已用 %d token（prompt %d + completion %d，%d 次调用），上限 %d",
		a.prompt+a.compl, a.prompt, a.compl, a.calls, a.budget)
}

// Add 记一次成功调用的用量；跨过上限即置 trip（本次调用照常返回，下一次拒发）。
func (a *Account) Add(u openai.Usage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prompt += int64(u.PromptTokens)
	a.compl += int64(u.CompletionTokens)
	a.calls++
	if a.trip == "" && a.over() {
		a.trip = a.snapshot()
	}
}

// Check 未超限返回 nil；超限返回 ErrBudgetExceeded（含账面快照）。调用入口在
// 发请求前调——拒绝发生在外面，一个字节都不发给端点。
func (a *Account) Check() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.trip == "" {
		return nil
	}
	return fmt.Errorf("%w（%s）——停止制作保钱包；产物已按段保留，调整 LLM_PROJECT_TOKEN_BUDGET 后重跑", ErrBudgetExceeded, a.trip)
}

// Total 账面快照（manifest 留账/日志用）。
func (a *Account) Total() (prompt, completion, calls int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.prompt, a.compl, a.calls
}

type ctxKeyAccount struct{}

// WithAccount 把账本挂进 ctx——下游所有 LLM 调用自动过闸记账。
func WithAccount(ctx context.Context, a *Account) context.Context {
	return context.WithValue(ctx, ctxKeyAccount{}, a)
}

// AccountFrom 取 ctx 里的账本（没挂过返回 nil——调用方照常无闸运行）。
func AccountFrom(ctx context.Context) *Account {
	a, _ := ctx.Value(ctxKeyAccount{}).(*Account)
	return a
}

// budgetCheck 调用入口的过闸桩：拒发返回错误，放行返回 nil。
func budgetCheck(ctx context.Context) error {
	if a := AccountFrom(ctx); a != nil {
		return a.Check()
	}
	return nil
}

// CheckBudget 对外暴露的过闸检查：调度层在派发重活（写段/开审/开终审）前先问
// 一嘴，耗尽就直接刹车——不必等第一笔 LLM 调用被拒再被上层误译成「审查器故障」。
func CheckBudget(ctx context.Context) error { return budgetCheck(ctx) }

// budgetAdd 调用入口的记账桩。
func budgetAdd(ctx context.Context, u openai.Usage) {
	if a := AccountFrom(ctx); a != nil {
		a.Add(u)
	}
}
