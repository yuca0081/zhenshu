package agentlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/pipeline"
)

func TestDoneWritesCachedTokens(t *testing.T) {
	// 2026-10-19 起账本带 cached_tokens（平台前缀缓存命中）——5 小时额度账
	// 要能看命中率。锁定：Add/Total 累计 details、Done 落字段、无 details 不炸。
	dir := t.TempDir()
	p := pipeline.NewProject(dir, "p-test")
	t1 := Begin(p, "probe.task", "探针")
	t1.Add(openai.Usage{PromptTokens: 100, CompletionTokens: 10,
		PromptTokensDetails: &openai.PromptTokensDetails{CachedTokens: 80}})
	t1.Add(openai.Usage{PromptTokens: 50, CompletionTokens: 5}) // 无 details（端点未上报）
	t1.Done(nil)

	b, err := os.ReadFile(filepath.Join(dir, "projects", "p-test", "manifest.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var done map[string]any
	for _, line := range splitLines(b) {
		var m map[string]any
		if json.Unmarshal(line, &m) == nil && m["event"] == "agent.done" {
			done = m
		}
	}
	if done == nil {
		t.Fatal("未落 agent.done")
	}
	if done["prompt_tokens"].(float64) != 150 || done["cached_tokens"].(float64) != 80 {
		t.Fatalf("累计错: prompt=%v cached=%v（应 150/80——details 缺失按 0 计）", done["prompt_tokens"], done["cached_tokens"])
	}
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			if i > start {
				out = append(out, b[start:i])
			}
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
