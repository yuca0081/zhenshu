<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useMessage } from 'naive-ui'
import type { PrepView, ProjectView } from '../types'
import { api } from '../api'

// 音乐驱动 PV 的「想法」页签（M1 音乐工位）：一句话主题 → 做曲 → 音乐地图，
// 一站三交付（brief/曲/地图）。分镜工位与 PV 制作管线 M2/M3 上线后在此接续；
// 重做是破坏性操作（三件套级联作废），走双击武装确认（与 IdeaPane 同口径）。
const props = defineProps<{ view: ProjectView }>()
const message = useMessage()

const pv = ref<PrepView | null>(null)
const idea = ref('')
const starting = ref(false)
const armed = ref(false)
let armTimer: number | null = null
let poll: number | null = null

const running = computed(() => !!pv.value?.running)
const music = computed(() => pv.value?.music)
const done = computed(() => !!music.value?.map)
const audioUrl = computed(() => `/api/projects/${props.view.id}/music/audio`)

async function refresh() {
  const pp = await api.prep(props.view.id).catch(() => null)
  if (pp) {
    pv.value = pp
    if (!idea.value) idea.value = (pp.idea || '').trim()
  }
  schedule()
}
function schedule() {
  if (poll) { clearTimeout(poll); poll = null }
  if (running.value) poll = window.setTimeout(refresh, 3000)
}
onMounted(refresh)
onBeforeUnmount(() => { if (poll) clearTimeout(poll); if (armTimer) clearTimeout(armTimer) })

async function start(force = false) {
  if (starting.value || running.value) return
  if (!idea.value.trim()) { message.error('先给一句话主题（做什么氛围的曲子）'); return }
  if (force && !armed.value) {
    armed.value = true
    if (armTimer) clearTimeout(armTimer)
    armTimer = window.setTimeout(() => { armed.value = false }, 3000)
    return
  }
  armed.value = false
  starting.value = true
  try {
    await api.runPrep(props.view.id, idea.value.trim(), force)
    await refresh()
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    starting.value = false
  }
}
async function cancel() {
  try { await api.cancelPrep(props.view.id); await refresh() } catch (e) { message.error((e as Error).message) }
}

function energyColor(e: number): string {
  // 能量 1–5 → 强调色透明度梯度（brief 与地图共用的能量弧直观条）
  return `color-mix(in srgb, var(--accent) ${Math.round((0.18 + e * 0.16) * 100)}%, transparent)`
}
function fmtSec(t: number): string {
  return `${t.toFixed(1)}s`
}
</script>

<template>
  <div class="mip">
    <div class="head">
      <h2>音乐驱动 · PV</h2>
      <p class="sub">曲先行——分镜跟着曲子的节拍走。给一句话主题，AI 做一首带段落能量弧的纯音乐整曲＋音乐地图。</p>
    </div>

    <div v-if="running" class="live">
      <span class="live-dot" />做曲中：写 brief → 编曲渲染 → 音乐地图 → 自检环比对
      <button class="ghost" @click="cancel">停止</button>
    </div>

    <div v-else class="composer">
      <textarea
        v-model="idea"
        rows="2"
        placeholder="一句话主题：做什么氛围、什么感觉的曲子（如「午夜城市霓虹，电子感，带爆点副歌」）"
      />
      <div class="row">
        <button class="primary" :disabled="starting" @click="start(done)">
          {{ starting ? '启动中…' : done ? '按新主题重新做曲' : '开始做曲' }}
        </button>
        <span v-if="done && armed" class="arm-hint">再点一次确认重做（当前曲与地图会被作废）</span>
      </div>
    </div>

    <div v-if="done && music" class="result">
      <div class="sec-title">曲 brief（创作意图）</div>
      <p v-if="music.brief_summary" class="brief-line">
        <b>{{ music.brief_summary.key }}</b> 调 · {{ music.brief_summary.bpm }} BPM ·
        {{ music.brief_summary.bars }} 小节 ·
        <span v-for="s in music.brief_summary.sections" :key="s.name" class="chip">
          {{ s.name }}×{{ s.bars }} <i :style="{ background: energyColor(s.energy) }">E{{ s.energy }}</i>
        </span>
      </p>

      <div class="sec-title">整曲试听</div>
      <audio v-if="music.audio" :src="audioUrl" controls preload="none" class="player" />
      <p v-else class="dim">曲子还没落盘。</p>

      <div class="sec-title">音乐地图（分镜只认它）</div>
      <div v-if="music.map" class="map">
        <div v-for="s in music.map.sections" :key="s.name + s.start" class="map-row">
          <span class="map-name">{{ s.name }}</span>
          <span class="map-bar">
            <i :style="{ width: ((s.end - s.start) / (music.map!.duration || 1) * 100) + '%', background: energyColor(s.energy) }" />
          </span>
          <span class="map-time">{{ fmtSec(s.start) }}–{{ fmtSec(s.end) }}</span>
          <span class="map-energy">E{{ s.energy }}</span>
        </div>
      </div>
      <p class="dim">不满意？改主题点「重新做曲」；满意就等分镜工位（M2）接续。</p>
    </div>

    <div v-if="!done" class="note">
      <p class="dim">原型闸已过（切点卡拍 0ms 偏差）；分镜工位与 PV 制作管线在 M2/M3 上线——现在先把曲子做出来听。</p>
    </div>
  </div>
</template>

<style scoped>
.mip { max-width: 680px; margin: 40px auto; padding: 0 24px; display: flex; flex-direction: column; gap: 22px; }
.head h2 { font-size: 20px; letter-spacing: 0.2em; color: var(--ink); margin: 0 0 10px; }
.sub { color: var(--muted); font-size: 12.5px; margin: 0; line-height: 1.7; }

.live { display: flex; align-items: center; gap: 10px; padding: 14px 16px; border: 1px solid color-mix(in srgb, var(--accent) 30%, transparent); color: var(--ink); font-size: 13.5px; background: color-mix(in srgb, var(--accent) 5%, transparent); }
.live-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--accent); animation: pulse 1.2s infinite; }
@keyframes pulse { 50% { opacity: .3; } }

.composer { display: flex; flex-direction: column; gap: 10px; }
.composer textarea { width: 100%; box-sizing: border-box; background: transparent; border: 1px solid var(--line); color: var(--ink); font-size: 13.5px; line-height: 1.6; padding: 10px 12px; resize: vertical; font-family: inherit; }
.composer textarea:focus { outline: none; border-color: var(--accent); }
.row { display: flex; align-items: center; gap: 12px; }
button { cursor: pointer; font-size: 13px; padding: 8px 18px; font-family: inherit; }
button.primary { border: 1px solid var(--accent); background: var(--accent); color: var(--accent-ink); font-weight: 600; letter-spacing: 0.06em; }
button.primary:disabled { opacity: .5; cursor: default; }
button.ghost { background: none; border: 1px solid var(--line); color: var(--muted); padding: 5px 12px; font-size: 12px; }
button.ghost:hover { border-color: var(--accent); color: var(--accent); }
.arm-hint { color: var(--accent); font-size: 12px; }

.result { display: flex; flex-direction: column; gap: 8px; }
.sec-title { font-size: 11px; letter-spacing: 0.18em; text-transform: uppercase; color: var(--muted); margin-top: 10px; }
.brief-line { color: var(--ink); font-size: 13px; line-height: 2.1; margin: 0; }
.brief-line b { color: var(--accent); }
.chip { display: inline-block; border: 1px solid var(--line); padding: 1px 8px; margin-right: 6px; font-size: 12px; color: var(--muted); font-variant-numeric: tabular-nums; }
.chip i { font-style: normal; padding: 0 5px; font-size: 11px; color: var(--accent-ink); margin-left: 4px; }
.player { width: 100%; height: 40px; }

.map { display: flex; flex-direction: column; gap: 5px; }
.map-row { display: grid; grid-template-columns: 72px 1fr 110px 32px; align-items: center; gap: 10px; font-size: 12.5px; color: var(--muted); }
.map-name { color: var(--ink); }
.map-bar { height: 12px; background: color-mix(in srgb, var(--ink) 4%, transparent); overflow: hidden; }
.map-bar i { display: block; height: 100%; }
.map-time { font-variant-numeric: tabular-nums; }
.map-energy { color: var(--accent); text-align: right; font-variant-numeric: tabular-nums; }
.dim { color: var(--muted); font-size: 12px; margin: 0; line-height: 1.7; }

.note { border-left: 2px solid var(--accent); padding: 2px 0 2px 14px; }
</style>
