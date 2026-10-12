import type { ActivityEvent, AudioMeta, AuthStatus, ChatRef, Manuscript, Msg, ProjectRow, ProjectView, RefVideo, RefVideoStyle, Storyboard, Style, StyleSamples, UserInfo, UserRow, VocabEntry } from './types'

// ── 会话认证（cookie 会话，HttpOnly，浏览器自动携带）──────────
// 凭据不进 JS：localStorage 存 token / ?token= 进 URL 的旧方案已废。
// 401 统一回调 onUnauthorized（App 启动时注入）→ 切登录页。
let onUnauthorized: (() => void) | null = null
export function setOnUnauthorized(fn: () => void) { onUnauthorized = fn }

async function authFetch(input: RequestInfo, init?: RequestInit): Promise<Response> {
  const res = await fetch(input, init)
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
async function req<T>(input: RequestInfo, init?: RequestInit): Promise<T> {
  return j<T>(await authFetch(input, init))
}
function post<T>(input: RequestInfo, body?: unknown, signal?: AbortSignal): Promise<T> {
  return req<T>(input, body === undefined
    ? { method: 'POST', signal }
    : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body), signal })
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

  createProject: (name: string, topic: string, aspect = '9:16') =>
    post<{ id: string; name: string }>('/api/projects', { name, topic, aspect }),

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

  manuscript: (id: string) => req<Manuscript>(`/api/projects/${id}/manuscript`),

  // 文稿弹窗保存（article.txt 为真源；storyboard_stale=分镜基于旧稿需重新生成）
  saveManuscript: (id: string, content: string) =>
    req<{ ok: boolean; word_count: number; storyboard_stale: boolean }>(`/api/projects/${id}/manuscript`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content }),
    }),

  // 风格弹窗选定库内条目（v3：写风格简报，特色开关默认全开；设计系统本片现发明，
  // 样张确认硬门不再被跳过）。disabled = 关掉的特色 id 列表。
  applyStyle: (id: string, styleId: string, disabled: string[] = []) =>
    post<{ ok: boolean; name: string; signatures: number }>(`/api/projects/${id}/style/apply`, { style_id: styleId, disabled }),

  style: (id: string) => req<StyleSamples>(`/api/projects/${id}/style`),

  confirmStyle: (id: string) => post<{ ok: boolean }>(`/api/projects/${id}/style/confirm`),

  chat: (id: string, content: string, refs: ChatRef[] = []) =>
    post<{ ok: boolean }>(`/api/projects/${id}/chat`, { content, refs }),

  // 中断当前项目正在执行的对话轮（服务端取消其上下文；ok=false=没有在跑的轮）
  cancelChat: (id: string) => post<{ ok: boolean }>(`/api/projects/${id}/chat/cancel`),

  // AI 起草文稿：按目标字数直驱（分钟换算在 UI 侧）。signal 供取消按钮掐断请求，
  // 服务端随连接断开中止生成
  draftManuscript: (topic: string, words = 500, signal?: AbortSignal) =>
    post<{ content: string }>('/api/draft/manuscript', { topic, words }, signal),

  // 链接导入（readability 正文提取，文稿弹窗用）
  extractLink: (url: string) =>
    post<{ title: string; content: string; words: number }>('/api/extract_link', { url }),

  messages: (id: string, after = 0) => req<Msg[]>(`/api/projects/${id}/messages?after=${after}`),

  cancel: (id: string) => post<{ ok: boolean }>(`/api/projects/${id}/cancel`),

  // 制作活动流（manifest 原样事件，尾部 500 条正序；事件流面板数据源）
  activity: (id: string) => req<ActivityEvent[]>(`/api/projects/${id}/activity`),

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
}

// rev = 成片代次（重做完成后递增）：同 URL 会被 <video> 惰性复用，重做完必须换 URL 才能看到新画面
export const videoURL = (id: string, rev = 0) => `/api/projects/${id}/video/main.mp4${rev ? `?v=${rev}` : ''}`
// 参考引用媒体（抽帧 frame-*.png / 原视频 video.mp4；详情抽屉用）
export const referenceMediaURL = (id: string, file: string) => `/api/references/${id}/media/${file}`
// 风格库媒体（入库快照；详情抽屉用）
export const libraryMediaURL = (id: string, file: string) => `/api/library/${id}/media/${file}`
// 迷你真段 mp4（样张动态小样；后端白名单 clip-<N>.mp4）
export const styleClipURL = (id: string, file: string) => `/api/projects/${id}/style/clips/${file}`
