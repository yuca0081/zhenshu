package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// TestMain 包级缓存目录隔离：llm 包的任何测试都不许往仓库 data/ 写缓存。
func TestMain(m *testing.M) {
	if os.Getenv("LLM_CACHE_DIR") == "" {
		dir, err := os.MkdirTemp("", "llm-cache-test")
		if err == nil {
			os.Setenv("LLM_CACHE_DIR", dir)
		}
	}
	os.Exit(m.Run())
}

// 相同请求体第二次调用命中磁盘缓存：服务器只被请求一次，两次返回一致。
func TestDiskCacheHitOnIdenticalRequest(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"ok\":1}"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	defer srv.Close()
	p := &Provider{Name: "t", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	var out1, out2 map[string]any
	if _, err := p.GenerateJSON(context.Background(), "sys", "same-body", &out1); err != nil {
		t.Fatal(err)
	}
	if _, err := p.GenerateJSON(context.Background(), "sys", "same-body", &out2); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("相同请求应命中缓存，实际出网 %d 次", hits.Load())
	}
	if out1["ok"] != out2["ok"] {
		t.Fatalf("缓存返回不一致: %v vs %v", out1, out2)
	}
	// 不同请求体照常出网
	if _, err := p.GenerateJSON(context.Background(), "sys", "different-body", &out1); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("不同请求不应命中缓存，实际出网 %d 次", hits.Load())
	}
}

// 并发相同请求走单飞：只有一份出网，全员拿到结果。
func TestSingleflightDedupsConcurrentIdentical(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		time.Sleep(150 * time.Millisecond) // 留出并发窗口
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"ok\":1}"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	defer srv.Close()
	p := &Provider{Name: "t", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	const n = 4
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			var out map[string]any
			_, err := p.GenerateJSON(context.Background(), "sys", "concurrent", &out)
			if err == nil && out["ok"] != float64(1) {
				err = errors.New("返回不符")
			}
			errs <- err
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("并发调用失败: %v", err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("单飞应只出网 1 次，实际 %d 次", got)
	}
}

// LLM_DISK_CACHE=off 关缓存：相同请求两次都出网。
func TestDiskCacheDisabledByEnv(t *testing.T) {
	t.Setenv("LLM_DISK_CACHE", "off")
	// init 已被前序测试触发过（once）；直接验证 lookup/store 为 no-op
	body := []byte(`{"x":1}`)
	cacheStore("m-off", body, []byte(`{"choices":[{"message":{"content":"y"}}]}`))
	if _, ok := cacheLookup("m-off", body); ok {
		t.Fatal("缓存已关，不应命中")
	}
}
