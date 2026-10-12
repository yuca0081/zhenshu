<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NButton, NSpin } from 'naive-ui'
import type { AudioMeta, ProjectView, Storyboard } from '../types'
import { fmtClock } from '../segtime'
import { lastProduceFailed, stageLabel } from '../format'
import { api, videoURL } from '../api'
import Timeline from './Timeline.vue'

// 视频页签（原 StagePanel 成片/制作中两态合并 + 过期重做入口）：
// - 无视频未制作 → 开工引导；制作中 → 进度面板（含段级重做细进度）
// - 失败/中断后没在跑 → 「制作暂停」面板（等用户在对话栏发话，不许误落开工引导）
// - 有视频 → 播放器+时间轴；上游改过（video_stale）→ 黄条 + 「按最新内容重做」
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

// ── 成片代次刷新：换 URL 重载并回到原播放位置（重做完立刻看新画面）──
watch(() => props.videoRev, () => {
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
      <div class="prod-title">⏸ 制作暂停</div>
      <p class="prep-hint">
        上次制作{{ pausedWhy }}，产物已保留——文稿、分镜、风格都还在。
        到下方<b>对话栏</b>看智能体的失败说明：回一句「继续」它就接着修，或直接说要改什么。
      </p>
    </template>

    <!-- 开工引导（三前置未齐/未开跑） -->
    <template v-else-if="!view.has_video && !view.producing && !stageSummary">
      <div class="prod-title">三件事齐了就开工</div>
      <p class="prep-hint">
        补齐三个前置：<b>文稿</b>（编辑或 AI 起草）、<b>分镜</b>（我来生成）、<b>风格</b>（选库内或描述方向）——
        就在上方的页签里，齐了在对话里说「开始」。
      </p>
      <NButton type="primary" @click="emit('go-tab', 'manuscript')">先填文稿</NButton>
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
      <button v-if="view.producing" class="stop-btn" @click="emit('cancel')">■ 停止制作</button>
    </template>

    <!-- 成片：播放器 + 时间轴（整片过期重做按钮在上方页签行） -->
    <template v-else>
      <!-- 上次制作/重做失败且没在跑：当前播放的是失败前旧版，恢复入口在对话栏 -->
      <div v-if="failedIdleBanner" class="paused-banner">
        ⏸ 上次制作没有走完，当前播放的是旧版——到对话栏回复「继续」接着修
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
        <button v-if="view.producing" class="stop-btn" @click="emit('cancel')">■ 停止</button>
      </div>
      <div v-if="progressLine && stageSummary" class="progress-line">{{ progressLine }}</div>
      <div class="player">
        <video
          ref="videoEl" :src="videoURL(videoId, videoRev)" controls preload="metadata"
          @timeupdate="cur = videoEl!.currentTime"
          @play="playing = true" @pause="playing = false" @ended="playing = false"
        />
        <button v-show="!playing" class="now-btn" @click="pickTime(cur)">
          引用此刻 {{ fmtClock(cur) }}
        </button>
      </div>
      <Timeline
        v-if="storyboard"
        :storyboard="storyboard" :audio-meta="audioMeta" :current-time="cur"
        :picked-idx="pickedIdx" @seek="onSeek" @pick="pickSeg" @pick-time="pickTime"
      />
    </template>
  </div>
</template>

<style scoped>
.video-pane {
  flex: 1; min-height: 0; display: flex; flex-direction: column;
  align-items: center; justify-content: center; gap: 12px; padding: 16px 20px;
}
.video-pane.plain { gap: 18px; }
/* 播放器吃剩余高度，其余行（重做条/时间轴）保持固有高度不被挤出面板 */
.video-pane > .rework-strip, .video-pane > .tl { flex: none; }
.player { position: relative; width: min(100%, 1080px); flex: 1 1 0; min-height: 0; display: flex; justify-content: center; align-items: center; }
.player video { width: 100%; height: 100%; object-fit: contain; border-radius: 8px; background: #000; display: block; }
.video-pane.vertical .player { width: auto; }

/* 「引用此刻」悬浮在视频右上角（播放中隐藏），避开底部原生控制条 */
.now-btn {
  position: absolute; right: 10px; top: 10px; z-index: 4; cursor: pointer;
  border: 1px solid rgba(240, 198, 116, .45); background: rgba(16, 14, 8, .72); color: #f0c674;
  font-size: 12px; padding: 4px 12px; border-radius: 999px; backdrop-filter: blur(3px);
}
.now-btn:hover { background: rgba(42, 36, 22, .9); }
.rework-strip { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.rework-hint { font-size: 12px; color: #f0c674; }
.progress-line { flex: none; width: 100%; font-size: 12px; color: #8a8a96; padding: 0 4px; }
/* 制作暂停横幅（有旧成片）：琥珀警示，与 video_stale 黄灯同族色 */
.paused-banner {
  flex: none; width: 100%; box-sizing: border-box; font-size: 13px; color: #ffcf87;
  background: rgba(66, 46, 16, .3); border: 1px solid #5a4522; border-radius: 8px; padding: 7px 14px;
}
.pstage[data-skip="true"] { opacity: .45; border-style: dashed; }

/* 制作中（pstage = 进度胶囊） */
.prod-title { color: #c3c3cd; font-size: 15px; }
.prep-hint { max-width: 500px; text-align: center; color: #8a8a96; font-size: 13px; line-height: 1.9; margin: 0; }
.prep-hint b { color: #f0c674; font-weight: 600; }
.stages { display: flex; gap: 10px; flex-wrap: wrap; justify-content: center; }
.pstage {
  display: flex; align-items: center; gap: 6px; font-size: 13px; color: #7c7c88;
  border: 1px solid #2b2b33; border-radius: 999px; padding: 5px 14px;
}
.dot { width: 8px; height: 8px; border-radius: 50%; background: #3a3a45; }
.pstage[data-state="running"] { color: #8fc7ff; border-color: #2b4a66; }
.pstage[data-state="running"] .dot { background: #8fc7ff; animation: pulse 1.2s infinite; }
.pstage[data-state="done"] { color: #7ee2a8; border-color: #2c5c40; }
.pstage[data-state="done"] .dot { background: #7ee2a8; }
.pstage[data-state="error"] { color: #ff9d9d; border-color: #66302b; }
.pstage[data-state="error"] .dot { background: #ff9d9d; }
.stop-btn {
  align-self: center; padding: 6px 16px; border-radius: 8px; cursor: pointer;
  color: #ffb3b3; background: #2a1a1c; border: 1px solid #5a3230; font-size: 13px;
}
.stop-btn:hover { background: #3a2224; border-color: #7a403e; }
@keyframes pulse { 50% { opacity: .35; } }
</style>
