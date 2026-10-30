<script setup lang="ts">
import { KeepAlive, computed, ref, watch } from 'vue'
import { NButton, NInput, NInputNumber, NSelect, NSpin, useMessage } from 'naive-ui'
import { api, videoURL } from '../api'
import { cachePct, fmtTok } from '../format'
import type { ActivityEvent, AudioMeta, Manuscript, PrepView, ProjectView, Segment, ShotDirection, ShotSize, Storyboard, StyleSamples, TabID } from '../types'
import ManuscriptPane from './ManuscriptPane.vue'
import StylePane from './StylePane.vue'
import PrepPane from './PrepPane.vue'
import IdeaPane from './IdeaPane.vue'
import MusicIdeaPane from './MusicIdeaPane.vue'
import VideoPane from './VideoPane.vue'
import CoverPane from './CoverPane.vue'
import ActivityPane from './ActivityPane.vue'
import RIcon from './RIcon.vue'

// 舞台页签宿主（弹窗体系退役）：主题/文稿/风格/分镜/终审/视频/封面页占据中间区域。
// 页签常亮 + 红绿灯（门过没过）+ 过期点（成片落后上游）——App 持有 activeTab
// （未手动选择时按管线进度自动落页）。KeepAlive 缓存组件页签：切页不丢
// 文稿编辑态/风格开关态/视频播放位置。右上角常驻「下载成片」；过程页签替代
// 旧事件流弹窗（2026-10-18：升级为整页，可展开智能体对话与 token 账）。
const props = defineProps<{
  tab: TabID
  view: ProjectView | null
  storyboard: Storyboard | null
  styleSamples: StyleSamples | null
  manuscript: Manuscript | null
  prep: PrepView | null
  stageSummary: { key: string; state: string }[] | null
  videoId: string
  audioMeta: AudioMeta | null
  pickedIdx: number[]
  videoRev: number
  progressLine: string
  activity: ActivityEvent[]
  agentBusy: boolean // 对话栏 Agent 轮进行中（文稿页签据此锁编辑区）
  // 准备工位各站实时态（App 从 SSE stage 事件取，想法页签的进行中显示用）
  prepProgress: { script?: string; style?: string; storyboard?: string }
}>()
const emit = defineEmits<{
  tab: [t: TabID]
  seg: [idx: number, key: string]
  cancel: []
  'pick-time': [t: number]
  'rework-started': []
  'confirm-style': []
  'confirm-prep': []
  saved: []
  'chat-sent': []
  'go-tab': [t: TabID]
  'cancel-agent': []
  'prep-refresh': []
  'parse-started': [tab: 'styles' | 'videos'] // 项目解析已提交——App 跳风格库页跟进
}>()

// 页签状态灯（title 悬停说明）：绿=这关过了；黄=还没弄好（没存文稿/没生成分镜/
// 风格没确认）；琥珀描边=成片生成后又改过；视频页蓝=制作中。
// 段数/已确认之类的小字标已撤——状态看灯就够，细节在悬停说明和面板里。
// 想法页签：非 legacy 项目常驻（准备工位的启动页，完成后留作「换个想法重跑」入口）。
const showIdea = computed(() => !!props.prep && !props.prep.legacy)
// 音乐驱动 PV：文稿链三前置不适用——页签裁到「想法（PV 准备）＋视频」，M1 上线
// 后在这里长出做曲/地图/分镜工位。
const isMusic = computed(() => props.view?.mode === 'music')
const tabs = computed(() => {
  const list: { id: TabID; label: string }[] = []
  if (isMusic.value) {
    list.push({ id: 'idea', label: '想法' }, { id: 'video', label: '视频' })
  } else {
    if (showIdea.value) list.push({ id: 'idea', label: '主题' })
    // 页签序对齐准备链实际顺序 ①文稿→②风格→③分镜（2026-10-30 P3-10：旧序
    // 文稿/分镜/风格与主题页、过程页的链序三处三个说法）
    list.push({ id: 'manuscript', label: '文稿' }, { id: 'style', label: '风格' }, { id: 'storyboard', label: '分镜' })
    if (props.prep?.exists) list.push({ id: 'review', label: '终审' })
    list.push({ id: 'video', label: '视频' })
  }
  // 「过程」不进页签列——独立按钮放在页签行右侧（head-right），点击切到整页过程内容
  // 封面独立成页（从视频页右栏拆出）：有成片才有封面，跟在「视频」后面
  if (props.view?.has_video) list.push({ id: 'cover', label: '封面' })
  return list
})

function tabState(id: TabID): { cls: string[]; title: string; label: string } {
  const v = props.view
  if (!v) return { cls: [], title: '', label: '' }
  const stale = v.video_stale
  switch (id) {
    case 'idea':
      if (isMusic.value) {
        // PV 状态灯：黄=还没曲子；蓝跑=做曲中；绿=三件套就绪（试听在想法页签）
        const m = props.prep?.music
        return {
          cls: [props.prep?.running ? 'run' : m?.map ? 'ok' : 'wait'],
          title: props.prep?.running
            ? '做曲中（brief → 编曲 → 音乐地图 → 自检环）'
            : m?.map ? '整曲与音乐地图已就绪——「想法」页签试听' : '说一句话主题，AI 做曲＋出音乐地图',
          label: 'PV',
        }
      }
      return {
        cls: [props.prep?.running ? 'run' : props.prep?.exists ? 'ok' : 'wait'],
        title: props.prep?.running
          ? '准备阶段进行中（文稿→风格→分镜）'
          : props.prep?.exists ? '准备工件已出——这里可以换个主题重跑' : '写下主题、传点材料，让 AI 自动跑完文稿→风格→分镜',
        label: '',
      }
    case 'manuscript':
      return {
        cls: [v.gates.manuscript ? 'ok' : 'wait', stale.manuscript ? 'stale' : ''].filter(Boolean),
        title: v.gates.manuscript ? (stale.manuscript ? '已有文稿（成片生成后又改过，重做才进片）' : '已有文稿') : '还没有文稿——点开编辑或 AI 起草',
        label: '',
      }
    case 'storyboard':
      return {
        cls: [v.gates.storyboard ? 'ok' : 'wait', stale.storyboard ? 'stale' : ''].filter(Boolean),
        title: v.gates.storyboard ? (stale.storyboard ? '已有分镜（成片生成后又改过，重做才进片）' : `已有分镜（${v.seg_count} 段）`) : '还没有分镜——补好文稿后在对话里说「生成分镜」',
        label: '',
      }
    case 'style':
      return {
        cls: [v.gates.style_confirmed ? 'ok' : 'wait', stale.style ? 'stale' : ''].filter(Boolean),
        title: v.gates.style_confirmed ? (stale.style ? '风格已确认（成片生成后又改过，重做才进片）' : '风格已确认') : v.gates.style_draft ? '样张已出，还没确认' : '还没有风格——选库内条目或描述方向',
        label: '',
      }
    case 'review':
      return {
        cls: [v.gates.style_confirmed ? 'ok' : 'wait'],
        title: props.prep?.exists
          ? (v.gates.style_confirmed ? '打包终审已通过' : '三工件已出，等打包终审确认（全流程唯一人工门）')
          : '这个项目没走准备工位',
        label: '',
      }
    case 'cover':
      return {
        cls: [v.cover ? 'ok' : ''].filter(Boolean),
        title: v.cover ? '出片封面——预览、改标题、重做或从成片取一帧' : '还没有封面——制作收尾自动产出，也可在这里手动重做',
        label: '',
      }
    case 'activity':
      return {
        cls: [v.producing ? 'run' : props.activity.length ? 'ok' : ''].filter(Boolean),
        title: v.producing
          ? '制作进行中——每个智能体正在干什么、说了什么，点开卡片能看实时对话'
          : props.activity.length ? '制作过程回看：工位用时、智能体对话、token 消耗' : '还没有制作记录——开工后这里逐步记录',
        label: '',
      }
    default:
      return {
        cls: [v.has_video ? 'ok' : '', stale.manuscript || stale.storyboard || stale.style ? 'stale' : '', v.producing ? 'run' : ''].filter(Boolean),
        title: v.has_video
          ? (stale.manuscript || stale.storyboard || stale.style ? '成片是旧版——文稿/分镜/风格改过，可按最新重做' : '成片就绪')
          : v.producing ? '制作中' : '还没有成片',
        label: v.has_video ? (stale.manuscript || stale.storyboard || stale.style ? '旧版' : '') : '',
      }
  }
}

// ── 失败未读红点 ──────────────────────────────────────────────────
// 最近一次制作以失败/中断收场（非手动取消）而用户还没打开过「过程」页 →
// 胶囊按钮亮红点；进入过程页即视为已读。制作中不亮（有呼吸灯表达活着）。
const FAIL_EVENTS = new Set(['produce.error', 'rework.error', 'produce.interrupted', 'rework.interrupted'])
const lastFailAt = computed(() => {
  let t = 0
  for (const e of props.activity) {
    if (FAIL_EVENTS.has(e.event)) t = e.ms ?? (Date.parse(e.ts) || 0)
  }
  return t
})
const seenFailAt = ref(0)
watch(() => props.tab, t => { if (t === 'activity') seenFailAt.value = lastFailAt.value })
watch(lastFailAt, t => { if (props.tab === 'activity') seenFailAt.value = t })
const failUnseen = computed(() => !props.view?.producing && props.tab !== 'activity' && lastFailAt.value > seenFailAt.value)

// ── 分镜目标时长（时长概念归分镜：这里定总时长，AI 按它重排各段）──
const targetMin = ref(1)
const replanning = ref(false)
const minOptions = [1, 2, 3, 5].map(m => ({ label: `约 ${m} 分钟`, value: m }))
// 有分镜时按当前总时长就近回填档位（total_hint 秒）
watch(() => props.view?.total_hint, t => {
  if (!t) return
  const near = [1, 2, 3, 5].reduce((a, b) => (Math.abs(b * 60 - t) < Math.abs(a * 60 - t) ? b : a))
  targetMin.value = near
}, { immediate: true })
async function replanStoryboard() {
  if (!props.videoId || replanning.value || props.agentBusy) return
  replanning.value = true
  try {
    await api.chat(props.videoId,
      `请按目标总时长约 ${targetMin.value} 分钟重新规划分镜：重排各段的时长与画面要点（旁白可微调篇幅），总时长贴合目标，改完告诉我。`)
    emit('chat-sent')
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    replanning.value = false
  }
}

// ── 分镜手动编辑：表格 inline 改文案/时长，保存落盘（段数/段序服务端锁死）──
// 本地副本编辑（不动 props）；Agent 重排/重新生成后 props 变化且未 dirty 才回灌。
const edSb = ref<Storyboard | null>(null)
const sbDirty = ref(false)
const savingSb = ref(false)
watch(() => props.storyboard, sb => {
  if (!sbDirty.value) edSb.value = sb ? JSON.parse(JSON.stringify(sb)) : null
}, { immediate: true })
const edTotalSec = computed(() =>
  (edSb.value?.segments ?? []).reduce((a, s) => a + (s.duration_hint || 0), 0))
function resetSb() {
  sbDirty.value = false
  edSb.value = props.storyboard ? JSON.parse(JSON.stringify(props.storyboard)) : null
}
async function saveSb() {
  if (!edSb.value || !props.videoId || savingSb.value) return
  savingSb.value = true
  try {
    const body: Storyboard = {
      ...edSb.value,
      segments: edSb.value.segments.map(s => ({ ...s, duration_hint: s.duration_hint || 5 })),
    }
    await api.saveStoryboard(props.videoId, body)
    sbDirty.value = false
    emit('saved') // App 重拉 storyboard/view：mtime 变 → 成片过期灯亮
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    savingSb.value = false
  }
}

// ── 镜头层（一期：1 镜 = 1 段）：分镜表里的展开行编辑 ──
// 字段口径与后端 contract.ShotDirection 一致；保存仍走 saveStoryboard（后端回写
// draft.json，终审镜头门读的是同一份）。老项目没有 shot：先点「+ 补镜头」再编辑，
// 不点就保持原样（零感知）。
const shotSizes: { label: string; value: ShotSize }[] = [
  { label: '全景', value: 'wide' },
  { label: '中景', value: 'medium' },
  { label: '近景', value: 'close' },
  { label: '俯拍', value: 'overhead' },
  { label: '字卡', value: 'full' },
]
const expandedShot = ref<string | null>(null)
const edSegs = computed(() => edSb.value?.segments ?? [])
const hasShotLayer = computed(() => edSegs.value.some(s => !!s.shot))
const shotReady = computed(() => edSegs.value.filter(s => !shotIssue(s.shot)).length)
function shotSizeLabel(v?: string) {
  return shotSizes.find(x => x.value === v)?.label ?? (v || '未填')
}
function shotIssue(sh?: ShotDirection): string {
  if (!sh) return '缺镜头'
  if (!sh.size) return '缺景别'
  if (!(sh.camera || '').trim()) return '缺机位/运镜'
  if (!(sh.stage || '').trim()) return '缺舞台'
  if (!(sh.action || '').trim()) return '缺动作'
  const cast = (sh.cast || []).filter(c => (c.who || '').trim())
  if (cast.length < 1) return '至少 1 个角色'
  if (cast.length > 4) return '角色最多 4 个'
  return ''
}
function addEmptyShot(seg: Segment) {
  if (!seg.shot) {
    seg.shot = { size: 'medium', camera: '', stage: '', cast: [], action: '', dialogue: '', transition: '', ref_frame: '', ref_note: '' }
    sbDirty.value = true
  }
  expandedShot.value = seg.id
}
function toggleShot(seg: Segment) {
  if (!seg.shot) {
    addEmptyShot(seg)
    return
  }
  expandedShot.value = expandedShot.value === seg.id ? null : seg.id
}
function addCast(sh: ShotDirection) {
  if (!sh.cast) sh.cast = []
  sh.cast.push({ who: '', pose: '', emotion: '' })
  sbDirty.value = true
}
function removeCast(sh: ShotDirection, i: number) {
  if (!sh.cast) return
  sh.cast.splice(i, 1)
  sbDirty.value = true
}
// 终审镜头卡「编辑镜头」：跳到分镜页签并展开对应段（不丢当前编辑态）。
function editShot(id: string) {
  expandedShot.value = id
  emit('tab', 'storyboard')
}

// ── 段落分组行（工件 C 段落层）：段落主题作分界行插进分镜表 ──
// 行对象直接引用 edSb.segments 的段（v-model 绑定不受影响，保存逻辑零改动）。
// 编辑只改文案/时长（保存接口锁 id/idx，段数段序不可变），分组不受编辑影响；
// draft 缺失（老项目）或段落与当前分镜对不齐时退回平铺表，不硬凑。
type SbRow = { para: null | { title: string; segs: string[] }; no: number; seg: Segment | null }
const sbRows = computed<SbRow[]>(() => {
  const segs = edSb.value?.segments ?? props.storyboard?.segments ?? []
  const flat = (): SbRow[] => segs.map(s => ({ para: null, no: 0, seg: s }))
  const paras = props.prep?.draft?.paragraphs ?? []
  if (!paras.length || !segs.length) return flat()
  const byId = new Map(segs.map(s => [s.id, s]))
  const rows: SbRow[] = []
  let cursor = 0
  for (let i = 0; i < paras.length; i++) {
    rows.push({ para: paras[i], no: i + 1, seg: null })
    for (const id of paras[i].segs) {
      const s = byId.get(id)
      if (!s || segs[cursor]?.id !== id) return flat()
      cursor++
      rows.push({ para: null, no: 0, seg: s })
    }
  }
  if (cursor !== segs.length) return flat()
  return rows
})
const sbParaCount = computed(() => props.prep?.draft?.paragraphs?.length ?? 0)

// ── 整片过期重做（页签行右侧）：上游（文稿/分镜/风格）在成片生成后又改过 ──
// 按钮收进页签行靠右（原视频页整行黄条太占地），悬停 title 说明具体哪几样改过。
const message = useMessage()
const redoNames = computed(() => {
  const v = props.view
  if (!v?.has_video) return []
  return [
    v.video_stale.manuscript ? '文稿' : '',
    v.video_stale.storyboard ? '分镜' : '',
    v.video_stale.style ? '风格' : '',
  ].filter(Boolean)
})
const showRedo = computed(() =>
  props.tab === 'video' && !!props.view?.has_video && !props.view.producing && !props.stageSummary && redoNames.value.length > 0)
const redoing = ref(false)
// 两段式确认（2026-10-19 中-36，替代 window.confirm——原生弹窗死按钮问题，全站已弃用）
const redoArmed = ref(false)
async function redoFull() {
  if (redoing.value || !props.view) return
  if (!redoArmed.value) {
    redoArmed.value = true
    window.setTimeout(() => { redoArmed.value = false }, 4000)
    return
  }
  redoArmed.value = false
  redoing.value = true
  try {
    await api.produce(props.videoId)
    emit('rework-started') // App 重拉 view（producing 态/停止按钮）
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    redoing.value = false
  }
}

// ── 解析到风格库（项目解析路，2026-10-29）：本片设计系统 → 视频解析暂存区 ──
// 与上传视频解析同走草稿修订→入库；成功即跳「风格库 → 视频解析」看进度。
const parsing = ref(false)
async function parseToLibrary() {
  if (parsing.value) return
  parsing.value = true
  try {
    const r = await api.parseProjectStyle(props.videoId)
    emit('parse-started', 'videos')
    if (r.existing) message.info('本项目已有解析记录，已跳到「风格库 → 视频解析」')
    else message.success('解析已开始，已跳到「风格库 → 视频解析」跟进')
  } catch (e) {
    message.error(`解析启动失败：${(e as Error).message}`)
  } finally {
    parsing.value = false
  }
}
</script>

<template>
  <section class="stage">
    <div class="pane">
      <!-- 页签行：主题/文稿/风格/分镜/终审/视频/封面 + 状态灯（红绿灯/过期点）+ 右上角 下载成片/过程 -->
      <header v-if="view" class="tabs">
        <button
          v-for="t in tabs" :key="t.id" class="tab" :class="[tabState(t.id).cls, { on: tab === t.id }]"
          :title="tabState(t.id).title" @click="emit('tab', t.id)"
        >
          <span class="dot" />{{ t.label }}<span v-if="tabState(t.id).label" class="tab-sub">{{ tabState(t.id).label }}</span>
        </button>
        <div class="head-right">
          <!-- 整片过期重做：悬停显示哪几样改过；两段式确认（第一枪只亮确认态） -->
          <NButton
            v-if="showRedo" class="redo-btn" size="tiny" :type="redoArmed ? 'error' : 'warning'" :loading="redoing"
            :title="`${redoNames.join('、')}在成片生成后又改过——成片还是旧版。点此按最新内容重做：没变的产物会复用，只重做出入的部分。`"
            @click="redoFull"
          >{{ redoArmed ? '确认重做？' : '按最新内容重做' }}</NButton>
          <!-- 解析到风格库：项目解析路入口（读定稿设计系统+样张，与上传视频解析同走
               暂存区草稿→修订→入库；幂等，重复点击复用既有条目。页签行拥挤（2026-10-30）：
               收成纯图标，全称与解释在悬停 title 里） -->
          <button
            v-if="view.has_video && !view.producing" class="dl-btn parse-btn icon-btn" :disabled="parsing"
            title="解析到风格库：把本片风格解析成风格库条目——读定稿设计系统与样张，去「风格库 → 视频解析」修订后入库，之后新建项目可选用"
            @click="parseToLibrary"
          ><RIcon name="upload" :size="13" /></button>
          <!-- 下载成片 -->
          <a
            v-if="view.has_video" class="dl-btn"
            :href="videoURL(videoId, videoRev)" :download="`${view.name}.mp4`"
            title="下载成片 main.mp4"
          ><RIcon name="download" :size="13" />下载</a>
          <!-- 过程回看：独立按钮（点击切到整页「过程」内容）；
               右侧小字 = 本次制作全部智能体任务的输入 token 与缓存命中率（projectView.tokens） -->
          <button
            class="feed-btn" :class="{ on: tab === 'activity' }"
            :title="tabState('activity').title"
            @click="emit('tab', tab === 'activity' ? 'video' : 'activity')"
          ><span v-if="view.producing" class="live-dot" /><span v-else-if="failUnseen" class="fail-dot" />过程<span
              v-if="view.tokens?.calls"
              class="feed-tok num"
              title="本次制作全部智能体任务的输入 token 累计与平台前缀缓存命中率（manifest 全量累计，含此前每一轮）"
            >输入{{ fmtTok(view.tokens.prompt) }}<template v-if="view.tokens.cached">·缓存{{ cachePct(view.tokens.cached, view.tokens.prompt) }}</template></span></button>
        </div>
      </header>

      <template v-if="view">
        <!-- KeepAlive 要求恰好一个子节点：内部不许放注释/多根（v-if 链整体算一个） -->
        <KeepAlive>
          <MusicIdeaPane
            v-if="tab === 'idea' && view.mode === 'music'"
            :view="view"
          />
          <IdeaPane
            v-else-if="tab === 'idea'"
            :project-id="videoId" :prep="prep" :progress="prepProgress"
            @go-tab="t => emit('go-tab', t)" @refresh="emit('prep-refresh')"
          />
          <ManuscriptPane
            v-else-if="tab === 'manuscript'"
            :project-id="videoId" :manuscript="manuscript"
            :video-stale="view.video_stale.manuscript" :agent-busy="agentBusy"
            @saved="emit('saved')" @cancel-agent="emit('cancel-agent')"
          />
          <div v-else-if="tab === 'storyboard' && storyboard" class="sb-wrap">
            <div class="sb-head">
              分镜表
              <span class="sb-meta">{{ edSb?.segments.length ?? storyboard.segments.length }} 段<template v-if="sbParaCount"> / {{ sbParaCount }} 段落</template> / 约 {{ Math.round(edTotalSec || view.total_hint) }} 秒<template v-if="hasShotLayer"> · 镜头 {{ shotReady }}/{{ edSb?.segments.length ?? 0 }}</template></span>
              <span class="sb-dur">
                <NSelect v-model:value="targetMin" size="tiny" :options="minOptions" :disabled="agentBusy" style="width: 108px" />
                <NButton size="tiny" :loading="replanning" :disabled="agentBusy" @click="replanStoryboard">按此时长重排</NButton>
                <NButton v-if="sbDirty" size="tiny" @click="resetSb">还原</NButton>
                <NButton size="tiny" type="primary" :disabled="!sbDirty || agentBusy" :loading="savingSb" @click="saveSb">保存修改</NButton>
              </span>
            </div>
            <div class="sb-scroll">
              <table class="sb">
                <thead><tr><th style="width:44px">段</th><th style="width:130px">标题</th><th>旁白</th><th style="width:220px">镜头</th><th style="width:86px">秒</th></tr></thead>
                <tbody>
                  <template v-for="r in sbRows" :key="r.seg ? r.seg.id : 'para' + r.no">
                    <tr v-if="r.para" class="sb-para">
                      <td colspan="5"><b>段落 {{ r.no }}</b>{{ r.para.title }}<i>{{ r.para.segs.length }} 段</i></td>
                    </tr>
                    <tr v-else-if="r.seg" class="sb-row edit">
                      <td class="c">{{ r.seg.idx }}</td>
                      <td><NInput v-model:value="r.seg.key" size="tiny" :disabled="agentBusy" @update:value="sbDirty = true" /></td>
                      <td><NInput v-model:value="r.seg.narration" type="textarea" size="tiny" :autosize="{ minRows: 2, maxRows: 7 }" :disabled="agentBusy" @update:value="sbDirty = true" /></td>
                      <td class="shot-cell">
                        <button v-if="r.seg.shot" class="shot-summary" type="button" :disabled="agentBusy" @click="toggleShot(r.seg)">
                          <b>{{ shotSizeLabel(r.seg.shot.size) }}</b>
                          <span>{{ r.seg.shot.camera || '未写机位' }}</span>
                          <i v-if="shotIssue(r.seg.shot)" class="bad">{{ shotIssue(r.seg.shot) }}</i>
                        </button>
                        <button v-else class="shot-add" type="button" :disabled="agentBusy" @click="addEmptyShot(r.seg)">+ 补镜头</button>
                      </td>
                      <td class="c"><NInputNumber v-model:value="r.seg.duration_hint" size="tiny" :min="0.5" :max="180" :step="0.5" :disabled="agentBusy" style="width: 80px" @update:value="sbDirty = true" /></td>
                    </tr>
                    <!-- 镜头编辑展开行：一期 1 镜 = 1 段，不另做嵌套镜头编辑器 -->
                    <tr v-if="r.seg && expandedShot === r.seg.id" class="sb-shot-edit">
                      <td colspan="5">
                        <div v-if="r.seg.shot" class="shot-form">
                          <div class="shot-grid">
                            <label>景别
                              <NSelect v-model:value="r.seg.shot.size" size="tiny" :options="shotSizes" :disabled="agentBusy" @update:value="sbDirty = true" />
                            </label>
                            <label>机位/运镜
                              <NInput v-model:value="r.seg.shot.camera" size="tiny" placeholder="从哪拍到哪、怎么动" :disabled="agentBusy" @update:value="sbDirty = true" />
                            </label>
                          </div>
                          <label>舞台 / 道具 / 前后景
                            <NInput v-model:value="r.seg.shot.stage" type="textarea" size="tiny" :autosize="{ minRows: 1, maxRows: 3 }" :disabled="agentBusy" @update:value="sbDirty = true" />
                          </label>
                          <div class="shot-cast">
                            <span class="shot-label">出场角色</span>
                            <div v-for="(c, ci) in (r.seg.shot.cast || [])" :key="ci" class="cast-row">
                              <NInput v-model:value="c.who" size="tiny" placeholder="谁（角色表同名）" :disabled="agentBusy" @update:value="sbDirty = true" />
                              <NInput v-model:value="c.pose" size="tiny" placeholder="姿态/动作" :disabled="agentBusy" @update:value="sbDirty = true" />
                              <NInput v-model:value="c.emotion" size="tiny" placeholder="情绪" :disabled="agentBusy" @update:value="sbDirty = true" />
                              <button class="cast-del" type="button" :disabled="agentBusy" title="删除这个角色" @click="removeCast(r.seg.shot, ci)">×</button>
                            </div>
                            <button class="cast-add" type="button" :disabled="agentBusy || (r.seg.shot.cast || []).length >= 4" @click="addCast(r.seg.shot)">+ 角色</button>
                          </div>
                          <label>本镜的戏（必须看得见）
                            <NInput v-model:value="r.seg.shot.action" type="textarea" size="tiny" :autosize="{ minRows: 1, maxRows: 3 }" :disabled="agentBusy" @update:value="sbDirty = true" />
                          </label>
                          <div class="shot-grid">
                            <label>台词 / 字卡
                              <NInput v-model:value="r.seg.shot.dialogue" size="tiny" placeholder="短句，可空" :disabled="agentBusy" @update:value="sbDirty = true" />
                            </label>
                            <label>与下一镜接法
                              <NInput v-model:value="r.seg.shot.transition" size="tiny" placeholder="匹配切/白闪/直切/叠化" :disabled="agentBusy" @update:value="sbDirty = true" />
                            </label>
                          </div>
                          <div class="shot-grid">
                            <label>参考帧（项目内路径）
                              <NInput v-model:value="r.seg.shot.ref_frame" size="tiny" placeholder="可空" :disabled="agentBusy" @update:value="sbDirty = true" />
                            </label>
                            <label>参考说明
                              <NInput v-model:value="r.seg.shot.ref_note" size="tiny" placeholder="只借构图/光/角色关系" :disabled="agentBusy" @update:value="sbDirty = true" />
                            </label>
                          </div>
                          <div class="shot-brief">原画面提示（旧分镜稿，保留作参考）：{{ r.seg.visual_brief }}</div>
                        </div>
                      </td>
                    </tr>
                  </template>
                </tbody>
              </table>
              <div class="hint">直接在表格里改文案和秒数；点「镜头」格展开导演字段（景别/机位/舞台/角色/动作/台词），改完点右上「保存修改」（保存后成片会标记为旧版）。终审确认要求每段镜头完整；要增删段或大改结构，在对话里说。加聊天引用请到时间轴点段。</div>
            </div>
            <!-- Agent 轮进行中锁分镜：与文稿页同款——提示+中断出口浮在分镜区域内，不单独占行 -->
            <div v-if="agentBusy" class="sb-lock">
              <NSpin :size="14" />
              <span>AI 正在工作中——本轮可能重排分镜，编辑已临时锁定</span>
              <NButton size="tiny" quaternary @click="emit('cancel-agent')">中断 AI</NButton>
            </div>
          </div>
          <div v-else-if="tab === 'storyboard'" class="sb-empty">
            还没有分镜。先补好文稿（「文稿」页签），然后在对话里说「生成分镜」——我会拆段、写画面、标时长。
          </div>
          <StylePane
            v-else-if="tab === 'style'"
            :project-id="videoId" :view="view" :style-samples="styleSamples" :prep="prep"
            :video-stale="view.video_stale.style"
            @saved="emit('saved')" @confirm-style="emit('confirm-style')" @chat-sent="emit('chat-sent')"
          />
          <PrepPane
            v-else-if="tab === 'review'"
            :project-id="videoId" :prep="prep" :confirmed="!!view.gates.style_confirmed" :agent-busy="agentBusy"
            @confirmed="emit('confirm-prep')" @chat-sent="emit('chat-sent')" @edit-shot="editShot"
          />
          <VideoPane
            v-else-if="tab === 'video'"
            :view="view" :storyboard="storyboard" :audio-meta="audioMeta" :picked-idx="pickedIdx"
            :video-id="videoId" :video-rev="videoRev" :progress-line="progressLine" :stage-summary="stageSummary"
            @seg="(i, k) => emit('seg', i, k)" @pick-time="t => emit('pick-time', t)"
            @cancel="emit('cancel')"
            @go-tab="t => emit('go-tab', t)"
            @refresh="emit('saved')"
          />
          <CoverPane
            v-else-if="tab === 'cover'"
            :view="view" :video-id="videoId" :video-rev="videoRev"
          />
          <ActivityPane
            v-else-if="tab === 'activity'"
            :project-id="videoId" :events="activity" :producing="!!view.producing" :totals="view.tokens"
          />
        </KeepAlive>
      </template>

      <div v-else class="empty">
        <div class="none">← 左侧新建或选择一个项目</div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.stage { flex: 1; min-height: 0; padding: 12px 16px 16px; display: flex; }
.pane {
  position: relative; /* 事件流面板的定位锚 */
  flex: 1; min-width: 0; display: flex; flex-direction: column;
  background: var(--surface); border: 1px solid var(--line-soft); overflow: hidden;
  container-type: inline-size; /* 页签行按面板实宽做响应式（窄时收起 token 小账） */
}

/* ── 页签行（激活强调色下划线；编号已撤——页签靠文字与状态灯识别） ── */
/* 三栏布局里中间栏宽度有限（对话栏 400px 固定），这行内容多——所有元素
   nowrap + flex:none 保证文字不折行；真放不下时整行横向滚动兜底，
   不让页签文字被挤成两行（2026-10-29 修）。 */
.tabs {
  flex: none; display: flex; align-items: center; gap: 2px;
  padding: 6px 8px 0; border-bottom: 1px solid var(--line-soft);
  overflow-x: auto; scrollbar-width: thin;
}
.tab {
  display: inline-flex; align-items: center; gap: 7px; cursor: pointer; flex: none;
  white-space: nowrap;
  border: 0; border-bottom: 2px solid transparent; background: transparent;
  color: var(--muted); font-size: 13px; padding: 7px 8px 9px; font-family: inherit;
  transition: color var(--dur-2) var(--ease), border-color var(--dur-2) var(--ease);
}
.tab:hover { color: var(--ink); background: color-mix(in srgb, var(--ink) 3%, transparent); }
.tab.on { color: var(--accent); border-bottom-color: var(--accent); font-weight: 600; }
.tab-sub { font-size: 11px; color: var(--muted); font-weight: 400; }
.dot { width: 7px; height: 7px; border-radius: 50%; background: var(--muted); opacity: 0.5; flex: none; }
.tab.ok .dot { background: var(--ok); opacity: 1; }
/* 黄=这一关还没过（没存/没生成/没确认），常亮不闪——闪是"进行中"的语言 */
.tab.wait .dot { background: var(--warn); opacity: 1; }
.tab.run .dot { background: var(--info); opacity: 1; animation: pulse 1.2s infinite; }
/* 过期：整颗页签描琥珀边（黄灯语义=成片落后于我） */
.tab.stale { color: var(--warn); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--warn) 35%, transparent); }
.tab.stale .dot { background: var(--warn); opacity: 1; }
.tab.on.stale { border-bottom-color: var(--warn); }
@keyframes pulse { 50% { opacity: .35; } }

/* ── 页签行右上角：重做/下载（过程回看走「过程」页签）── */
.head-right {
  margin-left: auto; flex: none; white-space: nowrap;
  display: flex; align-items: center; gap: 6px; padding: 0 2px 6px;
}
.redo-btn { margin-bottom: 0; }
.dl-btn {
  display: inline-flex; align-items: center; gap: 6px; cursor: pointer;
  border: 1px solid var(--line); background: transparent; color: var(--muted);
  font-size: 12px; letter-spacing: 0.04em; padding: 4px 10px; text-decoration: none; font-family: inherit;
  transition: color var(--dur-1) var(--ease), border-color var(--dur-1) var(--ease);
}
.dl-btn:hover { border-color: var(--accent); color: var(--accent); }
/* 解析到风格库：同 dl-btn 方角按钮，button 元素补字体继承与禁用态；
   icon-btn=纯图标态（页签行拥挤，全称进悬停 title） */
.parse-btn { font-family: inherit; }
.parse-btn:disabled { opacity: .55; cursor: default; }
.parse-btn:hover:not(:disabled) { border-color: var(--accent); color: var(--accent); }
.icon-btn { padding: 4px 7px; }
/* 过程回看按钮；on=当前正看过程页，live-dot=制作中呼吸灯 */
.feed-btn {
  display: inline-flex; align-items: center; gap: 6px; cursor: pointer;
  border: 1px solid var(--line); background: transparent; color: var(--muted);
  font-size: 12px; letter-spacing: 0.04em; padding: 4px 10px; font-family: inherit;
  transition: color var(--dur-1) var(--ease), border-color var(--dur-1) var(--ease);
}
.feed-btn:hover { border-color: var(--muted); color: var(--ink); }
.feed-btn.on { border-color: var(--accent); color: var(--accent); }
.feed-btn .live-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--info); animation: pulse 1.2s infinite; }
/* 全程 token 小账：输入 token + 缓存命中率，做次要信息色不抢「过程」本体；
   面板窄于 ~860px 时收起（悬停「过程」的 title 仍在）——六页签+右侧按钮组的
   行宽在 740-860px 区间也放不下这份小账（2026-10-30 拥挤优化实测） */
.feed-btn .feed-tok { margin-left: 2px; font-size: 11px; color: var(--muted); opacity: .85; }
.feed-btn:hover .feed-tok, .feed-btn.on .feed-tok { color: inherit; opacity: .75; }
@container (max-width: 859px) { .feed-tok { display: none; } }
/* 失败未读红点：最近一次制作失败/中断后还没看过过程页 */
.feed-btn .fail-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--err); box-shadow: 0 0 6px color-mix(in srgb, var(--err) 80%, transparent); }

.empty { flex: 1; display: flex; align-items: center; justify-content: center; }
.none { color: var(--muted); }

/* ── 分镜表（发丝线 + tabular 数字） ── */
.sb-wrap { position: relative; flex: 1; min-height: 0; display: flex; flex-direction: column; }
.sb-head {
  flex: none; display: flex; align-items: center; gap: 10px;
  padding: 12px 18px; font-weight: 600; border-bottom: 1px solid var(--line-soft); color: var(--ink);
}
.sb-meta { font-size: 12px; font-weight: 400; color: var(--muted); font-variant-numeric: tabular-nums; }
.sb-dur { margin-left: auto; display: flex; align-items: center; gap: 6px; font-weight: 400; }
.sb-scroll { flex: 1; min-height: 0; overflow-y: auto; }
/* fixed 布局：列宽按表头锁死、旁白列吃剩余宽——auto 布局被长标题/画面内容
   挤爆时旁白会一字一行竖排（2026-10-30 GUI 实测 t13） */
.sb { width: 100%; table-layout: fixed; border-collapse: collapse; font-size: 13px; }
.sb th { position: sticky; top: 0; background: var(--surface); text-align: left; padding: 10px 12px; color: var(--muted); font-weight: 600; border-bottom: 1px solid var(--line); }
.sb td { padding: 6px 8px; border-bottom: 1px solid var(--line-soft); vertical-align: top; }
.sb-para td {
  background: color-mix(in srgb, var(--ink) 3%, var(--surface));
  padding: 7px 12px; font-size: 12.5px; color: var(--accent);
  border-bottom: 1px solid var(--line-soft); letter-spacing: .3px;
}
.sb-para b { font-weight: 600; margin-right: 8px; opacity: .8; font-variant-numeric: tabular-nums; }
.sb-para i { font-style: normal; color: var(--muted); margin-left: 10px; font-size: 11.5px; }
.c { color: var(--muted); text-align: center; vertical-align: middle; font-variant-numeric: tabular-nums; }
/* ── 镜头格 / 展开编辑行（一期：1 镜 = 1 段）── */
.shot-cell { padding: 5px 8px; }
.shot-summary {
  width: 100%; display: flex; flex-direction: column; gap: 2px; text-align: left;
  border: 1px solid var(--line-soft); background: color-mix(in srgb, var(--ink) 3%, transparent);
  padding: 5px 8px; cursor: pointer; font-family: inherit; color: var(--ink);
}
.shot-summary:hover { border-color: var(--accent); }
.shot-summary b { font-size: 11.5px; color: var(--accent); font-weight: 600; }
.shot-summary span { font-size: 11.5px; color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.shot-summary i { font-style: normal; font-size: 10.5px; color: var(--ok); }
.shot-summary i.bad { color: var(--warn); }
.shot-add {
  width: 100%; border: 1px dashed var(--line); background: transparent; color: var(--muted);
  font-size: 11.5px; padding: 7px 8px; cursor: pointer; font-family: inherit;
}
.shot-add:hover:not(:disabled) { border-color: var(--accent); color: var(--accent); }
.sb-shot-edit > td { background: color-mix(in srgb, var(--ink) 2.5%, var(--surface)); padding: 10px 14px 12px; }
.shot-form { display: flex; flex-direction: column; gap: 8px; font-size: 12px; }
.shot-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
.shot-form label { display: flex; flex-direction: column; gap: 3px; color: var(--muted); font-size: 11.5px; }
.shot-cast { display: flex; flex-direction: column; gap: 4px; }
.shot-label { color: var(--muted); font-size: 11.5px; }
.cast-row { display: grid; grid-template-columns: 1fr 1.4fr 0.8fr 24px; gap: 6px; align-items: center; }
.cast-del { border: 0; background: transparent; color: var(--muted); cursor: pointer; font-size: 15px; line-height: 1; }
.cast-del:hover:not(:disabled) { color: var(--err); }
.cast-add {
  align-self: flex-start; border: 1px dashed var(--line); background: transparent; color: var(--muted);
  font-size: 11.5px; padding: 2px 10px; cursor: pointer; font-family: inherit;
}
.cast-add:hover:not(:disabled) { border-color: var(--accent); color: var(--accent); }
.cast-add:disabled, .cast-del:disabled { opacity: .45; cursor: default; }
.shot-brief {
  color: var(--muted); font-size: 11.5px; opacity: .75;
  border-top: 1px dashed var(--line-soft); padding-top: 6px; line-height: 1.6;
}
.hint { padding: 10px 14px; color: var(--muted); opacity: .7; font-size: 12px; }
.sb-empty { flex: 1; display: flex; align-items: center; justify-content: center; color: var(--muted); font-size: 13px; line-height: 1.9; text-align: center; padding: 20px 40px; }
/* Agent 轮进行中的分镜锁：覆盖整个分镜区域（含表头按钮行）的提示 + 中断出口 */
.sb-lock {
  position: absolute; top: 0; right: 0; bottom: 0; left: 0; z-index: 30;
  display: flex; align-items: center; justify-content: center; gap: 12px;
  background: color-mix(in srgb, var(--bg) 66%, transparent);
  color: var(--accent); font-size: 12.5px; text-align: center; padding: 0 24px;
}
.sb-lock :deep(.n-button) { color: var(--accent); flex: none; }
.sb-lock :deep(.n-button:hover) { color: var(--accent-hover); }
</style>
