package llm

// 智谱编码套餐（GLM Coding Plan）专属联网搜索 MCP——web_search_prime。
// 计费走套餐的「MCP 每月额度」（网络搜索/网页读取/开源仓库共享），不烧现金余额；
// 通用 mcp-broker 与 REST web_search API 是另外的按量计费产品，欠费会被拒（2026-09-30 实测），别混用。
// 协议：MCP streamable HTTP（JSON-RPC，响应为 SSE 帧）；一次搜索 = initialize → initialized 通知 → tools/call，
// 会话用完即弃（无状态，省掉会话生命周期管理）。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// SearchResult 一条搜索结果（对齐 web_search_prime 返回，只留有用的）。
type SearchResult struct {
	Title       string `json:"title"`
	Content     string `json:"content"`
	Link        string `json:"link"`
	Media       string `json:"media"`
	PublishDate string `json:"publish_date"`
}

// WebSearch 联网搜索一次。recency 可空（noLimit）或 oneDay/oneWeek/oneMonth/oneYear；
// location 可空（服务端默认 cn）或 cn/us。query 建议 ≤70 字。
// 配置：SEARCH_API_KEY（缺省 LLM_API_KEY）/ SEARCH_MCP_URL（缺省套餐专属端点）。
func WebSearch(ctx context.Context, query, recency, location string) ([]SearchResult, error) {
	key := os.Getenv("SEARCH_API_KEY")
	if key == "" {
		key = os.Getenv("LLM_API_KEY")
	}
	if key == "" {
		return nil, fmt.Errorf("SEARCH_API_KEY 未配置（亦无 LLM_API_KEY 可复用）")
	}
	endpoint := os.Getenv("SEARCH_MCP_URL")
	if endpoint == "" {
		endpoint = "https://open.bigmodel.cn/api/mcp/web_search_prime/mcp"
	}
	args := map[string]any{"search_query": query}
	if recency != "" {
		args["search_recency_filter"] = recency
	}
	if location != "" {
		args["location"] = location
	}

	init, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-03-26", "capabilities": map[string]any{},
			"clientInfo": map[string]any{"name": "zhenshu-agent", "version": "1.0"},
		},
	})
	_, hdr, err := mcpPost(ctx, endpoint, key, "", init)
	if err != nil {
		return nil, fmt.Errorf("MCP 握手失败: %w", err)
	}
	sid := hdr.Get("Mcp-Session-Id")

	// initialized 通知：协议要求，202 无响应体，失败不挡搜索（服务端未实测强制）
	if note, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err == nil {
		_, _, _ = mcpPost(ctx, endpoint, key, sid, note)
	}

	call, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": "web_search_prime", "arguments": args},
	})
	b, _, err := mcpPost(ctx, endpoint, key, sid, call)
	if err != nil {
		return nil, err
	}
	var msg struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := decodeMCPMessage(b, &msg); err != nil {
		return nil, fmt.Errorf("MCP 响应解析失败: %w（原文前 200 字: %.200s）", err, b)
	}
	if msg.Error != nil {
		return nil, fmt.Errorf("搜索接口报错: %s", msg.Error.Message)
	}
	if msg.Result.IsError || len(msg.Result.Content) == 0 {
		text := ""
		if len(msg.Result.Content) > 0 {
			text = msg.Result.Content[0].Text
		}
		return nil, fmt.Errorf("搜索执行失败: %s", text)
	}
	return parseSearchResults(msg.Result.Content[0].Text)
}

// parseSearchResults 工具返回的 text 有两种形态：直接的结果数组，或再包一层 JSON 字符串的数组
// （实测 web_search_prime 是后者）。两种都接。
func parseSearchResults(text string) ([]SearchResult, error) {
	var rs []SearchResult
	if err := json.Unmarshal([]byte(text), &rs); err == nil {
		return rs, nil
	}
	var wrapped string
	if err := json.Unmarshal([]byte(text), &wrapped); err != nil {
		return nil, fmt.Errorf("结果不是合法结构: %w（前 100 字: %.100s）", err, text)
	}
	if err := json.Unmarshal([]byte(wrapped), &rs); err != nil {
		return nil, fmt.Errorf("内层结果解析失败: %w（前 100 字: %.100s）", err, wrapped)
	}
	return rs, nil
}

// mcpPost 发一次 JSON-RPC。非 200 一律报错（欠费 500 的 body 是纯文本，原样带回并给可读提示）。
// mcpClient 包级复用（连接池）：搜索调用频繁，每次新建 client 白付握手。
var mcpClient = &http.Client{Timeout: 60 * time.Second}

func mcpPost(ctx context.Context, endpoint, key, sid string, body []byte) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	httpResp, err := mcpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer httpResp.Body.Close()
	b, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, nil, err
	}
	if httpResp.StatusCode != http.StatusOK {
		text := strings.TrimSpace(string(b))
		if strings.Contains(text, "欠费") || strings.Contains(text, "余额不足") {
			return nil, nil, fmt.Errorf("智谱账户欠费——搜索走编码套餐 MCP 每月额度，但账户有欠费未结清；到 open.bigmodel.cn 财务页处理后重试")
		}
		return nil, nil, fmt.Errorf("HTTP %d: %.300s", httpResp.StatusCode, text)
	}
	return b, httpResp.Header, nil
}

// decodeMCPMessage 响应有两种形态：SSE 帧（"data: {...}" 行）或纯 JSON。取第一条带 result/error 的消息。
func decodeMCPMessage(b []byte, out any) error {
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		if err := json.Unmarshal([]byte(payload), out); err == nil {
			return nil
		}
	}
	return json.Unmarshal(b, out)
}
