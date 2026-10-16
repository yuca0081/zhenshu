package artifacts

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		rel  string
		want Class
	}{
		{"project.json", Keeper},
		{`renders\main.mp4`, Keeper},
		{"renders/timeline.json", Keeper},
		{"audio/seg01.wav", Keeper},
		{"style/sample-0.png", Keeper},
		{"style/clips/clip-1.mp4", Keeper},
		{"style/font_assets.json", Keeper},
		{"llm/comp-seg03.json", Keeper},
		{"llm/comp-seg03.json.scene.src", Keeper},
		{"llm/prep-render-cache/ab12.txt", Keeper},
		{"renders/segs/seg03.mp4.sum", Keeper},
		{"reports/director-review.json", Keeper},
		{"assets/katex/katex.min.js", Keeper},
		{"references/../x", Keeper}, // 分类不做安全校验，只认前缀（防穿越在 HTTP 层）
		{"reports/frameqa/seg03/f-1.png", Discard},
		{"reports/prereview/round1/shot.png", Discard},
		{"reports/replay/round2/shot.png", Discard},
		{"reports/director/review/shot.png", Discard},
		{".hf-seg/seg03.html", Discard},
		{".hf-clip/clip-1.html", Discard},
		{"compositions/frames/seg03.html", Discard},
		{"compositions/caps/seg03.html", Discard},
		{"renders/preview/shot.png", Discard},
		{"llm/prep-draft.request.md", Discard},
		{"llm/prep-script.jsonl", Discard},
		{"npm-cache/_cacache/index", Garbage},
		{"deep/npm-cache/x.bin", Garbage},
		{"renders/.seg03-abc.tmp.mp4", Garbage},
		{"style/.atomic-project.json", Garbage},
	}
	for _, c := range cases {
		if got := Classify(c.rel); got != c.want {
			t.Errorf("Classify(%q) = %v, want %v", c.rel, got, c.want)
		}
	}
}

// fakeStore 内存正本后端：验证 Prune 的「先确认后删」与 Sync/Restore 往返。
type fakeStore struct{ objs map[string]bool }

func (f *fakeStore) Put(key, srcPath string) error { f.objs[key] = true; return nil }
func (f *fakeStore) Open(string) (io.ReadSeekCloser, int64, time.Time, error) {
	return nil, 0, time.Time{}, os.ErrNotExist
}
func (f *fakeStore) Stat(key string) bool { return f.objs[key] }
func (f *fakeStore) Delete(key string) error {
	delete(f.objs, key)
	return nil
}
func (f *fakeStore) List(prefix string) ([]string, error) {
	var out []string
	for k := range f.objs {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, k)
		}
	}
	return out, nil
}
func (f *fakeStore) DeletePrefix(prefix string) error {
	for k := range f.objs {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(f.objs, k)
		}
	}
	return nil
}
func (f *fakeStore) Localize(key string) (string, error) { return key, nil }
func (f *fakeStore) Fetch(key, dstPath string) error {
	if !f.objs[key] {
		return os.ErrNotExist
	}
	return os.WriteFile(dstPath, []byte(key), 0o644)
}
func (f *fakeStore) Available() bool { return true }

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSyncUpRestorePrune(t *testing.T) {
	root := t.TempDir()
	prefix := "projects/p1"
	j := filepath.Join
	write(t, j(root, prefix, "project.json"), "{}")
	write(t, j(root, prefix, "renders/main.mp4"), "VIDEO")
	write(t, j(root, prefix, "reports/frameqa/f.png"), "PNG")
	write(t, j(root, prefix, "npm-cache/junk"), "JUNK")

	st := &fakeStore{objs: map[string]bool{}}

	// SyncUp：只传 Keeper；垃圾与弃置不上传
	if n, err := SyncUp(st, root, prefix); err != nil || n != 2 {
		t.Fatalf("SyncUp = %d, %v; want 2 keeper uploads", n, err)
	}
	if !st.objs["projects/p1/project.json"] || !st.objs["projects/p1/renders/main.mp4"] {
		t.Fatal("keeper 未上传")
	}
	if len(st.objs) != 2 {
		t.Fatalf("Discard/Garbage 被上传: %v", st.objs)
	}

	// Prune（远端正本已在）：Keeper 大文件删、Keeper 小文件留、Discard/Garbage 删
	freed, files, err := Prune(st, root, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if files != 3 || freed != int64(len("VIDEO")+len("PNG")+len("JUNK")) {
		t.Fatalf("Prune = %d files, %d bytes; want 3 files", files, freed)
	}
	if _, err := os.Stat(j(root, prefix, "project.json")); err != nil {
		t.Fatal("契约 JSON 不应被清理")
	}
	for _, gone := range []string{"renders/main.mp4", "reports/frameqa/f.png", "npm-cache/junk"} {
		if _, err := os.Stat(j(root, prefix, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s 应已被清理", gone)
		}
	}
	if _, err := os.Stat(j(root, prefix, "npm-cache")); !os.IsNotExist(err) {
		t.Fatal("空目录应被摘除")
	}

	// Prune（远端正本缺失）：Keeper 大文件必须保住——安全不变量
	write(t, j(root, prefix, "renders/main.mp4"), "VIDEO2")
	write(t, j(root, prefix, "reports/frameqa/g.png"), "PNG2")
	st2 := &fakeStore{objs: map[string]bool{"projects/p1/project.json": true}}
	_, files, err = Prune(st2, root, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(j(root, prefix, "renders/main.mp4")); err != nil {
		t.Fatal("远端无正本时大文件不得删除")
	}
	if files == 0 {
		t.Fatal("Discard/Garbage 仍应被清理")
	}

	// Restore：把 Keeper 回源，Discard 不回
	if err := os.Remove(j(root, prefix, "renders/main.mp4")); err != nil {
		t.Fatal(err)
	}
	if n, err := Restore(st, root, prefix); err != nil || n != 1 {
		t.Fatalf("Restore = %d, %v; want 1", n, err)
	}
	if b, _ := os.ReadFile(j(root, prefix, "renders/main.mp4")); string(b) != "projects/p1/renders/main.mp4" {
		t.Fatalf("回源内容不对: %q", b)
	}
	if _, err := os.Stat(j(root, prefix, "reports/frameqa/f.png")); !os.IsNotExist(err) {
		t.Fatal("Discard 不应回源")
	}
}

func TestLocalStoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	st := NewLocal(root)
	if st.Available() {
		t.Fatal("localStore 不应标记为远端后端")
	}
	key := ProjectKey("p1", "renders/main.mp4")
	if err := st.Put(key, "whatever"); err != nil {
		t.Fatal(err)
	}
	if st.Stat(key) {
		t.Fatal("未落盘不应 Stat 通过")
	}
	write(t, filepath.Join(root, "projects/p1/renders/main.mp4"), "V")
	p, err := st.Localize(key)
	if err != nil || p != filepath.Join(root, "projects", "p1", "renders", "main.mp4") {
		t.Fatalf("Localize = %q, %v", p, err)
	}
	keys, err := st.List("projects/p1")
	if err != nil || !reflect.DeepEqual(keys, []string{"projects/p1/renders/main.mp4"}) {
		t.Fatalf("List = %v, %v", keys, err)
	}
	if keys2, _ := st.List("projects/none"); len(keys2) != 0 {
		t.Fatalf("不存在前缀应为空清单: %v", keys2)
	}
	if err := st.DeletePrefix("projects/p1"); err != nil {
		t.Fatal(err)
	}
	if st.Stat(key) {
		t.Fatal("DeletePrefix 后不应存在")
	}
}
