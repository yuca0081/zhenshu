package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// 漂移门禁：SegTail 是 Go/Py/TS 三端隐式契约（MP4 段长 = 配音时长 + 尾垫），
// 任何一端单方面改动都会让「引用此刻」的段内偏移错 0.35×N 秒。
// 这里强制 ai/assemble.py 与 web/src/segtime.ts 的字面值与 contract.SegTail 一致。
func TestSegTailMatchesTwinImplementations(t *testing.T) {
	py, err := os.ReadFile(filepath.Join("..", "..", "..", "ai", "assemble.py"))
	if err != nil {
		t.Fatalf("assemble.py 不可读: %v", err)
	}
	ts, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "src", "segtime.ts"))
	if err != nil {
		t.Fatalf("segtime.ts 不可读: %v", err)
	}
	tail := regexp.QuoteMeta(strconv.FormatFloat(SegTail, 'f', -1, 64))
	if !regexp.MustCompile(`\+\s*` + tail).Match(py) {
		t.Errorf("ai/assemble.py 中未找到「+ %s」尾垫字面值：若改了 contract.SegTail，须同步 scene = duration_s + %s", tail, tail)
	}
	if !regexp.MustCompile(`SEG_TAIL\s*=\s*` + tail).Match(ts) {
		t.Errorf("web/src/segtime.ts 中 SEG_TAIL 与 contract.SegTail（%s）不一致", tail)
	}
}

