package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

// Account 记账与超限：Add 累计、跨线置 trip、Check 拒发、Total 快照。
func TestAccountOverBudget(t *testing.T) {
	a := NewAccount(0, 0, 0, 100)
	if err := a.Check(); err != nil {
		t.Fatalf("空账不该超限: %v", err)
	}
	a.Add(openai.Usage{PromptTokens: 60, CompletionTokens: 30})
	if err := a.Check(); err != nil {
		t.Fatalf("90/100 不该超限: %v", err)
	}
	a.Add(openai.Usage{PromptTokens: 20})
	if err := a.Check(); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("110/100 应超限: %v", err)
	}
	p, c, n := a.Total()
	if p != 80 || c != 30 || n != 2 {
		t.Fatalf("账面不符: prompt=%d compl=%d calls=%d", p, c, n)
	}
}

// 预算不限（≤0）永不超限。
func TestAccountUnlimited(t *testing.T) {
	a := NewAccount(0, 0, 0, 0)
	for i := 0; i < 100; i++ {
		a.Add(openai.Usage{PromptTokens: 1000})
	}
	if err := a.Check(); err != nil {
		t.Fatalf("不限额不该超限: %v", err)
	}
}

// 播种即超限：重启后带着历史账超限的项目，第一笔调用就被拒。
func TestAccountSeededOver(t *testing.T) {
	a := NewAccount(500, 600, 3, 1000)
	if err := a.Check(); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("播种即超限应拒发: %v", err)
	}
}

// 预算闸挡在出网之前：超限的 ChatTemp 一个请求都不发（服务器计数必须为 0）。
func TestChatTempBudgetRefusedBeforeNetwork(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`))
	}))
	defer srv.Close()
	p := &Provider{Name: "t", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	// 播种即超账（历史 50/上限 10）：第一笔调用在出网前就被拒
	ctx := WithAccount(context.Background(), NewAccount(50, 0, 1, 10))
	_, _, err := p.ChatTemp(ctx, []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "hello"},
	}, nil, 0.1)
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("应返回预算错误: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("超限后仍发了 %d 个请求", hits.Load())
	}
}
