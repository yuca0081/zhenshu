import type { ActivityEvent, AudioMeta, BgmTrack, TranscriptMsg, AuthStatus, ChatRef, LlmSettingsInput, LlmSettingsView, LlmTestResult, Manuscript, MaterialFile, Msg, PrepView, ProjectRow, ProjectView, RefVideo, RefVideoStyle, Storyboard, Style, StyleSamples, UserInfo, UserRow, VocabEntry } from './types'

// ── 会话认证（cookie 会话，HttpOnly，浏览器自动携带）──────────
// 凭据不进 JS：localStorage 存 token / ?token= 进 URL 的旧方案已废。
// 401 统一回调 onUnauthorized（App 启动时注入）→ 切登录页。
let onUnauthorized: (() => void) | null = null
export function setOnUnauthorized(fn: () => void) { onUnauthorized = fn }

// 请求超时（2026-10-30 GUI 实测根因 B）：fetch 默认无超时，后端重启/代理悬空时
// 单个请求能挂几分钟，App 的 inflight 去重会把同 key 的后续刷新全部饿死——页面
// 冻结到浏览器层兜底为止。30s 覆盖常规接口；个别慢接口（封面重做）显式放宽。
const REQ_TIMEOUT = 30_000

async function authFetch(input: RequestInfo, init?: RequestInit, timeoutMs = REQ_TIMEOUT): Promise<Response> {
  const timeout = AbortSignal.timeout(timeoutMs)
  const signal = init?.signal ? AbortSignal.any([init.signal, timeout]) : timeout
  let res: Response
  try {
    res = await fetch(input, { ...init, signal })
  } catch (e) {
    if (timeout.aborted) throw new Error(`请求超时（${Math.round(timeoutMs / 1000)}s）——服务暂不可达，稍后会自动恢复`)
    throw e
  }
  if (res.status === 401) onUnauthorized?.()
  return res
}

export const sseURL = (id: string) => `/api/projects/${id}/events`

async function j<T>(res: Response): Promise<T> {
  if (!res.ok) {
    // 错误体形如 { error }，非 JSON/缺字段退 statusText（body 按 unknown 收窄，不再 `{} as any`）
    const body = (await res.json().catch(() => null)) as { error?: string } | null
    throw new Error(body?.error ?? res.statusText)
  }
  return res.json()
}

// req/post = authFetch + JSON 解包一步到位（此前 23 处 .then(r => j<T>(r)) 样板）
async function req<T>(input: RequestInfo, init?: RequestInit, timeoutMs?: number): Promise<T> {
  return j<T>(await authFetch(input, init, timeoutMs))
}
function post<T>(input: RequestInfo, body?: unknown, timeoutMs?: number): Promise<T> {
  return req<T>(input, body === undefined
    ? { method: 'POST' }
    : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }, timeoutMs)
}

export const api = {
  // ── 用户会话 ──
  authStatus: () => req<AuthStatus>('/api/auth/status'),
  login: (username: string, password: string) =>
    post<UserInfo>('/api/auth/login', { username, password }),
  logout: () => post<{ ok: boolean }>('/api/auth/logout'),
  // 用户管理（管理员）：固定账号模型，无自助注册
  listUsers: () => req<UserRow[]>('/api/auth/users'),
  createUser: (username: string, password: string, role: 'admin' | 'user') =>
    post<UserRow>('/api/auth/users', { username, password, role }),

  listProjects: () => req<ProjectRow[]>('/api/projects'),

  createProject: (name: string, topic: string, aspect = '9:16', mode = 'script') =>
    post<{ id: string; name: string }>('/api/projects', { name, topic, aspect, mode }),

  project: (id: string) => req<ProjectView>(`/api/projects/${id}`),

  // 删除项目（磁盘目录+聊天记录全删；制作中后端 409 拒绝）
  deleteProject: (id: string) => req<{ ok: boolean }>(`/api/projects/${id}`, { method: 'DELETE' }),

  // 整片制作（三前置齐后开跑；重做整片同入口——管线按指纹复用未变产物）
  produce: (id: string) => post<{ ok: boolean; started: boolean }>(`/api/projects/${id}/produce`),

  storyboard: (id: string) => req<Storyboard>(`/api/projects/${id}/storyboard`),

  // 手动编辑分镜落盘（段数/段序不可变；改完成片过期灯自动亮）
  saveStoryboard: (id: string, sb: Storyboard) =>
    req<{ ok: boolean }>(`/api/projects/${id}/storyboard`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(sb),
    }),

  audioMeta: (id: string) => req<AudioMeta>(`/api/projects/${id}/audio_meta`),

  // 项目配乐：设置/更换/关闭（track="off" 关闭）；成片母版在后端立即重混
  setBGM: (id: string, track: string) =>
    post<{ ok: boolean; bgm: string; remixed: boolean; message?: string; error?: string }>(
      `/api/projects/${id}/bgm`, { track }),

  // 曲库清单 + 试听音频地址（曲名过服务端曲库校验，无穿越面）
  bgmLibrary: () => req<BgmTrack[]>('/api/bgm/library'),
  bgmAudioURL: (name: string) => `/api/bgm/audio/${encodeURIComponent(name)}`,

  // 出片封面重做（同步；t=帧时刻秒缺省沿用既有帧位，title 缺省沿用既有/项目名）
  // 同步截图要跑 python，超时放宽到 180s（其余接口默认 30s）
  regenCover: (id: string, body: { t?: number; title?: string } = {}) =>
    post<{ ok: boolean; t: number; title: string }>(`/api/projects/${id}/cover`, body, 180_000),
  coverURL: (id: string, rev = 0) => `/api/projects/${id}/cover.png${rev ? `?v=${rev}` : ''}`,

  manuscript: (id: string) => req<Manuscript>(`/api/projects/${id}/manuscript`),

  // 文稿保存（article.txt 为真源；storyboard_stale=分镜基于旧稿需重新生成；
  // prep_invalidated=准备工件已级联作废需重跑重审——类型如实声明，调用方不许
  // 再 cast 嗅探，2026-10-19 中-41）
  saveManuscript: (id: string, content: string) =>
    req<{ ok: boolean; word_count: number; storyboard_stale: boolean; prep_invalidated: boolean }>(`/api/projects/${id}/manuscript`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content }),
    }),

  // 风格弹窗选定库内条目（v3：写风格简报，特色开关默认全开；设计系统本片现发明，
  // 样张确认硬门不再被跳过）。disabled = 关掉的特色 id 列表。
  applyStyle: (id: string, styleId: string, disabled: string[] = []) =>
    post<{ ok: boolean; name: string; signatures: number }>(`/api/projects/${id}/style/apply`, { style_id: styleId, disabled }),

  style: (id: string) => req<StyleSamples>(`/api/projects/${id}/style`),

  // 准备阶段三工件打包视图（终审页签；exists=false = 非新路径项目）
  prep: (id: string) => req<PrepView>(`/api/projects/${id}/prep`),

  // 启动/取消三工位链（主题页签；异步跑，进度走 SSE 与轮询）
  runPrep: (id: string, idea: string, force = false) =>
    post<{ ok: boolean; started: boolean }>(`/api/projects/${id}/prep/run`, { idea, force }),
  cancelPrep: (id: string) => post<{ ok: boolean }>(`/api/projects/${id}/prep/cancel`),

  // 项目材料库（主题页上传的参考资料，①文稿 read_source 消费；同名覆盖=更新）
  materials: (id: string) => req<MaterialFile[]>(`/api/projects/${id}/materials`),
  saveMaterial: (id: string, name: string, content: string) =>
    post<{ ok: boolean; materials: MaterialFile[] }>(`/api/projects/${id}/materials`, { name, content }),
  deleteMaterial: (id: string, name: string) =>
    req<{ ok: boolean; materials: MaterialFile[] }>(
      `/api/projects/${id}/materials?name=${encodeURIComponent(name)}`, { method: 'DELETE' }),

  // 打包终审确认（全流程唯一人工门）：文字稿+风格包+分镜稿一次确认
  confirmPrep: (id: string) => post<{ ok: boolean; direction: string }>(`/api/projects/${id}/prep/confirm`),

  chat: (id: string, content: string, refs: ChatRef[] = []) =>
    post<{ ok: boolean }>(`/api/projects/${id}/chat`, { content, refs }),

  // 中断当前项目正在执行的对话轮（服务端取消其上下文；ok=false=没有在跑的轮）
  cancelChat: (id: string) => post<{ ok: boolean }>(`/api/projects/${id}/chat/cancel`),

  messages: (id: string, after = 0) => req<Msg[]>(`/api/projects/${id}/messages?after=${after}`),

  cancel: (id: string) => post<{ ok: boolean }>(`/api/projects/${id}/cancel`),

  // 制作活动流（manifest 原样事件，尾部正序；「过程」页数据源，默认 500 条、可到 2000）
  activity: (id: string, limit?: number) =>
    req<ActivityEvent[]>(`/api/projects/${id}/activity${limit ? `?limit=${limit}` : ''}`),

  // 智能体任务对话留痕（llm/transcripts/<task>.jsonl，写入侧已减脂）
  transcript: (id: string, task: string) =>
    req<TranscriptMsg[]>(`/api/projects/${id}/transcript?task=${encodeURIComponent(task)}`),

  listLibrary: () => req<Style[]>('/api/library'),

  publishPack: (id: string) => post<Style>(`/api/library/${id}/publish`),

  // 删除风格条目（含媒体快照）。已出片项目不受影响：样张/皮肤在选用时已拷进项目
  deleteLibrary: (id: string) => req<{ ok: boolean }>(`/api/library/${id}`, { method: 'DELETE' }),

  // 风格媒体（视频解析入库的原片+抽帧快照；老条目服务端自动回退 references 目录）
  libraryFrames: (id: string) =>
    req<{ frames: string[]; has_video: boolean }>(`/api/library/${id}/frames`),

  // 风格词表（只读；条目标签的 id→名称/简介 join 用）
  listVocab: () => req<VocabEntry[]>('/api/vocab'),

  // ── 参考视频解析 ──
  uploadReference: (file: File) => {
    const fd = new FormData()
    fd.append('video', file)
    return req<RefVideo>('/api/references', { method: 'POST', body: fd })
  },
  listReferences: () => req<RefVideo[]>('/api/references'),
  referenceFrames: (id: string) =>
    req<{ frames: string[]; has_video: boolean }>(`/api/references/${id}/frames`),
  analyzeReference: (id: string) => post<{ ok: boolean }>(`/api/references/${id}/analyze`),
  confirmReference: (id: string) =>
    post<{ ok: boolean; style_id?: string }>(`/api/references/${id}/confirm`),
  // 确认页编辑解析草稿（入库前增删改特色）
  updateReferenceDraft: (id: string, style: RefVideoStyle) =>
    req<{ ok: boolean }>(`/api/references/${id}/draft`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ style }),
    }),

  // 项目解析（两路解析入口二）：读项目定稿设计系统+样张帧 → 暂存条目 → 异步文本
  // 打标；幂等（本项目已有条目返回 existing=true），进度轮询 listReferences
  parseProjectStyle: (id: string) =>
    post<{ ok: boolean; ref_id: string; existing?: boolean }>(`/api/projects/${id}/style/parse`),

  // ── 模型设置（管理员，docs/llm-settings-2026-10-29.md）──
  // GET 视图含工位清单与各工位生效值；PUT 整体保存（key 空=保持已存；warning=远程库
  // 写失败但本机已生效）；test 连通探针
  llmSettings: () => req<LlmSettingsView>('/api/llm-settings'),
  saveLlmSettings: (cfg: LlmSettingsInput) =>
    req<{ ok: boolean; warning?: string }>('/api/llm-settings', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(cfg),
    }),
  testLlmProvider: (t: { provider?: string; base_url?: string; api_key?: string; model?: string }) =>
    post<LlmTestResult>('/api/llm-settings/test', t),
}

// rev = 成片代次（重做完成后递增）：同 URL 会被 <video> 惰性复用，重做完必须换 URL 才能看到新画面
export const videoURL = (id: string, rev = 0) => `/api/projects/${id}/video/main.mp4${rev ? `?v=${rev}` : ''}`
// 项目内白名单文件（style/样张真迹、assets/img/素材、reports/ 审查截帧；过程页对话缩略图用）
export const artifactFileURL = (id: string, path: string) => `/api/projects/${id}/file/${path}`
// 参考引用媒体（抽帧 frame-*.png / 原视频 video.mp4；详情抽屉用）
export const referenceMediaURL = (id: string, file: string) => `/api/references/${id}/media/${file}`
// 风格库媒体（入库快照；详情抽屉用）
export const libraryMediaURL = (id: string, file: string) => `/api/library/${id}/media/${file}`
// 迷你真段 mp4（样张动态小样；后端白名单 clip-<N>.mp4）
export const styleClipURL = (id: string, file: string) => `/api/projects/${id}/style/clips/${file}`
