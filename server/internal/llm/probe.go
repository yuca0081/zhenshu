package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Probe 连通性探针（e2e-4min 手册的 8-token 做法）：一次极小 chat 调用当场验证
// URL/Key/模型三元组。不走磁盘缓存/并发闸/重试退避——管理员手点的诊断活，
// 要的就是一次真实往返；失败原因原样带回（含上游状态码与响应体摘录）。
func Probe(ctx context.Context, baseURL, apiKey, model string) (time.Duration, string, error) {
	body, err := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "连通性探针：请只回复 ok"}},
		"max_tokens": 8,
	})
	if err != nil {
		return 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimSuffix(baseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := httpClient.Do(req)
	if err != nil {
		return time.Since(start), "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return time.Since(start), "", err
	}
	if resp.StatusCode != http.StatusOK {
		return time.Since(start), "", &StatusError{Code: resp.StatusCode, Body: string(b)}
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(b, &out); err != nil || len(out.Choices) == 0 {
		return time.Since(start), "", err
	}
	return time.Since(start), out.Choices[0].Message.Content, nil
}

// EnvKey .env 的兜底 Key（LLM_API_KEY，旧名 DEEPSEEK_API_KEY）。探针/视图用。
func EnvKey() string {
	if v := os.Getenv("LLM_API_KEY"); v != "" {
		return v
	}
	return os.Getenv("DEEPSEEK_API_KEY")
}

// EnvFallbacks 显式值留空时的 env/内置默认兜底（base/model 都保证非空）。
// keySet 单独回：探针没 key 时好报「配个 Key」而不是拿空 key 去撞 401。
func EnvFallbacks() (baseURL, model string, keySet bool) {
	baseURL = getenvOrOld("LLM_BASE_URL", "DEEPSEEK_BASE_URL")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	model = getenvOrOld("LLM_MODEL", "DEEPSEEK_MODEL")
	if model == "" {
		model = defaultModel
	}
	return baseURL, model, EnvKey() != ""
}

func getenvOrOld(newName, oldName string) string {
	if v := os.Getenv(newName); v != "" {
		return v
	}
	return os.Getenv(oldName)
}
