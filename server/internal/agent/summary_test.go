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
