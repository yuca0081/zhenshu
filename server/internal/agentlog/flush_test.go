package agentlog

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"

	"webpage-video-agent/server/internal/pipeline"
)

func newLedgerProject(t *testing.T) *pipeline.Project {
	t.Helper()
	dir := t.TempDir()
	p := pipeline.NewProject(dir, "p1")
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func readEvents(t *testing.T, p *pipeline.Project) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(p.Artifact("manifest.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil {
			rows = append(rows, m)
		}
	}
	return rows
}

// TestIncrementalFlushAndSpend 增量落账与汇总口径（中-4）：
// 未落账增量过 2 万 token 自动 Flush；死亡任务（无 agent.done）的增量合计计入
// ProjectSpend；正常收尾任务只按 done 行算一次，增量不重复计数。
func TestIncrementalFlushAndSpend(t *testing.T) {
	p := newLedgerProject(t)

	// 任务 A：正常收尾。两次大调用触发两次 Flush，随后 Done 落全量。
	a := Begin(p, "prep.style", "准备②风格")
	a.Add(openai.Usage{PromptTokens: 30000, CompletionTokens: 5000})
	a.Add(openai.Usage{PromptTokens: 30000, CompletionTokens: 5000})
	a.Done(nil)

	// 任务 B：进程死亡，只有增量行没有 done 行。
	b := Begin(p, "seg.write.seg01", "写画面 seg01")
	b.Add(openai.Usage{PromptTokens: 25000, CompletionTokens: 5000})
	// 不调 Done——模拟进程死亡

	spendP, spendC, spendCalls := ProjectSpend(p)
	// 任务 A：done 全量 60000+10000=70000（增量行不重复计）；任务 B：增量 25000+5000=30000
	if spendP != 60000+25000 || spendC != 10000+5000 {
		t.Fatalf("汇总口径不符: prompt=%d compl=%d", spendP, spendC)
	}
	if spendCalls != 2+1 {
		t.Fatalf("调用数汇总不符: %d", spendCalls)
	}
	// 收尾补个终态只为关留痕文件（Windows TempDir 清理需要）；断言已完毕，
	// 此刻再落 done 不影响上面的汇总结论。
	b.Done(errors.New("进程死亡（测试模拟）"))
}

// TestFlushThreshold 低于阈值不落增量行，过线才落。
func TestFlushThreshold(t *testing.T) {
	p := newLedgerProject(t)
	at := Begin(p, "t1", "任务1")
	defer at.Done(errors.New("test-end")) // 关留痕文件（Windows TempDir 清理需要）
	at.Add(openai.Usage{PromptTokens: 100, CompletionTokens: 10}) // 远低于 2 万
	rows := readEvents(t, p)
	for _, r := range rows {
		if r["event"] == "agent.usage" {
			t.Fatal("低于阈值不应落增量行")
		}
	}
}
