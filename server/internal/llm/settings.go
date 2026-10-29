// 工位模型设置（docs/llm-settings-2026-10-29.md）：设置面板两层结构的数据层。
// providers = 供应商台账（URL/Key/模型名）；role_default = 三档兜底绑定；
// stations = 工位覆盖绑定。FromEnv 解析链：工位绑定 → 档位兜底 → .env → 内置
// 默认——文件全空时行为与纯 .env 完全一致，零迁移。FromEnv 每次作业现读快照，
// 保存即热生效（正在跑的作业拿旧值跑完，可接受）。
package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
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

// ProviderCfg 供应商台账条目：一个 OpenAI 兼容端点。字段留空 = 沿用 .env 兜底值
//（可只配模型名复用 .env 的 URL/Key，零配置加第二档模型）。
type ProviderCfg struct {
	Label   string `json:"label"`            // 面板显示名（空 = 用 id）
	BaseURL string `json:"base_url"`         // 留空 = .env LLM_BASE_URL
	APIKey  string `json:"api_key,omitempty"` // 留空 = .env LLM_API_KEY；接口侧永不回明文
	Model   string `json:"model"`            // 留空 = .env 模型名
}

// Binding 绑定：工位或档 → 供应商。Provider 空 = 不绑定（回落下一层）。
// Effort 可选覆盖思考档（low/high/disabled），空 = .env LLM_EFFORT。
type Binding struct {
	Provider string `json:"provider"`
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

// SaveSettings 校验 → 原子落盘（tmp+rename）→ 换快照。校验失败返回错误不改现状。
// 先严格校验（PUT 方要看到错误）再宽容清洗（全空绑定不落盘）——启动读盘走
// LoadSettings 才是纯宽容路径。
func SaveSettings(s *Settings) error {
	if s == nil {
		s = &Settings{}
	}
	if err := validateSettings(s); err != nil {
		return err
	}
	normalizeSettings(s)
	settingsMu.Lock()
	defer settingsMu.Unlock()
	s.Version = 2
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if settingsPath == "" {
		return errors.New("设置文件路径未初始化（InitSettings）")
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return err
	}
	tmp := settingsPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil { // 含 key，权限收紧
		return err
	}
	if err := os.Rename(tmp, settingsPath); err != nil {
		return err
	}
	settingsSnap.Store(s)
	return nil
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
			s.Providers[id] = p
		}
	}
	for k, b := range s.Stations {
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
		if _, ok := ParseEffort(b.Effort); b.Effort != "" && !ok {
			return fmt.Errorf("工位 %s 的思考档 %q 不合法（low/high/disabled）", k, b.Effort)
		}
	}
	return nil
}

func hasHTTPScheme(u string) bool {
	n := len(u)
	return (n > 7 && u[:8] == "https://") || (n > 6 && u[:7] == "http://")
}

// resolveProvider 解析 role 的供应商：工位层优先，无/悬空回落档位层。
// 供应商与思考档是两条独立通道（工位可以只调 effort 不换模型）。
func (s *Settings) resolveProvider(role Role) (string, ProviderCfg, bool) {
	if s == nil {
		return "", ProviderCfg{}, false
	}
	if IsStation(role) {
		if b, ok := s.Stations[string(role)]; ok && b.Provider != "" {
			if pc, ok := s.Providers[b.Provider]; ok {
				return b.Provider, pc, true
			}
		}
	}
	if b, ok := s.RoleDefault[string(BaseRole(role))]; ok && b.Provider != "" {
		if pc, ok := s.Providers[b.Provider]; ok {
			return b.Provider, pc, true
		}
	}
	return "", ProviderCfg{}, false
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
