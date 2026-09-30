package styleref

import (
	"os"
	"path/filepath"
	"testing"
)

// 同秒连点上传两个视频必须落到不同 ref 目录（历史缺陷：目录复用导致
// 第二个视频覆盖第一个，两条登记共用一个 id）。
func TestSaveSecondLevelCollision(t *testing.T) {
	s := NewStore(t.TempDir(), t.TempDir())
	a, err := s.Save("a.mp4", func(dst string) error {
		return os.WriteFile(dst, []byte("a"), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Save("b.mp4", func(dst string) error {
		return os.WriteFile(dst, []byte("b"), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatalf("同秒撞号: %s", a.ID)
	}
	for id, want := range map[string]string{a.ID: "a", b.ID: "b"} {
		fb, err := os.ReadFile(filepath.Join(s.dir(id), "video.mp4"))
		if err != nil {
			t.Fatal(err)
		}
		if string(fb) != want {
			t.Fatalf("%s 内容被覆盖: %q", id, fb)
		}
	}
}
