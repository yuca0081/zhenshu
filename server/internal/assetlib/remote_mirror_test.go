package assetlib

// 远程镜像集成测试：打真 PG + MinIO（仓库根 .env 的 PG_DSN/MINIO_*），
// 未配置则跳过。隔离：独立表 assets_test + 独立桶 zhenshu-test，不碰生产
// assets 表/zhenshu 桶；用例自清理（行+对象全删）。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/joho/godotenv"
)

func testRemote(t *testing.T) *Remote {
	t.Helper()
	for _, p := range []string{"../../.env", "../../../.env"} {
		if _, err := os.Stat(p); err == nil {
			_ = godotenv.Load(p)
			break
		}
	}
	dsn := os.Getenv("PG_DSN")
	ep := os.Getenv("MINIO_ENDPOINT")
	if dsn == "" || ep == "" {
		t.Skip("PG_DSN/MINIO_ENDPOINT 未配置，跳过远程镜像集成测试")
	}
	r, err := OpenRemote(RemoteConfig{
		DSN: dsn, Endpoint: ep,
		AccessKey: os.Getenv("MINIO_ACCESS_KEY"), SecretKey: os.Getenv("MINIO_SECRET_KEY"),
		Bucket: "zhenshu-test", Region: os.Getenv("MINIO_REGION"), Secure: os.Getenv("MINIO_SECURE") == "1",
		Table: "assets_test",
	})
	if err != nil {
		t.Fatalf("OpenRemote: %v", err)
	}
	return r
}

func cleanupRemote(t *testing.T, r *Remote) {
	t.Helper()
	ctx := context.Background()
	es, err := r.ListEntries()
	if err != nil {
		return
	}
	for _, e := range es {
		_ = r.DeleteEntry(e.ID)
		_ = r.RemoveObject(ctx, e)
	}
}

// 入库即双写 + 空库对账拉回：容器重建场景的全链路（Ingest 推 → 新库 SyncRemote 拉回）。
func TestRemoteMirrorRoundTrip(t *testing.T) {
	r := testRemote(t)
	cleanupRemote(t, r)
	defer cleanupRemote(t, r)

	// 库 A：入库两张图（真实 Ingest 路径，推 PG 行 + MinIO 对象）
	dirA := t.TempDir()
	libA := New(dirA)
	libA.SetRemote(r)
	src1 := filepath.Join(dirA, "s1.png")
	src2 := filepath.Join(dirA, "s2.png")
	os.WriteFile(src1, []byte("fake-png-1"), 0o644)
	os.WriteFile(src2, []byte("fake-png-2"), 0o644)
	e1, err := libA.Ingest("image", src1, []string{"测试"}, "镜像测试 1", "upload", nil)
	if err != nil {
		t.Fatal(err)
	}
	e2, err := libA.Ingest("image", src2, []string{"测试"}, "镜像测试 2", "upload", nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := r.ListEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("PG 镜像行数 %d ≠ 2", len(rows))
	}

	// 库 B：全新空目录（catalog/媒体全无）——SyncRemote 应从远端拉回条目+对象
	dirB := t.TempDir()
	libB := New(dirB)
	libB.SetRemote(r)
	if err := libB.SyncRemote(); err != nil {
		t.Fatal(err)
	}
	es, err := libB.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 2 {
		t.Fatalf("拉回条目数 %d ≠ 2", len(es))
	}
	for id, want := range map[string]string{e1.ID: "fake-png-1", e2.ID: "fake-png-2"} {
		var e Entry
		for _, x := range es {
			if x.ID == id {
				e = x
			}
		}
		if e.ID == "" {
			t.Fatalf("条目 %s 未拉回", id)
		}
		b, err := os.ReadFile(filepath.Join(dirB, "library", filepath.FromSlash(e.File)))
		if err != nil {
			t.Fatalf("%s 对象未落盘: %v", id, err)
		}
		if string(b) != want {
			t.Fatalf("%s 对象内容错: %q", id, b)
		}
	}

	// 二次对账幂等：无新拉回无新推（远端行数不变）
	if err := libB.SyncRemote(); err != nil {
		t.Fatal(err)
	}
	rows2, _ := r.ListEntries()
	if len(rows2) != 2 {
		t.Fatalf("幂等对账后远端行数 %d ≠ 2", len(rows2))
	}
}

// 本地文件丢失（catalog 在、对象缺）：对账从远端补回文件。
func TestSyncRemoteRestoresMissingFile(t *testing.T) {
	r := testRemote(t)
	cleanupRemote(t, r)
	defer cleanupRemote(t, r)

	dir := t.TempDir()
	lib := New(dir)
	lib.SetRemote(r)
	src := filepath.Join(dir, "s.png")
	os.WriteFile(src, []byte("precious"), 0o644)
	e, err := lib.Ingest("image", src, nil, "补文件测试", "upload", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "library", filepath.FromSlash(e.File))); err != nil {
		t.Fatal(err)
	}
	if err := lib.SyncRemote(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "library", filepath.FromSlash(e.File)))
	if err != nil || string(b) != "precious" {
		t.Fatalf("文件未从远端补回: %v %q", err, b)
	}
}

// 无 hash 条目（BGM 类）对账不重复推送：远端行不被反复 upsert。
func TestSyncRemoteSkipsHashlessDuplicates(t *testing.T) {
	r := testRemote(t)
	cleanupRemote(t, r)
	defer cleanupRemote(t, r)

	dir := t.TempDir()
	lib := New(dir)
	// 手写 catalog：一条无 hash 条目 + 本地文件
	entry := Entry{ID: "aud-t0001", Type: "audio", File: "media/audio/aud-t0001.mp3", Tags: []string{"BGM"}, Desc: "t", Origin: "builtin", Meta: map[string]any{"duration": 30}}
	os.MkdirAll(filepath.Join(dir, "library", "media", "audio"), 0o755)
	os.WriteFile(filepath.Join(dir, "library", "media", "audio", "aud-t0001.mp3"), []byte("mp3"), 0o644)
	cat, _ := json.Marshal([]Entry{entry})
	os.WriteFile(filepath.Join(dir, "library", "catalog.json"), cat, 0o644)
	lib.SetRemote(r)

	if err := lib.SyncRemote(); err != nil { // 首次：推 1
		t.Fatal(err)
	}
	if err := lib.SyncRemote(); err != nil { // 二次：应 0 推（hash 双空=一致）
		t.Fatal(err)
	}
	rows, _ := r.ListEntries()
	if len(rows) != 1 || rows[0].ID != "aud-t0001" {
		t.Fatalf("无 hash 条目对账异常: %+v", rows)
	}
	// tags/desc 往返不丢
	if rows[0].Desc != "t" || len(rows[0].Tags) != 1 {
		t.Fatalf("字段往返丢失: %+v", rows[0])
	}
	_ = fmt.Sprintf("")
}
