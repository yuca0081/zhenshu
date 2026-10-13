package produce

// 素材工位（真图按需申请制）：场景生成两段式——第一段模型自己判断这段画面要不要
// 真图，要就交素材申请单（英文检索词 + 用途一句话）；平台拿申请单去 Met 开放藏品
// 检索（公版）→ 下载压规格进 assets/img/ + assets/materials.json 归档 → 回图让模型
// 出带图终稿。素材是加分项：不申请/检索落空/任何失败都按无素材继续（第一段式的
// 画面稿直接生效），不挡出片。场景 HTML 对 assets/img/ 的引用仍过白名单校验防裂图
// （契约 A4 本就允许引用 assets/img/ 本地图）。贴图的呈现方式（裁剪/构图/边框/角度）
// 不设配方，由模型按本段画面与本片设计系统自己定。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"webpage-video-agent/server/internal/pipeline"
)

const (
	materialMaxPerReq   = 2        // 一段一次申请单上限
	materialMaxTotal    = 24       // 全片素材上限（防跑飞）
	materialDownloadMax = 64 << 20 // 原图下载上限 64MB
	materialWidthMax    = 1600     // 压图目标宽
)

// Material 一条真图素材（file 是项目根相对路径，场景 <img src> 直用）。
type Material struct {
	File      string `json:"file"`
	Seg       string `json:"seg"`             // 首次申请段（可跨段复用：登记簿按检索词共享）
	Query     string `json:"query,omitempty"` // 检索词
	Source    string `json:"source"`          // met
	ObjectID  int    `json:"object_id,omitempty"`
	Title     string `json:"title,omitempty"`
	Artist    string `json:"artist,omitempty"`
	Date      string `json:"date,omitempty"`
	Credit    string `json:"credit,omitempty"` // 授权/馆藏来源行
	OriginURL string `json:"origin_url,omitempty"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	Note      string `json:"note,omitempty"` // 申请时给的用途一句话
}

// MaterialStore 素材库落盘（assets/materials.json，按需追加）。
type MaterialStore struct {
	Materials []Material `json:"materials"`
	PlannedAt time.Time  `json:"planned_at"`
	Notes     string     `json:"notes,omitempty"`
}

func materialsPath(p *pipeline.Project) string { return p.Artifact("assets/materials.json") }

// matStoreMu 登记簿读改写串行化：sceneGen 段间并发（SCENE_CONCURRENCY），fulfillRequests
// 会并发追加入库。
var matStoreMu sync.Mutex

// loadMaterials 读素材库（无文件/损坏返回 nil——素材永远可选）。
func loadMaterials(p *pipeline.Project) *MaterialStore {
	var st MaterialStore
	if b, err := os.ReadFile(materialsPath(p)); err == nil && json.Unmarshal(b, &st) == nil {
		return &st
	}
	return nil
}

// saveMaterials 登记簿落盘（调用方持有 matStoreMu）。
func saveMaterials(p *pipeline.Project, st *MaterialStore) error {
	if err := os.MkdirAll(filepath.Dir(materialsPath(p)), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(materialsPath(p), b, 0o644)
}

// materialFiles 素材白名单（相对路径集合）。
func materialFiles(st *MaterialStore) map[string]bool {
	out := map[string]bool{}
	if st == nil {
		return out
	}
	for _, m := range st.Materials {
		out[m.File] = true
	}
	return out
}

// materialsForSeg 登记簿里首次申请段是 segID 的素材——本段此前申请到的真图，
// 重生成/修复轮直接带图，不再走申请单。
func materialsForSeg(p *pipeline.Project, segID string) []Material {
	st := loadMaterials(p)
	if st == nil {
		return nil
	}
	var out []Material
	for _, m := range st.Materials {
		if m.Seg == segID {
			out = append(out, m)
		}
	}
	return out
}

// materialRequest 场景生成第一段式提交的素材申请单条目。
type materialRequest struct {
	Query string `json:"query"` // Met 检索词（具体英文实物名词）
	Why   string `json:"why"`   // 画面哪里要用（一句话）
}

// cleanRequests 清洗申请单：去空词去重、用途截断、夹到单次上限。
func cleanRequests(reqs []materialRequest) []materialRequest {
	var out []materialRequest
	seen := map[string]bool{}
	for _, req := range reqs {
		q := strings.TrimSpace(req.Query)
		if q == "" || seen[strings.ToLower(q)] {
			continue
		}
		seen[strings.ToLower(q)] = true
		out = append(out, materialRequest{Query: q, Why: pipeline.TruncRunes(strings.TrimSpace(req.Why), 40)})
		if len(out) >= materialMaxPerReq {
			break
		}
	}
	return out
}

// fulfillRequests 处理一段的申请单：登记簿里同检索词已采过的直接复用（跨段共享），
// 未命中的走 Met 检索下载入库。逐条独立，失败跳过（manifest 记录），全落空返回空
// ——调用方按无素材继续（第一段式画面稿生效）。
func (r *Runner) fulfillRequests(ctx context.Context, p *pipeline.Project, segID string, reqs []materialRequest) []Material {
	if len(reqs) == 0 {
		return nil
	}
	client := newMetClient()
	var out []Material
	for _, req := range reqs {
		if ctx.Err() != nil {
			break
		}
		matStoreMu.Lock()
		// 复用双检：查先（免下载）；下载后再查（并发段可能刚采了同词）
		if m, ok := findByQuery(p, req.Query); ok {
			matStoreMu.Unlock()
			out = append(out, m)
			continue
		}
		matStoreMu.Unlock()
		m, err := client.fetchOne(ctx, p, segID, req.Query, req.Why, 0)
		if err != nil {
			r.Manifest(p, "materials.skip", segID+" "+req.Query+": "+pipeline.FirstLine(err.Error(), 100))
			continue
		}
		matStoreMu.Lock()
		st := loadMaterials(p)
		if st == nil {
			st = &MaterialStore{PlannedAt: time.Now()}
		}
		if dup, ok := findByQuery(p, req.Query); ok { // 并发同词：丢本次下载复用已入库的
			matStoreMu.Unlock()
			out = append(out, dup)
			continue
		}
		if len(st.Materials) >= materialMaxTotal {
			matStoreMu.Unlock()
			r.Manifest(p, "materials.skip", segID+" "+req.Query+": 全片素材到顶 "+fmt.Sprint(materialMaxTotal))
			continue
		}
		st.Materials = append(st.Materials, m)
		err = saveMaterials(p, st)
		matStoreMu.Unlock()
		if err != nil {
			r.Manifest(p, "materials.error", err.Error())
			continue
		}
		out = append(out, m)
		r.emit(p.ID, "progress", "compositions", fmt.Sprintf("%s 素材检索 %q ✓（%s）", segID, req.Query, m.Title))
	}
	return out
}

// findByQuery 登记簿里找同检索词条目（大小写不敏感；调用方持有 matStoreMu）。
func findByQuery(p *pipeline.Project, query string) (Material, bool) {
	st := loadMaterials(p)
	if st == nil {
		return Material{}, false
	}
	for _, m := range st.Materials {
		if strings.EqualFold(strings.TrimSpace(m.Query), query) {
			return m, true
		}
	}
	return Material{}, false
}

// materialAskBlock 第一段式的申请单机制说明（本段暂无素材时注入）。
const materialAskBlock = `
## 真图素材申请（可选）
- 本段画面若需要**真实世界的实物图像**（历史实物/名画/雕像/建筑/遗址等照片级素材），
  在输出 JSON 里加 "image_requests":[{"query":"英文检索词","why":"画面哪里要用一句话"}]，最多 2 条
- 检索词用具体英文实物名词（如 "socrates bust"、"greek amphora chariot"、"parthenon temple"），
  别用抽象词；图源是大都会博物馆公版藏品库，检索结果不保证贴合——平台回图后你再决定用不用
- 画面用程序化图形就够的段（抽象概念/图表/氛围/人物剪影）不要申请，省略 image_requests 即可
`

// materialsNote 场景生成提示里的本段素材块（清单+引用语法，呈现方式不设配方）。
func materialsNote(mats []Material) string {
	if len(mats) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## 本段真图素材（公版，已落盘，可直接引用）\n")
	for _, m := range mats {
		line := "- `" + m.File + "`｜" + m.Title
		if m.Artist != "" {
			line += "（" + m.Artist + "）"
		}
		if m.Date != "" {
			line += "·" + m.Date
		}
		if m.Width > 0 {
			line += fmt.Sprintf("｜原图 %d×%d", m.Width, m.Height)
		}
		if m.Note != "" {
			line += "｜申请用途：" + m.Note
		}
		b.WriteString(line + "\n")
	}
	b.WriteString(`- 引用语法：<img src="assets/img/文件名">，**只准引用上面清单内的文件**（清单外引用会被校验拒绝）
- 用不用、选哪张、怎么裁剪构图与呈现（满幅/局部/形状/角度全由你按本段画面和本片设计系统定）；禁止拉伸变形；没有合适的就一张都不用
`)
	return b.String()
}

// materialNote 素材真图的视觉注入说明（图附在样张之后）。
func materialNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("\n- 附图最后 %d 张是本段素材真图（公版，清单见素材块）：看清内容再决定用不用、怎么用\n", n)
}

// materialDataURLs 素材图 → data URL（注入场景生成的视觉上下文，让模型看着真图定摆位）。
func materialDataURLs(p *pipeline.Project, mats []Material) []string {
	var urls []string
	for _, m := range mats {
		b, err := os.ReadFile(p.Artifact(filepath.FromSlash(m.File)))
		if err != nil {
			continue
		}
		urls = append(urls, "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(b))
	}
	return urls
}

// assetsImgSrcRe 场景 HTML 的图片引用（包级预编译：sceneValidate 每段×每修复轮都跑）。
var assetsImgSrcRe = regexp.MustCompile(`src="assets/img/([^"]+)"`)

// materialWhitelistCheck 场景 HTML 里 assets/img/ 引用必须命中素材清单（清单外文件
// 不存在，渲染必裂图）。无素材库的项目跳过。
func materialWhitelistCheck(p *pipeline.Project, html string) []string {
	st := loadMaterials(p)
	if st == nil || len(st.Materials) == 0 {
		return nil
	}
	allow := materialFiles(st)
	var errs []string
	seen := map[string]bool{}
	for _, m := range assetsImgSrcRe.FindAllStringSubmatch(html, -1) {
		rel := "assets/img/" + m[1]
		if !allow[rel] && !seen[rel] {
			seen[rel] = true
			errs = append(errs, fmt.Sprintf("图片引用不在素材清单: %s（只准引用素材块列出的文件）", rel))
		}
	}
	return errs
}

// ── Met 开放藏品客户端 ────────────────────────────────────────────

type metClient struct {
	base string
	http *http.Client
}

func newMetClient() *metClient {
	base := os.Getenv("MET_API_BASE")
	if base == "" {
		base = "https://collectionapi.metmuseum.org/public/collection"
	}
	return &metClient{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 60 * time.Second}}
}

type metObject struct {
	ObjectID          int    `json:"objectID"`
	Title             string `json:"title"`
	ArtistDisplayName string `json:"artistDisplayName"`
	ObjectDate        string `json:"objectDate"`
	CreditLine        string `json:"creditLine"`
	PrimaryImage      string `json:"primaryImage"`
	IsPublicDomain    bool   `json:"isPublicDomain"`
}

// search v1.1 检索（v1 search 已于 2026-10 退役）。
func (m *metClient) search(ctx context.Context, q string) ([]int, error) {
	var out struct {
		ObjectIDs []int `json:"objectIDs"`
	}
	if err := m.getJSON(ctx, fmt.Sprintf("%s/v1.1/search?hasImages=true&limit=40&q=%s", m.base, url.QueryEscape(q)), &out); err != nil {
		return nil, err
	}
	return out.ObjectIDs, nil
}

func (m *metClient) object(ctx context.Context, id int) (*metObject, error) {
	var o metObject
	err := m.getJSON(ctx, fmt.Sprintf("%s/v1/objects/%d", m.base, id), &o)
	if err != nil { // Met 有突发限流：隔半秒重试一次（睡眠可被取消打断）
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		err = m.getJSON(ctx, fmt.Sprintf("%s/v1/objects/%d", m.base, id), &o)
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func (m *metClient) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "zhenshu-video-agent/1.0")
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("met api %d: %s", resp.StatusCode, url)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// fetchOne 检索→挑公版→下载压图→返回登记条目（入库由调用方做，登记簿要串行化）。
// i 是该检索词第几张（0 起），决定文件名序号。
func (m *metClient) fetchOne(ctx context.Context, p *pipeline.Project, seg, query, use string, idx int) (Material, error) {
	ids, err := m.search(ctx, query)
	if err != nil {
		return Material{}, fmt.Errorf("检索失败: %w", err)
	}
	// 同检索词多张时错位取（第 idx 个公版结果），避免多张同名
	offset := 0
	for got := 0; got <= idx && offset < len(ids); offset++ {
		if offset > 0 {
			// 节流：object 是逐个抓的；select 版睡眠可被取消打断（与 object 重试同款）
			select {
			case <-time.After(250 * time.Millisecond):
			case <-ctx.Done():
				return Material{}, ctx.Err()
			}
		}
		o, err := m.object(ctx, ids[offset])
		if err != nil || !o.IsPublicDomain || o.PrimaryImage == "" {
			continue
		}
		if got == idx {
			return m.download(ctx, p, seg, query, use, idx, o)
		}
		got++
	}
	return Material{}, fmt.Errorf("公版结果不足（%q 公版候选耗尽）", query)
}

func (m *metClient) download(ctx context.Context, p *pipeline.Project, seg, query, use string, idx int, o *metObject) (Material, error) {
	dir := p.Artifact("assets/img")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Material{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", o.PrimaryImage, nil)
	if err != nil {
		return Material{}, err
	}
	req.Header.Set("User-Agent", "zhenshu-video-agent/1.0")
	resp, err := m.http.Do(req)
	if err != nil {
		return Material{}, fmt.Errorf("下载原图失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Material{}, fmt.Errorf("下载原图 %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp("", "material-*.img")
	if err != nil {
		return Material{}, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close() // defer 后进先出：先关句柄再删文件——注册顺序反了的话 Remove
	// 先跑而句柄还开着，Windows 上必失败，错误路径的临时文件照样漏
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, materialDownloadMax))
	if err != nil {
		return Material{}, fmt.Errorf("下载原图失败: %w", err)
	}
	if n == materialDownloadMax {
		// 恰好顶到 64MB 上限不可区分「真有这么大」与「被截断」，而截断图压图/渲染
		// 必炸——按下载失败处理（真 ≥64MB 的公版原图罕见，宁缺毋滥）
		return Material{}, fmt.Errorf("原图达到下载上限 %d MB，疑似截断，放弃", materialDownloadMax>>20)
	}
	if n <= 256 { // 只防空响应；真垃圾交给 ffmpeg 压图环节拒绝
		return Material{}, fmt.Errorf("原图过小（%d B）", n)
	}
	tmp.Close()

	// 压图：宽 ≤1600 保比例，JPEG q4（确定性规格）
	file := fmt.Sprintf("%s-%d-%s.jpg", seg, idx+1, slugify(o.Title, o.ObjectID))
	dst := filepath.Join(dir, file)
	ff, err := pipeline.FFmpegPath()
	if err != nil {
		return Material{}, err
	}
	cmd := pipeline.CommandContext(ctx, ff, "-y", "-v", "error", "-i", tmp.Name(),
		"-vf", fmt.Sprintf("scale='min(%d,iw)':-2", materialWidthMax), "-q:v", "4", dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		return Material{}, fmt.Errorf("压图失败: %s\n%s", err, pipeline.TailLines(string(out), 5))
	}
	f, err := os.Open(dst)
	if err != nil {
		return Material{}, err
	}
	cfg, _, err := image.DecodeConfig(f)
	f.Close()
	if err != nil {
		return Material{}, fmt.Errorf("压图结果不可读: %w", err)
	}
	return Material{
		File: "assets/img/" + file, Seg: seg, Query: query, Source: "met",
		ObjectID: o.ObjectID, Title: o.Title, Artist: strings.TrimSpace(o.ArtistDisplayName),
		Date: o.ObjectDate, Credit: o.CreditLine, OriginURL: o.PrimaryImage,
		Width: cfg.Width, Height: cfg.Height, Note: use,
	}, nil
}

// ── 小工具 ────────────────────────────────────────────────────────

var reSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(title string, objectID int) string {
	s := strings.ToLower(title)
	s = reSlug.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 24 { // 按连字符边界截断，别把词切一半
		if i := strings.LastIndex(s[:24], "-"); i > 0 {
			s = s[:i]
		} else {
			s = s[:24]
		}
	}
	s = strings.Trim(s, "-")
	if s == "" {
		return fmt.Sprintf("obj%d", objectID)
	}
	return fmt.Sprintf("%s-%d", s, objectID)
}
