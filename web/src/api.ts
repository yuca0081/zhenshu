import type { AudioMeta, ChatRef, Manuscript, Msg, ProjectRow, ProjectView, RefVideo, Storyboard, Style, StyleSamples } from './types'

// ── API token（服务端 .env 配了 API_TOKEN 时启用）──────────────
// 401 时提示输入一次，存 localStorage；之后所有请求带头，媒体 URL 带 ?token=
const TOKEN_KEY = 'api_token'
const apiToken = () => localStorage.getItem(TOKEN_KEY) || ''

let prompting = false
async function authFetch(input: RequestInfo, init?: RequestInit): Promise<Response> {
  const h = new Headers(init?.headers)
  const t = apiToken()
  if (t) h.set('X-API-Token', t)
  let res = await fetch(input, { ...init, headers: h })
  if (res.status === 401 && !prompting) {
    prompting = true
    try {
      const t2 = (window.prompt('此服务已启用访问鉴权，请输入 API Token：') || '').trim()
      if (t2) {
        localStorage.setItem(TOKEN_KEY, t2)
        h.set('X-API-Token', t2)
        res = await fetch(input, { ...init, headers: h })
      }
    } finally {
      prompting = false
    }
  }
  return res
}

// <video>/<img>/EventSource 走不了请求头：token 以查询参数补（服务端两处都认）
const withToken = (u: string) => {
  const t = apiToken()
  return t ? u + (u.includes('?') ? '&' : '?') + 'token=' + encodeURIComponent(t) : u
}
export const sseURL = (id: string) => withToken(`/api/projects/${id}/events`)

async function j<T>(res: Response): Promise<T> {
  if (!res.ok) throw new Error((await res.json().catch(() => ({} as any))).error ?? res.statusText)
  return res.json()
}

// req/post = authFetch + JSON 解包一步到位（此前 23 处 .then(r => j<T>(r)) 样板）
async function req<T>(input: RequestInfo, init?: RequestInit): Promise<T> {
  return j<T>(await authFetch(input, init))
}
function post<T>(input: RequestInfo, body?: unknown): Promise<T> {
  return req<T>(input, body === undefined
    ? { method: 'POST' }
    : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
}

export const api = {
  listProjects: () => req<ProjectRow[]>('/api/projects'),

  createProject: (name: string, topic: string, aspect = '9:16') =>
    post<{ id: string; name: string }>('/api/projects', { name, topic, aspect }),

  project: (id: string) => req<ProjectView>(`/api/projects/${id}`),

  storyboard: (id: string) => req<Storyboard>(`/api/projects/${id}/storyboard`),

  audioMeta: (id: string) => req<AudioMeta>(`/api/projects/${id}/audio_meta`),

  manuscript: (id: string) => req<Manuscript>(`/api/projects/${id}/manuscript`),

  // 文稿弹窗保存（article.txt 为真源；storyboard_stale=分镜基于旧稿需重新生成）
  saveManuscript: (id: string, content: string) =>
    req<{ ok: boolean; word_count: number; storyboard_stale: boolean }>(`/api/projects/${id}/manuscript`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content }),
    }),

  // 风格弹窗选定库内风格（预览后显式确认 = 硬门通过）
  applyStyle: (id: string, styleId: string) =>
    post<{ ok: boolean; direction: string }>(`/api/projects/${id}/style/apply`, { style_id: styleId }),

  style: (id: string) => req<StyleSamples>(`/api/projects/${id}/style`),

  confirmStyle: (id: string) => post<{ ok: boolean }>(`/api/projects/${id}/style/confirm`),

  chat: (id: string, content: string, refs: ChatRef[] = []) =>
    post<{ ok: boolean }>(`/api/projects/${id}/chat`, { content, refs }),

  frameText: (id: string, seg: string) =>
    authFetch(`/api/projects/${id}/frames/${seg}`).then(r => {
      if (!r.ok) throw new Error(r.statusText)
      return r.text()
    }),

  draftManuscript: (topic: string, minutes = 1) =>
    post<{ content: string }>('/api/draft/manuscript', { topic, minutes }),

  // 链接导入（readability 正文提取，文稿弹窗用）
  extractLink: (url: string) =>
    post<{ title: string; content: string; words: number }>('/api/extract_link', { url }),

  messages: (id: string, after = 0) => req<Msg[]>(`/api/projects/${id}/messages?after=${after}`),

  produce: (id: string) => post<{ ok: boolean }>(`/api/projects/${id}/produce`),

  // 一键段级重做（检视态按钮直连；instruction 空 = 服务端复用该段上次指令）
  reworkStart: (id: string, segments: string[], instruction = '') =>
    post<{ ok: boolean; labels: string[]; instruction: string }>(`/api/projects/${id}/rework`, { segments, instruction }),

  reworkLast: (id: string, seg: string) =>
    req<{ instruction: string }>(`/api/projects/${id}/rework/last?seg=${encodeURIComponent(seg)}`),

  cancel: (id: string) => post<{ ok: boolean }>(`/api/projects/${id}/cancel`),

  listLibrary: () => req<Style[]>('/api/library'),

  publishPack: (id: string) => post<Style>(`/api/library/${id}/publish`),

  // 风格媒体（视频解析入库的原片+抽帧快照；老条目服务端自动回退 references 目录）
  libraryFrames: (id: string) =>
    req<{ frames: string[]; has_video: boolean }>(`/api/library/${id}/frames`),

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
    post<{ ok: boolean; style_id: string }>(`/api/references/${id}/confirm`),
}

// rev = 成片代次（重做完成后递增）：同 URL 会被 <video> 惰性复用，重做完必须换 URL 才能看到新画面
export const videoURL = (id: string, rev = 0) => withToken(`/api/projects/${id}/video/main.mp4${rev ? `?v=${rev}` : ''}`)
// 活合成物壳的确定性依赖：项目内本地 gsap（与渲染引擎同一份，无 CDN）
export const assetURL = (id: string, p: string) => withToken(`/api/projects/${id}/assets/${p}`)
// 参考引用媒体（抽帧 frame-*.png / 原视频 video.mp4；详情抽屉用）
export const referenceMediaURL = (id: string, file: string) => withToken(`/api/references/${id}/media/${file}`)
// 风格库媒体（入库快照；详情抽屉用）
export const libraryMediaURL = (id: string, file: string) => withToken(`/api/library/${id}/media/${file}`)
