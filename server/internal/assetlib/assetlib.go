// Package assetlib：素材库（docs/asset-library.md §4/§9）。
//
// 素材统一成一种条目（catalog.json 唯一索引），新增素材类型 = 加一个 type 值：
//
//	id / type(image|audio|video) / file(相对库根) / tags+desc(LLM 检索面)
//	/ origin(fetch|gen|upload|builtin|film) / meta(确定性代码提取)
//
// 磁盘为事实源（data/library/catalog.json + media/）；配置 PG+MinIO 后双写远程
// 镜像（remote.go）：入库即推，启动 SyncRemote 对账，容器重建/换机可拉回。
// catalog 写入只发生在本包（含 mutex + 临时文件原子替换），Python 侧只读
// catalog + 拷贝文件（render_spec 解析），远端存在与否对调用方无感。
package assetlib

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Entry 素材条目（§4 统一条目模型）。
type Entry struct {
	ID     string         `json:"id"`   // img-000123 / aud-calm；所有消费方只持 id
	Type   string         `json:"type"` // image | audio | video
	File   string         `json:"file"` // media/<type>/<id>.<ext>，相对库根
	Tags   []string       `json:"tags"`
	Desc   string         `json:"desc"`
	Origin string         `json:"origin"` // fetch | gen | upload | builtin | film
	Meta   map[string]any `json:"meta,omitempty"`
}

// Library 素材库（文件后端；dir = data/library）。
type Library struct {
	dir    string
	mu     sync.Mutex
	remote *Remote // PG+MinIO 镜像（可选；nil = 纯本地，行为与历史版本一致）
}

func New(dataDir string) *Library { return &Library{dir: filepath.Join(dataDir, "library")} }

// SetRemote 挂远程镜像（main 启动时连上才调；之后 Ingest/SeedBGM 自动双写）。
func (l *Library) SetRemote(r *Remote) { l.remote = r }

func (l *Library) Root() string        { return l.dir }
func (l *Library) catalogPath() string { return filepath.Join(l.dir, "catalog.json") }

// Load 全量条目（catalog 缺失 = 空库）。
func (l *Library) Load() ([]Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.load()
}

func (l *Library) load() ([]Entry, error) {
	b, err := os.ReadFile(l.catalogPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var es []Entry
	if err := json.Unmarshal(b, &es); err != nil {
		return nil, fmt.Errorf("catalog.json 损坏: %w", err)
	}
	return es, nil
}

func (l *Library) save(es []Entry) error {
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(es, "", "  ")
	if err != nil {
		return err
	}
	tmp := l.catalogPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, l.catalogPath())
}

var typePrefix = map[string]string{"image": "img", "audio": "aud", "video": "vid"}

// Ingest 素材入库：src 文件移入 media/<type>/，确定性提取 meta（尺寸/主色不做——
// 尺寸是硬事实，主色等 LLM 需求出现再说）。同 hash 幂等：返回已有条目。
func (l *Library) Ingest(typ, srcPath string, tags []string, desc, origin string, extra map[string]any) (*Entry, error) {
	prefix, ok := typePrefix[typ]
	if !ok {
		return nil, fmt.Errorf("未知素材类型 %q（image|audio|video）", typ)
	}
	b, err := os.ReadFile(srcPath)
	if err != nil {
		return nil, err
	}
	hash := contentHash(b)
	l.mu.Lock()
	defer l.mu.Unlock()
	es, err := l.load()
	if err != nil {
		return nil, err
	}
	for i := range es {
		if h, _ := es[i].Meta["hash"].(string); h == hash && es[i].Type == typ {
			return &es[i], nil // 同内容已入库
		}
	}
	ext := strings.ToLower(filepath.Ext(srcPath))
	if ext == "" {
		return nil, errors.New("素材缺扩展名")
	}
	id := fmt.Sprintf("%s-%06d", prefix, len(es)+1)
	for {
		clash := false
		for _, e := range es {
			if e.ID == id {
				clash = true
				break
			}
		}
		if !clash {
			break
		}
		id += "x" // 极端撞号（历史手工编号）时退让
	}
	rel := filepath.ToSlash(filepath.Join("media", typ, id+ext))
	dst := filepath.Join(l.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		return nil, err
	}
	meta := map[string]any{"hash": hash, "bytes": len(b), "ingested_at": time.Now().Format(time.RFC3339)}
	if typ == "image" {
		if w, h, ok := imageDims(b); ok {
			meta["w"], meta["h"] = w, h
		}
	}
	for k, v := range extra {
		meta[k] = v
	}
	e := Entry{ID: id, Type: typ, File: rel, Tags: dedupeTags(tags), Desc: desc, Origin: origin, Meta: meta}
	es = append(es, e)
	if err := l.save(es); err != nil {
		return nil, err
	}
	l.pushRemote(e, b)
	return &e, nil
}

// pushRemote 单条目镜像双写（PG 行 + MinIO 对象；body nil = 只登记行，对象
// 留给 SyncRemote 按缺补传——严禁拿空 body 上传覆盖好对象）。失败只 warn：
// 本地已落盘是事实源，镜像缺口由下次启动 SyncRemote 对账补齐，不值得挡入库。
// 调用方需持有 l.mu。
func (l *Library) pushRemote(e Entry, body []byte) {
	if l.remote == nil {
		return
	}
	if err := l.remote.PushEntry(e); err != nil {
		log.Printf("[assetlib] 镜像条目 %s 入 PG 失败（启动对账会补）: %v", e.ID, err)
		return
	}
	if body == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := l.remote.PutObject(ctx, e, bytes.NewReader(body), int64(len(body))); err != nil {
		log.Printf("[assetlib] 镜像对象 %s 上 MinIO 失败（启动对账会补）: %v", e.File, err)
	}
}

// Get 按 id 取条目。
func (l *Library) Get(id string) (Entry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	es, err := l.load()
	if err != nil {
		return Entry{}, false
	}
	for _, e := range es {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// Search 统一检索（§5）：type 圈范围、q 匹配 tags/desc（子串，大小写不敏感），
// q 空 = 该类型全量。返回按入库时间新在前，limit<=0 不限。
func (l *Library) Search(typ, q string, limit int) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	es, err := l.load()
	if err != nil {
		return nil
	}
	q = strings.ToLower(strings.TrimSpace(q))
	var out []Entry
	for i := len(es) - 1; i >= 0; i-- { // 倒序 = 新在前
		e := es[i]
		if typ != "" && e.Type != typ {
			continue
		}
		if q != "" {
			hit := strings.Contains(strings.ToLower(e.Desc), q)
			if !hit {
				for _, t := range e.Tags {
					if strings.Contains(strings.ToLower(t), q) {
						hit = true
						break
					}
				}
			}
			if !hit {
				continue
			}
		}
		out = append(out, e)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// Resolve id → 绝对路径（渲染/预览读文件用；不拷贝）。
func (l *Library) Resolve(id string) (string, error) {
	e, ok := l.Get(id)
	if !ok {
		return "", fmt.Errorf("素材 %s 不在 catalog", id)
	}
	p := filepath.Join(l.dir, filepath.FromSlash(e.File))
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("素材 %s 文件缺失: %w", id, err)
	}
	return p, nil
}

// ResolveInto id → 拷入项目目录（保持渲染本地性；§5）。
// 返回相对 destDir 的文件名 <id><ext>；幂等（已存在直接复用）。
// 本地库文件缺失且有远程镜像时先拉对象（跨机引导后首渲自动补货）。
func (l *Library) ResolveInto(id, destDir string) (string, error) {
	e, ok := l.Get(id)
	if !ok {
		return "", fmt.Errorf("素材 %s 不在 catalog", id)
	}
	name := e.ID + strings.ToLower(filepath.Ext(e.File))
	dst := filepath.Join(destDir, name)
	if _, err := os.Stat(dst); err == nil {
		return name, nil
	}
	src := filepath.Join(l.dir, filepath.FromSlash(e.File))
	if _, err := os.Stat(src); err != nil && l.remote != nil {
		l.mu.Lock()
		err = l.remote.FetchObject(context.Background(), e, src)
		l.mu.Unlock()
		if err != nil {
			return "", fmt.Errorf("素材 %s 本地缺失且远端拉取失败: %w", id, err)
		}
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		return "", err
	}
	return name, nil
}

// supportFiles 库载荷里走直读路径（不进 catalog）的支撑文件：BGM/sfx 清单与
// 音效本体。镜像必须带上，否则跨机恢复后 SeedBGM 读不到清单、stitch 缺 sfx。
var supportFiles = []string{
	"media/audio/tracks.json",
	"media/audio/sfx/sfx.json",
	"media/audio/sfx/whoosh.mp3",
	"media/audio/sfx/riser.mp3",
}

// SeedBGM 曲库入 catalog（origin=builtin，幂等按 file）。启动时调一次。
func (l *Library) SeedBGM() error {
	type track struct {
		Name     string  `json:"name"`
		File     string  `json:"file"`
		Duration float64 `json:"duration"`
		Mood     string  `json:"mood"`
	}
	b, err := os.ReadFile(filepath.Join(l.dir, "media", "audio", "tracks.json"))
	if err != nil {
		return nil // 曲库不存在 = 无 BGM 功能，不算错
	}
	var ts []track
	if json.Unmarshal(b, &ts) != nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	es, err := l.load()
	if err != nil {
		return err
	}
	changed := false
	var fresh []Entry
	for _, tr := range ts {
		rel := "media/audio/" + tr.File
		found := false
		for _, e := range es {
			if e.File == rel {
				found = true
				break
			}
		}
		if found {
			continue
		}
		if _, err := os.Stat(filepath.Join(l.dir, filepath.FromSlash(rel))); err != nil {
			continue
		}
		es = append(es, Entry{
			ID: "aud-" + strings.TrimSuffix(tr.File, filepath.Ext(tr.File)),
			Type: "audio", File: rel, Tags: []string{"BGM", tr.Mood, tr.Name},
			Desc: fmt.Sprintf("程序化合成 BGM《%s》（%s，%.0f 秒循环）", tr.Name, tr.Mood, tr.Duration),
			Origin: "builtin", Meta: map[string]any{"duration": tr.Duration, "mood": tr.Mood},
		})
		fresh = append(fresh, es[len(es)-1])
		changed = true
	}
	if !changed {
		return nil
	}
	if err := l.save(es); err != nil {
		return err
	}
	for _, e := range fresh {
		l.pushRemote(e, nil) // 对象由 SyncRemote 按缺补传（这里只先行登记行）
	}
	return nil
}

// SyncRemote 启动对账（挂了镜像才调）：双向补齐本地 catalog ↔ PG assets，
// 并保证本地条目对应的媒体文件齐全（缺的从 MinIO 拉；MinIO 也没有的 warn）。
// 冲突规则：同 id 双边 hash 不同 = 本地胜（磁盘是事实源），远端行/对象被覆盖。
func (l *Library) SyncRemote() error {
	if l.remote == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	local, err := l.load()
	if err != nil {
		return err
	}
	remoteEs, err := l.remote.ListEntries()
	if err != nil {
		return fmt.Errorf("读远端 assets: %w", err)
	}
	localByID := map[string]Entry{}
	for _, e := range local {
		localByID[e.ID] = e
	}
	remoteByID := map[string]Entry{}
	for _, e := range remoteEs {
		remoteByID[e.ID] = e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// ① 远端有、本地无：拉回（catalog + 对象落盘）
	pulled := 0
	for _, e := range remoteEs {
		if _, ok := localByID[e.ID]; ok {
			continue
		}
		if err := l.remote.FetchObject(ctx, e, filepath.Join(l.dir, filepath.FromSlash(e.File))); err != nil {
			log.Printf("[assetlib] 对账拉回 %s 失败: %v", e.ID, err)
			continue // 文件都拉不回的条目不进 catalog，避免悬空引用
		}
		local = append(local, e)
		localByID[e.ID] = e
		pulled++
	}
	// ② 本地有、远端无：推上行+对象。行已一致但对象缺失（上次推送被中断
	// 留下的半态）也要补传——所以一致行照样 HEAD 校验，库规模（百级）下开销可忽略。
	pushed := 0
	for _, e := range local {
		body, err := os.ReadFile(filepath.Join(l.dir, filepath.FromSlash(e.File)))
		if err != nil {
			continue // 本地文件缺失：③里统一 warn（推无可推）
		}
		re, ok := remoteByID[e.ID]
		if ok && metaHash(re) == metaHash(e) && l.remote.HasObject(ctx, e) {
			continue // 行+对象都一致
		}
		if ok && metaHash(re) != metaHash(e) {
			log.Printf("[assetlib] 对账冲突 %s（本地胜，远端被覆盖）", e.ID)
		}
		l.pushRemote(e, body)
		pushed++
	}
	// ③ 补齐本地缺文件（远端拉回的条目在①已落盘；这里兜底历史缺口）
	for _, e := range local {
		p := filepath.Join(l.dir, filepath.FromSlash(e.File))
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if err := l.remote.FetchObject(ctx, e, p); err != nil {
			log.Printf("[assetlib] %s 本地文件缺失且远端无对象: %v", e.ID, err)
		}
	}
	// ④ 支撑文件（清单/sfx，不在 catalog）：本地有就确保远端有，本地缺就从远端拉
	for _, rel := range supportFiles {
		e := Entry{ID: "support:" + rel, Type: "audio", File: rel}
		p := filepath.Join(l.dir, filepath.FromSlash(rel))
		if _, err := os.Stat(p); err == nil {
			if !l.remote.HasObject(ctx, e) {
				if body, rerr := os.ReadFile(p); rerr == nil {
					if perr := l.remote.PutObject(ctx, e, bytes.NewReader(body), int64(len(body))); perr != nil {
						log.Printf("[assetlib] 支撑文件 %s 推远失败: %v", rel, perr)
					}
				}
			}
		} else if l.remote.HasObject(ctx, e) {
			if err := l.remote.FetchObject(ctx, e, p); err != nil {
				log.Printf("[assetlib] 支撑文件 %s 拉回失败: %v", rel, err)
			} else {
				log.Printf("[assetlib] 支撑文件 %s 已从远端拉回", rel)
			}
		}
	}
	if pulled > 0 {
		if err := l.save(local); err != nil {
			return err
		}
	}
	log.Printf("[assetlib] 远程镜像对账完成：拉回 %d 条，推远 %d 条，本地共 %d 条", pulled, pushed, len(local))
	return nil
}

func metaHash(e Entry) string {
	h, _ := e.Meta["hash"].(string)
	return h
}

// TagsForQuery 搜索词 → tags（§5：fetch 图搜索词即天然 tags）。
// "艾尔登法环 玛莲妮亚 壁纸" → ["艾尔登法环","玛莲妮亚","壁纸","艾尔登法环 玛莲妮亚 壁纸"]。
func TagsForQuery(q string) []string {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil
	}
	var tags []string
	for _, t := range strings.FieldsFunc(q, func(r rune) bool { return r == ' ' || r == '　' || r == ',' || r == '，' }) {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	tags = append(tags, q)
	return dedupeTags(tags)
}

func dedupeTags(tags []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// imageDims 常见图格式尺寸（解码失败返回 ok=false，不挡入库）。
func imageDims(b []byte) (w, h int, ok bool) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

// contentHash 文件内容指纹（入库去重/幂等键）。FNV-1a 64bit 足够。
func contentHash(b []byte) string {
	var h uint64 = 14695981039346656037
	for _, c := range b {
		h ^= uint64(c)
		h *= 1099511628211
	}
	return fmt.Sprintf("%016x", h)
}
