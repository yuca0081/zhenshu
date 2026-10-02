package llm

// httptest 假 MCP 端点验证握手/解析/错误映射。真端点烧套餐 MCP 每月额度，测试不打真端点。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newFakeMCP 返回按 JSON-RPC method 分流的假端点（响应一律 SSE 帧 + 会话头，与真端点一致）。
func newFakeMCP(t *testing.T, callResult string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-test")
			w.WriteHeader(http.StatusOK)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Name != "web_search_prime" {
				http.Error(w, "bad tool", http.StatusBadRequest)
				return
			}
			if save := r.Context().Value(saveKey); save != nil { // 供用例断言参数
				save.(chan map[string]any) <- p.Arguments
			}
			msg := map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": callResult}},
				},
			}
			b, _ := json.Marshal(msg)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: " + string(b) + "\n"))
		}
	}))
}

type saveKeyType int

const saveKey saveKeyType = 0

func TestWebSearchMCPHandshakeAndParse(t *testing.T) {
	// 真端点返回的是「再包一层 JSON 字符串的结果数组」，用例固定复现这一形态
	wrapped, _ := json.Marshal([]SearchResult{
		{Title: "Go 1.26 released", Content: "Go 1.26 arrived", Link: "https://go.dev", Media: "go.dev", PublishDate: "2026-08"},
	})
	doubleEncoded, _ := json.Marshal(string(wrapped))

	argsCh := make(chan map[string]any, 1)
	srv := newFakeMCP(t, string(doubleEncoded))
	srv.Config.Handler = withSave(srv.Config.Handler, argsCh)
	defer srv.Close()
	t.Setenv("SEARCH_MCP_URL", srv.URL)
	t.Setenv("SEARCH_API_KEY", "test-key")

	rs, err := WebSearch(context.Background(), "Go latest version", "oneMonth", "cn")
	if err != nil {
		t.Fatalf("WebSearch: %v", err)
	}
	if len(rs) != 1 || rs[0].Title != "Go 1.26 released" || rs[0].Link != "https://go.dev" {
		t.Errorf("解析结果不符: %+v", rs)
	}
	got := <-argsCh
	if got["search_query"] != "Go latest version" || got["search_recency_filter"] != "oneMonth" || got["location"] != "cn" {
		t.Errorf("tools/call 参数不符: %v", got)
	}
}

func TestWebSearchDirectArrayAlsoAccepted(t *testing.T) {
	direct, _ := json.Marshal([]SearchResult{{Title: "t", Link: "l"}})
	srv := newFakeMCP(t, string(direct))
	defer srv.Close()
	t.Setenv("SEARCH_MCP_URL", srv.URL)
	t.Setenv("SEARCH_API_KEY", "k")

	rs, err := WebSearch(context.Background(), "q", "", "")
	if err != nil || len(rs) != 1 || rs[0].Title != "t" {
		t.Fatalf("直接数组形态应兼容: rs=%+v err=%v", rs, err)
	}
}

func TestWebSearchArrearsReadable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "您的账户已欠费，请充值后重试。", http.StatusInternalServerError)
	}))
	defer srv.Close()
	t.Setenv("SEARCH_MCP_URL", srv.URL)
	t.Setenv("SEARCH_API_KEY", "k")

	_, err := WebSearch(context.Background(), "q", "", "")
	if err == nil || !strings.Contains(err.Error(), "欠费") || !strings.Contains(err.Error(), "open.bigmodel.cn") {
		t.Fatalf("欠费应转可读提示，got %v", err)
	}
}

func TestWebSearchMissingKey(t *testing.T) {
	t.Setenv("SEARCH_API_KEY", "")
	t.Setenv("LLM_API_KEY", "")
	if _, err := WebSearch(context.Background(), "q", "", ""); err == nil || !strings.Contains(err.Error(), "未配置") {
		t.Fatalf("缺 key 应报未配置，got %v", err)
	}
}

// withSave 把 tools/call 的 arguments 复制进 channel，供用例断言请求参数。
func withSave(next http.Handler, ch chan map[string]any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), saveKey, ch)))
	})
}
