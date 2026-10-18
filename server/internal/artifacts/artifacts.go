package artifacts

// 产物对象存储层：项目产物与参考库的「货」（正本）进 MinIO，本地目录降级为工作区+缓存。
// 设计文档 docs/storage-cloud-migration.md；双后端模式照抄 stylelib/media.go（风格库迁移已验证）。
//
// key 镜像磁盘布局：projects/<项目ID>/<项目内相对路径>、references/<refID>/<参考内相对路径>。
// 与风格库同一纪律：URL 永不入库、key 拼装不出后端——前端永远走 /api 路由由 Go 回源。
//
// 文件三分类（Classify）决定去留：Keeper 上传且平时留本地（契约与成品）；Discard 可再生成
// 不上传、完结清理时删本地；Garbage 纯垃圾随时删。安全不变量：清理任何 Keeper 大文件前必须
// Stat 确认远端正本在；MinIO 未接线（Available=false）时清理整体不运行。

import (
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Store 产物后端。MinIO 未配置时由 localStore 兜底：写操作退化为无操作、读操作直取
// 本地路径——demo 容器与本机开发的行为与现状完全一致。
type Store interface {
	// Put 覆盖式上传（来源是本地文件；localStore 为无操作）。调用方自保证文件已落稳。
	Put(key, srcPath string) error
	// Open 读对象本体（返回值可直接喂 http.ServeContent，minio.Object 自带 Seek）。
	Open(key string) (rc io.ReadSeekCloser, size int64, modTime time.Time, err error)
	// Stat 远端正本是否存在。
	Stat(key string) bool
	// Delete 删单个远端对象（本地作废工件时同步删正本，防回源复活陈旧文件）。
	Delete(key string) error
	// List 前缀下全部完整 key。
	List(prefix string) ([]string, error)
	// DeletePrefix 整前缀删除（删项目/参考时清对象；部分失败只报 error，孤儿货无害）。
	DeletePrefix(prefix string) error
	// Localize 确保 key 对应的本地文件可得并返回本地绝对路径——渲染子进程与多模态
	// 只认真实文件。本地有直接回；缺则从远端回源落地。localStore 不回源（本地即正本）。
	Localize(key string) (string, error)
	// Fetch 把远端对象下载到指定本地路径（Restore 的底层）。
	Fetch(key, dstPath string) error
	// Available 是否为远端正本后端。GC 清理只允许在 true 时运行。
	Available() bool
}

// ProjectKey / ReferenceKey 拼 key。rel 一律以 / 分隔（内部统一 ToSlash）。
func ProjectKey(projectID, rel string) string { return "projects/" + projectID + "/" + rel }
func ReferenceKey(refID, rel string) string   { return "references/" + refID + "/" + rel }

// Class 文件三分类。
type Class int

const (
	Keeper  Class = iota // 上传 + 平时留本地：契约 JSON、指纹、成品媒体
	Discard              // 不上传（可再生成/纯诊断），完结清理时删本地
	Garbage              // 不上传、随时无条件删（npm-cache、残留临时文件）
)

// Classify 以实体目录内相对路径判定类别。rel 接受本机分隔符，内部统一按 / 处理。
func Classify(rel string) Class {
	rel = filepath.ToSlash(rel)
	segs := strings.Split(rel, "/")
	base := segs[len(segs)-1]
	// 残留临时文件（tmpSuffixName/WriteFileAtomic 的中间态转正失败才会留下来）
	if strings.Contains(base, ".tmp.") || strings.HasPrefix(base, ".atomic-") {
		return Garbage
	}
	for _, s := range segs {
		if s == "npm-cache" {
			return Garbage
		}
	}
	// 可再生成的中间产物：QA/预审/回放截帧、渲染壳工程、预览目录
	for _, dir := range []string{"reports/frameqa", "reports/prereview", "reports/replay",
		"reports/director", ".hf-seg", ".hf-clip", "compositions/frames", "compositions/caps",
		"renders/preview"} {
		if rel == dir || strings.HasPrefix(rel, dir+"/") {
			return Discard
		}
	}
	// LLM 诊断日志：请求报文与会话过程记录，排查用，不参与缓存/恢复契约
	if strings.HasPrefix(rel, "llm/") {
		// 智能体对话留痕（「过程」页要回看）随正本走：上传且算 Keeper 小文件
		if strings.HasPrefix(rel, "llm/transcripts/") {
			return Keeper
		}
		if strings.HasSuffix(base, ".request.md") {
			return Discard
		}
		if strings.HasPrefix(base, "prep-") && strings.HasSuffix(base, ".jsonl") {
			return Discard
		}
	}
	return Keeper
}

// heavy 是否按扩展名判定为大文件（清理候选；删前须远端正本确认）。
func heavy(rel string) bool {
	switch strings.ToLower(rel[strings.LastIndex(rel, ".")+1:]) {
	case "mp4", "mov", "webm", "wav", "mp3", "flac", "png", "jpg", "jpeg", "webp", "gif",
		"ttf", "otf", "woff", "woff2":
		return true
	}
	return false
}

// ── localStore（MinIO 未接线的现状行为）─────────────────────────

// localStore 本地即正本：Put 无操作、读直取本地路径。root = DATA_DIR。
type localStore struct{ root string }

// NewLocal 构造本地后端。
func NewLocal(dataDir string) Store { return &localStore{root: dataDir} }

func (s *localStore) Put(key, srcPath string) error { return nil }
func (s *localStore) Available() bool               { return false }
func (s *localStore) LocalPath(key string) string {
	return filepath.Join(s.root, filepath.FromSlash(key))
}

func (s *localStore) Localize(key string) (string, error) {
	p := s.LocalPath(key)
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return "", fmt.Errorf("产物 %s 本地不存在", key)
	}
	return p, nil
}

func (s *localStore) Open(key string) (io.ReadSeekCloser, int64, time.Time, error) {
	f, err := os.Open(s.LocalPath(key))
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		f.Close()
		return nil, 0, time.Time{}, fmt.Errorf("stat %s: %w", key, err)
	}
	return f, st.Size(), st.ModTime(), nil
}

func (s *localStore) Stat(key string) bool {
	st, err := os.Stat(s.LocalPath(key))
	return err == nil && !st.IsDir()
}

func (s *localStore) Delete(key string) error {
	return os.Remove(s.LocalPath(key))
}

func (s *localStore) List(prefix string) ([]string, error) {
	dir := filepath.Join(s.root, filepath.FromSlash(prefix))
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // 前缀不存在 = 空清单
			}
			return err
		}
		if !d.IsDir() {
			rel, relErr := filepath.Rel(s.root, p)
			if relErr != nil {
				return relErr
			}
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out, err
}

func (s *localStore) DeletePrefix(prefix string) error {
	return os.RemoveAll(filepath.Join(s.root, filepath.FromSlash(prefix)))
}

func (s *localStore) Fetch(key, dstPath string) error {
	src := s.LocalPath(key)
	if _, err := os.Stat(src); err != nil {
		return err
	}
	return copyFile(src, dstPath)
}

// ── 批量操作（对两种后端一视同仁；localStore 的上传/回源自然退化为无操作或直取）──

// SyncUp 把 root/prefix 下本地有而远端缺的 Keeper 文件传上去（幂等，Stat 已在则跳过）。
// 返回上传文件数。单文件失败记日志继续——不打断制作，缺口由后续检查点或 -storage-sync 补。
func SyncUp(st Store, root, prefix string) (int, error) {
	if !st.Available() {
		return 0, nil
	}
	dir := filepath.Join(root, filepath.FromSlash(prefix))
	n := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		key := filepath.ToSlash(rel)
		if Classify(strings.TrimPrefix(key, prefix+"/")) != Keeper {
			return nil
		}
		if st.Stat(key) {
			return nil
		}
		if putErr := st.Put(key, p); putErr != nil {
			log.Printf("[artifacts] 上传 %s 失败（后续检查点或 -storage-sync 会补）: %v", key, putErr)
			return nil
		}
		n++
		return nil
	})
	return n, err
}

// Restore 把远端前缀下本地缺失的 Keeper 文件回源落地——给清理过的老项目恢复工作区
// （produce/prep 开跑前调用）。返回回源文件数；localStore 无操作。
func Restore(st Store, root, prefix string) (int, error) {
	if !st.Available() {
		return 0, nil
	}
	keys, err := st.List(prefix)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, key := range keys {
		rel := strings.TrimPrefix(key, prefix+"/")
		if rel == "" || Classify(rel) != Keeper {
			continue
		}
		local := filepath.Join(root, filepath.FromSlash(key))
		if _, err := os.Stat(local); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
			return n, err
		}
		if err := st.Fetch(key, local); err != nil {
			log.Printf("[artifacts] 回源 %s 失败: %v", key, err)
			continue
		}
		n++
	}
	return n, nil
}

// Prune 清理 root/prefix 下的本地文件，给磁盘腾地方（GC 专用）：
//   - Garbage/Discard 直接删（不上传：纯垃圾或可再生成）；
//   - Keeper 大文件仅在远端正本 Stat 确认后删（安全不变量：先确认后删）；
//   - 其余 Keeper 小文件（JSON/txt/指纹）保留——它们是契约与事实源，体量可忽略。
//
// 调用方负责确认项目不在制作中且 MinIO 已接线（Available=false 时防御性直接返回）。
// 返回释放的字节数与删除的文件数。删完后自底向上摘掉空目录。
func Prune(st Store, root, prefix string) (freed int64, files int, err error) {
	if !st.Available() {
		return 0, 0, nil
	}
	dir := filepath.Join(root, filepath.FromSlash(prefix))
	var dirs []string
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			if p != dir {
				dirs = append(dirs, p)
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		key := filepath.ToSlash(rel)
		cls := Classify(strings.TrimPrefix(key, prefix+"/"))
		if cls == Keeper && !heavy(key) {
			return nil
		}
		if cls == Keeper {
			if !st.Stat(key) {
				log.Printf("[artifacts] 清理跳过 %s：远端正本未确认", key)
				return nil
			}
		}
		if st, statErr := os.Stat(p); statErr == nil {
			freed += st.Size()
		}
		if rmErr := os.Remove(p); rmErr != nil {
			log.Printf("[artifacts] 清理 %s 失败: %v", key, rmErr)
			return nil
		}
		files++
		return nil
	})
	if err != nil {
		return freed, files, err
	}
	// 自底向上摘空目录（reports/、npm-cache/ 清完常只剩壳）
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // 非空时 Remove 失败属预期，忽略
	}
	return freed, files, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// DeleteKeys 尽力删远端对象（本地作废工件时同步删正本——不删的话清理过的老项目
// 回源会把陈旧工件带回来）。未接线/失败只记日志：孤儿对象无害，顶多占点空间。
func DeleteKeys(st Store, keys ...string) {
	if st == nil || !st.Available() {
		return
	}
	for _, key := range keys {
		if err := st.Delete(key); err != nil {
			log.Printf("[artifacts] 删除远端 %s 失败（孤儿对象无害）: %v", key, err)
		}
	}
}

// DeletePrefixBestEffort 尽力删远端整前缀（删项目/作废小样目录时用）。
func DeletePrefixBestEffort(st Store, prefix string) {
	if st == nil || !st.Available() {
		return
	}
	if err := st.DeletePrefix(prefix); err != nil {
		log.Printf("[artifacts] 删除远端前缀 %s 失败（孤儿对象无害）: %v", prefix, err)
	}
}
