<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { AudioMeta, Storyboard } from '../types'
import { segTable } from '../segtime'

// 多轨时间轴：标尺（可点/拖擦洗）+ 分镜/字幕/配音三轨 + 播放头。
// 数据：分镜 duration_hint；有 audio_meta 时用真实配音时长（+0.35 尾垫，与 MP4 拼接口径一致）
// 和字级时间戳切短语字幕。
// 长片可读：内容层与视口层分离——内容层按「平均每段 ≥88px、最短段 ≥48px」定宽（封顶 3 倍视口），
// 视口出横向滚动条；块定位仍是百分比（相对内容层），三轨/标尺/播放头天然同坐标系。
const props = defineProps<{
  storyboard: Storyboard | null
  audioMeta: AudioMeta | null
  currentTime: number
  duration: number
  pickedIdx: number[]
}>()
const emit = defineEmits<{ seek: [t: number]; pick: [idx: number, key: string]; 'pick-time': [t: number] }>()

const PUNCT = '，。！？；、：,.!?;:…—·'
const hasPunct = (s: string) => [...s].some(ch => PUNCT.includes(ch))

interface SegCol { idx: number; segId: string; key: string; dur: number; start: number }
interface Phrase { idx: number; key: string; start: number; dur: number; text: string }

const segs = computed<SegCol[]>(() => segTable(props.storyboard, props.audioMeta))
const total = computed(() => segs.value.reduce((a, s) => a + s.dur, 0) || 1)

// 短语字幕：按标点收句，超 4.5 秒强收（与管线 tts_align 的断句习惯一致）
const phrases = computed<Phrase[]>(() => {
  const sb = props.storyboard
  if (!sb || !props.audioMeta) return []
  const out: Phrase[] = []
  sb.segments.forEach((s, i) => {
    const v = props.audioMeta?.voices[i]
    if (!v) return
    let cur: { start: number; end: number; text: string } | null = null
    for (const w of v.words ?? []) {
      if (!cur) cur = { start: w.start, end: w.end, text: '' }
      cur.end = w.end
      cur.text += w.text
      if (hasPunct(w.text) || cur.end - cur.start > 4.5) {
        out.push({ idx: s.idx, key: s.key, start: segs.value[i].start + cur.start, dur: Math.max(cur.end - cur.start, 0.1), text: cur.text })
        cur = null
      }
    }
    if (cur) out.push({ idx: s.idx, key: s.key, start: segs.value[i].start + cur.start, dur: Math.max(cur.end - cur.start, 0.1), text: cur.text })
  })
  return out
})

const pct = (t: number) => `${(t / total.value) * 100}%`

// ── 横向滚动：内容层定宽 ─────────────────────────────
const viewportEl = ref<HTMLElement | null>(null)
const viewW = ref(0)
let ro: ResizeObserver | null = null
onMounted(() => {
  ro = new ResizeObserver(entries => {
    for (const e of entries) viewW.value = e.contentRect.width
  })
  if (viewportEl.value) ro.observe(viewportEl.value)
})
onBeforeUnmount(() => ro?.disconnect())

const contentW = computed(() => {
  const w = Math.max(viewW.value, 320)
  const arr = segs.value
  if (!arr.length) return w
  const avg = total.value / arr.length
  const minDur = Math.min(...arr.map(s => s.dur))
  const pps = Math.min(Math.max(w / total.value, 88 / avg, 48 / minDur), (3 * w) / total.value)
  return Math.max(w, total.value * pps)
})

// 播放跟随：播放头滚出可视区时把视口带过去（拖动擦洗时不抢）
watch(() => props.currentTime, () => {
  const vp = viewportEl.value
  if (dragging.value || !vp || contentW.value <= vp.clientWidth) return
  const x = (Math.min(props.currentTime, total.value) / total.value) * contentW.value
  const l = vp.scrollLeft
  if (x < l + 24 || x > l + vp.clientWidth - 24) {
    vp.scrollLeft = Math.min(Math.max(x - vp.clientWidth * 0.35, 0), contentW.value - vp.clientWidth)
  }
})

// 标尺刻度：主刻度间隔从 [1,2,5,10,15,30,60] 里选（约 6~9 个），总长不太长时补 1/5 秒细刻度
const ticks = computed(() => {
  const step = [1, 2, 5, 10, 15, 30, 60].find(s => total.value / s <= 9) ?? 120
  const arr: { t: number; label: string }[] = []
  for (let t = 0; t <= total.value + 1e-6; t += step) arr.push({ t, label: fmt(t) })
  return arr
})
const minorStep = computed(() => {
  const s = [1, 5, 10, 30].find(s => total.value / s <= 120)
  return s && total.value / s > 14 ? s : 0
})
const minors = computed(() => {
  if (!minorStep.value) return []
  const arr: number[] = []
  for (let t = 0; t <= total.value + 1e-6; t += minorStep.value) arr.push(t)
  return arr
})

const fmt = (t: number) => {
  const s = Math.max(0, Math.floor(t))
  return `${String(Math.floor(s / 60)).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}`
}
const playheadPct = computed(() => pct(Math.min(props.currentTime, total.value)))
const activeIdx = computed(() =>
  segs.value.find(s => props.currentTime >= s.start && props.currentTime < s.start + s.dur)?.idx ?? -1)

// 擦洗：播放头手柄拖动 + 标尺点击定位
const tracksEl = ref<HTMLElement | null>(null)
const dragging = ref(false)
const dragT = ref(0)
function tAt(e: { clientX: number }) {
  const r = tracksEl.value!.getBoundingClientRect()
  return Math.min(Math.max((e.clientX - r.left) / r.width, 0), 1) * total.value
}
function onScrubStart(e: PointerEvent) {
  dragging.value = true
  dragT.value = tAt(e)
  ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
  emit('seek', dragT.value)
}
function onScrubMove(e: PointerEvent) {
  if (!dragging.value) return
  dragT.value = tAt(e)
  emit('seek', dragT.value)
}
function onScrubEnd() { dragging.value = false }

function onRulerClick(e: PointerEvent) { emit('seek', tAt(e)) }
function onRulerDblClick(e: MouseEvent) { emit('pick-time', tAt(e)); emit('seek', tAt(e)) }
// 单击 = 只定位预览；Ctrl/Cmd+单击 = 引用该段；双击 = 引用该时刻
function pickSeg(s: SegCol, e?: MouseEvent) {
  emit('seek', s.start + 0.01)
  if (e?.ctrlKey || e?.metaKey) emit('pick', s.idx, s.key)
}
function pickPhrase(p: Phrase, e: MouseEvent) {
  emit('seek', p.start + 0.01)
  if (e.ctrlKey || e.metaKey) emit('pick', p.idx, p.key)
}
// 双击任意轨道 = 引用该时刻
function onLaneDblClick(e: MouseEvent, start: number, dur: number) {
  const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
  const local = Math.min(Math.max((e.clientX - r.left) / r.width, 0), 1) * dur
  emit('pick-time', start + local)
  emit('seek', start + local)
}
</script>

<template>
  <div class="tl">
    <div class="gutter">
      <div class="corner">{{ fmt(dragging ? dragT : currentTime) }}<em>/ {{ fmt(total) }}</em></div>
      <div class="lbl">分镜</div>
      <div v-if="phrases.length" class="lbl">字幕</div>
      <div v-if="audioMeta" class="lbl">配音</div>
    </div>

    <div ref="viewportEl" class="viewport">
      <div ref="tracksEl" class="tracks" :style="{ width: contentW + 'px' }">
      <!-- 标尺：点击定位 / 双击引用该时刻；播放头手柄在此拖动 -->
      <div class="ruler" @pointerdown="onRulerClick" @dblclick="onRulerDblClick">
        <i v-for="m in minors" :key="`m${m}`" class="minor" :style="{ left: pct(m) }" />
        <span v-for="tk in ticks" :key="`t${tk.t}`" class="tick" :style="{ left: pct(tk.t) }">{{ tk.label }}</span>
        <div
          class="handle" :class="{ scrubbing: dragging }"
          :style="{ left: playheadPct }"
          @pointerdown.stop="onScrubStart" @pointermove="onScrubMove" @pointerup="onScrubEnd" @pointercancel="onScrubEnd"
        >
          <span v-if="dragging" class="bubble">{{ fmt(dragT) }}</span>
        </div>
      </div>

      <!-- 分镜轨 -->
      <div class="lane seg">
        <div
          v-for="s in segs" :key="s.idx" class="blk"
          :class="{ on: s.idx === activeIdx, picked: pickedIdx.includes(s.idx) }"
          :style="{ left: pct(s.start), width: pct(s.dur) }"
          :title="`段${s.idx} ${s.key}（Ctrl+点击引用 · 双击引用时刻）`"
          @click="pickSeg(s, $event)"
          @dblclick.stop="onLaneDblClick($event, s.start, s.dur)"
        >
          <b>{{ s.idx }}</b>
          <i>{{ s.key }}</i>
        </div>
      </div>

      <!-- 字幕轨（有对齐数据才出） -->
      <div v-if="phrases.length" class="lane sub">
        <div
          v-for="(p, i) in phrases" :key="i" class="ph"
          :style="{ left: pct(p.start), width: pct(p.dur) }"
          :title="`${p.text}（Ctrl+点击引用）`"
          @click="pickPhrase(p, $event)"
          @dblclick.stop="onLaneDblClick($event, p.start, p.dur)"
        >{{ p.text }}</div>
      </div>

      <!-- 配音轨：块内细条 = 逐字时刻（近似波形） -->
      <div v-if="audioMeta" class="lane aud">
        <div
          v-for="(s, i) in segs" :key="s.idx" class="wav"
          :class="{ on: s.idx === activeIdx }"
          :style="{ left: pct(s.start), width: pct(s.dur) }"
          :title="`段${s.idx} 配音 ${s.dur.toFixed(1)}s（Ctrl+点击引用）`"
          @click="pickSeg(s, $event)"
          @dblclick.stop="onLaneDblClick($event, s.start, s.dur)"
        >
          <i
            v-for="(w, j) in (audioMeta.voices[i]?.words ?? [])" :key="j"
            :style="{
              left: `${(w.start / s.dur) * 100}%`,
              width: `${Math.max(((w.end - w.start) / s.dur) * 100, 1.2)}%`,
              height: `${38 + ((j * 37) % 55)}%`,
            }"
          />
        </div>
      </div>

      <!-- 播放头竖线（贯穿三轨） -->
      <div class="line" :class="{ scrubbing: dragging }" :style="{ left: playheadPct }" />
      </div>
    </div>
  </div>
</template>

<style scoped>
.tl { width: min(100%, 1080px); display: flex; gap: 8px; user-select: none; }
.gutter { flex: none; width: 46px; display: flex; flex-direction: column; }
.corner { height: 20px; font-size: 11px; color: #9a9aa6; font-variant-numeric: tabular-nums; white-space: nowrap; }
.corner em { font-style: normal; color: #55555f; margin-left: 3px; }
.lbl { flex: 1; display: flex; align-items: center; font-size: 10px; color: #55555f; letter-spacing: 2px; }

/* 视口层：长片出横向滚动条（细样式，贴时间轴下方）；内容层定宽保证块可读 */
.viewport {
  flex: 1; min-width: 0; overflow-x: auto; overflow-y: hidden;
  padding-bottom: 9px; /* 给滚动条留位，不压轨道 */
  scrollbar-width: thin; scrollbar-color: #2e2e38 #101016;
}
.viewport::-webkit-scrollbar { height: 8px; }
.viewport::-webkit-scrollbar-track { background: #101016; border-radius: 4px; }
.viewport::-webkit-scrollbar-thumb { background: #2e2e38; border-radius: 4px; }
.viewport::-webkit-scrollbar-thumb:hover { background: #3a3a45; }

.tracks { position: relative; min-width: 100%; display: flex; flex-direction: column; gap: 3px; }

.ruler {
  position: relative; height: 20px; border-radius: 5px; cursor: crosshair;
  background: #14141a; border: 1px solid #232329;
}
.minor { position: absolute; top: 60%; width: 1px; height: 40%; background: #2b2b33; }
.tick { position: absolute; top: 1px; font-size: 9px; color: #6b6b77; transform: translateX(3px); font-variant-numeric: tabular-nums; }

.lane { position: relative; height: 26px; border-radius: 5px; background: #101016; overflow: hidden; }
.lane.seg { height: 30px; }

.blk {
  position: absolute; top: 0; bottom: 0; cursor: pointer; overflow: hidden;
  background: #1c1c23; border: 1px solid #2b2b33; border-radius: 5px;
  display: flex; align-items: center; gap: 5px; padding: 0 6px;
  transition: border-color .15s, background .15s;
}
.blk:hover { border-color: #f0c674; }
.blk.on { background: #241f14; border-color: rgba(240, 198, 116, .55); }
.blk.picked { border-color: #f0c674; background: #2a2416; }
.blk b { font-size: 10px; color: #7c7c88; flex: none; }
.blk i { font-style: normal; font-size: 11px; color: #c3c3cd; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.blk.on i { color: #f0c674; }

.ph {
  position: absolute; top: 0; bottom: 0; cursor: pointer; overflow: hidden; white-space: nowrap;
  background: #14211a; border: 1px solid #2c5c40; border-radius: 5px; color: #9fd8b6;
  font-size: 10.5px; line-height: 24px; padding: 0 5px; text-overflow: ellipsis;
  transition: background .15s;
}
.ph:hover { background: #1b3226; }

.wav {
  position: absolute; top: 0; bottom: 0; cursor: pointer; overflow: hidden;
  background: #121b24; border: 1px solid #2b4a66; border-radius: 5px;
}
.wav.on { border-color: #8fc7ff; }
.wav i {
  position: absolute; top: 50%; transform: translateY(-50%); border-radius: 1px;
  background: #3f6d94; min-width: 1px;
}
.wav.on i { background: #8fc7ff; }

.handle {
  position: absolute; top: -1px; width: 11px; height: 22px; margin-left: -5px; z-index: 3;
  cursor: ew-resize; touch-action: none;
}
.handle::before {
  content: ''; position: absolute; left: 50%; top: 7px; transform: translateX(-50%);
  border: 5px solid transparent; border-top: 7px solid #f0c674; filter: drop-shadow(0 1px 2px #000);
}
.handle:hover::before, .handle.scrubbing::before { border-top-color: #ffe2a0; }
.bubble {
  pointer-events: none; position: absolute; top: -22px; left: 50%; transform: translateX(-50%);
  background: #f0c674; color: #14140f; font-size: 10px; font-weight: 600;
  padding: 1px 6px; border-radius: 4px; white-space: nowrap; font-variant-numeric: tabular-nums;
}
.line {
  position: absolute; top: 20px; bottom: 0; width: 1px; margin-left: -0.5px; z-index: 2;
  background: #f0c674; pointer-events: none;
}
.line.scrubbing { background: #ffe2a0; }
.handle, .line { transition: left .25s linear; }
.handle.scrubbing, .line.scrubbing { transition: none; }
</style>
