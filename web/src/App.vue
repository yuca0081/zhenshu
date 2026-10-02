<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import {
  NConfigProvider, NMessageProvider, darkTheme,
} from 'naive-ui'
import HistoryRail from './components/HistoryRail.vue'
import StagePanel from './components/StagePanel.vue'
import ChatPanel from './components/ChatPanel.vue'
import GatesModal from './components/GatesModal.vue'
import NewProjectModal from './components/NewProjectModal.vue'
import MaterialCenter from './components/MaterialCenter.vue'
import HomeView from './components/HomeView.vue'
import { api, sseURL, videoURL } from './api'
import type { AudioMeta, ChatRef, Manuscript, Msg, ProjectRow, ProjectView, Storyboard, Style, StyleSamples } from './types'
import { segAt, segTable } from './segtime'

const projects = ref<ProjectRow[]>([])
const current = ref<string>('')
// 三页导航：首页（落地+快速开始）/ 工作区（项目三栏）/ 风格库（风格+视频解析）
type PageID = 'home' | 'workspace' | 'materials'
const tabs = [
  { id: 'home', label: '首页' }, { id: 'workspace', label: '工作区' }, { id: 'materials', label: '风格库' },
] as const
const page = ref<PageID>('home')
const view = ref<ProjectView | null>(null)
const storyboard = ref<Storyboard | null>(null)
const style = ref<StyleSamples | null>(null)
const manuscript = ref<Manuscript | null>(null)
const msgs = ref<Msg[]>([])
const chatDraft = ref('')
const chatRefs = ref<ChatRef[]>([]) // 引用列表（时间轴/分镜点选加入，输入框上方展示）
const audioMeta = ref<AudioMeta | null>(null) // 时间轴字幕/配音轨数据源
// 制作管线各段状态：'' | running | done | error
const stageState = ref<Record<string, string>>({})
const agentBusy = ref(false)
const library = ref<Style[]>([])
const inspect = ref(false) // 检视模式（标题行按钮触发，StagePanel 消费）
const videoRev = ref(0) // 成片代次：done 后递增 → video 换 URL 强制重载（重做完立刻看到新画面）
const progressLine = ref('') // 制作/重做细粒度进度（SSE progress，瞬时态不入聊天史）

let es: EventSource | null = null
let busyTimer: number | null = null

const stageOrder = ['tts', 'compositions', 'assemble', 'check', 'render', 'frameqa']

async function loadProjects() {
  projects.value = await api.listProjects()
}

async function loadLibrary() {
  library.value = await api.listLibrary().catch(() => [])
}

async function publishPack(id: string) {
  await api.publishPack(id).catch(() => {})
  await loadLibrary()
}

async function loadProject(id: string) {
  view.value = await api.project(id)
  storyboard.value = view.value.gates.storyboard ? await api.storyboard(id).catch(() => null) : null
  style.value = view.value.gates.style_draft ? await api.style(id).catch(() => null) : null
  // 无条件重拉：门未过也要清掉旧项目的残留（否则新项目会显示上一个项目的文稿/分镜/样张）
  manuscript.value = view.value.gates.manuscript ? await api.manuscript(id).catch(() => null) : null
  audioMeta.value = await api.audioMeta(id).catch(() => null)
}

async function loadMsgs() {
  if (!current.value) return
  const after = msgs.value.length ? Math.max(...msgs.value.map(m => m.id)) : 0
  const inc = await api.messages(current.value, after).catch(() => [])
  const seen = new Set(msgs.value.map(m => m.id))
  const fresh = inc.filter(m => !seen.has(m.id))
  if (fresh.length) msgs.value.push(...fresh)
}

function selectProject(id: string) {
  current.value = id
  page.value = 'workspace'
  inspect.value = false
  msgs.value = []
  chatRefs.value = []
  stageState.value = {}
  // 先清上一项目的全部展示数据：loadProject 是异步的，不清会在新项目页闪现旧内容
  view.value = null
  storyboard.value = null
  style.value = null
  manuscript.value = null
  audioMeta.value = null
}

// 📎 引用：点分镜/时间轴加段级（按段去重）；双击轨道/「引用此刻」加时刻级（按 ±0.75s 去重）；
// 检视点元素加元素级（按元素去重）；可单删、可清空
function addRef(idx: number, key: string) {
  if (!chatRefs.value.some(r => r.idx === idx)) chatRefs.value = [...chatRefs.value, { idx, key }]
}
function addElementRef(ref: ChatRef) {
  if (ref.elementId && chatRefs.value.some(r => r.elementId === ref.elementId)) return
  chatRefs.value = [...chatRefs.value, ref]
}
function addTimeRef(t: number) {
  const hit = segAt(segTable(storyboard.value, audioMeta.value), t)
  if (!hit) return
  if (chatRefs.value.some(r => r.idx === hit.idx && r.t != null && Math.abs(r.t - t) < 0.75)) return
  chatRefs.value = [...chatRefs.value, { idx: hit.idx, key: hit.key, t }]
}
function removeRef(ref: ChatRef) {
  chatRefs.value = chatRefs.value.filter(r =>
    ref.elementId ? r.elementId !== ref.elementId
      : ref.t != null ? !(r.idx === ref.idx && r.t != null && Math.abs(r.t - ref.t) < 0.75)
      : r.idx !== ref.idx)
}

async function refresh() {
  if (!current.value) return
  await loadProject(current.value).catch(() => {})
  await loadMsgs()
}

function markBusy() {
  agentBusy.value = true
  if (busyTimer) clearTimeout(busyTimer)
  busyTimer = window.setTimeout(() => { agentBusy.value = false }, 90_000)
}

function openSSE(id: string) {
  es?.close()
  es = new EventSource(sseURL(id))
  es.addEventListener('msg', () => loadMsgs())
  es.addEventListener('agent_msg', ev => {
    agentBusy.value = false
    loadMsgs()
    loadLibrary() // Agent 可能刚起草了风格包
  })
  es.addEventListener('tool_call', () => { markBusy(); loadMsgs() })
  es.addEventListener('tool_result', () => { loadMsgs(); loadProject(id).catch(() => {}) })
  es.addEventListener('stage', ev => {
    const d = JSON.parse((ev as MessageEvent).data)
    if (d.stage === 'pipeline') return
    const st = d.detail.startsWith('running') ? 'running'
      : d.detail.startsWith('done') ? 'done'
      : d.detail.startsWith('error') || d.detail.startsWith('repair') ? 'error' : 'running'
    stageState.value = { ...stageState.value, [d.stage]: st }
    loadMsgs()
  })
  es.addEventListener('progress', ev => {
    const d = JSON.parse((ev as MessageEvent).data)
    progressLine.value = typeof d === 'string' ? d : d.detail ?? ''
  })
  es.addEventListener('style_confirmed', () => loadProject(id).catch(() => {}))
  es.addEventListener('done', () => {
    stageState.value = {}
    progressLine.value = ''
    videoRev.value++
    refresh()
  })
  es.addEventListener('cancelled', () => { stageState.value = {}; progressLine.value = ''; refresh() })
  es.addEventListener('error', () => refresh()) // SSE 层错误 → 全量刷新
}

watch(current, id => {
  if (id) { openSSE(id); refresh() }
})

onBeforeUnmount(() => {
  es?.close()
  if (busyTimer) clearTimeout(busyTimer)
})

// 初始：加载列表，按 URL hash 恢复页面
loadProjects()
loadLibrary()

// ── 页面状态 ↔ URL hash：刷新/前进后退/分享链接不再弹回首页 ──────────
// #/home（缺省）/ #/materials / #/workspace/<项目id>
function hashTarget(): string {
  if (page.value === 'workspace') return current.value ? `#/workspace/${current.value}` : '#/workspace'
  if (page.value === 'materials') return '#/materials'
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
  } else if (h.startsWith('#/workspace')) {
    const id = /^#\/workspace\/([A-Za-z0-9_-]+)/.exec(h)?.[1] ?? ''
    // 同项目只切页不重载（重载会清聊天/阶段态）；异项目走完整选择流程
    if (id && id !== current.value) selectProject(id)
    else page.value = 'workspace'
  } else {
    page.value = 'home'
  }
}
window.addEventListener('hashchange', applyHash)
applyHash()

const showNew = ref(false)
// 三前置弹窗：文稿/分镜/风格（点对话栏标签打开）
const gateModal = ref<'manuscript' | 'storyboard' | 'style' | null>(null)
async function onCreated(id: string) {
  showNew.value = false
  await loadProjects()
  selectProject(id)
}

const stageSummary = computed(() => {
  const states = stageOrder.map(s => stageState.value[s] ?? (view.value?.producing ? 'pending' : ''))
  if (!states.some(Boolean)) return null
  return stageOrder.map((s, i) => ({ key: s, state: states[i] }))
})

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
</script>

<template>
  <n-config-provider :theme="darkTheme" :theme-overrides="themeOverrides">
    <n-message-provider>
      <div class="app">
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
        </nav>

        <!-- 首页：落地 + 快速开始 -->
        <HomeView
          v-if="page === 'home'" :projects="projects"
          @new="showNew = true" @open="selectProject" @materials="page = 'materials'"
        />

        <!-- 风格库页：风格 + 视频解析 -->
        <MaterialCenter
          v-else-if="page === 'materials'" :packs="library" @publish="publishPack" @ingested="loadLibrary"
        />

        <!-- 工作区：项目三栏 -->
        <div v-else class="shell">
          <HistoryRail :projects="projects" :current="current" @select="selectProject" />
          <main class="center">
            <header class="topbar">
              <div class="title">
                <span class="name">{{ view?.name ?? '未选择项目' }}</span>
              </div>
              <!-- 成片态操作（状态 chips 在对话栏顶部） -->
              <div v-if="view?.has_video" class="ops">
                <button
                  class="op-btn" :class="{ on: inspect }"
                  :disabled="!!stageSummary || view.producing"
                  @click="inspect = !inspect"
                >{{ inspect ? '退出检视' : '检视' }}</button>
                <a class="op-btn" :href="videoURL(current)" :download="`${view.name}.mp4`">下载成片</a>
              </div>
            </header>
            <StagePanel
              v-if="current"
              :view="view" :storyboard="storyboard" :style-samples="style" :manuscript="manuscript"
              :stage-summary="stageSummary" :video-id="current" :audio-meta="audioMeta"
              :picked-idx="chatRefs.map(r => r.idx)" :inspect="inspect" :video-rev="videoRev" :progress-line="progressLine"
              @inspect-off="inspect = false"
              @edit-manuscript="gateModal = 'manuscript'"
              @confirm-style="async () => { if (current) { await api.confirmStyle(current); refresh() } }"
              @seg="addRef"
              @seg-element="addElementRef"
              @pick-time="addTimeRef"
              @rework-started="refresh"
              @cancel="async () => { if (current) { await api.cancel(current).catch(() => {}); refresh() } }"
            />
            <div v-else class="empty-ws">
              从左侧选择一个项目，或
              <button class="link" @click="showNew = true">新建一个</button>
            </div>
          </main>
          <ChatPanel
            :project-id="current" :msgs="msgs" :busy="agentBusy" v-model:draft="chatDraft" :refs="chatRefs" :view="view"
            @sent="markBusy" @remove-ref="removeRef" @clear-refs="chatRefs = []" @open-gate="g => (gateModal = g)"
          />
        </div>
      </div>
      </n-message-provider>
      <NewProjectModal v-model:show="showNew" @created="onCreated" />
      <GatesModal
        :project-id="current" :gate="gateModal" :view="view"
        @close="gateModal = null" @saved="refresh" @chat-sent="markBusy"
      />
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

/* ── 工作区三栏 ─────────────────────────────── */
.shell { flex: 1; min-height: 0; display: flex; }
.center { flex: 1; display: flex; flex-direction: column; min-width: 0; }
.topbar {
  height: 46px; flex: none; display: flex; align-items: center; justify-content: space-between;
  padding: 0 20px; border-bottom: 1px solid #232329; background: #121217;
}
.title { display: flex; align-items: baseline; gap: 12px; min-width: 0; }
.name { font-size: 15px; font-weight: 600; color: #c9c9d1; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.ops { display: flex; gap: 8px; }
.op-btn {
  border: 1px solid #5c4d24; background: #211d12; color: #f0c674;
  font-size: 12px; padding: 4px 14px; border-radius: 999px; cursor: pointer;
  text-decoration: none; display: inline-flex; align-items: center;
}
.op-btn:hover { background: #2a2416; }
.op-btn.on { background: #f0c674; color: #14140f; font-weight: 600; }
.op-btn:disabled { opacity: .4; cursor: not-allowed; }
.empty-ws { flex: 1; display: flex; align-items: center; justify-content: center; gap: 6px; color: #55555f; }
.link { border: 0; background: transparent; color: #8fc7ff; cursor: pointer; font-size: 14px; }
.link:hover { text-decoration: underline; }

/* ── 弹窗通用皮（NModal teleport 到 body，必须全局）：GatesModal/NewProjectModal 共用，
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
