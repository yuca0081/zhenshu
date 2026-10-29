<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import {
  NConfigProvider, NMessageProvider, createDiscreteApi, darkTheme,
} from 'naive-ui'
import HistoryRail from './components/HistoryRail.vue'
import StagePanel from './components/StagePanel.vue'
import ChatPanel from './components/ChatPanel.vue'
import NewProjectModal from './components/NewProjectModal.vue'
import LibraryCenter from './components/LibraryCenter.vue'
import HomeView from './components/HomeView.vue'
import LoginView from './components/LoginView.vue'
import AccountMenu from './components/AccountMenu.vue'
import UserAdminModal from './components/UserAdminModal.vue'
import LlmSettingsPage from './components/LlmSettingsPage.vue'
import { api, setOnUnauthorized, sseURL } from './api'
import type { ActivityEvent, AudioMeta, ChatRef, Manuscript, Msg, PrepView, ProjectRow, ProjectView, Storyboard, Style, StyleSamples, TabID, UserInfo } from './types'
import { errMsg } from './format'
import { segAt, segTable } from './segtime'

// ── 登录会话：boot 先问 /api/auth/status，再决定进应用还是停在登录页 ──
// auth_required=false = 服务端开放模式（本机回环免登录），user=null 时也可直接进。
const user = ref<UserInfo | null>(null)
const authRequired = ref(true)
const authReady = ref(false)
const showUserAdmin = ref(false)

// 任意接口 401（会话过期/被登出）→ 统一切回登录页；登录页自身的 401（密码错）
// 也走这里，但 user 本就为 null，等于无操作。
setOnUnauthorized(() => {
  if (!authReady.value || !authRequired.value || !user.value) return
  user.value = null
  topMsg.warning('登录已过期，请重新登录')
})

async function boot() {
  try {
    const st = await api.authStatus()
    authRequired.value = st.auth_required
    user.value = st.user
  } catch {
    // 服务不可达：按需登录态处理，登录页/应用内各自的重试反馈兜底
  }
  authReady.value = true
  if (!authRequired.value || user.value) {
    loadProjects()
    loadLibrary()
    applyHash()
  }
}

async function onLoggedIn(u: UserInfo) {
  user.value = u
  loadProjects()
  loadLibrary()
  applyHash()
}

async function onLogout() {
  try { await api.logout() } catch { /* cookie 清不掉也照样回登录页 */ }
  user.value = null
  current.value = ''
  ++reqSeq
  es?.close()
  page.value = 'home'
}

const projects = ref<ProjectRow[]>([])
const current = ref<string>('')
// 四页导航：首页（落地+快速开始）/ 工作区（项目三栏）/ 风格库（风格+视频解析）
// / 设置（模型设置，不进页签组——顶栏「设置」按钮进，admin 专属）
type PageID = 'home' | 'workspace' | 'materials' | 'settings'
const tabs = [
  { id: 'home', label: '首页' }, { id: 'workspace', label: '工作区' }, { id: 'materials', label: '风格库' },
] as const
const page = ref<PageID>('home')
// 风格库页签：风格库 / 视频解析（工作区「解析到风格库」按钮跳转时直接落在视频解析）
const materialsTab = ref<'styles' | 'videos'>('styles')
const view = ref<ProjectView | null>(null)
const storyboard = ref<Storyboard | null>(null)
const style = ref<StyleSamples | null>(null)
const manuscript = ref<Manuscript | null>(null)
const prep = ref<PrepView | null>(null) // 准备阶段三工件（终审页签数据源）
const msgs = ref<Msg[]>([])
const chatDraft = ref('')
const chatRefs = ref<ChatRef[]>([]) // 引用列表（时间轴/分镜点选加入，输入框上方展示）
const audioMeta = ref<AudioMeta | null>(null) // 时间轴字幕/配音轨数据源
// 制作管线各段状态：'' | running | done | error
const stageState = ref<Record<string, string>>({})
const agentBusy = ref(false)
// 对话轮整轮锁定态（文稿编辑区据此上锁）：agentBusy 有 90s 自愈衰减，盖不住
// 长思考轮（写稿一轮就要 1-4 分钟）；turnActive 由 markBusy 置位、agent_msg
// 终局清除，20min（=后端单轮上限）兜底自愈，断连丢事件也不会永久锁死。
const turnActive = ref(false)
const library = ref<Style[]>([])
const videoRev = ref(0) // 成片代次：done 后递增 → video 换 URL 强制重载（重做完立刻看到新画面）
const progressLine = ref('') // 制作/重做细粒度进度（SSE progress，瞬时态不入聊天史）
const activity = ref<ActivityEvent[]>([]) // 制作事件流（manifest 原样事件，「过程」页数据源）

// naive-ui 全局主色对齐页面金色（创建按钮/单选/焦点等不再出现默认绿）
const themeOverrides = {
  common: {
    primaryColor: '#f0c674',
    primaryColorHover: '#ffdd9a',
    primaryColorPressed: '#d9ae5b',
    primaryColorSuppl: '#f0c674',
    borderRadius: '8px',
  },
}

// App 自身在 n-message-provider 之外（provider 包的是模板内容），useMessage 拿不到
// 上下文——顶层加载/确认失败的反馈走离散 API，避免静默吞错 + unhandled rejection
const { message: topMsg } = createDiscreteApi(['message'], {
  configProviderProps: { theme: darkTheme, themeOverrides },
})

let es: EventSource | null = null
let busyTimer: number | null = null
let turnTimer: number | null = null
let activityTimer: number | null = null
let reqSeq = 0 // 会话内自增：切项目即作废在途响应——A 项目的数据晚到不许覆盖 B 的页
// 同 key 在途去重：key → 发起时的 reqSeq。只有"发起时与当前同代次"的在途请求才挡新请求；
// 记录的 seq 已过期（中途切过项目，旧响应注定被作废）时直接被新请求覆盖，否则快速
// A→B→A 时第二次 loadProject(A) 被旧请求挡掉、旧响应又被 seq 丢弃，页面空白到下个事件。
// finally 里同样校验代次再删：先完成的旧代请求不许误删新代请求的在途标记。
const inflight = new Map<string, number>()

// 本次运行收到的第一粒 stage 键（stageState 每次清空后重播）：首制第一粒必是
// tts（复用轮后端也发 done），重做第一粒是 compositions——判运行类型用；
// SSE 半路接入错过了首粒时，靠下面 runKind 的独有键纠偏。
const firstStageKey = ref('')
// 制作阶段序按运行类型取（2026-10-22 起首制全是段级流水线，旧栅栏六格里
// assemble/frameqa 只有段级重做路径还会发，首制永远灰着）：首制 = 配音→画面
// （字体/样张准备＋段级流水线主体，逐段 check 不单列——细账在过程页与进度行）→
// 拼接渲→全片终审；重做保持六格，tts 不跑但由 VideoPane 显示「配音不动」。
const runKind = computed<'produce' | 'rework'>(() => {
  const s = stageState.value
  if (s.segpipe || s.review) return 'produce' // 首制独有键
  if (s.assemble || s.frameqa) return 'rework' // 重做独有键
  return firstStageKey.value === 'compositions' ? 'rework' : 'produce'
})
const stageOrder = computed(() =>
  runKind.value === 'rework'
    ? ['tts', 'compositions', 'assemble', 'check', 'render', 'frameqa']
    : ['tts', 'compositions', 'render', 'review'])

async function loadProjects() {
  try {
    projects.value = await api.listProjects()
  } catch (e) {
    // 首屏/刷新失败要有反馈：否则空列表无解释，还是 unhandled rejection
    topMsg.error(`加载项目列表失败：${errMsg(e)}`)
  }
}

async function loadLibrary() {
  library.value = await api.listLibrary().catch(() => [])
}

async function publishPack(id: string) {
  await api.publishPack(id).catch(e => topMsg.error(`入库失败：${errMsg(e)}`))
  await loadLibrary()
}

// 跳风格库页并落到指定页签（工作区「解析到风格库」按钮用：解析提交后跟进进度）
function openMaterials(tab: 'styles' | 'videos') {
  materialsTab.value = tab
  page.value = 'materials'
}

async function loadProject(id: string) {
  const key = `project:${id}`
  const seq = reqSeq
  if (inflight.get(key) === seq) return // 同代次的同 key 在途请求才去重（语义见 inflight 声明处）
  inflight.set(key, seq)
  try {
    const v = await api.project(id)
    if (seq !== reqSeq) return
    view.value = v
    const [sb, st, ms, am, pp] = await Promise.all([
      v.gates.storyboard ? api.storyboard(id).catch(() => null) : Promise.resolve(null),
      v.gates.style_draft ? api.style(id).catch(() => null) : Promise.resolve(null),
      // 门未过给 null：清掉旧项目的残留（否则新项目页会闪现上一个项目的文稿/分镜/样张）
      v.gates.manuscript ? api.manuscript(id).catch(() => null) : Promise.resolve(null),
      api.audioMeta(id).catch(() => null),
      api.prep(id).catch(() => null),
    ])
    if (seq !== reqSeq) return
    storyboard.value = sb
    style.value = st
    manuscript.value = ms
    audioMeta.value = am
    // exists=false 也要保留：running/legacy/idea 是「想法」页签的状态源
    prep.value = pp
  } finally {
    if (inflight.get(key) === seq) inflight.delete(key)
  }
}

async function loadMsgs() {
  const id = current.value
  if (!id) return
  const key = `msgs:${id}`
  const seq = reqSeq
  if (inflight.get(key) === seq) return
  inflight.set(key, seq)
  try {
    const after = msgs.value.length ? Math.max(...msgs.value.map(m => m.id)) : 0
    // ?? [] 兜底：服务端把空历史序列化成 null（老接口），null.filter 会崩掉对话栏
    const inc = (await api.messages(id, after).catch(() => [])) ?? []
    if (seq !== reqSeq || id !== current.value) return // 切项目了：旧消息不许混进新对话栏
    const seen = new Set(msgs.value.map(m => m.id))
    const fresh = inc.filter(m => !seen.has(m.id))
    if (fresh.length) msgs.value.push(...fresh)
  } finally {
    if (inflight.get(key) === seq) inflight.delete(key)
  }
}

async function loadActivity() {
  const id = current.value
  if (!id) return
  const key = `activity:${id}`
  const seq = reqSeq
  if (inflight.get(key) === seq) return
  inflight.set(key, seq)
  try {
    const rows = await api.activity(id, 2000).catch(() => null) // 过程页整页回看，尾部窗口放大
    if (rows === null || seq !== reqSeq || id !== current.value) return
    activity.value = rows
  } finally {
    if (inflight.get(key) === seq) inflight.delete(key)
  }
}

// activity 节流（2026-10-19 中-39）：渲染期每段都发 progress/stage，逐条全量拉
// 2000 行是请求风暴——与 throttledRefresh 同款 3s 窗口 + 尾随补刷（最后一次
// 触发后 3s 必有一拉，终态事件不丢）。制作中的 5s 定时轮询在跑时直接让位（它
// 就是那段时间的刷新源）。
let lastActLoad = 0
let actTimer: number | null = null
function throttledLoadActivity() {
  if (activityTimer != null) return
  const now = Date.now()
  if (now - lastActLoad >= 3000) {
    lastActLoad = now
    loadActivity()
    return
  }
  if (actTimer == null) {
    actTimer = window.setTimeout(() => {
      actTimer = null
      throttledLoadActivity()
    }, 3000 - (now - lastActLoad))
  }
}

function selectProject(id: string) {
  // 同项目重复点选只切页不重载：下面是无条件清场，而 current 同值赋值不触发
  // watch → refresh 不会执行，清掉的数据永不回来（页面空白）
  if (id === current.value) { page.value = 'workspace'; return }
  current.value = id
  page.value = 'workspace'
  ++reqSeq // 在途响应全部作废：先于清场，晚到的旧数据在 resolve 处直接丢弃
  msgs.value = []
  chatRefs.value = []
  chatDraft.value = '' // 草稿不随项目带（中-40：两份清场曾都漏了它，切项目后半截话串台）
  progressLine.value = ''
  stageState.value = {}
  activity.value = []
  agentBusy.value = false // 上一项目的「Agent 工作中」不带给新项目（不然转圈挂到 90s 超时）
  endTurn() // 上一项目的对话轮锁同理不带给新项目
  manualTab.value = null // 换项目回到自动落页
  // 先清上一项目的全部展示数据：loadProject 是异步的，不清会在新项目页闪现旧内容
  view.value = null
  storyboard.value = null
  style.value = null
  manuscript.value = null
  prep.value = null
  audioMeta.value = null
}

// 📎 引用：点分镜/时间轴加段级（按段去重）；双击轨道/「引用此刻」加时刻级（按 ±0.75s 去重）；
// 可单删、可清空。元素级引用只在历史消息里出现（检视模式已下线，不再进引用列表）
function addRef(idx: number, key: string) {
  if (!chatRefs.value.some(r => r.idx === idx)) chatRefs.value = [...chatRefs.value, { idx, key }]
}
function addTimeRef(t: number) {
  const hit = segAt(segTable(storyboard.value, audioMeta.value), t)
  if (!hit) return
  if (chatRefs.value.some(r => r.idx === hit.idx && r.t != null && Math.abs(r.t - t) < 0.75)) return
  chatRefs.value = [...chatRefs.value, { idx: hit.idx, key: hit.key, t }]
}
function removeRef(ref: ChatRef) {
  // 可单删语义（中-L）：删段级只删段级条目本身，不级联吞掉同段的时刻级引用——
  // 要清 moments 再各自点删或清空
  chatRefs.value = chatRefs.value.filter(r =>
    ref.t != null ? !(r.idx === ref.idx && r.t != null && Math.abs(r.t - ref.t) < 0.75)
      : !(r.idx === ref.idx && r.t == null))
}

async function refresh() {
  if (!current.value) return
  await loadProject(current.value).catch(() => {})
  await loadMsgs()
  loadActivity() // 事件流跟随刷新（done/cancelled/error 等 SSE 监听都走 refresh）
}

// 断连自愈刷新节流：EventSource 原生错误后端一起、浏览器就重连一次，15s 内只刷
// 一轮——断连期间的重连风暴不放大成接口风暴
let lastNetRefresh = 0
function throttledRefresh() {
  const now = Date.now()
  if (now - lastNetRefresh < 15_000) return
  lastNetRefresh = now
  refresh()
}

// SSE 触发的全量加载节流：tool_result 每条都 loadProject（1+4 个请求），制作密集期
// 就是请求风暴。3s 最小间隔 + 尾随补一次——窗口内最后一条事件之后必再刷一轮，最终一致。
// 只节流 SSE 这条路：用户动作（保存/确认风格/done）仍直连 refresh 立即生效。
let lastSSEProjLoad = 0
let projLoadTimer: number | null = null
function throttledLoadProject() {
  const now = Date.now()
  if (now - lastSSEProjLoad < 3_000) {
    if (projLoadTimer == null) {
      projLoadTimer = window.setTimeout(() => {
        projLoadTimer = null
        if (current.value) loadProject(current.value).catch(() => {})
      }, 3_000 - (now - lastSSEProjLoad))
    }
    return
  }
  lastSSEProjLoad = now
  if (current.value) loadProject(current.value).catch(() => {})
}

function markBusy() {
  agentBusy.value = true
  if (busyTimer) clearTimeout(busyTimer)
  busyTimer = window.setTimeout(() => { agentBusy.value = false }, 90_000)
  turnActive.value = true
  if (turnTimer) clearTimeout(turnTimer)
  turnTimer = window.setTimeout(() => { turnActive.value = false }, 20 * 60_000)
}

function endTurn() {
  turnActive.value = false
  if (turnTimer) { clearTimeout(turnTimer); turnTimer = null }
}

// 中断对话栏 Agent 轮（文稿横幅「中断 AI」）：服务端取消正在跑的轮，收尾消息
// 走 agent_msg 事件回来；没有在跑的轮（ok=false，锁是残留态）也照常就地解锁
async function cancelAgentTurn() {
  if (!current.value) return
  try { await api.cancelChat(current.value) } catch { /* 服务不可达时先解锁，别把用户锁死在界面上 */ }
  endTurn()
  loadMsgs()
}

function openSSE(id: string) {
  es?.close()
  es = new EventSource(sseURL(id))
  es.addEventListener('msg', () => loadMsgs())
  es.addEventListener('agent_msg', ev => {
    agentBusy.value = false
    endTurn() // 轮终局（正常回复/出错/中断收尾都走 agent_msg）：解锁文稿编辑区
    loadMsgs()
    loadLibrary() // Agent 可能刚起草了风格包
  })
  es.addEventListener('tool_call', () => { markBusy(); loadMsgs() })
  es.addEventListener('tool_result', () => { loadMsgs(); throttledLoadProject() })
  es.addEventListener('stage', ev => {
    // 服务端事件体不可全信：非 JSON / 缺字段时忽略该条（原生监听器抛异常只会刷爆 console）
    try {
      const d = JSON.parse((ev as MessageEvent).data)
      if (!d?.stage || typeof d.detail !== 'string' || d.stage === 'pipeline') return
      const st = d.detail.startsWith('running') ? 'running'
        : d.detail.startsWith('done') ? 'done'
        : d.detail.startsWith('error') || d.detail.startsWith('repair') ? 'error' : 'running'
      if (!Object.keys(stageState.value).length) firstStageKey.value = d.stage
      stageState.value = { ...stageState.value, [d.stage]: st }
      loadMsgs()
      throttledLoadActivity() // 阶段推进即拉事件流（用时/打回记帐在 manifest 里；3s 节流）
    } catch { /* 坏事件静默丢弃 */ }
  })
  es.addEventListener('progress', ev => {
    try {
      const d = JSON.parse((ev as MessageEvent).data)
      progressLine.value = typeof d === 'string' ? d : d?.detail ?? ''
    } catch { return } // 非 JSON 进度行不覆盖当前值
    throttledLoadActivity()
  })
  es.addEventListener('style_confirmed', () => loadProject(id).catch(() => {}))
  es.addEventListener('style_applied', () => { loadProject(id).catch(() => {}); loadLibrary() })
  // 准备工位：确认门落定 / 反馈重跑完成 → 重拉三工件与门态
  es.addEventListener('prep_confirmed', () => loadProject(id).catch(() => {}))
  es.addEventListener('prep_updated', () => loadProject(id).catch(() => {}))
  es.addEventListener('done', () => {
    stageState.value = {}
    progressLine.value = ''
    videoRev.value++
    refresh()
  })
  es.addEventListener('cancelled', () => { stageState.value = {}; progressLine.value = ''; refresh() })
  // 制作失败同样清场：只 refresh 不清 stageState 的话，has_video=false + 残留阶段态
  // 会让视频页签永远转圈显示「制作管线运行中」且没有停止按钮（假运行中）。
  // 业务 error 事件（管线终态）与 EventSource 原生连接错误分开：原生错误（后端断连，
  // 浏览器每 ~3s 自动重连）只做节流刷新——不清制作中的阶段态（清了进度 UI 消失到
  // 下个 stage 事件才回来），更不能按重连节奏每次全量刷出请求风暴。
  es.addEventListener('error', ev => {
    if ((ev as MessageEvent).data) {
      stageState.value = {}
      progressLine.value = ''
      refresh()
    } else {
      throttledRefresh()
    }
  })
}

// 两个联动同源（view.producing）合成一个 watch：
// ① 自愈兜底（覆盖 SSE 断连/丢事件/服务重启）：一旦变 false 就清制作中的瞬时态并换代
//    成片——磁盘事实（producing=false）永远压过前端内存态；
// ② 制作中每 5s 轮询事件流：render.seg / 预审轮次这类 manifest 细粒度事件没有对应
//    SSE，SSE stage/progress 只兜住粗粒度推进，细账靠轮询补。
watch(() => view.value?.producing, (now, was) => {
  if (was && now === false) {
    stageState.value = {}
    progressLine.value = ''
    videoRev.value++
  }
  if (now && activityTimer == null) activityTimer = window.setInterval(loadActivity, 5000)
  if (!now && activityTimer != null) { clearInterval(activityTimer); activityTimer = null }
})
watch(current, id => {
  if (id) { openSSE(id); refresh() }
  else es?.close() // 项目删空/切换到空：断开上一个项目的订阅
})
// 定时器/SSE/监听的统一拆解（中-L5：原先两处 onBeforeUnmount 分立，漏一个就是泄漏）
onBeforeUnmount(() => {
  es?.close()
  if (activityTimer != null) clearInterval(activityTimer)
  if (actTimer != null) clearTimeout(actTimer)
  if (projLoadTimer != null) clearTimeout(projLoadTimer)
  if (busyTimer) clearTimeout(busyTimer)
  if (turnTimer) clearTimeout(turnTimer)
  window.removeEventListener('hashchange', applyHash)
})

// 初始：先问登录态（boot 内部再拉列表/恢复页面；applyHash 走 async 续程，
// 此时本 setup 已全部声明完毕，无 TDZ 问题）
boot()

// ── 页面状态 ↔ URL hash：刷新/前进后退/分享链接不再弹回首页 ──────────
// #/home（缺省）/ #/materials[/videos] / #/workspace/<项目id>
function hashTarget(): string {
  if (page.value === 'workspace') return current.value ? `#/workspace/${current.value}` : '#/workspace'
  if (page.value === 'materials') return materialsTab.value === 'videos' ? '#/materials/videos' : '#/materials'
  if (page.value === 'settings') return '#/settings'
  return '#/home'
}
watch([page, current], () => {
  const t = hashTarget()
  if (location.hash !== t) location.hash = t
})
function applyHash() {
  const h = location.hash
  if (h.startsWith('#/materials')) {
    page.value = 'materials'
    if (/^#\/materials\/videos/.test(h)) materialsTab.value = 'videos'
  } else if (h.startsWith('#/workspace')) {
    const id = /^#\/workspace\/([A-Za-z0-9_-]+)/.exec(h)?.[1] ?? ''
    // 同项目只切页不重载（重载会清聊天/阶段态）；异项目走完整选择流程
    if (id && id !== current.value) selectProject(id)
    else page.value = 'workspace'
  } else if (h.startsWith('#/settings')) {
    page.value = 'settings'
  } else {
    page.value = 'home'
  }
}
window.addEventListener('hashchange', applyHash)
// applyHash() 的首启调用放 script 末尾：它会走 selectProject → manualTab 等
// 后文才声明的状态，提前调用 = TDZ ReferenceError（直链 #/workspace/<id> 进项目必炸）。

const showNew = ref(false)

// ── 中央页签（弹窗体系退役）：未手动选择时按管线进度自动落页，点过就以用户为准 ──
const manualTab = ref<TabID | null>(null)
const autoTab = computed<TabID>(() => {
  const v = view.value
  if (!v) return 'manuscript'
  if (v.has_video || v.producing) return 'video'
  // 音乐驱动 PV：准备期只有「想法（PV 准备页）」一站，文稿链的自动落页
  // 规则（白板→想法/三工件→终审/风格→分镜→文稿）对它不适用
  if (v.mode === 'music') return 'idea'
  // 准备链在跑 / 白板新项目（无文稿无工件非 legacy）→ 停在「想法」启动页
  if (prep.value?.running) return 'idea'
  if (prep.value && !prep.value.exists && !prep.value.legacy && !v.gates.manuscript) return 'idea'
  // 打包终审（全流程唯一人工门）：三工件出齐未确认 → 停在终审
  if (prep.value?.exists && prep.value.script && prep.value.stylepack && prep.value.draft && !prep.value.confirmed) return 'review'
  if (v.gates.style_draft) return 'style'
  if (v.gates.storyboard) return 'storyboard'
  return 'manuscript'
})
const activeTab = computed<TabID>(() => manualTab.value ?? autoTab.value)
function setTab(t: TabID) { manualTab.value = t }

async function onCreated(id: string) {
  showNew.value = false
  await loadProjects()
  selectProject(id)
}

// 删除项目：磁盘目录+聊天全删（制作中后端 409 拒）。删的是当前项目则清空工作区。
// 确认在 HistoryRail 侧两段式按钮（自绘，替代 window.confirm——原生弹窗在已知
// 环境会死按钮，全站已弃用，2026-10-19 中-36）。
async function onDeleteProject(id: string) {
  try {
    await api.deleteProject(id)
  } catch (e) {
    topMsg.error(`删除失败：${errMsg(e)}`)
    return
  }
  if (current.value === id) {
    current.value = '' // watch(current) 关 SSE；下方手动清展示数据
    ++reqSeq // 在途响应作废：删掉的项目晚到数据不许写回清空后的页面
    view.value = null
    storyboard.value = null
    style.value = null
    manuscript.value = null
    prep.value = null
    audioMeta.value = null
    msgs.value = []
    chatRefs.value = []
    chatDraft.value = ''
    progressLine.value = ''
    stageState.value = {}
    activity.value = []
    agentBusy.value = false
    endTurn()
    manualTab.value = null
  }
  await loadProjects()
}

const stageSummary = computed(() => {
  const states = stageOrder.value.map(s => stageState.value[s] ?? (view.value?.producing ? 'pending' : ''))
  if (!states.some(Boolean)) return null
  return stageOrder.value.map((s, i) => ({ key: s, state: states[i] }))
})

// 挑选中的段序号（中-L5：原先模板内联 map，App 任意状态变更都新建数组触发
// Timeline 重渲染）
const pickedIdx = computed(() => chatRefs.value.map(r => r.idx))

// 准备工位各站实时态（SSE stage 事件：prep/script|style|storyboard → running/done/error）
// → 想法页签的「进行到哪一步」显示
const prepProgress = computed(() => ({
  script: stageState.value['prep/script'] ?? '',
  style: stageState.value['prep/style'] ?? '',
  storyboard: stageState.value['prep/storyboard'] ?? '',
}))

// 确认风格（唯一必须用户拍板的门）＝打包确认：与制作硬门同账本（stylepack.Confirmed），
// 防两套确认位劈叉（2026-10-17 彩虹项目实测死局）。老项目兼容已弃（2026-10-17 用户拍板：
// 只保新架构，老项目数据清除）。
// 失败弹错但仍然刷新——把服务端的真实状态拉回来，不能让前端停在「已确认」的假象里
async function confirmStyle() {
  if (!current.value) return
  try {
    await api.confirmPrep(current.value)
  } catch (e) {
    topMsg.error(`风格确认失败：${errMsg(e)}`)
  }
  refresh()
}

// 首启的 applyHash 已移入 boot()（登录态确定后才恢复页面）
</script>

<template>
  <n-config-provider :theme="darkTheme" :theme-overrides="themeOverrides">
    <n-message-provider>
      <!-- 登录页（鉴权启用且未登录时独占全屏）；服务不可达的 boot 失败同样停在登录页可重试 -->
      <LoginView v-if="authReady && authRequired && !user" @logged-in="onLoggedIn" />
      <div v-else-if="authReady" class="app">
        <nav class="navbar">
          <div class="nb-brand">
            <span class="nb-logo">帧</span>
            <b>帧述</b>
          </div>
          <div class="nb-tabs">
            <button
              v-for="t in tabs" :key="t.id" class="nb-tab" :class="{ on: page === t.id }" @click="page = t.id"
            >{{ t.label }}</button>
          </div>
          <button class="nb-new" @click="showNew = true">＋ 新建</button>
          <button v-if="user?.role === 'admin'" class="nb-set" :class="{ on: page === 'settings' }"
            title="各工位的大模型与 URL" @click="page = 'settings'">设置</button>
          <AccountMenu v-if="user" :user="user" @logout="onLogout" @manage="showUserAdmin = true" />
        </nav>

        <!-- 首页：落地 + 快速开始 -->
        <HomeView
          v-if="page === 'home'" :projects="projects"
          @new="showNew = true" @open="selectProject" @materials="page = 'materials'"
          @delete="onDeleteProject"
        />

        <!-- 风格库页：风格 + 视频解析 -->
        <LibraryCenter
          v-else-if="page === 'materials'" :packs="library" :initial-tab="materialsTab" @publish="publishPack" @ingested="loadLibrary" @deleted="loadLibrary"
        />

        <!-- 设置页：模型设置（大模型台账 + 工位图绑定） -->
        <LlmSettingsPage v-else-if="page === 'settings'" />

        <!-- 工作区：项目三栏 -->
        <div v-else class="shell">
          <HistoryRail :projects="projects" :current="current" @select="selectProject" @delete="onDeleteProject" />
          <main class="center">
            <!-- :key=current：切项目强制重挂载。StagePanel/ChatPanel 内部持有 per-project
                 的编辑态脏标记（分镜 sbDirty、文稿 dirty、风格 detail/direction），
                 其 watch 都是"dirty 时不回灌服务器数据"——组件实例跨项目复用的话，A 项目
                 未保存的编辑会原样保存进 B 项目（切项目清的是 App 级 props，清不到这些
                 内部脏标记，详见 StagePanel sbDirty watch / ManuscriptPane dirty watch）。
                 全局态（主题等）在 App 层，不受重挂载影响。 -->
            <StagePanel
              v-if="current" :key="current"
              :tab="activeTab" :view="view" :storyboard="storyboard" :style-samples="style" :manuscript="manuscript"
              :prep="prep"
              :stage-summary="stageSummary" :video-id="current" :audio-meta="audioMeta"
              :picked-idx="pickedIdx" :video-rev="videoRev" :progress-line="progressLine"
              :activity="activity" :agent-busy="turnActive" :prep-progress="prepProgress"
              @tab="setTab" @go-tab="setTab"
              @saved="refresh" @chat-sent="markBusy"
              @confirm-style="confirmStyle"
              @confirm-prep="refresh"
              @prep-refresh="refresh"
              @seg="addRef"
              @pick-time="addTimeRef"
              @cancel="async () => { if (current) { await api.cancel(current).catch(() => {}); refresh() } }"
              @cancel-agent="cancelAgentTurn"
              @rework-started="refresh"
              @parse-started="openMaterials"
            />
            <div v-else class="empty-ws">
              从左侧选择一个项目，或
              <button class="link" @click="showNew = true">新建一个</button>
            </div>
          </main>
          <ChatPanel
            :key="current" :project-id="current" :msgs="msgs" :busy="agentBusy" v-model:draft="chatDraft" :refs="chatRefs"
            @sent="markBusy" @remove-ref="removeRef" @clear-refs="chatRefs = []"
          />
        </div>
      </div>
      </n-message-provider>
      <NewProjectModal v-model:show="showNew" @created="onCreated" />
      <UserAdminModal v-model:show="showUserAdmin" />
    </n-config-provider>
  </template>

<style>
:root { color-scheme: dark; }
* { box-sizing: border-box; margin: 0; }
html, body, #app { height: 100%; }
body {
  background: #0e0e12; color: #e6e6ea;
  font-family: 'Segoe UI', 'Microsoft YaHei', system-ui, sans-serif;
  font-size: 14px; overflow: hidden;
}
.app { height: 100vh; display: flex; flex-direction: column; }

/* ── 全局导航栏 ─────────────────────────────── */
.navbar {
  height: 48px; flex: none; display: flex; align-items: center; gap: 22px;
  padding: 0 18px; border-bottom: 1px solid #232329; background: #121217;
}
.nb-brand { display: flex; align-items: center; gap: 9px; }
.nb-logo {
  width: 27px; height: 27px; border-radius: 8px; display: flex; align-items: center; justify-content: center;
  background: linear-gradient(135deg, #f0c674, #c89b4b); color: #14140f; font-weight: 700; font-size: 14px;
  box-shadow: 0 2px 8px rgba(240, 198, 116, .25);
}
.nb-brand b { letter-spacing: 2px; color: #f0c674; font-size: 14.5px; }
.nb-tabs { display: flex; gap: 6px; flex: 1; }
.nb-tab {
  border: 0; background: transparent; color: #9a9aa6; font-size: 13.5px;
  padding: 6px 16px; border-radius: 999px; cursor: pointer;
}
.nb-tab:hover { color: #e6e6ea; background: #1b1b22; }
.nb-tab.on { background: #f0c674; color: #14140f; font-weight: 600; }
.nb-new {
  border: 0; background: linear-gradient(135deg, #f0c674, #d9ae5b); color: #14140f;
  font-size: 13px; font-weight: 700; padding: 6px 18px; border-radius: 999px; cursor: pointer;
}
.nb-new:hover { filter: brightness(1.06); }
.nb-set {
  border: 1px solid #2a2a33; background: transparent; color: #9a9aa6;
  font-size: 13px; padding: 5px 14px; border-radius: 999px; cursor: pointer;
}
.nb-set:hover { color: #e6e6ea; border-color: #f0c674; }
.nb-set.on { color: #14140f; background: #f0c674; border-color: #f0c674; font-weight: 600; }

/* ── 工作区三栏 ─────────────────────────────── */
.shell { flex: 1; min-height: 0; display: flex; }
.center { flex: 1; display: flex; flex-direction: column; min-width: 0; }
.empty-ws { flex: 1; display: flex; align-items: center; justify-content: center; gap: 6px; color: #55555f; }
.link { border: 0; background: transparent; color: #8fc7ff; cursor: pointer; font-size: 14px; }
.link:hover { text-decoration: underline; }

/* ── 弹窗通用皮（NModal teleport 到 body，必须全局）：NewProjectModal/重做弹窗用，
      各自只留宽高等差异 ── */
.app-modal {
  background: #14141a; border: 1px solid #26262e; border-radius: 14px;
  --n-padding-left: 22px; --n-padding-right: 22px; --n-padding-top: 20px; --n-padding-bottom: 20px;
}
.app-modal .n-card-header { padding: 18px 22px 0; }
.app-modal .n-card-header__main { font-weight: 700; letter-spacing: 2px; color: #f0c674; font-size: 15px; }
.app-modal .n-card__content { padding: 14px 22px 20px; }
.app-modal .n-base-close { color: #7c7c88; top: 16px; right: 16px; }
.app-modal .n-base-close:hover { color: #e6e6ea; }
.app-modal .n-input { background: #101016; border: 1px solid #232329; border-radius: 10px; }
.app-modal .n-input:hover { border-color: #3a3a45; }
.app-modal .n-input--focus { border-color: rgba(240, 198, 116, .55); }
</style>
