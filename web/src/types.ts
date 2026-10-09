export interface ProjectRow {
  id: string
  name: string
  status: string
  created_at: string
  has_video: boolean
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
  // 成片生成后上游又改过（后端按文件 mtime 对比）：页签黄灯 + 重做整片入口
  video_stale: { manuscript: boolean; storyboard: boolean; style: boolean }
}

// 工作区中央页签（文稿/分镜/风格/视频四页占据中间区域）
export type TabID = 'manuscript' | 'storyboard' | 'style' | 'video'

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
}

export interface StyleSamples {
  direction: string
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
}
