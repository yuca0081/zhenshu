package llm

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeKV KVStore 测试替身。normalize=true 模拟 PG jsonb 的落库归一化
//（重排键序、去空白）——canonSettings 比较基线正是为此而生，替身不复刻
// 这一点就测不出「字节不同值相同」的误报。
type fakeKV struct {
	mu        sync.Mutex
	data      map[string][]byte
	getErr    error
	putErr    error
	normalize bool
	puts      int
}

func (f *fakeKV) GetKV(key string) (val []byte, updatedAt time.Time, ok bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, time.Time{}, false, f.getErr
	}
	b, ok := f.data[key]
	if !ok {
		return nil, time.Time{}, false, nil
	}
	return b, time.Now(), true, nil
}

func (f *fakeKV) PutKV(key string, val []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return f.putErr
	}
	if f.data == nil {
		f.data = map[string][]byte{}
	}
	if f.normalize {
		val = []byte(canonSettings(val))
	}
	f.data[key] = val
	f.puts++
	return nil
}

func (f *fakeKV) get(t *testing.T, key string) []byte {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data[key]
}

// withKVSettings 快照/路径/接线都是包级状态：先复位、退出还原，互不串台。
func withKVSettings(t *testing.T) *fakeKV {
	t.Helper()
	oldSnap, oldPath := settingsSnap.Load(), settingsPath
	settingsPath = filepath.Join(t.TempDir(), "llm-config.json")
	settingsSnap.Store(&Settings{})
	kv := &fakeKV{normalize: true}
	t.Cleanup(func() {
		detachKV()
		settingsSnap.Store(oldSnap)
		settingsPath = oldPath
	})
	return kv
}

func writeLocalFile(t *testing.T, s *Settings) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := os.WriteFile(settingsPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// DB 有值 → DB 是事实源：覆盖本地文件的快照，并把库值回写本地缓存。
func TestBootstrapDBWinsOverFile(t *testing.T) {
	kv := withKVSettings(t)
	writeLocalFile(t, &Settings{Providers: map[string]ProviderCfg{
		"file_only": {BaseURL: "https://file.example/v4", Models: []string{"m-file"}},
	}})
	if err := kv.PutKV(llmSettingsKey, mustMarshal(&Settings{Providers: map[string]ProviderCfg{
		"db_side": {BaseURL: "https://db.example/v4", APIKey: "k-db", Models: []string{"m-db"}},
	}})); err != nil {
		t.Fatal(err)
	}

	settingsKV = kv
	bootstrapFromKV()

	snap := settingsSnap.Load()
	if snap == nil || len(snap.Providers) != 1 || snap.Providers["db_side"].Models[0] != "m-db" {
		t.Fatalf("快照应以 DB 为准: %+v", snap)
	}
	// 本地文件降级为缓存：内容被回灌成 DB 值
	b, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var cached Settings
	if err := json.Unmarshal(b, &cached); err != nil {
		t.Fatal(err)
	}
	if len(cached.Providers) != 1 || cached.Providers["db_side"].APIKey != "k-db" {
		t.Fatalf("缓存文件应回灌 DB 值: %s", b)
	}
	backend, ts := StorageInfo()
	if backend != "db" || ts.IsZero() {
		t.Fatalf("StorageInfo 应报 db: %s %v", backend, ts)
	}
}

// DB 无值 + 本地文件有配置 → 种子进库（老机配置零手工迁移）。
func TestBootstrapSeedsDBFromFile(t *testing.T) {
	kv := withKVSettings(t)
	writeLocalFile(t, &Settings{Providers: map[string]ProviderCfg{
		"legacy": {BaseURL: "https://old.example/v4", APIKey: "k-legacy", Models: []string{"m-old"}},
	}})
	if err := LoadSettings(); err != nil { // 模拟 main.go：InitSettings 先读文件进快照，AttachKV 才种子
		t.Fatal(err)
	}

	settingsKV = kv
	bootstrapFromKV()

	b := kv.get(t, llmSettingsKey)
	if b == nil {
		t.Fatal("本地配置应已种子进库")
	}
	var seeded Settings
	if err := json.Unmarshal(b, &seeded); err != nil {
		t.Fatal(err)
	}
	if seeded.Providers["legacy"].APIKey != "k-legacy" {
		t.Fatalf("种子内容不符: %s", b)
	}
	// 快照保持本地值不变（种子不改变本机行为）
	if snap := settingsSnap.Load(); snap.Providers["legacy"].BaseURL != "https://old.example/v4" {
		t.Fatalf("种子不应改动快照: %+v", snap)
	}
}

// 保存写穿远程库；key 纪律：库里能看到明文（与旧文件同权限域），API 面不回明文由掩码层保证。
func TestSaveWritesThroughKV(t *testing.T) {
	kv := withKVSettings(t)
	settingsKV = kv
	bootstrapFromKV()

	warning, err := SaveSettingsWithStatus(&Settings{Providers: map[string]ProviderCfg{
		"p1": {BaseURL: "https://x.example/v4", APIKey: "k1", Models: []string{"m1"}},
	}})
	if err != nil || warning != "" {
		t.Fatalf("保存应无错无 warning: err=%v warning=%q", err, warning)
	}
	if kv.get(t, llmSettingsKey) == nil {
		t.Fatal("保存应写穿远程库")
	}
	// 文件缓存同步落了
	if _, err := os.Stat(settingsPath); err != nil {
		t.Fatalf("文件缓存应同步落盘: %v", err)
	}
}

// 远程库写失败不阻塞本机：快照+文件照常生效，warning 回传面板提示他机暂不同步。
func TestSaveKVFailureWarnsButLocalWorks(t *testing.T) {
	kv := withKVSettings(t)
	kv.putErr = errors.New("库炸了")
	settingsKV = kv
	bootstrapFromKV()

	warning, err := SaveSettingsWithStatus(&Settings{Providers: map[string]ProviderCfg{
		"p1": {BaseURL: "https://x.example/v4", APIKey: "k1", Models: []string{"m1"}},
	}})
	if err != nil {
		t.Fatalf("库写失败不应返回错误: %v", err)
	}
	if warning == "" {
		t.Fatal("应回传 warning 供面板提示")
	}
	if snap := settingsSnap.Load(); snap.Providers["p1"].APIKey != "k1" {
		t.Fatalf("本机快照应照常生效: %+v", snap)
	}
	if _, err := os.Stat(settingsPath); err != nil {
		t.Fatalf("本机文件应照常生效: %v", err)
	}
}

// 另一台工作机保存后，本机轮询拉新（模拟跨机同步）。
func TestRefreshPullsRemoteChange(t *testing.T) {
	kv := withKVSettings(t)
	settingsKV = kv
	bootstrapFromKV()

	// 他机写入（fakeKV 归一化模拟 jsonb：本机基线与之值等串不同也不该误判）
	if err := kv.PutKV(llmSettingsKey, mustMarshal(&Settings{Providers: map[string]ProviderCfg{
		"remote": {BaseURL: "https://r.example/v4", APIKey: "k-r", Models: []string{"m-r"}},
	}})); err != nil {
		t.Fatal(err)
	}
	refreshFromKV()

	snap := settingsSnap.Load()
	if snap == nil || len(snap.Providers) != 1 || snap.Providers["remote"].Models[0] != "m-r" {
		t.Fatalf("轮询应拉到远端变更: %+v", snap)
	}
	// 再拉一次：值没变不得重复换快照（cache 回写也不该再动）
	refreshFromKV()
	if kv.puts != 1 {
		t.Fatalf("本机不应在 refresh 路径回写远程库: puts=%d", kv.puts)
	}
}

// 断连沿用最近快照，不炸不换。
func TestRefreshKeepsSnapshotOnDBError(t *testing.T) {
	kv := withKVSettings(t)
	if err := kv.PutKV(llmSettingsKey, mustMarshal(&Settings{Providers: map[string]ProviderCfg{
		"p1": {BaseURL: "https://x.example/v4", APIKey: "k1", Models: []string{"m1"}},
	}})); err != nil {
		t.Fatal(err)
	}
	settingsKV = kv
	bootstrapFromKV()

	kv.getErr = errors.New("断连了")
	refreshFromKV()
	if snap := settingsSnap.Load(); snap.Providers["p1"].Models[0] != "m1" {
		t.Fatalf("断连应沿用最近快照: %+v", snap)
	}
	// 恢复后自动续上
	kv.getErr = nil
	refreshFromKV()
	if snap := settingsSnap.Load(); len(snap.Providers) != 1 {
		t.Fatalf("恢复后应回到 DB 值: %+v", snap)
	}
}

// canonSettings：jsonb 归一化后值等则串等；损坏输入回 ""（与合法 "{}" 区分）。
func TestCanonSettings(t *testing.T) {
	a := canonSettings([]byte(`{"version":2,"providers":{"p1":{"base_url":"https://x/v4"}}}`))
	b := canonSettings([]byte(`{
		"providers": {"p1": {"base_url": "https://x/v4"}},
		"version": 2
	}`))
	if a == "" || a != b {
		t.Fatalf("值等应串等（键序/空白无关）: %q vs %q", a, b)
	}
	if canonSettings([]byte(`{oops`)) != "" {
		t.Fatal("损坏输入应回空串")
	}
	if canonSettings([]byte(`{}`)) == "" {
		t.Fatal("合法空对象不应回空串")
	}
}
