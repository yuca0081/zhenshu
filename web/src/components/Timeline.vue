<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { NDropdown, useMessage } from 'naive-ui'
import type { DropdownOption } from 'naive-ui'
import type { AlignWord, AudioMeta, BgmInfo, BgmTrack, Storyboard } from '../types'
import { segTable, type SegCol } from '../segtime'
import { api } from '../api'
import RIcon from './RIcon.vue'

// 多轨时间轴：标尺（可点/拖擦洗）+ 分镜/字幕/配音/配乐四轨 + 播放头。
// 数据：分镜 duration_hint；有 audio_meta 时用真实配音时长（+0.35 尾垫，与 MP4 拼接口径一致）
// 和字级时间戳切短语字幕。
// 配乐控制集成在配乐轨上（2026-10-30 从视频页右栏移入）：曲名/理由随块展示，
// 轨头块内嵌试听与换曲下拉（曲库懒加载，含「关配乐」）。
// 长片可读：内容层与视口层分离——内容层按「平均每段 ≥88px、最短段 ≥48px」定宽（封顶 3 倍视口），
// 视口出横向滚动条；块定位仍是百分比（相对内容层），三轨/标尺/播放头天然同坐标系。
const props = defineProps<{
  storyboard: Storyboard | null
  audioMeta: AudioMeta | null
  bgm: BgmInfo | null
  videoId: string
  currentTime: number
  pickedIdx: number[]
}>()
const emit = defineEmits<{
  seek: [t: number]
  pick: [idx: number, key: string]
  'pick-time': [t: number]
  'bgm-applied': [remixed: boolean]
}>()
const message = useMessage()

const PUNCT = '，。！？；、：,.!?;:…—·'
const hasPunct = (s: string) => [...s].some(ch => PUNCT.includes(ch))

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

// ── 配乐控制（从视频页右栏移入，2026-10-30）：当前曲 + 试听 + 换曲/关闭 ──
// 曲库懒加载（首次展开换曲菜单才拉）；换曲=后端 StitchNow 重混，画面不动、
// 音轨变——remixed 时父级换播放器 URL 重载，并刷新 view 让曲名/理由即时对齐。
const bgmLib = ref<BgmTrack[] | null>(null)
const bgmBusy = ref(false)
const bgmDur = computed(() =>
  bgmLib.value?.find(t => t.name === props.bgm?.name)?.duration ?? 0)
function loadBgmLib() {
  if (!bgmLib.value) api.bgmLibrary().then(l => (bgmLib.value = l)).catch(() => {})
}
const bgmMenu = computed<DropdownOption[]>(() => {
  const cur = props.bgm?.name
  const opts: DropdownOption[] = []
  // 当前曲不在清单（被移库）也兜出选项，避免菜单里看不到现状
  if (cur && !bgmLib.value?.some(t => t.name === cur)) opts.push({ label: `${cur}（当前）`, key: cur })
  for (const t of bgmLib.value ?? []) opts.push({ label: `${t.name} · ${t.mood}`, key: t.name })
  if (cur) opts.push({ type: 'divider', key: 'd' }, { label: '关配乐', key: 'off' })
  return opts
})
const auditionEl = ref<HTMLAudioElement | null>(null)
const auditioning = ref(false)
function toggleAudition() {
  const name = props.bgm?.name
  if (!name) return
  if (auditioning.value && auditionEl.value) {
    auditionEl.value.pause()
    return
  }
  auditionEl.value?.pause()
  const el = new Audio(api.bgmAudioURL(name))
  auditionEl.value = el
  auditioning.value = true
  void el.play()
  el.onended = () => { auditioning.value = false }
}
onBeforeUnmount(() => { auditionEl.value?.pause(); auditionEl.value = null })
async function applyBGM(track: string) {
  if (bgmBusy.value) return
  bgmBusy.value = true
  try {
    const r = await api.setBGM(props.videoId, track)
    if (r.error) message.error(r.error)
    else message.success(r.message ?? '配乐已更新')
    emit('bgm-applied', !!r.remixed)
  } catch (e) {
    message.error(String(e))
  } finally {
    bgmBusy.value = false
  }
}

// 配乐轨：按曲库时长铺循环块（时长未知铺一整条）；头块带曲名与控制，理由进 tooltip
const bgmBlocks = computed(() => {
  const b = props.bgm
  if (!b?.name) return []
  const dur = bgmDur.value > 0 ? bgmDur.value : total.value
  const out: { start: number; dur: number; first: boolean }[] = []
  for (let t = 0; t < total.value - 1e-6; t += dur) {
    out.push({ start: t, dur: Math.min(dur, total.value - t), first: out.length === 0 })
  }
  return out
})

// 波形近似（配音轨细条 = 逐字时刻）：逐字 DOM 无上限（10 分钟片约 2000 节点），
// 按固定步长采样封顶 ~500/段。绝对定位用采样词自身的 start——稀疏化只降低密度，
// 不改布局（不按采样序号重排，避免条形位置漂移）
const WAVE_MAX_NODES = 500
const waveWords = computed<AlignWord[][]>(() =>
  segs.value.map((_, i) => {
    const words = props.audioMeta?.voices[i]?.words ?? []
    const stride = Math.max(1, Math.ceil(words.length / WAVE_MAX_NODES))
    return stride === 1 ? words : words.filter((_, k) => k % stride === 0)
  }))

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
      <div class="lbl">配乐</div>
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

      <!-- 字幕轨（有对齐数据才出）：key 用短语起始时间（index 会随断句变化错位复用） -->
      <div v-if="phrases.length" class="lane sub">
        <div
          v-for="p in phrases" :key="p.start" class="ph"
          :style="{ left: pct(p.start), width: pct(p.dur) }"
          :title="`${p.text}（Ctrl+点击引用）`"
          @click="pickPhrase(p, $event)"
          @dblclick.stop="onLaneDblClick($event, p.start, p.dur)"
        >{{ p.text }}</div>
      </div>

      <!-- 配音轨：块内细条 = 逐字时刻（近似波形，采样封顶见 waveWords） -->
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
            v-for="w in waveWords[i] ?? []" :key="w.start"
            :style="{
              left: `${(w.start / s.dur) * 100}%`,
              width: `${Math.max(((w.end - w.start) / s.dur) * 100, 1.2)}%`,
              height: `${38 + ((w.start * 37) % 55)}%`,
            }"
          />
        </div>
      </div>

      <!-- 配乐轨：循环铺底（块=曲库时长），点击定位；理由在 tooltip。
           控制集成在头块内（试听 + 换曲下拉含「关配乐」）；无配乐时出虚线占位块提供选曲入口 -->
      <div class="lane bgm">
        <template v-if="bgmBlocks.length">
          <div
            v-for="(b, i) in bgmBlocks" :key="i" class="bgmblk"
            :class="{ head: b.first, alt: i % 2 === 1 }"
            :style="{ left: pct(b.start), width: pct(b.dur) }"
            :title="`♪ ${bgm!.name}（${bgmDur > 0 ? `曲长 ${bgmDur.toFixed(0)}s 循环铺底` : '铺底'}${bgm!.reason ? '；选曲理由：' + bgm!.reason : ''}）`"
            @click="emit('seek', b.start + 0.01)"
          >
            <template v-if="b.first">
              <span class="note">♪</span><span class="bgm-name">{{ bgm!.name }}</span>
              <span class="bgm-ctl" @click.stop>
                <button
                  class="mini" :disabled="bgmBusy"
                  :title="auditioning ? '停止试听' : '试听当前配乐'"
                  @click="toggleAudition"
                ><RIcon :name="auditioning ? 'stop' : 'play'" :size="10" /></button>
                <NDropdown trigger="click" :options="bgmMenu" @select="applyBGM" @update:show="(s: boolean) => s && loadBgmLib()">
                  <button class="mini" :disabled="bgmBusy" title="换曲 / 关配乐">
                    <RIcon name="music" :size="10" />
                  </button>
                </NDropdown>
              </span>
            </template>
            <template v-else>♪</template>
          </div>
        </template>
        <div v-else class="bgmblk none" :style="{ left: pct(0), width: '100%' }" title="还没有配乐——点「选配乐」从曲库挑一首">
          <span class="note">♪</span>无配乐
          <span class="bgm-ctl" @click.stop>
            <NDropdown trigger="click" :options="bgmMenu" @select="applyBGM" @update:show="(s: boolean) => s && loadBgmLib()">
              <button class="mini" :disabled="bgmBusy"><RIcon name="plus" :size="10" />选配乐</button>
            </NDropdown>
          </span>
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
.corner { height: 20px; font-size: 11px; color: var(--muted); font-variant-numeric: tabular-nums; white-space: nowrap; }
.corner em { font-style: normal; color: var(--muted); opacity: .6; margin-left: 3px; }
.lbl { flex: 1; display: flex; align-items: center; font-size: 10px; color: var(--muted); opacity: .7; letter-spacing: 0.18em; }

/* 视口层：长片出横向滚动条（细样式，贴时间轴下方）；内容层定宽保证块可读 */
.viewport {
  flex: 1; min-width: 0; overflow-x: auto; overflow-y: hidden;
  padding-bottom: 9px; /* 给滚动条留位，不压轨道 */
  scrollbar-width: thin; scrollbar-color: var(--line) transparent;
}
.viewport::-webkit-scrollbar { height: 8px; }
.viewport::-webkit-scrollbar-track { background: transparent; }
.viewport::-webkit-scrollbar-thumb { background: var(--line); }
.viewport::-webkit-scrollbar-thumb:hover { background: var(--muted); }

.tracks { position: relative; min-width: 100%; display: flex; flex-direction: column; gap: 3px; }

.ruler {
  position: relative; height: 20px; cursor: crosshair;
  background: var(--surface); border: 1px solid var(--line-soft);
}
.minor { position: absolute; top: 60%; width: 1px; height: 40%; background: var(--line-soft); }
.tick { position: absolute; top: 1px; font-size: 9px; color: var(--muted); transform: translateX(3px); font-variant-numeric: tabular-nums; }

.lane { position: relative; height: 26px; background: color-mix(in srgb, var(--ink) 3%, transparent); overflow: hidden; }
.lane.seg { height: 30px; }

.blk {
  position: absolute; top: 0; bottom: 0; cursor: pointer; overflow: hidden;
  background: color-mix(in srgb, var(--ink) 6%, var(--surface)); border: 1px solid var(--line-soft);
  display: flex; align-items: center; gap: 5px; padding: 0 6px;
  transition: border-color var(--dur-1) var(--ease), background var(--dur-1) var(--ease);
}
.blk:hover { border-color: var(--accent); }
.blk.on { background: color-mix(in srgb, var(--accent) 14%, var(--surface)); border-color: color-mix(in srgb, var(--accent) 55%, transparent); }
.blk.picked { border-color: var(--accent); background: color-mix(in srgb, var(--accent) 20%, var(--surface)); }
.blk b { font-size: 10px; color: var(--muted); flex: none; font-variant-numeric: tabular-nums; }
.blk i { font-style: normal; font-size: 11px; color: var(--ink); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.blk.on i { color: var(--accent); }

.ph {
  position: absolute; top: 0; bottom: 0; cursor: pointer; overflow: hidden; white-space: nowrap;
  background: color-mix(in srgb, var(--ok) 12%, transparent); border: 1px solid color-mix(in srgb, var(--ok) 35%, transparent);
  color: var(--ok);
  font-size: 10.5px; line-height: 24px; padding: 0 5px; text-overflow: ellipsis;
  transition: background var(--dur-1) var(--ease);
}
.ph:hover { background: color-mix(in srgb, var(--ok) 20%, transparent); }

.wav {
  position: absolute; top: 0; bottom: 0; cursor: pointer; overflow: hidden;
  background: color-mix(in srgb, var(--info) 8%, transparent); border: 1px solid color-mix(in srgb, var(--info) 30%, transparent);
}
.wav.on { border-color: var(--info); }
.wav i {
  position: absolute; top: 50%; transform: translateY(-50%);
  background: color-mix(in srgb, var(--info) 55%, transparent); min-width: 1px;
}
.wav.on i { background: var(--info); }

/* 配乐轨：中性灰调区分配音蓝调；head 块带曲名+内嵌控制（试听/换曲），后续循环块淡化只留 ♪ */
.bgmblk {
  position: absolute; top: 0; bottom: 0; cursor: pointer; overflow: hidden; white-space: nowrap;
  display: flex; align-items: center; gap: 4px; padding: 0 6px;
  background: color-mix(in srgb, var(--ink) 6%, transparent); border: 1px solid var(--line-soft);
  color: var(--muted); font-size: 10.5px; text-overflow: ellipsis;
  transition: border-color var(--dur-1) var(--ease);
}
.bgmblk:hover { border-color: var(--muted); }
.bgmblk.alt { opacity: .62; }
.bgmblk.head { z-index: 1; min-width: 170px; }
.bgmblk.none { border-style: dashed; }
.bgmblk .note { font-size: 10px; color: var(--accent); }
.bgm-name { overflow: hidden; text-overflow: ellipsis; }
.bgm-ctl { display: inline-flex; align-items: center; gap: 3px; margin-left: auto; flex: none; }
.mini {
  display: inline-flex; align-items: center; gap: 3px; cursor: pointer; flex: none;
  border: 1px solid var(--line-soft); background: transparent; color: var(--muted);
  font-size: 10px; font-family: inherit; line-height: 1.3; padding: 1px 5px;
  transition: color var(--dur-1) var(--ease), border-color var(--dur-1) var(--ease);
}
.mini:hover { color: var(--accent); border-color: var(--accent); }
.mini:disabled { opacity: .55; cursor: default; }

.handle {
  position: absolute; top: -1px; width: 11px; height: 22px; margin-left: -5px; z-index: 3;
  cursor: ew-resize; touch-action: none;
}
.handle::before {
  content: ''; position: absolute; left: 50%; top: 7px; transform: translateX(-50%);
  border: 5px solid transparent; border-top: 7px solid var(--accent); filter: drop-shadow(0 1px 2px rgb(0 0 0 / 0.4));
}
.handle:hover::before, .handle.scrubbing::before { border-top-color: var(--accent-hover); }
.bubble {
  pointer-events: none; position: absolute; top: -22px; left: 50%; transform: translateX(-50%);
  background: var(--accent); color: var(--accent-ink); font-size: 10px; font-weight: 600;
  padding: 1px 6px; white-space: nowrap; font-variant-numeric: tabular-nums;
}
.line {
  position: absolute; top: 20px; bottom: 0; width: 1px; margin-left: -0.5px; z-index: 2;
  background: var(--accent); pointer-events: none;
}
.line.scrubbing { background: var(--accent-hover); }
.handle, .line { transition: left .25s linear; }
.handle.scrubbing, .line.scrubbing { transition: none; }
</style>
