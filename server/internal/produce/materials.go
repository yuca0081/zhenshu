package produce

// 素材工位（真图拼贴材料链）：分镜定稿后，素材策划（LLM，每片一次）挑出值得贴真图的段
// → Met 开放藏品检索（公版，API 可达性见 e2e 实测：本机 wikimedia 不通、met 通）→
// 下载压规格进 assets/img/ + assets/materials.json 归档授权 → 场景生成注入素材清单与
// 真图（模型撕边拼贴入画，引用白名单校验防裂图）。契约 A4（docs/contract.md）本就允许
// 引用 assets/img/ 本地图——口子是设计内的，这里补的是检索与注入。
// 素材是加分项：任何一步失败都降级为无素材继续（与真迹同纪律），不挡出片。

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
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"webpage-video-agent/server/internal/llm"
	"webpage-video-agent/server/internal/pipeline"
)

const (
	materialMaxPerSeg   = 3        // 每段素材上限
	materialMaxTotal    = 12       // 全片素材上限（防跑飞）
	materialDownloadMax = 64 << 20 // 原图下载上限 64MB
	materialWidthMax    = 1600     // 压图目标宽
)

// Material 一条真图素材（file 是项目根相对路径，场景 <img src> 直用）。
type Material struct {
	File      string `json:"file"`
	Seg       string `json:"seg"`             // 策划建议用段（可跨段复用）
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
	Note      string `json:"note,omitempty"` // 策划给的用法一句话
}

// MaterialStore 素材库落盘（assets/materials.json）。
type MaterialStore struct {
	Materials []Material `json:"materials"`
	PlannedAt time.Time  `json:"planned_at"`
	Notes     string     `json:"notes,omitempty"`
}

func materialsPath(p *pipeline.Project) string { return p.Artifact("assets/materials.json") }

// loadMaterials 读素材库（无文件/损坏返回 nil——素材永远可选）。
func loadMaterials(p *pipeline.Project) *MaterialStore {
	var st MaterialStore
	if b, err := os.ReadFile(materialsPath(p)); err == nil && json.Unmarshal(b, &st) == nil {
		return &st
	}
	return nil
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

// materialNeeds 素材策划（LLM）作业：哪些段值得贴真图、用什么检索词。
type materialNeeds struct {
	Needs []struct {
		Seg   string `json:"seg"`
		Query string `json:"query"`
		Count int    `json:"count"`
		Use   string `json:"use"`
	} `json:"needs"`
	Notes string `json:"notes,omitempty"`
}

// ensureMaterials 素材工位入口。required=true 全量策划+抓取（produce 首跑）；
// required=false 只加载已有（rework 不现抓网络）。任何失败降级为无素材，不挡出片。
func (r *Runner) ensureMaterials(ctx context.Context, p *pipeline.Project, required bool) {
	if _, err := os.Stat(materialsPath(p)); err == nil {
		return // 已建库（幂等）
	}
	if !required {
		return
	}
	if err := ctx.Err(); err != nil {
		return
	}
	id := p.ID
	r.emit(id, "stage", "materials", "running")
	r.Manifest(p, "stage.start", "materials")
	defer func() {
		if st := loadMaterials(p); st != nil && len(st.Materials) > 0 {
			r.emit(id, "stage", "materials", fmt.Sprintf("done（%d 张）", len(st.Materials)))
			r.Manifest(p, "stage.finish", "materials")
		} else {
			r.emit(id, "stage", "materials", "skipped（无素材需求或获取失败，画面走程序化）")
			r.Manifest(p, "stage.finish", "materials")
		}
	}()

	needs, err := r.planMaterials(ctx, p)
	if err != nil {
		r.Manifest(p, "materials.plan_error", pipeline.FirstLine(err.Error(), 120))
		return
	}
	if len(needs.Needs) == 0 {
		r.Manifest(p, "materials.plan", "no-needs")
		return
	}

	client := newMetClient()
	st := &MaterialStore{PlannedAt: time.Now(), Notes: needs.Notes}
	perSeg := map[string]int{}
	for _, n := range needs.Needs {
		if ctx.Err() != nil {
			break
		}
		if perSeg[n.Seg] >= materialMaxPerSeg || len(st.Materials) >= materialMaxTotal {
			break
		}
		count := n.Count
		if count < 1 {
			count = 1
		}
		if count > materialMaxPerSeg-perSeg[n.Seg] {
			count = materialMaxPerSeg - perSeg[n.Seg]
		}
		for i := 0; i < count; i++ {
			m, err := client.fetchOne(ctx, p, n.Seg, n.Query, n.Use, perSeg[n.Seg])
			if err != nil {
				r.Manifest(p, "materials.skip", n.Seg+": "+pipeline.FirstLine(err.Error(), 100))
				continue
			}
			st.Materials = append(st.Materials, m)
			perSeg[n.Seg]++
			if len(st.Materials) >= materialMaxTotal {
				break
			}
		}
	}
	if len(st.Materials) == 0 {
		r.Manifest(p, "materials.result", "0 张（检索全部落空）")
		return
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	if err := os.MkdirAll(filepath.Dir(materialsPath(p)), 0o755); err != nil {
		r.Manifest(p, "materials.error", err.Error())
		return
	}
	if err := os.WriteFile(materialsPath(p), b, 0o644); err != nil {
		r.Manifest(p, "materials.error", err.Error())
		return
	}
	r.Manifest(p, "materials.result", fmt.Sprintf("%d 张", len(st.Materials)))
}

// planMaterials 素材策划：LLM 通读分镜，挑段+给英文检索词（Met 库是英文）。
func (r *Runner) planMaterials(ctx context.Context, p *pipeline.Project) (*materialNeeds, error) {
	sb, err := p.LoadStoryboard()
	if err != nil {
		return nil, fmt.Errorf("需要分镜: %w", err)
	}
	prov, err := llm.FromEnv(llm.RoleVisual)
	if err != nil {
		return nil, fmt.Errorf("素材策划需要 API 模式: %w", err)
	}
	var b strings.Builder
	for _, seg := range sb.Segments {
		fmt.Fprintf(&b, "- %s｜%s｜画面提示：%s\n", seg.ID, seg.Key, runeTrunc(seg.VisualBrief, 60))
	}
	var out materialNeeds
	_, err = prov.GenerateJSONNoThink(ctx, "你是短视频的视觉素材策划，只输出 JSON。", fmt.Sprintf(`下面是分镜清单。判断哪些段值得贴**真图拼贴素材**（名画/雕像/陶器/遗址/历史实物照片，来自大都会博物馆开放藏品库，全部公版），哪些段程序化图形就够（抽象概念/图表/氛围/人物剪影）。

## 分镜
%s
## 输出（只输出 JSON，无围栏）
{"needs":[{"seg":"segNN","query":"英文检索词","count":1,"use":"这条素材在这段怎么用（人话 ≤30字）"}],"notes":"整体素材策略一句话"}

纪律：
- 宁缺毋滥：只有画面里出现**具体历史实物/人物/场景**的段才要；全片 ≤10 条
- query 是 met 藏品库检索词，用具体英文名词（如 "socrates bust"、"greek amphora chariot"、"parthenon temple"），别用抽象词（如 "philosophy"）
- 同一实物只搜一次；count 一般 1（备选 2）
- 一段都没有就给 {"needs":[]}
`, b.String()), &out)
	if err != nil {
		return nil, fmt.Errorf("素材策划失败: %w", err)
	}
	// 清洗：seg 必须在分镜内，count 夹取
	valid := map[string]bool{}
	for _, seg := range sb.Segments {
		valid[seg.ID] = true
	}
	clean := &materialNeeds{Notes: out.Notes}
	for _, n := range out.Needs {
		if !valid[n.Seg] || strings.TrimSpace(n.Query) == "" {
			continue
		}
		if n.Count < 1 {
			n.Count = 1
		}
		if n.Count > materialMaxPerSeg {
			n.Count = materialMaxPerSeg
		}
		clean.Needs = append(clean.Needs, n)
	}
	return clean, nil
}

// materialsNote 场景生成提示里的素材块（清单+拼贴语法纪律）。无素材返回 ""。
func materialsNote(p *pipeline.Project) string {
	st := loadMaterials(p)
	if st == nil || len(st.Materials) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## 本片真图拼贴素材（公版，已落盘 assets/img/，可直接引用）\n")
	for _, m := range st.Materials {
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
			line += "｜建议用途：" + m.Note
		}
		b.WriteString(line + "\n")
	}
	b.WriteString(`- 引用语法：<img src="assets/img/文件名">，**只准引用上面清单内的文件**（清单外引用会被校验拒绝）
- 拼贴语法：撕边 clip-path + 米白纸衬底 + 胶带/图钉固定 + 轻微旋转 + 底部说明行（作者/年代），照片当**拼贴材料**按构图摆，不是满屏壁纸；禁止拉伸变形
- 素材可跨段复用；没有合适素材就照常程序化画，别硬凑
`)
	return b.String()
}

// materialNote 素材真图的视觉注入说明（图附在样张之后）。
func materialNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("\n- 附图最后 %d 张是本片素材库的真图（公版，已落盘 assets/img/，清单见素材块）：看清内容再决定哪张进画面，摆位与裁剪按构图需要（object-fit/clip-path 撕边），别整幅满屏\n", n)
}

// materialImages 素材图 → data URL（注入场景生成的视觉上下文，让模型看着真图摆位置）。
func materialImages(p *pipeline.Project) []string {
	st := loadMaterials(p)
	if st == nil {
		return nil
	}
	var urls []string
	for _, m := range st.Materials {
		if len(urls) >= 6 {
			break
		}
		b, err := os.ReadFile(p.Artifact(filepath.FromSlash(m.File)))
		if err != nil {
			continue
		}
		urls = append(urls, "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(b))
	}
	return urls
}

// materialWhitelistCheck 场景 HTML 里 assets/img/ 引用必须命中素材清单（清单外文件
// 不存在，渲染必裂图）。无素材库的项目跳过。
func materialWhitelistCheck(p *pipeline.Project, html string) []string {
	st := loadMaterials(p)
	if st == nil || len(st.Materials) == 0 {
		return nil
	}
	allow := materialFiles(st)
	var errs []string
	re := regexp.MustCompile(`src="assets/img/([^"]+)"`)
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(html, -1) {
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
	if err != nil { // Met 有突发限流：隔半秒重试一次
		time.Sleep(500 * time.Millisecond)
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

// fetchOne 检索→挑公版→下载压图→登记。i 是该段第几张（0 起），决定文件名序号；
// 检索词跨段去重由调用方保证（same query repeated 会在 skip 清单里暴露）。
func (m *metClient) fetchOne(ctx context.Context, p *pipeline.Project, seg, query, use string, idx int) (Material, error) {
	ids, err := m.search(ctx, query)
	if err != nil {
		return Material{}, fmt.Errorf("检索失败: %w", err)
	}
	// 同段同一检索词多张时错位取（第 idx 个公版结果），避免三张同名
	offset := 0
	for got := 0; got <= idx && offset < len(ids); offset++ {
		if offset > 0 {
			time.Sleep(250 * time.Millisecond) // 节流：object 是逐个抓的
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
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, materialDownloadMax))
	if err != nil {
		return Material{}, fmt.Errorf("下载原图失败: %w", err)
	}
	if n <= 256 { // 只防空响应；真垃圾交给 ffmpeg 压图环节拒绝
		return Material{}, fmt.Errorf("原图过小（%d B）", n)
	}
	tmp.Close()

	// 压图：宽 ≤1600 保比例，JPEG q4（确定性规格，与真迹压图同思路）
	file := fmt.Sprintf("%s-%d-%s.jpg", seg, idx+1, slugify(o.Title, o.ObjectID))
	dst := filepath.Join(dir, file)
	ff, err := pipeline.FFmpegPath()
	if err != nil {
		return Material{}, err
	}
	cmd := exec.CommandContext(ctx, ff, "-y", "-v", "error", "-i", tmp.Name(),
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
