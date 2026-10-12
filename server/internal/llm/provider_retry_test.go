package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 401 属客户端错（key 错/欠费封禁），重试也不会好：generate 必须 1 次即失败并
// 透传状态码——此前任何错误都烧满 3 次退避重试，401 也要白等几秒还掩盖病因。
func TestGenerateNoRetryOn401(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"1002","message":"invalid api key"}}`))
	}))
	defer srv.Close()
	p := &Provider{Name: "t", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	var out map[string]any
	_, err := p.generate(context.Background(), "sys", "hi", nil, "", &out, 0.7, EffortDefault)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusUnauthorized {
		t.Fatalf("应透传 StatusError(401)，得到 %v", err)
	}
	if n != 1 {
		t.Fatalf("401 不应重试：实际请求 %d 次", n)
	}
}
