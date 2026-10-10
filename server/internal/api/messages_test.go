package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// 空历史必须回 [] 不能回 null：内存存储（未配 PG）重启后老项目聊天记录清零是常态，
// 前端 loadMsgs 对响应直接 .filter，null 会把对话栏整个崩掉（2026-10-11 实测翻车）。
// 与 activity/listProjects 的「空列表序列化成 []」同一纪律，此测试锁死。
func TestMessagesEmptyHistoryReturnsArrayNotNull(t *testing.T) {
	s, _ := newTestServer(t)
	r := gin.New()
	r.GET("/api/projects/:id/messages", s.messages)

	w := doJSON(t, r, "GET", "/api/projects/p-none/messages", nil)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	body := strings.TrimSpace(w.Body.String())
	if body != "[]" {
		t.Fatalf("空历史应回 []，实际 %s", body)
	}
	var msgs []map[string]any
	if err := json.Unmarshal([]byte(body), &msgs); err != nil {
		t.Fatalf("响应不是合法 JSON 数组: %v", err)
	}
}
