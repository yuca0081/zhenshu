// 工位模型设置（docs/llm-settings-2026-10-29.md）：设置面板两层结构的数据层。
// providers = 供应商台账（URL/Key/模型名）；role_default = 三档兜底绑定；
// stations = 工位覆盖绑定。FromEnv 解析链：工位绑定 → 档位兜底 → .env → 内置
// 默认——设置全空时行为与纯 .env 完全一致，零迁移。FromEnv 每次作业现读快照，
// 保存即热生效（正在跑的作业拿旧值跑完，可接受）。
//
// 存储双模式（settingskv.go）：PG 已接 → app_settings 行 'llm_settings' 是事实源，
// 本地 data/llm-config.json 降级为缓存（种子/回灌/30s 轮询/保存写穿）；
// PG 未接 → 维持纯文件模式。
package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 工位常量：值即设置文件 stations 段的键。每个工位从属三档之一（BaseRole），
// 未绑定 provider 时跟随所属档。调用点按作业身份选工位常量，FromEnv 签名不变。
const (
	StationPrepScript      Role = "prep_script"       // 文稿
	StationPrepStyleText   Role = "prep_style_text"   // 风格（文字）
	StationPrepStyleVision Role = "prep_style_vision" // 风格（看图）
	StationPrepStoryboard  Role = "prep_storyboard"   // 分镜
	StationDirector        Role = "director"          // 导演定调
	StationSceneGen        Role = "scene_gen"         // 场景生成
	StationFrameQA         Role = "frame_qa"          // 帧检查
	StationJudge           Role = "judge"             // 审核
	StationBgmPick         Role = "bgm_pick"          // 配乐
	StationStyleClips      Role = "style_clips"       // 样张片段
	StationDistill         Role = "distill"           // 画材包蒸馏
	StationRefAnalyze      Role = "ref_analyze"       // 视频解析
	StationRefTag          Role = "ref_tag"           // 打标
	StationSupervisor      Role = "supervisor"        // 项目对话
	StationVisionDigest    Role = "vision_digest"     // 看图转述
)

// StationMeta 工位元数据：设置面板「工位图」分组渲染与 GET 视图共用。
type StationMeta struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Group string `json:"group"` // prep=准备 / produce=制作 / library=风格库 / common=通用
	Base  Role   `json:"base"`  // 未绑定时的兜底档
}

// StationRegistry 工位全集（面板与 FromEnv 的工位白名单，两边共用）。
var StationRegistry = []StationMeta{
	{ID: "prep_script", Label: "文稿", Group: "prep", Base: RolePlan},
	{ID: "prep_style_text", Label: "风格（文字）", Group: "prep", Base: RolePlan},
	{ID: "prep_style_vision", Label: "风格（看图）", Group: "prep", Base: RoleVisual},
	{ID: "prep_storyboard", Label: "分镜", Group: "prep", Base: RolePlan},
	{ID: "director", Label: "导演定调", Group: "prep", Base: RolePlan},
	{ID: "scene_gen", Label: "场景生成", Group: "produce", Base: RolePlan},
	{ID: "frame_qa", Label: "帧检查", Group: "produce", Base: RoleVisual},
	{ID: "judge", Label: "审核", Group: "produce", Base: RoleVisual},
	{ID: "bgm_pick", Label: "配乐", Group: "produce", Base: RoleDialogue},
	{ID: "style_clips", Label: "样张片段", Group: "produce", Base: RolePlan},
	{ID: "distill", Label: "画材包蒸馏", Group: "library", Base: RoleVisual},
	{ID: "ref_analyze", Label: "视频解析", Group: "library", Base: RoleVisual},
	{ID: "ref_tag", Label: "打标", Group: "library", Base: RolePlan},
	{ID: "supervisor", Label: "项目对话", Group: "common", Base: RoleDialogue},
	{ID: "vision_digest", Label: "看图转述", Group: "common", Base: RoleVisual},
}

var stationBase = func() map[Role]Role {
	m := make(map[Role]Role, len(StationRegistry))
	for _, s := range StationRegistry {
		m[Role(s.ID)] = s.Base
	}
	return m
}()

// IsStation 工位白名单判断。
func IsStation(r Role) bool { _, ok := stationBase[r]; return ok }

// BaseRole 工位 → 所属档；三档自身原样返回。
func BaseRole(r Role) Role {
	if b, ok := stationBase[r]; ok {
		return b
	}
	return r
}

// ProviderCfg 供应商台账条目：一条数据 = 一个 OpenAI 兼容端点（URL+Key），
// Models = 该端点可用的模型名列表，绑定处从列表里选具体模型。
// 字段留空 = 沿用 .env 兜底值；Models 空 = 模型名沿用 .env（可只配 URL/Key
// 复用 .env 分档模型）。
type ProviderCfg struct {
	Label   string   `json:"label"`              // 面板显示名（空 = 用 id）
	BaseURL string   `json:"base_url"`           // 留空 = .env LLM_BASE_URL
	APIKey  string   `json:"api_key,omitempty"`  // 留空 = .env LLM_API_KEY；接口侧永不回明文
	Models  []string `json:"models,omitempty"`   // 模型名列表；绑定取 Model 字段从这选
	Model   string   `json:"model,omitempty"`    // 【只读兼容】v2 单模型字段：normalize 迁移进 Models 后清空
}

// FirstModel 该端点的默认模型（列表第一个；空列表 = ""，解析链回落 .env）。
func (p ProviderCfg) FirstModel() string {
	if len(p.Models) > 0 {
		return p.Models[0]
	}
	return ""
}

// HasModel 绑定校验用：模型名在列表内（列表空 = 未配模型，绑定不该带模型名）。
func (p ProviderCfg) HasModel(m string) bool {
	for _, v := range p.Models {
		if v == m {
			return true
		}
	}
	return false
}

// Binding 绑定：工位或档 → 供应商 + 具体模型。Provider 空 = 不绑定（回落下一层）；
// Model 空 = 用所选供应商列表的第一个；Effort 可选覆盖思考档（low/high/disabled），
// 空 = .env LLM_EFFORT。
type Binding struct {
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
}

// Settings 设置文件 data/llm-config.json 的全部内容。
type Settings struct {
	Version     int                    `json:"version"`
	Providers   map[string]ProviderCfg `json:"providers"`
	RoleDefault map[string]Binding     `json:"role_default"` // 键：dialogue/plan/visual
	Stations    map[string]Binding     `json:"stations"`     // 键：StationRegistry 的 id
}

var (
	settingsPath string
	settingsMu   sync.Mutex // 写文件 + 换指针
	settingsSnap atomic.Pointer[Settings]
)

// InitSettings 启动接线：记路径并读一次（文件缺失/损坏不拒启，按全空处理）。
func InitSettings(path string) {
	settingsPath = path
	if err := LoadSettings(); err != nil {
		fmt.Printf("⚠  模型设置 %s 读取失败（%v），按 .env 兜底运行\n", path, err)
	}
}

// LoadSettings 读文件进快照。文件不存在 = 合法空设置（首启常态）。
func LoadSettings() error {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	b, err := os.ReadFile(settingsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s := &Settings{}
			normalizeSettings(s) // 补空 map：GET 视图/前端消费统一拿 {} 而不是 null
			settingsSnap.Store(s)
			return nil
		}
		return err
	}
	s := &Settings{}
	if err := json.Unmarshal(b, s); err != nil {
		return err
	}
	normalizeSettings(s)
	settingsSnap.Store(s)
	return nil
}

// SettingsSnapshot 当前快照（只读；nil = 未初始化，走纯 env 路径）。
func SettingsSnapshot() *Settings { return settingsSnap.Load() }

// SettingsPath 设置文件路径（未初始化为空串）。
func SettingsPath() string { return settingsPath }

var providerIDPat = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// SaveSettings 校验 → 落存储（kv 模式写穿远程库；file 模式 tmp+rename 原子落盘）
// → 换快照。校验失败返回错误不改现状。先严格校验（PUT 方要看到错误）再宽容清洗
// （全空绑定不落盘）——启动读盘走 LoadSettings 才是纯宽容路径。warning 弃置
// （内部/测试调用只关心成败）；API 层用 SaveSettingsWithStatus 拿同步状态。
func SaveSettings(s *Settings) error {
	_, err := SaveSettingsWithStatus(s)
	return err
}

// SaveSettingsWithStatus SaveSettings 的带状态版：远程库写失败不阻塞本机——
// 快照+文件照常生效，以 warning 回传（面板提示「其他工作机暂不会同步」）。
// 纯文件模式保持旧语义：落盘失败返回错误且不换快照。
func SaveSettingsWithStatus(s *Settings) (warning string, err error) {
	if s == nil {
		s = &Settings{}
	}
	if err := validateSettings(s); err != nil {
		return "", err
	}
	normalizeSettings(s)
	settingsMu.Lock()
	defer settingsMu.Unlock()
	s.Version = 3 // v3：台账 model 单值 → models 列表（读 v2 靠 normalize 迁移）
	b := mustMarshalIndent(s)
	kvMode := settingsKV != nil
	if kvMode {
		if perr := settingsKV.PutKV(llmSettingsKey, b); perr != nil {
			warning = fmt.Sprintf("远程库写入失败（%v）：本机已生效，其他工作机暂不会同步", perr)
		} else {
			kvLastCanon = canonSettings(b)
			kvUpdatedAt = time.Now() // StorageInfo 展示「上次更新」用（近似 DB 服务器时钟）
		}
	}
	if settingsPath != "" {
		if ferr := writeSettingsFile(b); ferr != nil {
			if !kvMode {
				return "", ferr // 纯文件模式无处落盘：报错不换快照
			}
			fmt.Printf("⚠  模型设置本地缓存写失败（%v）——远程库已生效，不影响本机\n", ferr)
		}
	} else if !kvMode {
		return "", errors.New("设置存储未初始化（InitSettings / AttachKV）")
	}
	settingsSnap.Store(s)
	return warning, nil
}

// writeSettingsFile 原子落盘（tmp+rename；0600，含 key 权限收紧）。
// kv 模式下这是缓存，file 模式下是正本。
func writeSettingsFile(b []byte) error {
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return err
	}
	tmp := settingsPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, settingsPath)
}

// NormalizeSettings normalizeSettings 的导出包装（API 视图层补空 map 用）。
func NormalizeSettings(s *Settings) { normalizeSettings(s) }

func normalizeSettings(s *Settings) {
	if s.Providers == nil {
		s.Providers = map[string]ProviderCfg{}
	}
	if s.RoleDefault == nil {
		s.RoleDefault = map[string]Binding{}
	}
	if s.Stations == nil {
		s.Stations = map[string]Binding{}
	}
	for id, p := range s.Providers {
		if p.Label == "" {
			p.Label = id
		}
		// v2 单模型 → 模型列表迁移（读旧库/旧缓存文件的唯一入口；写盘后即 v3 形状）
		if len(p.Models) == 0 && p.Model != "" {
			p.Models = []string{p.Model}
		}
		p.Model = ""
		p.Models = cleanModels(p.Models)
		s.Providers[id] = p
	}
	for k, b := range s.Stations {
		b.Model = pruneBoundModel(b.Model, b.Provider, s.Providers)
		if b.Provider != "" {
			if _, ok := s.Providers[b.Provider]; !ok {
				b.Provider = "" // 悬空引用只掐供应商，保留可能存在的 effort 覆盖
			}
		}
		if b.Provider == "" && b.Effort == "" {
			delete(s.Stations, k) // 全空绑定 = 没绑定，不占键位
			continue
		}
		s.Stations[k] = b
	}
	for k, b := range s.RoleDefault {
		b.Model = pruneBoundModel(b.Model, b.Provider, s.Providers)
		if b.Provider != "" {
			if _, ok := s.Providers[b.Provider]; !ok {
				b.Provider = ""
			}
		}
		if b.Provider == "" && b.Effort == "" {
			delete(s.RoleDefault, k)
			continue
		}
		s.RoleDefault[k] = b
	}
}

// cleanModels 模型名列表清洗：去空白、丢空串、去重、保序。
func cleanModels(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, m := range in {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil // 空列表归 nil：JSON 不落 models 键，与 v2 兼容形状一致
	}
	return out
}

// pruneBoundModel 绑定里的模型名防悬空：供应商缺省或列表里没这个名字 → 掐掉
//（保留 provider/effort，模型回落列表第一个）。
func pruneBoundModel(model, provider string, provs map[string]ProviderCfg) string {
	if model == "" {
		return ""
	}
	pc, ok := provs[provider]
	if !ok || !pc.HasModel(model) {
		return ""
	}
	return model
}

// validateSettings PUT 前置校验：供应商 id 形状、URL scheme、effort 枚举、
// 绑定引用存在、档位键合法、工位键在注册表内。
func validateSettings(s *Settings) error {
	for id, p := range s.Providers {
		if !providerIDPat.MatchString(id) {
			return fmt.Errorf("供应商 id %q 不合法（小写字母/数字/-/_，≤32 字符）", id)
		}
		if p.BaseURL != "" && !hasHTTPScheme(p.BaseURL) {
			return fmt.Errorf("供应商 %s 的 URL 必须以 http:// 或 https:// 开头", id)
		}
	}
	for k, b := range s.RoleDefault {
		switch Role(k) {
		case RoleDialogue, RolePlan, RoleVisual:
		default:
			return fmt.Errorf("档位键 %q 不合法（dialogue/plan/visual）", k)
		}
		if _, ok := s.Providers[b.Provider]; b.Provider != "" && !ok {
			return fmt.Errorf("档位 %s 绑定了不存在的供应商 %q", k, b.Provider)
		}
		if err := checkBoundModel("档位 "+k, b, s.Providers); err != nil {
			return err
		}
		if _, ok := ParseEffort(b.Effort); b.Effort != "" && !ok {
			return fmt.Errorf("档位 %s 的思考档 %q 不合法（low/high/disabled）", k, b.Effort)
		}
	}
	for k, b := range s.Stations {
		if !IsStation(Role(k)) {
			return fmt.Errorf("工位键 %q 不在工位清单内", k)
		}
		if _, ok := s.Providers[b.Provider]; b.Provider != "" && !ok {
			return fmt.Errorf("工位 %s 绑定了不存在的供应商 %q", k, b.Provider)
		}
		if err := checkBoundModel("工位 "+k, b, s.Providers); err != nil {
			return err
		}
		if _, ok := ParseEffort(b.Effort); b.Effort != "" && !ok {
			return fmt.Errorf("工位 %s 的思考档 %q 不合法（low/high/disabled）", k, b.Effort)
		}
	}
	return nil
}

// checkBoundModel 绑定的模型名必须在所选供应商的列表内（列表未配也拒——
// 显式绑定模型却无处安放是表单错误，静默吞掉会掩盖配置意图）。
func checkBoundModel(what string, b Binding, provs map[string]ProviderCfg) error {
	if b.Model == "" {
		return nil
	}
	if b.Provider == "" {
		return fmt.Errorf("%s 绑定了模型 %q 但没选供应商", what, b.Model)
	}
	pc, ok := provs[b.Provider]
	if !ok {
		return fmt.Errorf("%s 绑定的供应商 %q 不存在", what, b.Provider)
	}
	if len(pc.Models) == 0 {
		return fmt.Errorf("供应商 %s 还没配模型列表，先在「大模型」里加模型名", b.Provider)
	}
	if !pc.HasModel(b.Model) {
		return fmt.Errorf("%s 绑定的模型 %q 不在供应商 %s 的列表里", what, b.Model, b.Provider)
	}
	return nil
}

func hasHTTPScheme(u string) bool {
	n := len(u)
	return (n > 7 && u[:8] == "https://") || (n > 6 && u[:7] == "http://")
}

// resolveProvider 解析 role 的供应商：工位层优先，无/悬空回落档位层；同时选出
// 绑定模型（绑定 model 优先，空 = 该端点列表第一个，列表也空 = 回落 .env）。
// 供应商与思考档是两条独立通道（工位可以只调 effort 不换模型）。
func (s *Settings) resolveProvider(role Role) (string, ProviderCfg, string, bool) {
	if s == nil {
		return "", ProviderCfg{}, "", false
	}
	if IsStation(role) {
		if b, ok := s.Stations[string(role)]; ok && b.Provider != "" {
			if pc, ok := s.Providers[b.Provider]; ok {
				return b.Provider, pc, pickBoundModel(b.Model, pc), true
			}
		}
	}
	if b, ok := s.RoleDefault[string(BaseRole(role))]; ok && b.Provider != "" {
		if pc, ok := s.Providers[b.Provider]; ok {
			return b.Provider, pc, pickBoundModel(b.Model, pc), true
		}
	}
	return "", ProviderCfg{}, "", false
}

// pickBoundModel 绑定层模型选择：显式绑定 > 列表第一个 > ""（回落 .env 模型名）。
func pickBoundModel(bound string, pc ProviderCfg) string {
	if bound != "" {
		return bound
	}
	return pc.FirstModel()
}

// resolveEffort 绑定层思考档：工位层非空优先，否则档位层。空 = 未覆盖（回落 env）。
func (s *Settings) resolveEffort(role Role) string {
	if s == nil {
		return ""
	}
	if IsStation(role) {
		if b, ok := s.Stations[string(role)]; ok && b.Effort != "" {
			return b.Effort
		}
	}
	if b, ok := s.RoleDefault[string(BaseRole(role))]; ok && b.Effort != "" {
		return b.Effort
	}
	return ""
}
