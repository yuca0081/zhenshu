// 用户账号（固定账号模型：管理员预建，无自助注册）。与后端 store.User JSON 对齐。
export interface UserInfo {
  id: string
  username: string
  role: 'admin' | 'user'
}

// 用户管理列表行（含创建时间）
export interface UserRow extends UserInfo {
  created_at: string
}

// 启动首查（GET /api/auth/status）：auth_required=false = 服务端开放模式（本机免登录）
export interface AuthStatus {
  auth_required: boolean
  user: UserInfo | null
}

export interface ProjectRow {
  id: string
  name: string
  status: string
  created_at: string
  has_video: boolean
  // 最近一轮制作终态（后端与 agent 状态摘要同源）：error/interrupted 时列表标"上次制作失败"
  last_produce?: { event: string; ts: string } | null
  mode?: string // 'music' = 音乐驱动 PV（缺省/空 = 文稿驱动，后端老项目不回此字段）
  local?: boolean // 工作区在本机；false = 只在云端（换机场景，打开时后端自动恢复小件）
}

// 文稿（GET/PUT /manuscript；api.ts / App.vue / StagePanel 共用）
export interface Manuscript {
  content: string
  word_count: number
}

export interface ProjectView {
  id: string
  name: string
  aspect: string
  topic: string // 一句话主题（文稿弹窗 AI 起草的种子；老项目为空）
  mode?: string // 'music' = 音乐驱动 PV：工作区走 PV 准备页签，文稿链页签全隐藏（后端老项目不回）
  gates: {
    manuscript: boolean
    storyboard: boolean
    style_confirmed: boolean
    style_draft: boolean
  }
  seg_count: number
  total_hint: number
  has_video: boolean
  producing: boolean
  // 成片生成后上游又改过（后端按文件 mtime 对比）：页签黄灯 + 重做整片入口
  video_stale: { manuscript: boolean; storyboard: boolean; style: boolean }
  // 最近一轮制作终态（error/interrupted = 失败/中断，与列表 last_produce 同源）
  last_produce?: { event: string; ts: string } | null
  // 项目配乐（配乐工位自动选/用户手动换；name 空 = 无配乐）
  bgm?: { name: string; reason?: string } | null
  // 出片封面（出片尾巴自动产出；null = 还没有——老项目/生成失败，视频页可重做）
  cover?: { t: number; title: string } | null
  // 全程 token 消耗（manifest 全量 agent.done 累计，「过程」页头部展示）
  tokens?: { prompt: number; completion: number; calls: number; cached?: number }
}

// 项目配乐（projectView.bgm：配乐工位自动选/用户手动换；name 空 = 无配乐）
export interface BgmInfo { name: string; reason?: string }

// 曲库条目（GET /bgm/library；下拉+试听+音乐轴循环块用）
export interface BgmTrack {
  name: string
  mood: string
  bpm: number
  tempo: string // slow | mid | fast（v1 合成曲为空串=未标注）
  energy: number
  duration: number
}

// 工作区中央页签（想法/文稿/分镜/风格/终审/视频/过程——「想法」是准备工位的启动页）
export type TabID = 'idea' | 'manuscript' | 'storyboard' | 'style' | 'review' | 'video' | 'activity'

// ── 准备阶段三工件（GET /prep；docs/prep-agents-design.md）──────
export interface PrepSentence { id: string; text: string }
export interface PrepFact { claim: string; url: string }

export interface PrepScript {
  source_mode: string
  text: string
  sentences: PrepSentence[]
  fact_sheet?: PrepFact[]
  word_count: number
  target_seconds: number
}

export interface PrepGrammar {
  density: string
  band_min: number
  band_max: number
  continuity: string
}

export interface PrepDraftSeg {
  id: string
  idx: number
  key: string
  narration_refs: string
  visual_brief: { scene: string; first_frame: string; last_frame: string }
  duration_hint: number
}

export interface PrepMusicSection {
  name: string
  start: number
  end: number
  energy: number
}

export interface PrepMusic {
  brief: boolean // 曲 brief（创作意图）已落盘
  audio: boolean // 整曲 mp3 已落盘（试听走 /projects/:id/music/audio）
  brief_summary?: {
    name: string
    key: string
    bpm: number
    bars: number
    sections: { name: string; bars: number; energy: number }[]
  }
  map?: {
    name: string
    key: string
    bpm: number
    duration: number
    sections: PrepMusicSection[]
  }
}

// 项目材料库行（GET/POST/DELETE /materials；主题页上传的参考资料，
// ①文稿 read_source 消费——与原始稿同等对待）
export interface MaterialFile {
  name: string
  chars: number
}

export interface PrepView {
  exists: boolean // scripts/script.json 在 = 项目走准备工位（终审页签的显示条件）
  confirmed: boolean // 打包终审是否已确认（=开工硬门）
  running?: boolean // 三工位链在跑（主题页签的进度态）
  legacy?: boolean // 老项目（legacy 分镜已在，不走准备工位——隐藏主题页签）
  idea?: string // 已落盘的主题/想法文本（回填文本框）
  mode?: string // script=文稿驱动（默认，老项目缺省）｜music=音乐驱动 PV
  music?: PrepMusic // 音乐驱动 PV 的三件套视图（brief/曲/地图）
  style_name?: string
  script?: PrepScript
  stylepack?: {
    style_id: string
    description: string
    grammar: PrepGrammar
    sample_ids: string[]
    direction: string
    confirmed: boolean
  }
  design?: StyleSamples // 完整设计系统（样张预览用，与 style_samples.json 同源）
  draft?: {
    plan?: string
    paragraphs: { title: string; segs: string[] }[]
    segments: PrepDraftSeg[]
  }
}

export interface Segment {
  id: string
  idx: number
  key: string
  narration: string
  visual_brief: string
  duration_hint: number
}

export interface Storyboard {
  plan: string
  segments: Segment[]
}

export interface StyleSample {
  tag: string
  desc: string
  html: string
  // 迷你真段（样张升级）：3s 动态小样 mp4（相对项目根）；空 = 降级静态（clip_note 记原因）
  video?: string
  clip_note?: string
}

// 字体角色（design_system.fonts）：stack 拆开就是终审「自采依赖」里的字体清单
export interface FontRole {
  stack: string
  weight?: string
  tracking?: string
}
export interface FontRoles {
  display: FontRole
  body: FontRole
  data?: FontRole
}

// 样张阶段自装字体（<!-- font: Family | 协议 | 直链URL --> 声明，平台下载）：
// 终审页披露「装了哪些字体」——协议是模型自报的披露字段，来源可点开核对
export interface CustomFont {
  family: string
  license?: string
  url: string
}

export interface StyleSamples {
  direction: string
  samples: StyleSample[]
  skin?: Skin // 风格文件（§12.2）：样张阶段与样张同产，确认时锁定
  fonts?: FontRoles
  vend_libs?: string[] // 样张自采的外部库（lib@精确版本，正式段货架复用）
  custom_fonts?: CustomFont[]
  confirmed: boolean
}

// 风格文件：必填槽位（插座）+ 自由 CSS
export interface SkinSlots {
  bg: string
  txt: string
  accent: string
  font: string
  mute?: string
  surface?: string
  panel_txt?: string
  radius?: number
  border_w?: number
}

export interface Skin {
  name: string
  slots: SkinSlots
  css: string
}

export interface Msg {
  id: number
  role: 'user' | 'agent' | 'system'
  type: 'text' | 'tool_call' | 'progress' | 'error'
  content: string
  refs?: ChatRef[]
  created_at: string
}

// 风格库条目 v3（data/library/styles.json，唯一风格事实源）：
// 标签（词表 id，引用模型固有知识）+ 特色清单（参考片独有手法，制作时整包生效单条可关）
// + 证据（style_media 原片与抽帧）——不再携带 tokens/样张/皮肤（设计系统由各片现发明）
export interface Style {
  id: string
  name: string
  tags: StyleTags
  tag_evidence?: TagEvidence[]
  signatures?: Signature[]
  subject?: string        // 原片题材一句话（只供检索，永不进生成提示词）
  origin_project?: string // "video:<refID>" 或项目 id
  published: boolean
  created_at: string
}

export interface StyleTags { main: string; aux?: string[] }

export interface TagEvidence {
  tag: string
  note: string
  frames?: string[]
}

// 特色（§4.1）：六类枚举 + 机制化描述 + 证据帧。kind 决定 QA 考核方式（帧判/片判）
export interface Signature {
  id: string
  kind: 'camera' | 'transition' | 'motif' | 'motion' | 'material' | 'palette'
  name: string
  mechanism: string
  frames?: string[]
  use_hint?: string
}

// 风格词表（data/library/vocab.json）：公认风格名词登记册，冷启动为空表
export interface VocabEntry {
  id: string
  name: string
  aliases?: string[]
  brief: string
  tells?: string
}

// 音频对齐产物（audio/audio_meta.json）：时间轴字幕/音频轨的数据源
export interface AlignWord {
  text: string
  start: number
  end: number
}

export interface AudioVoice {
  id: string
  duration_s: number
  words: AlignWord[]
}

export interface AudioMeta {
  voices: AudioVoice[]
  timeline?: { id: string; start: number; end: number }[] // xfade 成片的实际段边界（有则 segTable 用它）
}

// 聊天输入框上方的引用项（时间轴/分镜点选加入，随消息结构化发给 Agent）
// 三态：段级（idx+key）/ 时刻级（idx+key+t）/ 元素级（+elementId+elementName）
// 元素级仅历史消息兼容展示——检视模式下线后不再产生新元素引用
export interface ChatRef {
  idx: number
  key: string
  t?: number           // 全局时刻（秒），时刻/元素引用携带
  elementId?: string   // 元素 DOM id（如 seg03-arrow2）
  elementName?: string // 人话名（如 箭头：蓝光弹开）
}

// 制作活动流（GET /activity，manifest.jsonl 原样事件，时间正序）：
// 每步用时由 stage.start/finish 的毫秒差算出；老条目无 ms 时前端按 ts 解析兜底。
// task 系字段由 agent.start/agent.done 携带（「过程」页按它聚合智能体任务卡片）
export interface ActivityEvent {
  ts: string
  ms?: number
  event: string
  detail: string
  task?: string
  label?: string
  state?: string
  prompt_tokens?: number
  completion_tokens?: number
  calls?: number
  cached_tokens?: number // 平台前缀缓存命中（2026-10-19 起记账；旧账目无此字段）
  transcript?: string
}

// 智能体任务对话留痕（GET /transcript?task=，llm/transcripts/<task>.jsonl 每行一条）。
// 后端写入时已减脂：超长截断；图片不落数据载荷，只记项目内路径清单（images）
export interface TranscriptMsg {
  role: string // system | user | assistant | tool | note
  content?: string
  name?: string // tool 消息的工具名
  tool_call_id?: string
  tool_calls?: { id: string; name: string; args: string }[]
  images?: { path: string; label?: string }[] // 注入模型的项目内图片（缩略图可点开原文件）
  ts?: string
}

// ── 参考视频解析（风格库页）：一视频 = 一条目 ────────────────
export interface RefVideoStyle {
  name: string
  subject?: string
  tags: StyleTags
  tag_evidence?: TagEvidence[]
  signatures?: Signature[]
}

export interface RefVideo {
  id: string
  name: string
  status: 'pending' | 'running' | 'done' | 'error'
  error?: string
  confirmed: boolean
  created_at: string
  style?: RefVideoStyle
  source?: string // ''/'upload' = 上传视频解析；'project' = 项目解析（工作区按钮发起）
  project_id?: string
}

// ── 风格详情抽屉（风格库/视频解析卡片点开共用）──────────────
// 两个 tab 各自把数据规范化成这个结构；tags 带 vocab 名称（打开抽屉时 join）
export interface TemplateDetailData {
  title: string
  status: { label: string; type: 'success' | 'warning' | 'error' | 'default' }
  subject?: string
  tags: { id: string; name: string; brief?: string }[]
  tagEvidence?: TagEvidence[]
  signatures?: Signature[]
  originLabel: string
  originHref?: string
  createdAt?: string
  packId?: string // 风格库条目：草稿态给「入库」
  published?: boolean
  refId?: string // 参考引用：抽帧截图 + 原视频；未入库给「入库」
  libMediaId?: string // 风格条目自带媒体（video: 来源）：走 /api/library/:id/media
  confirmed?: boolean
  editable?: boolean // 解析草稿（未入库）：特色可增删改
  draft?: RefVideoStyle // editable 时的完整草稿（含 name/subject）
  selectMode?: boolean // 选风格模式（风格页签用）：特色开关搬进抽屉（看着截图点菜）+「就选它」
}

// ── 模型设置（docs/llm-settings-2026-10-29.md）：大模型台账 + 工位图绑定 ──

// 大模型台账条目（GET 视图：Key 只出掩码，不回明文）
export interface LlmProviderView {
  id: string
  label: string
  base_url: string
  model: string
  has_key: boolean
  key_tail: string
}

// 绑定：工位/档位 → 供应商。provider 空 = 跟随下一层；effort 空 = .env 默认档
export interface LlmBinding {
  provider: string
  effort?: string
}

export interface LlmStationMeta {
  id: string
  label: string
  group: 'prep' | 'produce' | 'library' | 'common'
  base: 'dialogue' | 'plan' | 'visual'
}

// 单个工位/档位的生效值（来源标注：station/role/env/default）
export interface LlmEffective {
  provider: string
  model: string
  base_url: string
  effort: string
  source: 'station' | 'role' | 'env' | 'default'
}

export interface LlmSettingsView {
  providers: LlmProviderView[]
  role_default: Record<string, LlmBinding>
  stations: Record<string, LlmBinding>
  station_meta: LlmStationMeta[]
  effective: Record<string, LlmEffective>
  env: { key_set: boolean; base_url: string; model: string; effort: string }
}

// PUT 载荷：api_key 空 = 保持已存；clear_api_key=true 显式清除
export interface LlmProviderInput {
  id: string
  label: string
  base_url: string
  model: string
  api_key?: string
  clear_api_key?: boolean
}

export interface LlmSettingsInput {
  providers: LlmProviderInput[]
  role_default: Record<string, LlmBinding>
  stations: Record<string, LlmBinding>
}

export interface LlmTestResult {
  ok: boolean
  latency_ms: number
  reply?: string
  error?: string
}
