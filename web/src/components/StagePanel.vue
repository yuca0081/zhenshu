<script setup lang="ts">
import { KeepAlive, computed, ref, watch } from 'vue'
import { NButton, NInput, NInputNumber, NSelect, NSpin, useMessage } from 'naive-ui'
import { api, videoURL } from '../api'
import type { ActivityEvent, AudioMeta, Manuscript, PrepView, ProjectView, Storyboard, StyleSamples, TabID } from '../types'
import ManuscriptPane from './ManuscriptPane.vue'
import StylePane from './StylePane.vue'
import PrepPane from './PrepPane.vue'
import IdeaPane from './IdeaPane.vue'
import VideoPane from './VideoPane.vue'
import ActivityPane from './ActivityPane.vue'

// 舞台页签宿主（弹窗体系退役）：文稿/分镜/风格/终审/视频/过程页占据中间区域。
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
}>()

// 页签状态灯（title 悬停说明）：绿=这关过了；黄=还没弄好（没存文稿/没生成分镜/
// 风格没确认）；琥珀描边=成片生成后又改过；视频页蓝=制作中。
// 段数/已确认之类的小字标已撤——状态看灯就够，细节在悬停说明和面板里。
// 想法页签：非 legacy 项目常驻（准备工位的启动页，完成后留作「换个想法重跑」入口）。
const showIdea = computed(() => !!props.prep && !props.prep.legacy)
const tabs = computed(() => {
  const list: { id: TabID; label: string }[] = []
  if (showIdea.value) list.push({ id: 'idea', label: '想法' })
  list.push({ id: 'manuscript', label: '文稿' }, { id: 'storyboard', label: '分镜' }, { id: 'style', label: '风格' })
  if (props.prep?.exists) list.push({ id: 'review', label: '终审' })
  // 「过程」不进页签列——独立按钮放在页签行右侧（head-right），点击切到整页过程内容
  list.push({ id: 'video', label: '视频' })
  return list
})

function tabState(id: TabID): { cls: string[]; title: string; label: string } {
  const v = props.view
  if (!v) return { cls: [], title: '', label: '' }
  const stale = v.video_stale
  switch (id) {
    case 'idea':
      return {
        cls: [props.prep?.running ? 'run' : props.prep?.exists ? 'ok' : 'wait'],
        title: props.prep?.running
          ? '准备阶段进行中（文稿→风格→分镜）'
          : props.prep?.exists ? '准备工件已出——这里可以换个想法重跑' : '说一句话想法（可空），让 AI 自动跑完文稿→风格→分镜',
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
</script>

<template>
  <section class="stage">
    <div class="pane">
      <!-- 页签行：五页 + 状态灯（红绿灯/过期点）+ 右上角 下载成片/过程 -->
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
          <!-- 下载成片 -->
          <a
            v-if="view.has_video" class="dl-btn"
            :href="videoURL(videoId, videoRev)" :download="`${view.name}.mp4`"
          >↓ 下载成片</a>
          <!-- 过程回看：独立胶囊按钮（原事件流按钮样式），点击切到整页「过程」内容 -->
          <button
            class="feed-btn" :class="{ on: tab === 'activity' }"
            :title="tabState('activity').title"
            @click="emit('tab', tab === 'activity' ? 'video' : 'activity')"
          ><span v-if="view.producing" class="live-dot" /><span v-else-if="failUnseen" class="fail-dot" />过程</button>
        </div>
      </header>

      <template v-if="view">
        <!-- KeepAlive 要求恰好一个子节点：内部不许放注释/多根（v-if 链整体算一个） -->
        <KeepAlive>
          <IdeaPane
            v-if="tab === 'idea'"
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
              <span class="sb-meta">{{ edSb?.segments.length ?? storyboard.segments.length }} 段 / 约 {{ Math.round(edTotalSec || view.total_hint) }} 秒</span>
              <span class="sb-dur">
                <NSelect v-model:value="targetMin" size="tiny" :options="minOptions" :disabled="agentBusy" style="width: 108px" />
                <NButton size="tiny" :loading="replanning" :disabled="agentBusy" @click="replanStoryboard">按此时长重排</NButton>
                <NButton v-if="sbDirty" size="tiny" @click="resetSb">还原</NButton>
                <NButton size="tiny" type="primary" :disabled="!sbDirty || agentBusy" :loading="savingSb" @click="saveSb">保存修改</NButton>
              </span>
            </div>
            <div class="sb-scroll">
              <table class="sb">
                <thead><tr><th style="width:44px">段</th><th style="width:130px">标题</th><th>旁白</th><th style="width:200px">画面</th><th style="width:86px">秒</th></tr></thead>
                <tbody>
                  <tr v-for="s in edSb!.segments" :key="s.id" class="sb-row edit">
                    <td class="c">{{ s.idx }}</td>
                    <td><NInput v-model:value="s.key" size="tiny" :disabled="agentBusy" @update:value="sbDirty = true" /></td>
                    <td><NInput v-model:value="s.narration" type="textarea" size="tiny" :autosize="{ minRows: 2, maxRows: 7 }" :disabled="agentBusy" @update:value="sbDirty = true" /></td>
                    <td><NInput v-model:value="s.visual_brief" type="textarea" size="tiny" :autosize="{ minRows: 2, maxRows: 7 }" :disabled="agentBusy" @update:value="sbDirty = true" /></td>
                    <td class="c"><NInputNumber v-model:value="s.duration_hint" size="tiny" :min="0.5" :max="180" :step="0.5" :disabled="agentBusy" style="width: 80px" @update:value="sbDirty = true" /></td>
                  </tr>
                </tbody>
              </table>
              <div class="hint">直接在表格里改文案和秒数，改完点右上「保存修改」（保存后成片会标记为旧版）；要增删段或大改结构，在对话里说。加聊天引用请到时间轴点段。</div>
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
            :project-id="videoId" :view="view" :style-samples="styleSamples"
            :video-stale="view.video_stale.style"
            @saved="emit('saved')" @confirm-style="emit('confirm-style')" @chat-sent="emit('chat-sent')"
          />
          <PrepPane
            v-else-if="tab === 'review'"
            :project-id="videoId" :prep="prep" :confirmed="!!view.gates.style_confirmed" :agent-busy="agentBusy"
            @confirmed="emit('confirm-prep')" @chat-sent="emit('chat-sent')"
          />
          <VideoPane
            v-else-if="tab === 'video'"
            :view="view" :storyboard="storyboard" :audio-meta="audioMeta" :picked-idx="pickedIdx"
            :video-id="videoId" :video-rev="videoRev" :progress-line="progressLine" :stage-summary="stageSummary"
            @seg="(i, k) => emit('seg', i, k)" @pick-time="t => emit('pick-time', t)"
            @cancel="emit('cancel')"
            @go-tab="t => emit('go-tab', t)"
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
.stage { flex: 1; min-height: 0; padding: 16px 20px 20px; display: flex; }
.pane {
  position: relative; /* 事件流面板的定位锚 */
  flex: 1; min-width: 0; display: flex; flex-direction: column;
  background: #14141a; border: 1px solid #232329; border-radius: 12px; overflow: hidden;
}

/* ── 页签行 ── */
.tabs {
  flex: none; display: flex; align-items: center; gap: 4px;
  padding: 8px 12px 0; border-bottom: 1px solid #202027;
}
.tab {
  display: inline-flex; align-items: center; gap: 7px; cursor: pointer;
  border: 0; border-bottom: 2px solid transparent; background: transparent;
  color: #8a8a96; font-size: 13px; padding: 7px 14px 9px; margin-bottom: -1px;
}
.tab:hover { color: #d9d9e0; background: #191920; }
.tab.on { color: #f0c674; border-bottom-color: #f0c674; font-weight: 600; }
.tab-sub { font-size: 11px; color: #7c7c88; font-weight: 400; }
.dot { width: 7px; height: 7px; border-radius: 50%; background: #3a3a45; flex: none; }
.tab.ok .dot { background: #7ee2a8; }
/* 黄=这一关还没过（没存/没生成/没确认），常亮不闪——闪是"进行中"的语言 */
.tab.wait .dot { background: #f0c674; }
.tab.run .dot { background: #8fc7ff; animation: pulse 1.2s infinite; }
/* 过期：整颗页签描琥珀边（黄灯语义=成片落后于我） */
.tab.stale { color: #f0c674; box-shadow: inset 0 0 0 1px rgba(240, 198, 116, .35); border-radius: 8px 8px 0 0; }
.tab.stale .dot { background: #f0c674; }
.tab.on.stale { border-bottom-color: #f0c674; }
@keyframes pulse { 50% { opacity: .35; } }

/* ── 页签行右上角：重做/下载（过程回看走「过程」页签）── */
.head-right { margin-left: auto; display: flex; align-items: center; gap: 8px; padding: 0 2px 6px; }
.redo-btn { margin-bottom: 0; }
.dl-btn {
  display: inline-flex; align-items: center; gap: 6px; cursor: pointer;
  border: 1px solid #2b2b33; background: #191920; color: #c9c9d3;
  font-size: 12px; padding: 4px 12px; border-radius: 999px; text-decoration: none;
}
.dl-btn:hover { border-color: rgba(240, 198, 116, .45); color: #f0c674; background: #1e1a12; }
/* 过程回看按钮：原「事件流」胶囊样式；on=当前正看过程页，live-dot=制作中呼吸灯 */
.feed-btn {
  display: inline-flex; align-items: center; gap: 6px; cursor: pointer;
  border: 1px solid #2b2b33; background: #191920; color: #c9c9d3;
  font-size: 12px; padding: 4px 12px; border-radius: 999px;
}
.feed-btn:hover { border-color: #3a3a45; color: #e6e6ea; }
.feed-btn.on { border-color: rgba(240, 198, 116, .45); color: #f0c674; background: #1e1a12; }
.feed-btn .live-dot { width: 7px; height: 7px; border-radius: 50%; background: #8fc7ff; animation: pulse 1.2s infinite; }
/* 失败未读红点：最近一次制作失败/中断后还没看过过程页 */
.feed-btn .fail-dot { width: 7px; height: 7px; border-radius: 50%; background: #ff6b6b; box-shadow: 0 0 6px rgba(255, 107, 107, .8); }

.empty { flex: 1; display: flex; align-items: center; justify-content: center; }
.none { color: #55555f; }

/* ── 分镜表 ── */
.sb-wrap { position: relative; flex: 1; min-height: 0; display: flex; flex-direction: column; }
.sb-head {
  flex: none; display: flex; align-items: center; gap: 10px;
  padding: 12px 18px; font-weight: 600; border-bottom: 1px solid #202027; color: #d9d9e0;
}
.sb-meta { font-size: 12px; font-weight: 400; color: #7c7c88; }
.sb-dur { margin-left: auto; display: flex; align-items: center; gap: 6px; font-weight: 400; }
.sb-scroll { flex: 1; min-height: 0; overflow-y: auto; }
.sb { width: 100%; border-collapse: collapse; font-size: 13px; }
.sb th { position: sticky; top: 0; background: #14141a; text-align: left; padding: 10px 12px; color: #7c7c88; font-weight: 600; border-bottom: 1px solid #232329; }
.sb td { padding: 6px 8px; border-bottom: 1px solid #1d1d24; vertical-align: top; }
.c { color: #7c7c88; text-align: center; vertical-align: middle; }
.hint { padding: 10px 14px; color: #55555f; font-size: 12px; }
.sb-empty { flex: 1; display: flex; align-items: center; justify-content: center; color: #55555f; font-size: 13px; line-height: 1.9; text-align: center; padding: 20px 40px; }
/* Agent 轮进行中的分镜锁：覆盖整个分镜区域（含表头按钮行）的琥珀提示 + 中断出口 */
.sb-lock {
  position: absolute; top: 0; right: 0; bottom: 0; left: 0; z-index: 30;
  display: flex; align-items: center; justify-content: center; gap: 12px;
  background: rgba(20, 20, 26, .66);
  color: #f0c674; font-size: 12.5px; text-align: center; padding: 0 24px;
}
.sb-lock :deep(.n-button) { color: #f0c674; flex: none; }
.sb-lock :deep(.n-button:hover) { color: #ffdd9a; }
</style>
