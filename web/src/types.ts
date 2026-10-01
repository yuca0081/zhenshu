export interface ProjectRow {
  id: string
  name: string
  status: string
  created_at: string
  has_video: boolean
}

export interface ProjectView {
  id: string
  name: string
  aspect: string
  topic: string // 一句话主题（文稿弹窗 AI 起草的种子；老项目为空）
  gates: {
    manuscript: boolean
    storyboard: boolean
    style_confirmed: boolean
    style_draft: boolean
    style_direction: string
  }
  seg_count: number
  total_hint: number
  has_video: boolean
  producing: boolean
  // 检视壳复刻成片观感：stitch 转场模式 + 后期妆（与 main.mp4 同源；post=null=无后期）
  transition: string // cut | fade | xfade
  post: { vignette?: number; grain?: number; chroma?: number } | null
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
  timing_basis: string
  style_id: string
  plan: string
  segments: Segment[]
}

export interface StyleSample {
  tag: string
  desc: string
  html: string
}

export interface StyleSamples {
  direction: string
  desc: string
  samples: StyleSample[]
  skin?: Skin // 风格文件（§12.2）：样张阶段与样张同产，确认时锁定
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
  img_rot?: number
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
  tool_name?: string
  tool_args?: string
  created_at: string
}

// 风格库条目（data/library/styles.json，唯一风格事实源）：
// 身份（id/name/desc/genre/keywords）+ 外观（tokens/layouts）+ 证据（samples）+ 复用件（skin）
export interface Style {
  id: string
  name: string
  direction: string
  desc?: string
  genre?: string
  keywords?: string[]
  tokens?: StyleTokens
  layouts?: RefVideoLayout[]
  samples?: StyleLibrarySample[]
  skin?: Skin          // 风格文件（应用时拷进项目）
  origin_project?: string
  published: boolean
  created_at: string
}

export interface StyleLibrarySample {
  tag: string
  desc?: string
  html: string
}

// 音频对齐产物（audio/audio_meta.json）：时间轴字幕/音频轨的数据源
export interface AlignWord {
  text: string
  start: number
  end: number
}

export interface AudioVoice {
  id: string
  path: string
  duration_s: number
  words: AlignWord[]
}

export interface AudioMeta {
  tts_provider: string
  voice_id: string
  voices: AudioVoice[]
  timeline?: { id: string; start: number; end: number }[] // xfade 成片的实际段边界（有则 segTable 用它）
}

// 聊天输入框上方的引用项（时间轴/分镜/检视点选加入，随消息结构化发给 Agent）
// 三态：段级（idx+key）/ 时刻级（idx+key+t）/ 元素级（+elementId+elementName）
export interface ChatRef {
  idx: number
  key: string
  t?: number           // 全局时刻（秒），时刻/元素引用携带
  elementId?: string   // 元素 DOM id（如 seg03-arrow2）
  elementName?: string // 人话名（如 箭头：蓝光弹开）
}

// styles.json tokens 透传的已知字段（缺字段引擎走缺省，前端同样可缺省渲染）
export interface StyleTokens {
  font?: string
  bg?: {
    type?: string; from?: string; to?: string; angle?: number
    fx?: string[]
    glow?: { color: string; x: number; y: number; r: number; alpha?: string }[]
  }
  ink?: string
  txt?: string
  surface?: string
  surface_txt?: string
  accent?: string[]
  primary?: string
  arrow?: string
  border_w?: number
  radius?: number
  shadow?: string
  title?: { weight?: number; spacing?: number }
  cap?: { mode?: string; bg?: string; color?: string }
  img?: { frame?: string; shadow?: string }
}

// ── 参考视频解析（风格库页）────────────────────────────────
export interface RefVideoLayout {
  id: string
  name: string
  desc: string
}

export interface RefVideoStyle {
  direction: string
  genre: string
  keywords: string[]
  photo: string
  layouts: RefVideoLayout[]
  samples: { tag: string; desc: string; html: string }[]
  tokens?: StyleTokens // ref.json 原样透传（listReferences 已返回）
}

export interface RefVideo {
  id: string
  name: string
  status: 'pending' | 'running' | 'done' | 'error'
  error?: string
  confirmed: boolean
  created_at: string
  style?: RefVideoStyle
}

// ── 风格详情抽屉（风格库/视频解析卡片点开共用）──────────────
// 两个 tab 各自把数据规范化成这个结构（风格条目本体已带 tokens/layouts，无需 join）
export interface TemplateDetailData {
  title: string
  status: { label: string; type: 'success' | 'warning' | 'error' | 'default' }
  direction?: string
  genre?: string
  keywords?: string[]
  layouts?: RefVideoLayout[]
  tokens?: StyleTokens | null
  samples: { tag: string; desc?: string; html: string }[]
  originLabel: string
  originHref?: string
  createdAt?: string
  desc?: string
  extraTags?: string[]
  packId?: string // 风格库条目：草稿态给「入库」
  published?: boolean
  refId?: string // 参考引用：抽帧截图 + 原视频；未入库给「入库」
  libMediaId?: string // 风格条目自带媒体（video: 来源）：走 /api/library/:id/media
  confirmed?: boolean
}
