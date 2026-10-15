package stylelib

// 风格词表（style_vocab 账本，v3 §4.2）：公认风格名词的策展登记册。
// 冷启动 = 空表——第一条片入库时打的标签经查重后自动成为第一批词条。
// 收录标准（写进打标提示词）：只收有公认视觉定义的风格名词（设计史/影视行话
// 立得住），拒收「高级感/简洁大气」类空话。同义词归词表管（挂别名）；
// 条目不去重（同标签多条目正常，特色各自不同）。
// 存储在 catalog 后端（PG style_vocab 表或 vocab.json 回退），本文件只管复合逻辑。

import (
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"webpage-video-agent/server/internal/contract"
)

// Vocab 风格词表（内存形态）。Entries 以 id 为 key。
type Vocab struct {
	Entries map[string]VocabEntry `json:"entries"`
}

// VocabEntry 一个风格词条。
type VocabEntry struct {
	ID        string    `json:"id"`                // 小写蛇形，如 claymation
	Name      string    `json:"name"`              // 正式名，如 粘土定格
	Aliases   []string  `json:"aliases,omitempty"` // 中英+俗称，查重用
	Brief     string    `json:"brief"`             // 一句机制描述：色域/质感/光/字体气质
	Tells     string    `json:"tells,omitempty"`   // 判别特征：帧里认什么
	CreatedAt time.Time `json:"created_at"`
}

func (v *Vocab) Has(id string) bool { return v != nil && id != "" && v.Entries[id].ID != "" }

// LoadVocab 读词表（公开只读口；损坏返回错误——申请流程必须能区分空表与坏表）。
func (l *Library) LoadVocab() (*Vocab, error) {
	entries, err := l.cat.AllVocab()
	if err != nil {
		return nil, err
	}
	v := &Vocab{Entries: map[string]VocabEntry{}}
	for _, e := range entries {
		v.Entries[e.ID] = e
	}
	return v, nil
}

// VocabList 全部词条（按正式名排序，前端展示/打标提示注入用）。
func (l *Library) VocabList() []VocabEntry {
	v, err := l.LoadVocab()
	if err != nil {
		log.Printf("[stylelib] 词表读取失败: %v", err)
		return nil
	}
	out := make([]VocabEntry, 0, len(v.Entries))
	for _, e := range v.Entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// NormWord 归一化比对键：小写、去全部空白。
func NormWord(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), "")
}

// Find 归一化精确查重：name+aliases 全集里找（返回词条，命中=true）。
func (v *Vocab) Find(name string) (VocabEntry, bool) {
	if v == nil {
		return VocabEntry{}, false
	}
	key := NormWord(name)
	if key == "" {
		return VocabEntry{}, false
	}
	for _, e := range v.Entries {
		if NormWord(e.Name) == key {
			return e, true
		}
		for _, a := range e.Aliases {
			if NormWord(a) == key {
				return e, true
			}
		}
	}
	return VocabEntry{}, false
}

var vocabIDRe = regexp.MustCompile(`[^a-z0-9_]+`)

// VocabCreate 新建词条（调用方须已过查重；name/aliases 与现有词条归一化撞车时
// 返回已有词条 id 而不新建——「申请新词」幂等口）。ID 取申请值的小写蛇形，
// 非法/缺省时按名称 ASCII 化，再不行用时间戳兜底。
func (l *Library) VocabCreate(e VocabEntry) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	v, err := l.LoadVocab()
	if err != nil {
		return "", err
	}
	if hit, ok := v.Find(e.Name); ok {
		return hit.ID, nil // 归一化撞车：复用已有词条（申请新词幂等）
	}
	name := strings.TrimSpace(e.Name)
	if name == "" || strings.TrimSpace(e.Brief) == "" {
		return "", fmt.Errorf("新词条缺 name/brief")
	}
	id := strings.ToLower(strings.TrimSpace(e.ID))
	id = vocabIDRe.ReplaceAllString(id, "_")
	id = strings.Trim(id, "_")
	if id == "" || !isASCII(id) {
		id = asciiFallback(name)
	}
	if id == "" {
		id = fmt.Sprintf("w%d", time.Now().UnixNano()%1e9)
	}
	for v.Has(id) { // id 撞车追加序号
		id += "-2"
	}
	e.ID = id
	e.Name = name
	e.Aliases = dedupeAliases(e.Aliases)
	e.CreatedAt = time.Now()
	if err := l.cat.PutVocab(e); err != nil {
		return "", err
	}
	return id, nil
}

// VocabAttachAlias 语义查重命中的挂别名（词条存在性由调用方保证；别名归一化
// 撞已有词条时静默跳过——不制造重复别名）。
func (l *Library) VocabAttachAlias(id, alias string) error {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	v, err := l.LoadVocab()
	if err != nil {
		return err
	}
	e, ok := v.Entries[id]
	if !ok {
		return fmt.Errorf("词条 %s 不存在", id)
	}
	for _, a := range append(e.Aliases, e.Name) {
		if NormWord(a) == NormWord(alias) {
			return nil
		}
	}
	return l.cat.SetAliases(id, append(e.Aliases, alias))
}

// VocabBriefs 词表词条人话摘要（简报 TagBriefs 用；缺词条的 id 静默跳过——
// 简报与库解耦，词条事后被删不该让选用崩掉）。
func (l *Library) VocabBriefs(ids ...string) []contract.TagBrief {
	v, err := l.LoadVocab()
	if err != nil {
		return nil
	}
	var out []contract.TagBrief
	for _, id := range ids {
		if e, ok := v.Entries[id]; ok {
			out = append(out, contract.TagBrief{ID: e.ID, Name: e.Name, Brief: e.Brief})
		}
	}
	return out
}

// dedupeAliases 别名去重（含与 name 归一化同值的）。
func dedupeAliases(aliases []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(aliases))
	for _, a := range aliases {
		a = strings.TrimSpace(a)
		k := NormWord(a)
		if a == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, a)
	}
	return out
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}

// asciiFallback 中文名等非 ASCII id 的兜底：抽 ASCII 段作 id（如「蒸汽波 Vaporwave」
// → vaporwave）；全无 ASCII 返回 ""（调用方再用时间戳）。
func asciiFallback(name string) string {
	var parts []string
	for _, seg := range strings.Fields(name) {
		seg = strings.ToLower(seg)
		if isASCII(seg) {
			seg = vocabIDRe.ReplaceAllString(seg, "_")
			if s := strings.Trim(seg, "_"); s != "" {
				parts = append(parts, s)
			}
		}
	}
	return strings.Join(parts, "_")
}
