import type { AudioMeta, ChatRef, Msg, ProjectRow, ProjectView, RefVideo, Storyboard, Style, StyleSamples } from './types'

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

export const api = {
  listProjects: () => authFetch('/api/projects').then(r => j<ProjectRow[]>(r)),

  createProject: (name: string, topic: string, aspect = '9:16') =>
    authFetch('/api/projects', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, topic, aspect }),
    }).then(r => j<{ id: string; name: string }>(r)),

  project: (id: string) => authFetch(`/api/projects/${id}`).then(r => j<ProjectView>(r)),

  storyboard: (id: string) => authFetch(`/api/projects/${id}/storyboard`).then(r => j<Storyboard>(r)),

  audioMeta: (id: string) => authFetch(`/api/projects/${id}/audio_meta`).then(r => j<AudioMeta>(r)),

  manuscript: (id: string) =>
    authFetch(`/api/projects/${id}/manuscript`).then(r => j<{ content: string; word_count: number }>(r)),

  // 文稿弹窗保存（article.txt 为真源；storyboard_stale=分镜基于旧稿需重新生成）
  saveManuscript: (id: string, content: string) =>
    authFetch(`/api/projects/${id}/manuscript`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content }),
    }).then(r => j<{ ok: boolean; word_count: number; storyboard_stale: boolean }>(r)),

  // 风格弹窗选定库内风格（预览后显式确认 = 硬门通过）
  applyStyle: (id: string, styleId: string) =>
    authFetch(`/api/projects/${id}/style/apply`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ style_id: styleId }),
    }).then(r => j<{ ok: boolean; direction: string }>(r)),

  style: (id: string) => authFetch(`/api/projects/${id}/style`).then(r => j<StyleSamples>(r)),

  confirmStyle: (id: string) =>
    authFetch(`/api/projects/${id}/style/confirm`, { method: 'POST' }).then(r => j<{ ok: boolean }>(r)),

  chat: (id: string, content: string, refs: ChatRef[] = []) =>
    authFetch(`/api/projects/${id}/chat`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content, refs }),
    }).then(r => j<{ ok: boolean }>(r)),

  frameText: (id: string, seg: string) =>
    authFetch(`/api/projects/${id}/frames/${seg}`).then(r => {
      if (!r.ok) throw new Error(r.statusText)
      return r.text()
    }),

  draftManuscript: (topic: string, minutes = 1) =>
    authFetch('/api/draft/manuscript', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ topic, minutes }),
    }).then(r => j<{ content: string }>(r)),

  // 链接导入（readability 正文提取，文稿弹窗用）
  extractLink: (url: string) =>
    authFetch('/api/extract_link', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url }),
    }).then(r => j<{ title: string; content: string; words: number }>(r)),

  messages: (id: string, after = 0) =>
    authFetch(`/api/projects/${id}/messages?after=${after}`).then(r => j<Msg[]>(r)),

  produce: (id: string) =>
    authFetch(`/api/projects/${id}/produce`, { method: 'POST' }).then(r => j<{ ok: boolean }>(r)),

  // 一键段级重做（检视态按钮直连；instruction 空 = 服务端复用该段上次指令）
  reworkStart: (id: string, segments: string[], instruction = '') =>
    authFetch(`/api/projects/${id}/rework`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ segments, instruction }),
    }).then(r => j<{ ok: boolean; labels: string[]; instruction: string }>(r)),

  reworkLast: (id: string, seg: string) =>
    authFetch(`/api/projects/${id}/rework/last?seg=${encodeURIComponent(seg)}`)
      .then(r => j<{ instruction: string }>(r)),

  cancel: (id: string) =>
    authFetch(`/api/projects/${id}/cancel`, { method: 'POST' }).then(r => j<{ ok: boolean }>(r)),

  listLibrary: () => authFetch('/api/library').then(r => j<Style[]>(r)),

  publishPack: (id: string) =>
    authFetch(`/api/library/${id}/publish`, { method: 'POST' }).then(r => j<Style>(r)),

  // ── 参考视频解析 ──
  uploadReference: (file: File) => {
    const fd = new FormData()
    fd.append('video', file)
    return authFetch('/api/references', { method: 'POST', body: fd }).then(r => j<RefVideo>(r))
  },
  listReferences: () => authFetch('/api/references').then(r => j<RefVideo[]>(r)),
  referenceFrames: (id: string) =>
    authFetch(`/api/references/${id}/frames`).then(r => j<{ frames: string[]; has_video: boolean }>(r)),
  analyzeReference: (id: string) =>
    authFetch(`/api/references/${id}/analyze`, { method: 'POST' }).then(r => j<{ ok: boolean }>(r)),
  confirmReference: (id: string) =>
    authFetch(`/api/references/${id}/confirm`, { method: 'POST' }).then(r => j<{ ok: boolean; style_id: string }>(r)),
}

// rev = 成片代次（重做完成后递增）：同 URL 会被 <video> 惰性复用，重做完必须换 URL 才能看到新画面
export const videoURL = (id: string, rev = 0) => withToken(`/api/projects/${id}/video/main.mp4${rev ? `?v=${rev}` : ''}`)
// 活合成物壳的确定性依赖：项目内本地 gsap（与渲染引擎同一份，无 CDN）
export const assetURL = (id: string, p: string) => withToken(`/api/projects/${id}/assets/${p}`)
// 参考引用媒体（抽帧 frame-*.png / 原视频 video.mp4；详情抽屉用）
export const referenceMediaURL = (id: string, file: string) => withToken(`/api/references/${id}/media/${file}`)
