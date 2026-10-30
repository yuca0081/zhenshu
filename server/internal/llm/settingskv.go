// 模型设置远程库存储（docs/llm-settings-2026-10-29.md「远程库存储」一节）：
// app_settings 通用 KV（migrations/0009）成为 llm-config.json 的远程事实源，
// 多台工作机配同一 PG_DSN 即共享供应商台账与工位绑定。
//
// 语义三句话：
//   - PG 已接：app_settings 行 'llm_settings' 是事实源，本地文件降级为缓存
//    （启动回灌 + 每 30s 轮询拉新 + 保存写穿）；本地文件有配置而库为空时
//     种子进库，老机配置零手工迁移。
//   - PG 未接（开发/demo/内存模式）：不接线，纯文件模式，行为与从前完全一致。
//   - 远程库读/写失败不阻塞本机：沿用最近快照，保存走本地并在 PUT 响应带
//     warning（面板提示「其他工作机暂不同步」）。
//
// 同步走 30s 轮询而非 LISTEN/NOTIFY：配置面秒级延迟可接受，且轮询在断连后
// 天然自愈。每次 LLM 作业仍只读内存快照，解析链零 DB 开销不变。
package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// llmSettingsKey app_settings 表里本设置的行键。
const llmSettingsKey = "llm_settings"

// KVStore 通用设置 KV 的窄接口：避免 llm→store 的包依赖（import 环），
// store.Store 的 pgStore/memStore 实现天然满足，main.go 直接传入即可。
type KVStore interface {
	GetKV(key string) (val []byte, updatedAt time.Time, ok bool, err error)
	PutKV(key string, val []byte) error
}

var (
	settingsKV    KVStore   // nil = 未接远程库（纯文件模式）
	kvLastCanon   string    // 最近一次已知远程值的归一化串（变更比较基线；"" = 无行或损坏态）
	kvUpdatedAt   time.Time // 远程行最近写入时间（StorageInfo 展示用）
	kvHealthy     bool      // 仅驱动日志状态翻转，避免断连期每 30s 刷屏
	kvLoopStarted bool
)

// AttachKV 启动接线（main.go 在 store.Open 成功且 Kind()=="pg" 后调用）：
// 首次种子/回灌同步做完才起轮询 goroutine，保证 main 继续跑之前快照已就位。
func AttachKV(kv KVStore) {
	settingsMu.Lock()
	settingsKV = kv
	settingsMu.Unlock()
	bootstrapFromKV()
	if kvLoopStarted {
		return
	}
	kvLoopStarted = true
	go refreshLoop()
}

// detachKV 仅测试用：拆掉远程库接线并复位轮询状态（快照/路径归 withSettingsEnv 管）。
func detachKV() {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	settingsKV = nil
	kvLastCanon = ""
	kvUpdatedAt = time.Time{}
	kvHealthy = false
}

// bootstrapFromKV 首次对接：DB 有值 → 覆盖快照 + 回写本地文件缓存；
// DB 无值而本地有 → 本地配置种子进库；都没有 → 各自为空（首启常态）。
func bootstrapFromKV() {
	kv := currentKV()
	if kv == nil {
		return
	}
	b, ts, ok, err := kv.GetKV(llmSettingsKey)
	if err != nil {
		fmt.Printf("⚠  模型设置远程库读取失败（%v），先按本地文件运行，后台每 30s 重试\n", err)
		setHealthy(false)
		return
	}
	if ok {
		// DB 是事实源：不管本地文件是什么，以库为准。
		s := &Settings{}
		if uerr := json.Unmarshal(b, s); uerr != nil {
			fmt.Printf("⚠  远程库模型设置损坏（%v），先按本地文件运行，后台每 30s 重试\n", uerr)
			settingsMu.Lock()
			kvLastCanon = "" // 损坏态基线：修复后靠 canon 差异自然拉新
			kvUpdatedAt = ts
			settingsMu.Unlock()
			setHealthy(true)
			return
		}
		normalizeSettings(s)
		settingsMu.Lock()
		settingsSnap.Store(s)
		kvLastCanon = canonSettings(b)
		kvUpdatedAt = ts
		settingsMu.Unlock()
		setHealthy(true)
		if werr := writeSettingsFile(mustMarshalIndent(s)); werr != nil {
			fmt.Printf("⚠  模型设置本地缓存回写失败（%v）——不影响运行，重启读库\n", werr)
		}
		fmt.Printf("✓  模型设置已从远程库加载（%d 个供应商），本地文件降级为缓存，多机共享生效\n", len(s.Providers))
		return
	}
	// DB 无行：本地文件有实质配置（面板配过的供应商/绑定）就种子进库。
	snap := settingsSnap.Load()
	if snap != nil && (len(snap.Providers) > 0 || len(snap.Stations) > 0 || len(snap.RoleDefault) > 0) {
		b := mustMarshal(snap)
		if perr := kv.PutKV(llmSettingsKey, b); perr != nil {
			fmt.Printf("⚠  模型设置种子写入远程库失败（%v），后台每 30s 重试\n", perr)
			setHealthy(false)
			return
		}
		settingsMu.Lock()
		kvLastCanon = canonSettings(b)
		kvUpdatedAt = time.Now()
		settingsMu.Unlock()
		setHealthy(true)
		fmt.Printf("✓  本地模型设置已种子进远程库（%d 个供应商），其他工作机接入同库即可共享\n", len(snap.Providers))
		return
	}
	setHealthy(true) // 两边都空：正常首启，等面板首次保存
}

// refreshFromKV 单次轮询拉新：远程归一化值与基线不同才换快照（并回写缓存文件）。
// 断连沿用最近快照，恢复后自动续上。与 SaveSettings 并发时存在毫秒级窗口：
// refresh 可能拿旧值盖过刚保存的本地值，下个轮询周期自愈（配置面可接受）。
func refreshFromKV() {
	kv := currentKV()
	if kv == nil {
		return
	}
	b, ts, ok, err := kv.GetKV(llmSettingsKey)
	if err != nil {
		if healthy() {
			fmt.Printf("⚠  模型设置远程库拉取失败（%v），沿用最近快照\n", err)
		}
		setHealthy(false)
		return
	}
	setHealthy(true)
	if !ok { // 远端无行：保留本地（种子尚未成功的窗口期）
		return
	}
	canon := canonSettings(b)
	settingsMu.Lock()
	changed := canon != kvLastCanon
	settingsMu.Unlock()
	if !changed {
		return
	}
	if canon == "" { // 进入损坏态：只报一次（基线记 ""，修复后 canon 差异自然拉新）
		settingsMu.Lock()
		kvLastCanon = ""
		kvUpdatedAt = ts
		settingsMu.Unlock()
		fmt.Printf("⚠  远程库模型设置损坏，沿用最近快照\n")
		return
	}
	s := &Settings{}
	if uerr := json.Unmarshal(b, s); uerr != nil {
		fmt.Printf("⚠  远程库模型设置损坏（%v），沿用最近快照\n", uerr)
		return
	}
	normalizeSettings(s)
	settingsMu.Lock()
	settingsSnap.Store(s)
	kvLastCanon = canon
	kvUpdatedAt = ts
	settingsMu.Unlock()
	if werr := writeSettingsFile(mustMarshalIndent(s)); werr != nil {
		fmt.Printf("⚠  模型设置本地缓存回写失败（%v）——不影响运行\n", werr)
	}
}

func refreshLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		refreshFromKV()
	}
}

// StorageInfo GET 视图用：配置事实源与最近写入时间（file 模式回文件 mtime）。
func StorageInfo() (backend string, updatedAt time.Time) {
	if currentKV() != nil {
		settingsMu.Lock()
		defer settingsMu.Unlock()
		return "db", kvUpdatedAt
	}
	if settingsPath != "" {
		if fi, err := os.Stat(settingsPath); err == nil {
			return "file", fi.ModTime()
		}
	}
	return "file", time.Time{}
}

// canonSettings 归一化比较基线：PG jsonb 落库会重排键序、去空白，原始 bytes
// 比较会对每次轮询误报「已变更」。两侧都过 map 再紧凑序列化（Go 对 map 键序
// 稳定），值等则串等。解析失败回 ""（损坏态标记，与合法 "{}" 区分）。
func canonSettings(b []byte) string {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return ""
	}
	c, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(c)
}

// currentKV 快照式取接线（nil = 纯文件模式）。
func currentKV() KVStore {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	return settingsKV
}

func healthy() bool {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	return kvHealthy
}

func setHealthy(v bool) {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	kvHealthy = v
}

// mustMarshal / mustMarshalIndent Marshal 内存结构不会失败，panic 兜底纯防御。
func mustMarshal(s *Settings) []byte {
	b, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("模型设置序列化失败（不可能）: %v", err))
	}
	return b
}

func mustMarshalIndent(s *Settings) []byte {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("模型设置序列化失败（不可能）: %v", err))
	}
	return b
}
