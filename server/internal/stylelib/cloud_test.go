package stylelib

// 云存储后端测试。单测不需要外部依赖；PG/MinIO 集成测试环境门控——
// 配 TEST_PG_DSN / TEST_MINIO_* （如 SSH 隧道通了）才跑，否则跳过。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"webpage-video-agent/server/internal/contract"
)

// ── 单测（无外部依赖）──────────────────────────────────────

// payload 往返：条目 → 行 → 条目，嵌套数据（tag_evidence+signatures）不变形。
func TestStylePayloadRoundTrip(t *testing.T) {
	in := Style{
		ID: "vid-x", Name: "卡带未来主义",
		Tags: contract.StyleTags{Main: "cassette_futurism", Aux: []string{"retrofuturism"}},
		TagEvidence: []contract.TagEvidence{
			{Tag: "cassette_futurism", Note: "CRT 荧光屏", Frames: []string{"frame-0.8.png"}},
		},
		Signatures: []contract.Signature{
			{ID: "sig1", Kind: contract.SigKindPalette, Name: "磷光绿", Mechanism: "信息区荧光单色", Frames: []string{"frame-0.8.png"}},
		},
		Subject: "七八十年代计算机界面混剪", OriginProject: "video:x", Published: true,
		CreatedAt: time.Now(),
	}
	row := styleRow{
		ID: in.ID, Name: in.Name, TagsMain: in.Tags.Main, TagsAux: strList(in.Tags.Aux),
		Subject: in.Subject, OriginProject: in.OriginProject, Published: in.Published,
		CreatedAt: in.CreatedAt, UpdatedAt: time.Now(),
	}
	b, err := json.Marshal(stylePayload{TagEvidence: in.TagEvidence, Signatures: in.Signatures})
	if err != nil {
		t.Fatal(err)
	}
	row.Payload = b
	out, err := row.toStyle()
	if err != nil {
		t.Fatal(err)
	}
	if out.Tags.Main != in.Tags.Main || len(out.Tags.Aux) != 1 || out.Tags.Aux[0] != in.Tags.Aux[0] {
		t.Fatalf("标签往返变形: %+v", out.Tags)
	}
	if len(out.TagEvidence) != 1 || len(out.TagEvidence[0].Frames) != 1 || out.TagEvidence[0].Frames[0] != "frame-0.8.png" {
		t.Fatalf("证据往返变形: %+v", out.TagEvidence)
	}
	if len(out.Signatures) != 1 || out.Signatures[0].Mechanism != in.Signatures[0].Mechanism {
		t.Fatalf("特色往返变形: %+v", out.Signatures)
	}
	if out.Subject != in.Subject || out.OriginProject != in.OriginProject || !out.Published {
		t.Fatalf("列字段往返变形: %+v", out)
	}
	// payload 顶层只装嵌套数据（tag_evidence/signatures），不装列字段（单一事实来源，防两处漂移）
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 || top["tag_evidence"] == nil || top["signatures"] == nil {
		t.Fatalf("payload 顶层应只有 tag_evidence/signatures: %s", b)
	}
}

func TestValidMediaName(t *testing.T) {
	yes := []string{"video.mp4", "cover.png", "frame-0.8.png", "frame-0001.png", "frame-12_5.png"}
	no := []string{"", "frame-.png", "ref.json", "cover2.png", "FRAME-1.png",
		"frame-x/y.png", `frame-x\y.png`, "frame-..png", "frame-a b.png", "frame-0.8.jpg", "video.mp4.png"}
	for _, n := range yes {
		if !ValidMediaName(n) {
			t.Fatalf("%q 应通过白名单", n)
		}
	}
	for _, n := range no {
		if ValidMediaName(n) {
			t.Fatalf("%q 应被白名单拒收", n)
		}
	}
}

// dirMedia Open 返回值可直接喂 ServeContent（ReadSeekCloser 三件套齐全）。
func TestDirMediaOpenShape(t *testing.T) {
	l := New(t.TempDir())
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "frame-1.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := l.media.Put("vid-x", "frame-1.png", filepath.Join(src, "frame-1.png")); err != nil {
		t.Fatal(err)
	}
	if !l.media.Stat("vid-x", "frame-1.png") {
		t.Fatal("Stat 应命中")
	}
	rc, size, _, err := l.media.Open("vid-x", "frame-1.png")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if size != 3 {
		t.Fatalf("size=%d", size)
	}
	if _, err := rc.Seek(0, 0); err != nil {
		t.Fatalf("对象流须可 Seek（ServeContent 依赖）: %v", err)
	}
	if _, err := l.media.Localize("vid-x", "frame-gone.png"); err == nil {
		t.Fatal("缺失媒体 Localize 应报错")
	}
}

// ── 集成测试（环境门控）────────────────────────────────────

// TestPgCatalogIntegration PG 账本往返：upsert 幂等/保首建时间/publish/delete/词表。
// 配 TEST_PG_DSN 跑（如 ssh -L 5432 隧道通了的开发机），否则跳过。
func TestPgCatalogIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN 未配置，跳过 PG 集成测试")
	}
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: dsn, PreferSimpleProtocol: true,
	}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连 PG: %v", err)
	}
	cat := &pgCatalog{db: db}
	if err := cat.ensureSchema(); err != nil {
		t.Fatalf("建表: %v", err)
	}

	// 自清理：开头清上次失败残留，结尾兜底清本轮写入（只动测试专属 id，不碰业务数据）
	clean := func() {
		_ = cat.DeleteStyle("vid-test-pg")
		_ = cat.db.Exec(`DELETE FROM style_vocab WHERE id = 'test_bauhaus'`)
	}
	clean()
	t.Cleanup(clean)

	if e, err := cat.Empty(); err != nil || e == false {
		t.Skipf("styles 表非空（生产/演示库），跳过写入型集成测试：Empty=%v err=%v", e, err)
	}

	// 词条
	if err := cat.PutVocab(VocabEntry{ID: "test_bauhaus", Name: "包豪斯", Brief: "几何构成", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("PutVocab: %v", err)
	}
	vocab, err := cat.AllVocab()
	if err != nil || len(vocab) != 1 || vocab[0].ID != "test_bauhaus" {
		t.Fatalf("AllVocab: %v %+v", err, vocab)
	}
	if err := cat.SetAliases("test_bauhaus", []string{"Bauhaus"}); err != nil {
		t.Fatalf("SetAliases: %v", err)
	}
	vocab, _ = cat.AllVocab()
	if len(vocab[0].Aliases) != 1 || vocab[0].Aliases[0] != "Bauhaus" {
		t.Fatalf("别名未落: %+v", vocab[0])
	}

	// 条目 upsert
	created := time.Now().Add(-time.Hour).Truncate(time.Second)
	st := &Style{
		ID: "vid-test-pg", Name: "测试条目", Tags: contract.StyleTags{Main: "test_bauhaus"},
		Subject: "测试", OriginProject: "video:t", Published: true, CreatedAt: created,
		Signatures: []contract.Signature{{Kind: contract.SigKindPalette, Name: "n", Mechanism: "m"}},
	}
	if err := cat.PutStyle(st); err != nil {
		t.Fatalf("PutStyle: %v", err)
	}
	// 覆盖写（整行替换语义，与旧文件实现一致；重解析入库恒 published=true）：created_at 保首建值
	st2 := &Style{ID: "vid-test-pg", Name: "测试条目改", Tags: contract.StyleTags{Main: "test_bauhaus"}, Published: true, CreatedAt: time.Now()}
	if err := cat.PutStyle(st2); err != nil {
		t.Fatalf("PutStyle 覆盖: %v", err)
	}
	if !st2.CreatedAt.Equal(created) {
		t.Fatalf("覆盖写应保首建 created_at：want %v got %v", created, st2.CreatedAt)
	}
	got, err := cat.ListStyles()
	if err != nil || len(got) != 1 || got[0].Name != "测试条目改" || !got[0].Published {
		t.Fatalf("ListStyles: %v %+v", err, got)
	}
	if len(got[0].Tags.Aux) != 0 || got[0].Tags.Aux == nil {
		t.Fatalf("空 aux 应为空数组不是 NULL: %+v", got[0].Tags)
	}

	// publish / delete
	pk, err := cat.MarkPublished("nope")
	if err == nil || pk != nil {
		t.Fatal("不存在条目 publish 应 ErrNoEntry")
	}
	if err := cat.DeleteStyle("nope"); err == nil {
		t.Fatal("不存在条目 delete 应 ErrNoEntry")
	}
	if err := cat.DeleteStyle("vid-test-pg"); err != nil {
		t.Fatalf("DeleteStyle: %v", err)
	}
	if e, _ := cat.Empty(); !e {
		t.Fatal("删后应为空表")
	}
	if err := cat.db.Exec(`DELETE FROM style_vocab WHERE id = 'test_bauhaus'`).Error; err != nil {
		t.Fatal(err)
	}
}

// TestMinioMediaIntegration MinIO 媒体往返：Put/Open/Stat/List/DeletePrefix/Localize。
// 配 TEST_MINIO_ENDPOINT/ACCESS_KEY/SECRET_KEY/BUCKET 跑，否则跳过。
func TestMinioMediaIntegration(t *testing.T) {
	endpoint := os.Getenv("TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_MINIO_ENDPOINT 未配置，跳过 MinIO 集成测试")
	}
	mm, err := NewMinioMedia(endpoint,
		os.Getenv("TEST_MINIO_ACCESS_KEY"), os.Getenv("TEST_MINIO_SECRET_KEY"),
		getEnvOr("TEST_MINIO_BUCKET", "zhenshu"), false, t.TempDir())
	if err != nil {
		t.Fatalf("连 MinIO: %v", err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "frame-0.8.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mm.Put("vid-test-minio", "frame-0.8.png", filepath.Join(src, "frame-0.8.png")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !mm.Stat("vid-test-minio", "frame-0.8.png") {
		t.Fatal("Stat 应命中")
	}
	rc, size, _, err := mm.Open("vid-test-minio", "frame-0.8.png")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if size != 3 {
		t.Fatalf("size=%d", size)
	}
	if _, err := rc.Seek(1, 0); err != nil {
		t.Fatalf("对象流须可 Seek: %v", err)
	}
	rc.Close()
	names, err := mm.List("vid-test-minio")
	if err != nil || len(names) != 1 || names[0] != "frame-0.8.png" {
		t.Fatalf("List: %v %v", names, err)
	}
	// Localize 读穿：缓存落本地再回路径
	p, err := mm.Localize("vid-test-minio", "frame-0.8.png")
	if err != nil {
		t.Fatalf("Localize: %v", err)
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != "png" {
		t.Fatalf("Localize 落地内容错: %v %q", err, b)
	}
	if err := mm.DeletePrefix("vid-test-minio"); err != nil {
		t.Fatalf("DeletePrefix: %v", err)
	}
	if mm.Stat("vid-test-minio", "frame-0.8.png") {
		t.Fatal("删后 Stat 不应命中")
	}
}

func getEnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
