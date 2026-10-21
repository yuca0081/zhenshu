package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

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

// 缓存重放不重复记账（2026-10-21 修复）：同一请求第二次走磁盘缓存，账面只涨
// 一次、Usage 清零返回；已超限的账号命中缓存仍放行（免费的不再拦）；只有真正
// 出网的新请求才被预算闸拒。
func TestCacheReplayNotDoubleBilled(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"ok\":1}"}}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`))
	}))
	defer srv.Close()
	p := &Provider{Name: "t", BaseURL: srv.URL, APIKey: "k", Model: "m"}

	// 第一笔：出网记账 110；第二笔同请求：命中缓存，账面不涨、Usage 归零
	a := NewAccount(0, 0, 0, 1000)
	ctx := WithAccount(context.Background(), a)
	var out map[string]any
	if _, err := p.GenerateJSON(ctx, "sys", "replay-body", &out); err != nil {
		t.Fatal(err)
	}
	u2, err := p.GenerateJSON(ctx, "sys", "replay-body", &out)
	if err != nil {
		t.Fatal(err)
	}
	if u2.PromptTokens != 0 || u2.CompletionTokens != 0 {
		t.Fatalf("缓存命中应清零 Usage，实际 %+v", u2)
	}
	if pr, co, n := a.Total(); pr != 100 || co != 10 || n != 1 {
		t.Fatalf("缓存重放被重复记账: prompt=%d compl=%d calls=%d", pr, co, n)
	}

	// 已超限的账号：同请求命中缓存照常成功（免费），新请求才被拒
	over := WithAccount(context.Background(), NewAccount(99999, 0, 1, 100))
	if _, err := p.GenerateJSON(over, "sys", "replay-body", &out); err != nil {
		t.Fatalf("超限后免费缓存命中应放行: %v", err)
	}
	if _, err := p.GenerateJSON(over, "sys", "fresh-body", &out); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("超限后的新请求应被拒: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("全程应只出网 1 次，实际 %d", hits.Load())
	}
}

// 单飞等待者不记账（2026-10-21 修复）：N 路并发同请求只有 owner 出网并记账一次。
func TestSingleflightWaitersNotBilled(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		time.Sleep(150 * time.Millisecond) // 留出并发窗口
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"ok\":1}"}}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`))
	}))
	defer srv.Close()
	p := &Provider{Name: "t", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	a := NewAccount(0, 0, 0, 1000)
	ctx := WithAccount(context.Background(), a)
	const n = 4
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			var out map[string]any
			_, err := p.GenerateJSON(ctx, "sys", "sf-bill", &out)
			errs <- err
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("并发调用失败: %v", err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("单飞应只出网 1 次，实际 %d", hits.Load())
	}
	if pr, co, calls := a.Total(); pr != 100 || co != 10 || calls != 1 {
		t.Fatalf("等待者也被记账: prompt=%d compl=%d calls=%d", pr, co, calls)
	}
}
