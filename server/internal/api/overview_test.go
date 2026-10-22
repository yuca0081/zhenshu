package api

import (
	"os"
	"path/filepath"
	"testing"
)

func joinLines(ls []string) string {
	s := ""
	for _, l := range ls {
		s += l + "\n"
	}
	return s
}

// tailManifest 是 activity 接口（事件流面板）的数据口：坏行剔除、尾部截断、ms 字段透传。
func TestTailManifest(t *testing.T) {
	lines := []string{
		`{"event":"produce.start","detail":"","ts":"2026-09-30T10:00:00+08:00","ms":1791000000000}`,
		`not json`,
		``, // 撕裂/空行：坏行剔除（manifest 逐事件一行，空行只出现在写入被杀的中间）
		`{"event":"stage.start","detail":"tts","ts":"2026-09-30T10:00:05+08:00"}`, // 老条目：无 ms
	}
	path := filepath.Join(t.TempDir(), "manifest.jsonl")
	if err := os.WriteFile(path, []byte(joinLines(lines)), 0o644); err != nil {
		t.Fatal(err)
	}
	got := tailManifest(path, 500)
	if len(got) != 2 {
		t.Fatalf("坏行/空行应剔除，得 %d 条: %+v", len(got), got)
	}
	if got[0].MS != 1791000000000 {
		t.Fatalf("ms 字段应透传: %+v", got[0])
	}
	if got[1].MS != 0 || got[1].Event != "stage.start" {
		t.Fatalf("老条目 ms 缺省、事件保留: %+v", got[1])
	}
	// 尾部截断：只要最后 1 条
	got = tailManifest(path, 1)
	if len(got) != 1 || got[0].Event != "stage.start" {
		t.Fatalf("应只留尾部 1 条 stage.start: %+v", got)
	}
	// manifest 不存在（新建项目没开过工）：空切片而非 nil（JSON 必须 [] 不能 null）
	if got := tailManifest(filepath.Join(t.TempDir(), "none.jsonl"), 500); got == nil || len(got) != 0 {
		t.Fatalf("空输入应为空切片: %+v", got)
	}
}
