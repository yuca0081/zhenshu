package agent

import (
	"strings"
	"testing"
	"time"

	"webpage-video-agent/server/internal/produce"
)

// 首做失败（无旧成片）时，失败终态必须进状态摘要。藏了它，模型看着三门全✓
// 会把失败轮幻觉成「制作还在后台跑」，永远不再开工（2026-10-09《1984》根因）。
func TestSummaryFirstProduceFailureVisible(t *testing.T) {
	s := State{
		HasManuscript: true, HasStoryboard: true, SegCount: 24, TotalHint: 287,
		HasStyle: true, StyleConfirmed: true, StyleDirection: "电幕档案",
		LastEvent: produce.EventProduceError, LastDetail: "✗ content_overlap #seg18-key2",
		LastAt: time.Date(2026, 10, 9, 19, 20, 28, 0, time.Local),
	}
	sum := s.summary()
	for _, want := range []string{"最近一轮制作失败", "content_overlap", "成片从未出过", "start_production"} {
		if !strings.Contains(sum, want) {
			t.Fatalf("summary 缺 %q：\n%s", want, sum)
		}
	}
	if strings.Contains(sum, "失败前的旧版") {
		t.Fatalf("无旧成片不该出现旧版警告：\n%s", sum)
	}
}

// 有旧成片时保留旧行为：失败要警告舞台上可能是旧版，且不说「成片已就绪」。
func TestSummaryProduceFailureWithOldVideo(t *testing.T) {
	s := State{
		HasManuscript: true, HasStoryboard: true, HasStyle: true, StyleConfirmed: true,
		HasVideo: true, LastEvent: produce.EventReworkError, LastDetail: "✗ boom",
	}
	sum := s.summary()
	if !strings.Contains(sum, "失败前的旧版") {
		t.Fatalf("summary 缺旧版警告：\n%s", sum)
	}
	if strings.Contains(sum, "成片已就绪") {
		t.Fatalf("失败轮不该说成片已就绪：\n%s", sum)
	}
}

// 正常路径不变：无失败史 + 有成片 = 成片已就绪，无失败行。
func TestSummaryCleanVideoReady(t *testing.T) {
	s := State{
		HasManuscript: true, HasStoryboard: true, HasStyle: true, StyleConfirmed: true,
		HasVideo: true,
	}
	sum := s.summary()
	if !strings.Contains(sum, "成片已就绪") {
		t.Fatalf("summary 缺成片就绪：\n%s", sum)
	}
	if strings.Contains(sum, "失败") {
		t.Fatalf("summary 不应有失败行：\n%s", sum)
	}
}

// ── 全警报矩阵：每条异常事实都必须有出口，负断言钉住"不虚报" ──────────

// 成片过期（改稿后未重做）：必须说旧版并点名过期轴，不许再说"已就绪"。
func TestSummaryStaleVideo(t *testing.T) {
	s := State{
		HasManuscript: true, HasStoryboard: true, HasStyle: true, StyleConfirmed: true,
		HasVideo: true,
	}
	s.Stale.Manuscript = true
	s.Stale.Style = true
	sum := s.summary()
	if !strings.Contains(sum, "成片是旧版") || !strings.Contains(sum, "文稿") || !strings.Contains(sum, "风格") {
		t.Fatalf("summary 缺旧版事实：\n%s", sum)
	}
	if strings.Contains(sum, "成片已就绪") {
		t.Fatalf("过期成片不该说已就绪：\n%s", sum)
	}
}

// 服务重启中断（首做，无旧片）：必须可见且给出继续动作。
func TestSummaryInterruptedNoVideo(t *testing.T) {
	s := State{
		HasManuscript: true, HasStoryboard: true, HasStyle: true, StyleConfirmed: true,
		LastEvent: produce.EventProduceInterrupted, LastDetail: "上次服务重启时制作中断",
		LastAt: time.Date(2026, 10, 9, 20, 0, 0, 0, time.Local),
	}
	sum := s.summary()
	if !strings.Contains(sum, "服务重启中断") || !strings.Contains(sum, "start_production") {
		t.Fatalf("summary 缺中断事实/继续动作：\n%s", sum)
	}
}

// 取消 + 旧成片仍在：报取消，不说"已就绪"（旧片不是本轮结果）。
func TestSummaryCancelledWithOldVideo(t *testing.T) {
	s := State{
		HasManuscript: true, HasStoryboard: true, HasStyle: true, StyleConfirmed: true,
		HasVideo: true, LastEvent: produce.EventProduceCancelled,
	}
	sum := s.summary()
	if !strings.Contains(sum, "被取消") {
		t.Fatalf("summary 缺取消行：\n%s", sum)
	}
	if strings.Contains(sum, "成片已就绪") {
		t.Fatalf("取消后不该说成片已就绪：\n%s", sum)
	}
}

// 制作运行中：⏳ 行必须出现（这是"制作在跑"唯一合法依据）。
func TestSummaryProducing(t *testing.T) {
	s := State{HasManuscript: true, HasStoryboard: true, HasStyle: true, StyleConfirmed: true, Producing: true}
	if sum := s.summary(); !strings.Contains(sum, "制作管线运行中") {
		t.Fatalf("summary 缺运行中行：\n%s", sum)
	}
}

// 失败 + 过期叠加：失败行与旧版行必须同时在。
func TestSummaryFailureAndStale(t *testing.T) {
	s := State{
		HasManuscript: true, HasStoryboard: true, HasStyle: true, StyleConfirmed: true,
		HasVideo: true, LastEvent: produce.EventReworkError, LastDetail: "✗ boom",
	}
	s.Stale.Storyboard = true
	sum := s.summary()
	if !strings.Contains(sum, "最近一轮制作失败") || !strings.Contains(sum, "成片是旧版") {
		t.Fatalf("summary 失败/旧版事实不全：\n%s", sum)
	}
}
