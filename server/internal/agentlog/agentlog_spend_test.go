package agentlog

import (
	"os"
	"testing"

	"webpage-video-agent/server/internal/pipeline"
)

// ProjectSpend 从 manifest 播种历史账：只认 agent.done 行，数字字段缺失按 0。
func TestProjectSpend(t *testing.T) {
	dir := t.TempDir()
	p := pipeline.NewProject(dir, "p1")
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"event":"agent.start","task":"seg.audit.seg01.r1"}
{"event":"agent.done","task":"seg.audit.seg01.r1","state":"ok","prompt_tokens":100,"completion_tokens":20,"calls":2}
{"event":"agent.done","task":"seg.write.seg01","state":"fail","prompt_tokens":30}
{"event":"produce.tts","detail":"done"}
{broken json line}
`
	if err := os.WriteFile(p.Artifact("manifest.jsonl"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	prompt, completion, calls := ProjectSpend(p)
	if prompt != 130 || completion != 20 || calls != 2 {
		t.Fatalf("播种账不符: prompt=%d completion=%d calls=%d", prompt, completion, calls)
	}
	// 无 manifest：全零不炸
	if a, b, c := ProjectSpend(pipeline.NewProject(t.TempDir(), "p2")); a != 0 || b != 0 || c != 0 {
		t.Fatalf("无账应全零: %d %d %d", a, b, c)
	}
}
