package prep

import (
	"strings"
	"testing"
)

// 2026-10-29 提速拍板：样张判卷从「任何 issue 一票否决」改为「无 high 才算不过」。
// 实测样张 A 顶着 4 项口味精疵修满 2 轮一项未修掉照样放行——修复轮白烧 ~4.5 分钟。
func TestApplySampleSeverity(t *testing.T) {
	// med/low 小疵 → 放行（不立案）
	j := sampleJudge{Issues: []sampleIssue{
		{Kind: "offstyle", Detail: "密度略偏", Severity: "med"},
		{Kind: "other", Detail: "对齐微偏", Severity: "low"},
	}}
	applySampleSeverity(&j)
	if !j.Pass {
		t.Fatal("med/low issues must not fail the sample (记录不立案)")
	}
	if n := len(j.MedLowIssues()); n != 2 {
		t.Fatalf("MedLowIssues = %d, want 2", n)
	}
	if n := len(j.HighIssues()); n != 0 {
		t.Fatalf("HighIssues = %d, want 0", n)
	}

	// high 硬伤 → 打回，且修复材料只含 high
	j2 := sampleJudge{Issues: []sampleIssue{
		{Kind: "unreadable", Detail: "标题糊了", Severity: "med"}, // 硬伤三兄弟标低也压不住
		{Kind: "offstyle", Detail: "明显两路", Severity: "high"},
		{Kind: "other", Detail: "间距略大", Severity: "low"},
	}}
	applySampleSeverity(&j2)
	if j2.Pass {
		t.Fatal("high (or hard-kind) issues must fail the sample")
	}
	if n := len(j2.HighIssues()); n != 2 {
		t.Fatalf("HighIssues = %d, want 2 (hard kind forced high)", n)
	}
	if lines := issueLines(j2.HighIssues()); len(lines) != 2 || !strings.Contains(lines[0], "标题糊了") {
		t.Fatalf("issueLines mismatch: %q", lines)
	}

	// severity 缺失 → 从严记 high（兼容不守新 schema 的旧口径输出）
	j3 := sampleJudge{Issues: []sampleIssue{{Kind: "other", Detail: "没标严重度"}}}
	applySampleSeverity(&j3)
	if j3.Pass {
		t.Fatal("missing severity must default to high (从严兼容)")
	}
	if got := j3.Issues[0].Severity; got != "high" {
		t.Fatalf("missing severity should be normalized to high, got %q", got)
	}
}
