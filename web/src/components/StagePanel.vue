<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useMessage, NButton, NInput, NModal, NScrollbar, NSpin, NTag } from 'naive-ui'
import type { AudioMeta, Manuscript, ProjectView, Storyboard, StyleSamples } from '../types'
import { fmtClock, segTable, type SegCol } from '../segtime'
import { api, videoURL } from '../api'
import Timeline from './Timeline.vue'
import SampleFrame from './SampleFrame.vue'

const props = defineProps<{
  view: ProjectView | null
  storyboard: Storyboard | null
  styleSamples: StyleSamples | null
  manuscript: Manuscript | null
  stageSummary: { key: string; state: string }[] | null
  videoId: string
  audioMeta: AudioMeta | null
  pickedIdx: number[]
  videoRev: number // 成片代次（重做完成递增）：换 URL 强制重载
  progressLine: string // 制作/重做细粒度进度（App 转发 SSE progress）
}>()
const emit = defineEmits<{ 'confirm-style': []; seg: [idx: number, key: string]; cancel: []; 'pick-time': [t: number]; 'edit-manuscript': []; 'rework-started': [] }>()

const stageName: Record<string, string> = {
  tts: '配音', compositions: '画面', assemble: '组装', check: '检查', render: '渲染', frameqa: '画面审查',
}

// 视频播放 ↔ 时间轴播放头同步
const videoEl = ref<HTMLVideoElement | null>(null)
const cur = ref(0)
const dur = ref(0)
const playing = ref(false) // 播放中隐藏「引用此刻」悬浮钮
function onSeek(t: number) {
  cur.value = t
  if (videoEl.value) videoEl.value.currentTime = t
}
function pickSeg(idx: number, key: string) { emit('seg', idx, key) }
// 时刻引用：播放器「引用此刻」按钮 / 时间轴双击（全局秒，随消息结构化发出）
function pickTime(t: number) { emit('pick-time', t) }

// 皮肤槽位色板（非颜色槽位不进 chips，字体单独展示）
const skinChips = computed(() => {
  const sl = props.styleSamples?.skin?.slots
  if (!sl) return []
  const out = [
    { label: '底色', value: sl.bg },
    { label: '文字', value: sl.txt },
    { label: '强调', value: sl.accent },
  ]
  if (sl.mute) out.push({ label: '次级', value: sl.mute })
  if (sl.surface) out.push({ label: '面板', value: sl.surface })
  return out.filter(c => c.value)
})

const segsT = computed(() => segTable(props.storyboard, props.audioMeta))
// tts 显示「配音不动」：重做/复用链路跳过配音，后续阶段已有动静时 tts 恒 pending
const ttsIdle = computed(() => !!props.stageSummary?.some(x => x.state !== 'pending'))

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

// ── 一键重做此段（时间轴段块按钮）：预填该段上次指令，确认直连 REST（不走对话轮）──
const reworkShow = ref(false)
const reworkInstr = ref('')
const reworkBusy = ref(false)
const reworkSeg = ref<SegCol | null>(null)
const message = useMessage()
async function openRework(idx: number, key: string) {
  const s = segsT.value.find(x => x.idx === idx && x.key === key)
  if (!s) return
  reworkSeg.value = s
  reworkInstr.value = ''
  const r = await api.reworkLast(props.videoId, s.segId).catch(() => null)
  reworkInstr.value = r?.instruction ?? ''
  reworkShow.value = true
}
async function confirmRework() {
  const s = reworkSeg.value
  if (!s || reworkBusy.value) return
  reworkBusy.value = true
  try {
    await api.reworkStart(props.videoId, [s.segId], reworkInstr.value.trim())
    reworkShow.value = false
    emit('rework-started') // REST 直连不经 agent：让 App 重拉 view（producing 态/停止按钮）
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    reworkBusy.value = false
  }
}
</script>

<template>
  <section class="stage">
    <!-- 舞台四态：文稿 → 分镜 → 风格样张 → 视频 -->
    <template v-if="view">
      <!-- 状态 1：文稿（前置未齐时的底稿展示） -->
      <div v-if="!view.gates.storyboard && manuscript" class="pane">
        <div class="pane-head">
          文稿 <NTag size="small" :bordered="false">{{ manuscript.word_count }} 字</NTag>
          <NButton size="tiny" quaternary class="edit-btn" @click="emit('edit-manuscript')">编辑</NButton>
        </div>
        <NScrollbar class="ms-scroll">
          <article class="manuscript">{{ manuscript.content }}</article>
        </NScrollbar>
      </div>

      <!-- 状态 2：分镜表 -->
      <div v-else-if="view.gates.storyboard && !view.gates.style_draft && storyboard" class="pane">
        <div class="pane-head">
          分镜 <NTag size="small" :bordered="false">{{ storyboard.segments.length }} 段 / 约 {{ Math.round(view.total_hint) }} 秒</NTag>
        </div>
        <NScrollbar class="ms-scroll">
          <table class="sb">
            <thead><tr><th style="width:56px">段</th><th style="width:130px">标题</th><th>旁白</th><th style="width:170px">画面</th><th style="width:64px">秒</th></tr></thead>
            <tbody>
              <tr v-for="s in storyboard.segments" :key="s.id" class="sb-row" @click="emit('seg', s.idx, s.key)">
                <td class="c">{{ s.idx }}</td>
                <td><b>{{ s.key }}</b></td>
                <td class="narr">{{ s.narration }}</td>
                <td class="brief">{{ s.visual_brief }}</td>
                <td class="c">{{ Math.round(s.duration_hint) }}</td>
              </tr>
            </tbody>
          </table>
          <div class="hint">点任意段加入聊天引用列表（可删），随下一条消息发给 Agent</div>
        </NScrollbar>
      </div>

      <!-- 状态 3：风格样张（硬门：用户确认；制作中切到进度面板） -->
      <div v-else-if="view.gates.style_draft && styleSamples && !view.has_video && !view.producing && !stageSummary" class="pane">
        <div class="pane-head">
          风格样张 · {{ styleSamples.direction }}
          <NTag size="small" :bordered="false" :type="styleSamples.confirmed ? 'success' : 'warning'">
            {{ styleSamples.confirmed ? '已确认' : '待确认' }}
          </NTag>
        </div>
        <NScrollbar class="ms-scroll">
          <!-- 风格文件（§12.2）：确认样张 = 确认这份皮肤——四槽位色板 + 字体直读 -->
          <div v-if="styleSamples.skin" class="skin-bar">
            <span class="skin-name">皮肤 {{ styleSamples.skin.name }}</span>
            <span v-for="c in skinChips" :key="c.label" class="chip" :title="c.value">
              <i :style="{ background: c.value }" />{{ c.label }}
            </span>
            <code>{{ styleSamples.skin.slots.font }}</code>
          </div>
          <div class="samples">
            <figure v-for="s in styleSamples.samples" :key="s.tag" class="sample">
              <!-- 样张 HTML 固定 960×540，卡片只有半宽——必须整体缩放（SampleFrame），裸 iframe 只会露出左上局部 -->
              <SampleFrame :html="s.html" />
              <figcaption>
                <b>{{ s.tag }}</b>
                <span>{{ s.desc }}</span>
              </figcaption>
            </figure>
          </div>
          <div class="confirm-bar">
            <p>样张与成片同一套皮肤，所见即所得。确认后风格文件锁定进项目（这是唯一必须你拍板的门）。</p>
            <NButton v-if="!styleSamples.confirmed" type="primary" size="large" @click="emit('confirm-style')">
              确认风格，开工
            </NButton>
            <NButton v-else type="success" ghost size="large" disabled>已确认</NButton>
          </div>
        </NScrollbar>
      </div>

      <!-- 状态 4：成片（下载在标题行，「引用此刻」悬浮视频右上；舞台只留画面+时间轴） -->
      <div v-else-if="view.has_video" class="pane video-pane" :class="{ vertical: view.aspect === '9:16' }">
        <!-- 段级重做中：细进度条（其余段的画面/音频不动） -->
        <div v-if="stageSummary" class="rework-strip">
          <div
            v-for="st in stageSummary" :key="st.key" class="pstage"
            :data-state="st.state" :data-skip="st.key === 'tts' && st.state === 'pending' && ttsIdle"
          >
            <span class="dot" />{{ st.key === 'tts' && st.state === 'pending' && ttsIdle ? '配音不动' : stageName[st.key] ?? st.key }}
          </div>
          <span class="rework-hint">段级重做中</span>
          <button v-if="view.producing" class="stop-btn" @click="emit('cancel')">■ 停止</button>
        </div>
        <div v-if="progressLine && stageSummary" class="progress-line">{{ progressLine }}</div>
        <div class="player">
          <video
            ref="videoEl" :src="videoURL(videoId, videoRev)" controls preload="metadata"
            @timeupdate="cur = videoEl!.currentTime" @loadedmetadata="dur = videoEl!.duration || 0"
            @play="playing = true" @pause="playing = false" @ended="playing = false"
          />
          <button v-show="!playing" class="now-btn" @click="pickTime(cur)">
            引用此刻 {{ fmtClock(cur) }}
          </button>
        </div>
        <NModal v-model:show="reworkShow" preset="dialog" title="重做此段画面" positive-text="开始重做" negative-text="取消" :loading="reworkBusy" @positive-click="confirmRework">
          <p class="rework-modal-hint">只重生成这一段的画面（配音/时长/其余段不动），改完整片自动重缝。留空 = 用上次的要求再试一次。</p>
          <NInput v-model:value="reworkInstr" type="textarea" :rows="3" placeholder="画面修改要求，例：标题缩小放左上，配图换成战场全景" />
        </NModal>
        <Timeline
          v-if="storyboard"
          :storyboard="storyboard" :audio-meta="audioMeta" :current-time="cur" :duration="dur"
          :picked-idx="pickedIdx" :rework="!stageSummary" @seek="onSeek" @pick="pickSeg" @pick-time="pickTime"
          @rework="openRework"
        />
      </div>

      <!-- 制作中：进度面板（其余状态都不满足 = 管线在跑或等待中） -->
      <div v-else class="pane producing">
        <template v-if="!view.producing && !stageSummary">
          <div class="prod-title">三件事齐了就开工</div>
          <p class="prep-hint">
            点右侧对话栏上方的标签补齐：<b>文稿</b>（编辑或 AI 起草）、<b>分镜</b>（我来生成）、<b>风格</b>（选库内或描述方向），然后在对话里说「开始」。
          </p>
          <NButton type="primary" @click="emit('edit-manuscript')">先填文稿</NButton>
        </template>
        <template v-else>
          <NSpin size="large" />
          <div class="prod-title">制作管线运行中</div>
          <div v-if="stageSummary" class="stages">
            <div v-for="st in stageSummary" :key="st.key" class="pstage" :data-state="st.state">
              <span class="dot" />{{ stageName[st.key] ?? st.key }}
            </div>
          </div>
          <button v-if="view.producing" class="stop-btn" @click="emit('cancel')">■ 停止制作</button>
        </template>
      </div>
    </template>

    <div v-else class="pane empty">
      <div class="none">← 左侧新建或选择一个项目</div>
    </div>
  </section>
</template>

<style scoped>
.stage { flex: 1; min-height: 0; padding: 16px 20px 20px; display: flex; }
.pane {
  flex: 1; min-width: 0; display: flex; flex-direction: column;
  background: #14141a; border: 1px solid #232329; border-radius: 12px; overflow: hidden;
}
.pane-head {
  flex: none; display: flex; align-items: center; gap: 10px;
  padding: 12px 18px; font-weight: 600; border-bottom: 1px solid #202027; color: #d9d9e0;
}
.pane-head .edit-btn { margin-left: auto; font-weight: 400; }
.ms-scroll { flex: 1; }
.manuscript { padding: 26px 34px; line-height: 2.1; font-size: 15px; color: #cfcfd8; white-space: pre-wrap; max-width: 860px; margin: 0 auto; font-family: 'KaiTi', 'STKaiti', serif; font-size: 17px; }

/* 分镜表 */
.sb { width: 100%; border-collapse: collapse; font-size: 13px; }
.sb th { position: sticky; top: 0; background: #14141a; text-align: left; padding: 10px 12px; color: #7c7c88; font-weight: 600; border-bottom: 1px solid #232329; }
.sb td { padding: 10px 12px; border-bottom: 1px solid #1d1d24; vertical-align: top; }
.sb-row { cursor: pointer; }
.sb-row:hover td { background: #191920; }
.c { color: #7c7c88; text-align: center; }
.narr { color: #c3c3cd; }
.brief { color: #8a8a96; }
.hint { padding: 10px 14px; color: #55555f; font-size: 12px; }

/* 皮肤槽位条 */
.skin-bar {
  display: flex; align-items: center; gap: 10px; flex-wrap: wrap;
  padding: 12px 20px 0; font-size: 12px; color: #8a8a96;
}
.skin-name { color: #c3c3cd; font-weight: 600; }
.skin-bar code { background: #1b1b22; padding: 2px 8px; border-radius: 6px; font-size: 11px; }
.chip { display: inline-flex; align-items: center; gap: 5px; border: 1px solid #2b2b33; border-radius: 999px; padding: 2px 10px; }
.chip i { width: 12px; height: 12px; border-radius: 3px; display: inline-block; border: 1px solid rgba(255,255,255,.18); }

/* 样张 */
.samples { display: flex; gap: 16px; padding: 20px; flex-wrap: wrap; }
.sample { flex: 1; min-width: 320px; max-width: 480px; }
.sample :deep(.sf) { border: 1px solid #2b2b33; border-radius: 8px; }
figcaption { margin-top: 8px; display: flex; flex-direction: column; gap: 2px; }
figcaption b { font-size: 13px; }
figcaption span { font-size: 12px; color: #8a8a96; }
.confirm-bar { padding: 4px 20px 20px; display: flex; align-items: center; justify-content: space-between; gap: 20px; }
.confirm-bar p { color: #8a8a96; font-size: 13px; max-width: 560px; }

/* 成片 */
.video-pane { align-items: center; justify-content: center; gap: 12px; padding: 16px 20px; }
/* 播放器吃剩余高度，其余行（重做条/时间轴）保持固有高度不被挤出面板 */
.video-pane > .rework-strip, .video-pane > .tl { flex: none; }
.player { position: relative; width: min(100%, 1080px); flex: 1 1 0; min-height: 0; display: flex; justify-content: center; align-items: center; }
/* video 铺满播放器、画面 contain 居中（letterbox 同为黑底）——悬浮钮/控制条贴视频框 */
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
.pstage[data-skip="true"] { opacity: .45; border-style: dashed; }
.rework-modal-hint { font-size: 13px; color: #8a8a96; line-height: 1.8; margin: 0 0 10px; }

/* 制作中（pstage = 进度胶囊；不能叫 .stage，会和外层舞台 section 撞类名导致 align/padding 污染） */
.producing { align-items: center; justify-content: center; gap: 18px; }
.prod-title { color: #c3c3cd; font-size: 15px; }
.prep-hint { max-width: 480px; text-align: center; color: #8a8a96; font-size: 13px; line-height: 1.9; }
.prep-hint b { color: #f0c674; font-weight: 600; }
.stages { display: flex; gap: 10px; }
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
.empty { align-items: center; justify-content: center; }
.none { color: #55555f; }
</style>
