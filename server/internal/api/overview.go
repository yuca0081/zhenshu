// 过程页数据口：activity（事件流，manifest 尾部全量回放）与 transcript（智能体
// 任务对话留痕）。磁盘是事实源：manifest/目录不会撒谎，全部现算不落任何库。
package api

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/produce"
)

type manifestEvent struct {
	TS     string `json:"ts"`
	MS     int64  `json:"ms,omitempty"` // unix 毫秒（前端算每步用时；老条目缺省时按 ts 兜底解析）
	Event  string `json:"event"`
	Detail string `json:"detail,omitempty"`
	// agent.start / agent.done 的任务载荷（ManifestFields 写入的额外字段）。
	Task             string `json:"task,omitempty"`
	Label            string `json:"label,omitempty"`
	State            string `json:"state,omitempty"`
	PromptTokens     int    `json:"prompt_tokens,omitempty"`
	CompletionTokens int    `json:"completion_tokens,omitempty"`
	CachedTokens     int    `json:"cached_tokens,omitempty"` // 平台前缀缓存命中（2026-10-19 起记账；旧账目无此字段）
	Calls            int    `json:"calls,omitempty"`
	Transcript       string `json:"transcript,omitempty"`
}

// tailManifest manifest 尾部 n 条（坏行剔除，时间正序）。activity 接口（事件流
// 面板 500 条全量回放）的数据口。尾部窗口读走 produce.ReadJSONLTail：manifest
// 随制作轮次无限增长，逐次轮询都全量读+解析会让 I/O 随历史线性上涨。
// 恒返回非 nil（读不了 = 空切片）：空列表必须序列化成 [] 不能 null（前端直吃）。
func tailManifest(path string, n int) []manifestEvent {
	lines, err := produce.ReadJSONLTail(path, n)
	out := make([]manifestEvent, 0, len(lines))
	if err != nil {
		return out
	}
	// 坏行（撕裂/半行）与空事件行照旧剔除——unmarshalJSONL 只滤解析失败，
	// 这里还要求 event 字段非空（时间正序、坏行不进事件流的口径与 activity 一致）
	for _, line := range lines {
		var e manifestEvent
		if json.Unmarshal(line, &e) != nil || e.Event == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// activity 制作活动流：GET /projects/:id/activity —— manifest.jsonl 原样事件
// （尾部 n 条默认 500，「过程」页传 limit 最多 2000，时间正序）。前端事件流
// 面板/过程页的数据源：每步用时由 stage.start/finish 的毫秒差算出，打回
// （check.repair / review.* / frameqa）与终态事件原样给前端翻译。轻量只读
// （不像 overview 全树扫产物），供制作中轮询。无 manifest（新建项目没开过工）
// 回 [] 而不是 404/null：前端 .length/.map 直吃。
func (s *Server) activity(c *gin.Context) {
	p, ok := s.project(c)
	if !ok {
		return
	}
	limit := 500
	if n, err := strconv.Atoi(c.Query("limit")); err == nil && n >= 1 && n <= 2000 {
		limit = n
	}
	// 无 manifest（新建项目没开过工）回 [] 而不是 404/null：前端 .length/.map 直吃
	c.JSON(200, tailManifest(p.Artifact("manifest.jsonl"), limit))
}

// transcript 智能体任务对话留痕：GET /projects/:id/transcript?task=seg.audit.seg03.r1
// —— llm/transcripts/<task>.jsonl 逐行解析（写入侧已减脂：截断+图片占位）。
// 无留痕回 []（纪律同 activity）；task 名过 llm.ValidTask 校验防目录穿越。
func (s *Server) transcript(c *gin.Context) {
	p, ok := s.project(c)
	if !ok {
		return
	}
	task := c.Query("task")
	if !llm.ValidTask(task) {
		c.JSON(400, gin.H{"error": "task 名不合法"})
		return
	}
	b, err := os.ReadFile(p.Artifact("llm/transcripts/" + task + ".jsonl"))
	out := make([]map[string]any, 0, 64)
	if err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			var m map[string]any
			if json.Unmarshal([]byte(line), &m) == nil && m["role"] != nil {
				out = append(out, m)
			}
		}
	}
	c.JSON(200, out)
}
