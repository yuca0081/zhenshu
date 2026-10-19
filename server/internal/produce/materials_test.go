package produce

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"webpage-video-agent/server/internal/events"
	"webpage-video-agent/server/internal/pipeline"
)

// 素材工位回归钉（真图按需申请制）。

// Met 客户端：检索→筛公版→下载原图→压图落盘，全程 stub API（不花真调用）。
func TestMetClientFetchOne(t *testing.T) {
	// 现编一张有效小 JPEG（ffmpeg 压图走真 ffmpeg，缺了跳过本测）
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, img, nil); err != nil {
		t.Fatal(err)
	}
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(jpg.Bytes())
	}))
	defer imgSrv.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/v1.1/search"):
			json.NewEncoder(w).Encode(map[string]any{"objectIDs": []int{11, 22, 33}})
		case strings.Contains(r.URL.Path, "/v1/objects/"):
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/v1/objects/"))
			pd := id != 22 // 22 号非公版，应被跳过
			json.NewEncoder(w).Encode(map[string]any{
				"objectID": id, "title": "Test Relief", "artistDisplayName": "Anon",
				"objectDate": "4th century BCE", "creditLine": "Gift of X",
				"isPublicDomain": pd, "primaryImage": imgSrv.URL + "/orig/" + strconv.Itoa(id) + ".jpg",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	dir := t.TempDir()
	p := &pipeline.Project{ID: "t", Dir: dir}
	t.Setenv("MET_API_BASE", strings.TrimSuffix(api.URL, "/public/collection"))
	c := newMetClient()
	m, err := c.fetchOne(context.Background(), p, "seg01", "test relief", "主视觉照片", 0)
	if err != nil {
		t.Fatalf("fetchOne: %v", err)
	}
	// 22 号非公版被跳过 → 取到的是 11 号
	if m.ObjectID != 11 || !strings.HasSuffix(m.File, "seg01-1-test-relief-11.jpg") {
		t.Fatalf("选物/命名不对: %+v", m)
	}
	if m.Source != "met" || m.Credit != "Gift of X" || m.OriginURL == "" {
		t.Fatalf("归档字段缺失: %+v", m)
	}
	if _, err := os.Stat(filepath.Join(dir, m.File)); err != nil {
		t.Fatalf("压图产物缺失: %v", err)
	}
	// 序号错位取（idx=1 → 第二个公版结果 = 33 号）
	m2, err := c.fetchOne(context.Background(), p, "seg01", "test relief", "", 1)
	if err != nil || m2.ObjectID != 33 || !strings.HasSuffix(m2.File, "seg01-2-test-relief-33.jpg") {
		t.Fatalf("idx 错位取不对: %+v err=%v", m2, err)
	}
	// 公版候选耗尽应报错而不是返回空素材
	if _, err := c.fetchOne(context.Background(), p, "seg02", "nothing", "", 5); err == nil {
		t.Fatalf("公版耗尽应报错")
	}
}

// 申请单清洗：空词/重复词剔除、用途截断、单次上限夹取。
func TestCleanRequests(t *testing.T) {
	got := cleanRequests([]materialRequest{
		{Query: "  socrates bust ", Why: "主视觉"},
		{Query: "", Why: "空词"},
		{Query: "SOCRATES BUST", Why: "重复词（大小写不敏感）"},
		{Query: "greek amphora", Why: strings.Repeat("长", 100)},
		{Query: "parthenon temple", Why: "超出上限应被夹掉"},
	})
	if len(got) != 2 {
		t.Fatalf("应剩 2 条（去重+上限）: %+v", got)
	}
	if got[0].Query != "socrates bust" {
		t.Fatalf("检索词应去空格: %q", got[0].Query)
	}
	if n := len([]rune(got[1].Why)); n > 41 { // TruncRunes(40) 带 … 后缀
		t.Fatalf("用途应截断到 40 字: %d", n)
	}
	if cleanRequests(nil) != nil {
		t.Fatalf("空申请单应返回空")
	}
}

// 申请单处理：登记簿同词直接复用（跨段共享，不发网络）；空词已被清洗层挡掉。
func TestFulfillRequestsReusesRegistry(t *testing.T) {
	dir := t.TempDir()
	p := &pipeline.Project{ID: "t", Dir: dir}
	st := &MaterialStore{PlannedAt: time.Now(), Materials: []Material{{
		File: "assets/img/seg01-1-bust-11.jpg", Seg: "seg01", Query: "Socrates Bust",
		Source: "met", Title: "Bust of Socrates",
	}}}
	matStoreMu.Lock()
	if err := saveMaterials(p, st); err != nil {
		t.Fatal(err)
	}
	matStoreMu.Unlock()

	r := &Runner{Hub: events.NewHub()}
	got := r.fulfillRequests(context.Background(), p, "seg03", []materialRequest{
		{Query: "socrates bust", Why: "主视觉"},
	})
	if len(got) != 1 || got[0].File != "assets/img/seg01-1-bust-11.jpg" {
		t.Fatalf("同词应复用登记簿素材: %+v", got)
	}
	// 复用不改登记簿（Seg 仍是首次申请段）
	if got2 := materialsForSeg(p, "seg03"); len(got2) != 0 {
		t.Fatalf("复用不应改归属段: %+v", got2)
	}
	if got3 := materialsForSeg(p, "seg01"); len(got3) != 1 {
		t.Fatalf("本段素材应可按段取回: %+v", got3)
	}
}

// 素材库落盘后：清单块渲染、引用白名单两件齐活；清单外引用必须被拒；无素材全量静默。
func TestMaterialsNoteAndWhitelist(t *testing.T) {
	dir := t.TempDir()
	p := &pipeline.Project{ID: "t", Dir: dir}
	if materialsNote(nil) != "" || materialNote(0) != "" {
		t.Fatalf("无素材时应全量静默")
	}
	// 白名单校验：无素材库跳过
	if errs := materialWhitelistCheck(p, `<img src="assets/img/x.jpg">`); errs != nil {
		t.Fatalf("无素材库不应校验: %v", errs)
	}

	mats := []Material{{
		File: "assets/img/seg01-1-bust-11.jpg", Seg: "seg01", Title: "Test Relief", Source: "met",
		Artist: "Anon", Date: "4th c BCE", Width: 733, Height: 900, Note: "标题旁主视觉",
	}}
	b, _ := json.MarshalIndent(&MaterialStore{Materials: mats}, "", " ")
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(materialsPath(p), b, 0o644); err != nil {
		t.Fatal(err)
	}

	note := materialsNote(mats)
	for _, want := range []string{"assets/img/seg01-1-bust-11.jpg", "Test Relief", "标题旁主视觉", "只准引用上面清单内的文件"} {
		if !strings.Contains(note, want) {
			t.Fatalf("素材块缺 %q\n---\n%s", want, note)
		}
	}
	// 轻纪律：不再有写死的拼贴配方（撕边/胶带/旋转），呈现方式交还模型
	for _, banned := range []string{"撕边", "胶带", "图钉", "轻微旋转", "米白纸衬底"} {
		if strings.Contains(note, banned) {
			t.Fatalf("素材块不应含配方措辞 %q\n---\n%s", banned, note)
		}
	}
	// 白名单：清单内引用放行，清单外拒绝
	if errs := materialWhitelistCheck(p, `<img src="assets/img/seg01-1-bust-11.jpg">`); len(errs) != 0 {
		t.Fatalf("清单内引用被误拒: %v", errs)
	}
	errs := materialWhitelistCheck(p, `<img src="assets/img/seg01-1-bust-11.jpg"><img src="assets/img/hack.jpg">`)
	if len(errs) != 1 || !strings.Contains(errs[0], "hack.jpg") {
		t.Fatalf("清单外引用应被拒: %v", errs)
	}
	// 视觉注入：素材图文件缺失时静默跳过，不炸
	if imgs := materialVisImgs(p, mats); imgs != nil {
		t.Fatalf("文件缺失应返回 nil: %v", imgs)
	}
}

func TestMaterialNoteAndSlugify(t *testing.T) {
	n := materialNote(2)
	if !strings.Contains(n, "2 张") || !strings.Contains(n, "素材真图") {
		t.Fatalf("素材视觉注入说明不对: %s", n)
	}
	// 超长 slug 按连字符边界截断（不把词切一半）
	if got := slugify("The Death of Socrates, 1787", 436105); got != "the-death-of-socrates-436105" {
		t.Fatalf("slug 不对: %q", got)
	}
	if got := slugify("画", 7); got != "obj7" {
		t.Fatalf("纯非 ASCII 应回退 objectID: %q", got)
	}
	if got := slugify(fmt.Sprintf("%c", 0x4e00), 0); got != "obj0" {
		t.Fatalf("空 slug 应回退: %q", got)
	}
}
