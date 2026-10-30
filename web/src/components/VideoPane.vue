<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { NButton, NInput, NSelect, NSpin, useMessage } from 'naive-ui'
import type { AudioMeta, BgmTrack, ProjectView, Storyboard } from '../types'
import { fmtClock } from '../segtime'
import { lastProduceCancelled, lastProduceFailed, stageLabel } from '../format'
import { api, videoURL } from '../api'
import Timeline from './Timeline.vue'
import RIcon from './RIcon.vue'

// 视频页签（原 StagePanel 成片/制作中两态合并 + 过期重做入口）：
// - 无视频未制作 → 开工引导；制作中 → 进度面板（含段级重做细进度）
// - 失败/中断后没在跑 → 「制作暂停」面板（等用户在对话栏发话，不许误落开工引导）
// - 有视频 → 播放器+配乐条+时间轴；上游改过（video_stale）→ 黄条 + 「按最新内容重做」
const props = defineProps<{
  view: ProjectView | null
  storyboard: Storyboard | null
  audioMeta: AudioMeta | null
  pickedIdx: number[]
  videoId: string
  videoRev: number
  progressLine: string
  stageSummary: { key: string; state: string }[] | null
}>()
const emit = defineEmits<{
  seg: [idx: number, key: string]
  'pick-time': [t: number]
  cancel: []
  'go-tab': [tab: 'manuscript']
}>()
const message = useMessage()

// ── 配乐：当前曲 + 换曲下拉 + 试听 + 应用（换曲=后端 StitchNow 重混，画面不动）──
const bgmLib = ref<BgmTrack[] | null>(null)
const bgmPick = ref<string | null>(null)
const bgmBusy = ref(false)
const bgmDur = computed(() =>
  bgmLib.value?.find(t => t.name === props.view?.bgm?.name)?.duration ?? 0)
const bgmOptions = computed(() => {
  const cur = props.view?.bgm?.name
  const lib = bgmLib.value ?? []
  // 当前曲不在清单（被移库）也兜出选项，避免下拉显示成空
  if (cur && !lib.some(t => t.name === cur)) {
    return [{ label: `${cur}（当前）`, value: cur }, ...lib.map(t => ({ label: `${t.name} · ${t.mood}`, value: t.name }))]
  }
  return lib.map(t => ({ label: `${t.name} · ${t.mood}`, value: t.name }))
})
const auditionEl = ref<HTMLAudioElement | null>(null)
const auditionName = ref('')
function toggleAudition() {
  const name = props.view?.bgm?.name
  if (!name) return
  if (auditionName.value === name && auditionEl.value) {
    auditionEl.value.paused ? void auditionEl.value.play() : auditionEl.value.pause()
    return
  }
  auditionEl.value?.pause()
  const el = new Audio(api.bgmAudioURL(name))
  auditionEl.value = el
  auditionName.value = name
  void el.play()
  el.onended = () => { auditionName.value = '' }
}
onBeforeUnmount(() => auditionEl.value?.pause())
async function applyBGM(track: string) {
  if (!props.view || bgmBusy.value) return
  bgmBusy.value = true
  try {
    const r = await api.setBGM(props.view.id, track)
    if (r.error) message.error(r.error)
    else message.success(r.message ?? '配乐已更新')
    if (r.remixed) localRev.value++ // 重混完成 → 本地代次自增，播放器换 URL 重载（画面不动）
  } catch (e) {
    message.error(String(e))
  } finally {
    bgmBusy.value = false
  }
}

// ── 出片封面：预览 + 标题 + 重做（「用当前画面」= 播放器此刻抽帧）──
// 本地账面优先于 props.view.cover（重做成功即刷新，不等下一轮轮询回填）。
const cover = ref<{ t: number; title: string } | null>(null)
const coverTitle = ref('')
const coverDirty = ref(false)
const coverBusy = ref(false)
const coverRev = ref(0)
const coverMissing = ref(false)
watch(() => props.view, v => {
  if (!coverDirty.value) {
    cover.value = v?.cover ?? null
    if (v?.cover?.title) coverTitle.value = v.cover.title
  }
}, { immediate: true })
async function redoCover(useCurrentFrame: boolean) {
  if (!props.view || coverBusy.value) return
  coverBusy.value = true
  try {
    const body: { t?: number; title?: string } = { title: coverTitle.value }
    if (useCurrentFrame) body.t = cur.value
    const r = await api.regenCover(props.view.id, body)
    cover.value = { t: r.t, title: r.title }
    coverTitle.value = r.title
    coverDirty.value = false
    coverMissing.value = false
    coverRev.value++
    message.success(useCurrentFrame ? `封面已换成 ${fmtClock(r.t)} 的画面` : '封面已重做')
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    coverBusy.value = false
  }
}

// ── 视频播放 ↔ 时间轴播放头同步 ──
const videoEl = ref<HTMLVideoElement | null>(null)
const cur = ref(0)
const playing = ref(false) // 播放中隐藏「引用此刻」悬浮钮
function onSeek(t: number) {
  cur.value = t
  if (videoEl.value) videoEl.value.currentTime = t
}
function pickSeg(idx: number, key: string) { emit('seg', idx, key) }
function pickTime(t: number) { emit('pick-time', t) }

// tts 显示「配音不动」：重做/复用链路跳过配音，后续阶段已有动静时 tts 恒 pending
const ttsIdle = computed(() => !!props.stageSummary?.some(x => x.state !== 'pending'))

// ── 制作暂停态：失败/中断终态 + 没在跑 + 没有残留进度 ──
// 这是中间区唯一合法的"停"表述：首做失败不再误落「开工引导」（三门其实已齐），
// 有旧成片时在播放器上方挂横幅说明当前是旧版。恢复动作永远指向对话栏——
// 智能体带着失败原因在那里等用户发话。
const pausedIdle = computed(() =>
  !!props.view && !props.view.has_video && !props.view.producing && !props.stageSummary && lastProduceFailed(props.view))
const failedIdleBanner = computed(() =>
  !!props.view && props.view.has_video && !props.view.producing && !props.stageSummary && lastProduceFailed(props.view))
const pausedWhy = computed(() =>
  props.view?.last_produce?.event.endsWith('.interrupted') ? '因服务重启被中断' : '检查未通过或出错')

// 用户主动停止（区别于失败：无故障、产物按段保留）——恢复入口同样指向对话栏
const cancelledIdle = computed(() =>
  !!props.view && !props.view.has_video && !props.view.producing && !props.stageSummary &&
  lastProduceCancelled(props.view) && !lastProduceFailed(props.view))

// 三件齐+打包终审已确认、还没开工：这是「就绪」不是「没干活」——旧空态
// 「先填文稿」在这个时点是误导（2026-10-15 实测 P6），引导去对话栏开工
const readyToStart = computed(() =>
  !!props.view && !props.view.has_video && !props.view.producing && !props.stageSummary &&
  !!props.view.gates?.style_confirmed && !lastProduceFailed(props.view))

// ── 成片代次刷新：换 URL 重载并回到原播放位置（重做完立刻看新画面）──
// localRev：本页内自己的代次（换曲重混后画面不变但音轨变了，App 的 videoRev 不动）
const localRev = ref(0)
watch([() => props.videoRev, localRev], () => {
  const t = cur.value
  const el = videoEl.value
  if (!el) return
  el.load()
  if (t > 0) {
    const restore = () => { el.currentTime = Math.min(t, el.duration || t) }
    el.addEventListener('loadedmetadata', restore, { once: true })
  }
})
</script>

<template>
  <div v-if="view" class="video-pane" :class="{ vertical: view.aspect === '9:16', plain: !view.has_video }">
    <!-- 制作暂停：上次制作失败/中断，智能体带着失败原因在对话栏等回复 -->
    <template v-if="pausedIdle">
      <div class="prod-title"><RIcon name="pause" :size="16" />制作暂停</div>
      <p class="prep-hint">
        上次制作{{ pausedWhy }}，产物已保留——文稿、分镜、风格都还在。
        到下方<b>对话栏</b>看智能体的失败说明：回一句「继续」它就接着修，或直接说要改什么。
      </p>
    </template>

    <!-- 已停止（用户主动取消）：不是失败也不是就绪，产物按段保留 -->
    <template v-else-if="cancelledIdle">
      <div class="prod-title"><RIcon name="pause" :size="16" />已停止制作</div>
      <p class="prep-hint">
        按你的要求停了——产物已按段保留（配音、字幕与已渲段落都在）。
        到下方<b>对话栏</b>说「<b>继续</b>」可从断点接着做，或直接说要改什么。
      </p>
    </template>

    <!-- 就绪待开工（三件齐+终审已确认） -->
    <template v-else-if="readyToStart">
      <div class="prod-title ok"><RIcon name="check" :size="16" />终审已通过，随时开工</div>
      <p class="prep-hint">
        文稿、风格、分镜三件已齐，打包终审也确认过了——到下方<b>对话栏</b>说「<b>开始</b>」就开工制作。
      </p>
    </template>

    <!-- 开工引导（三前置未齐/未开跑）：按门态说下一步——旧文案与实际门不符
         （样张待确认却教人「说开始」，2026-10-30 GUI 实测 t14） -->
    <template v-else-if="!view.has_video && !view.producing && !stageSummary">
      <div class="prod-title">三件事齐了就开工</div>
      <p v-if="view.gates?.style_draft && !view.gates?.style_confirmed" class="prep-hint">
        文稿、分镜、风格样张都已就绪——还差一道人工门：去<b>「风格」页签</b>看样张并点
        「确认风格，开工」（或在「终审」页签打包确认）。确认后在对话里说「开始」。
      </p>
      <p v-else-if="view.gates?.manuscript && !view.gates?.storyboard" class="prep-hint">
        <b>文稿</b>已就绪——在下方<b>对话栏</b>说「生成分镜」，我来拆段、写画面、标时长。
      </p>
      <p v-else class="prep-hint">
        补齐三个前置：<b>文稿</b>（编辑或 AI 起草）、<b>分镜</b>（我来生成）、<b>风格</b>（选库内或描述方向）——
        就在上方的页签里，齐了在对话里说「开始」。
      </p>
      <NButton v-if="!view.gates?.manuscript" type="primary" @click="emit('go-tab', 'manuscript')">先填文稿</NButton>
    </template>

    <!-- 制作中：进度面板 -->
    <template v-else-if="!view.has_video">
      <NSpin size="large" />
      <div class="prod-title">制作管线运行中</div>
      <div v-if="stageSummary" class="stages">
        <div v-for="st in stageSummary" :key="st.key" class="pstage" :data-state="st.state">
          <span class="dot" />{{ stageLabel(st.key) }}
        </div>
      </div>
      <button v-if="view.producing" class="stop-btn" @click="emit('cancel')"><RIcon name="stop" :size="13" />停止制作</button>
    </template>

    <!-- 成片：左=播放器+时间轴，右=封面/配乐竖栏（2026-10-30 拥挤优化：
         原先横幅/配乐/封面/播放器/时间轴五层横条竖叠，时间轴被挤出屏——
         附属控制改吃横向富余，播放器+时间轴拿回纵向预算） -->
    <template v-else>
      <!-- 上次制作/重做失败且没在跑：当前播放的是失败前旧版，恢复入口在对话栏 -->
      <div v-if="failedIdleBanner" class="paused-banner">
        <RIcon name="pause" :size="14" />上次制作没有走完，当前播放的是旧版——到对话栏回复「继续」接着修
      </div>
      <!-- 段级重做中：细进度条（其余段的画面/音频不动） -->
      <div v-if="stageSummary" class="rework-strip">
        <div
          v-for="st in stageSummary" :key="st.key" class="pstage"
          :data-state="st.state" :data-skip="st.key === 'tts' && st.state === 'pending' && ttsIdle"
        >
          <span class="dot" />{{ st.key === 'tts' && st.state === 'pending' && ttsIdle ? '配音不动' : stageLabel(st.key) }}
        </div>
        <span class="rework-hint">段级重做中</span>
        <button v-if="view.producing" class="stop-btn" @click="emit('cancel')"><RIcon name="stop" :size="13" />停止</button>
      </div>
      <div v-if="progressLine && stageSummary" class="progress-line">{{ progressLine }}</div>
      <div class="out-wrap">
        <div class="player-col">
          <div class="player">
            <video
              ref="videoEl" :src="videoURL(videoId, videoRev + localRev)" controls preload="metadata"
              @timeupdate="cur = videoEl!.currentTime"
              @play="playing = true" @pause="playing = false" @ended="playing = false"
            />
            <button v-show="!playing" class="now-btn" @click="pickTime(cur)">
              引用此刻 {{ fmtClock(cur) }}
            </button>
          </div>
          <Timeline
            v-if="storyboard"
            :storyboard="storyboard" :audio-meta="audioMeta" :bgm="view.bgm ?? null"
            :bgm-dur="bgmDur" :current-time="cur"
            :picked-idx="pickedIdx" @seek="onSeek" @pick="pickSeg" @pick-time="pickTime"
          />
        </div>
        <aside class="side-rail">
          <!-- 封面卡：预览 + 标题 + 重做（重做中隐藏——收尾自动出封面，落定再回来看） -->
          <div v-if="!view.producing && !stageSummary" class="rail-card">
            <div class="rail-head">封面<span v-if="cover" class="rail-meta" title="封面帧在成片里的时刻">{{ fmtClock(cover.t) }} 帧</span></div>
            <img
              v-if="!coverMissing" class="cover-thumb" :class="{ vertical: view.aspect === '9:16' }"
              :src="api.coverURL(view.id, coverRev)" alt="封面预览"
              @error="coverMissing = true"
            />
            <div v-else class="cover-thumb none" :class="{ vertical: view.aspect === '9:16' }">尚未生成</div>
            <NInput
              v-model:value="coverTitle" size="tiny" :disabled="coverBusy"
              placeholder="封面标题（留空用项目名）" @update:value="coverDirty = true"
            />
            <div class="rail-btns">
              <NButton size="tiny" :loading="coverBusy" :disabled="coverBusy" @click="redoCover(false)">重做封面</NButton>
              <NButton size="tiny" type="primary" :loading="coverBusy" :disabled="coverBusy" @click="redoCover(true)">用当前画面</NButton>
            </div>
          </div>
          <!-- 配乐卡：当前曲+选曲理由（悬停看全文）· 试听 · 换曲 · 应用/关闭 -->
          <div class="rail-card">
            <div class="rail-head">配乐<span class="rail-meta" :title="view.bgm?.reason || ''">{{ view.bgm?.name || '无配乐' }}</span></div>
            <NSelect
              v-model:value="bgmPick" size="tiny"
              :options="bgmOptions" placeholder="换曲…" :loading="bgmBusy"
              @focus="!bgmLib && api.bgmLibrary().then(l => (bgmLib = l)).catch(() => {})"
            />
            <div class="rail-btns">
              <NButton size="tiny" :disabled="!view.bgm?.name" @click="toggleAudition">
                <RIcon :name="auditionName === view.bgm?.name ? 'stop' : 'play'" :size="11" />{{ auditionName === view.bgm?.name ? '停' : '试听' }}
              </NButton>
              <NButton size="tiny" type="primary" :disabled="!bgmPick || bgmBusy" :loading="bgmBusy" @click="bgmPick && applyBGM(bgmPick)">
                应用
              </NButton>
              <NButton v-if="view.bgm?.name" size="tiny" quaternary :disabled="bgmBusy" @click="applyBGM('off')">
                关配乐
              </NButton>
            </div>
          </div>
        </aside>
      </div>
    </template>
  </div>
</template>

<style scoped>
.video-pane {
  flex: 1; min-height: 0; display: flex; flex-direction: column;
  align-items: center; justify-content: center; gap: 12px; padding: 16px 20px;
}
.video-pane.plain { gap: 18px; }
/* 重做条保持固有高度不被挤出面板；播放器在其下吃剩余高度（成片区布局见 .out-wrap） */
.video-pane > .rework-strip { flex: none; }
.player { position: relative; width: min(100%, 1080px); flex: 1 1 0; min-height: 0; display: flex; justify-content: center; align-items: center; }
.player video { width: 100%; height: 100%; object-fit: contain; background: #000; display: block; }
.video-pane.vertical .player { width: auto; }

/* 「引用此刻」悬浮在视频右上角（播放中隐藏），避开底部原生控制条 */
.now-btn {
  position: absolute; right: 10px; top: 10px; z-index: 4; cursor: pointer;
  border: 1px solid color-mix(in srgb, var(--accent) 45%, transparent);
  background: rgb(var(--veil) / 0.72); color: var(--accent);
  font-size: 12px; font-family: inherit; padding: 4px 12px; backdrop-filter: blur(3px);
  font-variant-numeric: tabular-nums;
}
.now-btn:hover { background: rgb(var(--veil) / 0.9); }
.rework-strip { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.rework-hint { font-size: 12px; color: var(--accent); letter-spacing: 0.08em; }
.progress-line { flex: none; width: 100%; font-size: 12px; color: var(--muted); padding: 0 4px; }
/* ── 成片区布局（2026-10-30 拥挤优化）：左=播放器+时间轴（吃宽度与纵向预算），
   右=封面/配乐竖栏（固定窄栏，吃横向富余）。栏宽不够时整体换行到播放器下方 ── */
.out-wrap {
  flex: 1; min-height: 0; width: 100%;
  display: flex; align-items: stretch; justify-content: center; gap: 14px;
  flex-wrap: wrap;
}
.player-col {
  flex: 1 1 380px; min-width: 0; min-height: 0;
  display: flex; flex-direction: column; align-items: center; gap: 10px;
}
.player-col .tl { flex: none; max-width: 100%; }
/* 附属竖栏：固定窄栏（不收缩——基础宽度和 380+240+14=634，常规三栏下稳稳排得下，
   真放不下时整体换行到播放器下方）；两卡在栏内纵向堆叠 */
.side-rail {
  flex: 0 0 240px; min-width: 0; min-height: 0;
  display: flex; flex-direction: column; gap: 12px;
}
.rail-card {
  display: flex; flex-direction: column; gap: 8px;
  border: 1px solid var(--line-soft); padding: 10px 12px 12px;
}
.rail-head {
  font-size: 12px; font-weight: 600; color: var(--ink);
  display: flex; align-items: baseline; gap: 8px; min-width: 0;
}
.rail-meta {
  font-weight: 400; font-size: 11.5px; color: var(--muted);
  overflow: hidden; white-space: nowrap; text-overflow: ellipsis;
  font-variant-numeric: tabular-nums;
}
.rail-btns { display: flex; align-items: center; gap: 6px; }
.cover-thumb {
  width: 100%; aspect-ratio: 16 / 9; object-fit: cover; display: block;
  border: 1px solid var(--line); background: #000;
}
.cover-thumb.vertical { width: auto; height: 168px; aspect-ratio: 9 / 16; margin: 0 auto; }
.cover-thumb.none {
  display: flex; align-items: center; justify-content: center;
  color: var(--muted); font-size: 11px; background: color-mix(in srgb, var(--ink) 4%, var(--surface));
}
/* 制作暂停横幅（有旧成片）：琥珀警示，与 video_stale 黄灯同族色 */
.paused-banner {
  flex: none; width: 100%; box-sizing: border-box; font-size: 12.5px; color: var(--warn);
  display: inline-flex; align-items: center; gap: 8px; padding: 5px 14px;
  background: color-mix(in srgb, var(--warn) 8%, transparent);
  border: 1px solid color-mix(in srgb, var(--warn) 35%, transparent);
}
.pstage[data-skip="true"] { opacity: .45; border-style: dashed; }

/* 制作中（pstage = 进度块） */
.prod-title { color: var(--ink); font-size: 15px; display: inline-flex; align-items: center; gap: 8px; }
.prod-title.ok { color: var(--ok); }
.prep-hint { max-width: 500px; text-align: center; color: var(--muted); font-size: 13px; line-height: 1.9; margin: 0; }
.prep-hint b { color: var(--accent); font-weight: 600; }
.stages { display: flex; gap: 10px; flex-wrap: wrap; justify-content: center; }
.pstage {
  display: flex; align-items: center; gap: 6px; font-size: 13px; color: var(--muted);
  border: 1px solid var(--line); padding: 5px 14px; font-variant-numeric: tabular-nums;
}
.dot { width: 8px; height: 8px; border-radius: 50%; background: var(--muted); opacity: .5; }
.pstage[data-state="running"] { color: var(--info); border-color: color-mix(in srgb, var(--info) 40%, transparent); }
.pstage[data-state="running"] .dot { background: var(--info); opacity: 1; animation: pulse 1.2s infinite; }
.pstage[data-state="done"] { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 35%, transparent); }
.pstage[data-state="done"] .dot { background: var(--ok); opacity: 1; }
.pstage[data-state="error"] { color: var(--err); border-color: color-mix(in srgb, var(--err) 40%, transparent); }
.pstage[data-state="error"] .dot { background: var(--err); opacity: 1; }
.stop-btn {
  align-self: center; padding: 6px 16px; cursor: pointer; font-family: inherit;
  display: inline-flex; align-items: center; gap: 6px;
  color: var(--err); background: transparent; border: 1px solid color-mix(in srgb, var(--err) 45%, transparent); font-size: 13px;
}
.stop-btn:hover { background: color-mix(in srgb, var(--err) 10%, transparent); border-color: var(--err); }
@keyframes pulse { 50% { opacity: .35; } }
</style>
