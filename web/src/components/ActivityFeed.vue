<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import type { ActivityEvent } from '../types'

// 制作事件流：manifest 原始事件 → 人话行，最新的在最上。
// - 每步用时：stage.start/finish 配对算毫秒差（check 可能多轮，带轮次号）
// - 打回：check.repair 把刚收口的检查轮标红「打回」；预审/画面审查的 failed/rollback 同理
// - 终态：produce/rework 的 done/error/cancelled/interrupted，完成行带整轮总用时
const props = defineProps<{ events: ActivityEvent[]; producing: boolean }>()

const evMs = (e: ActivityEvent) => e.ms ?? Date.parse(e.ts)

const stageNames: Record<string, string> = {
  tts: '配音', materials: '素材', compositions: '画面', assemble: '组装',
  check: '质量检查', render: '渲染',
}

interface Row {
  key: number
  state: 'ok' | 'fail' | 'reject' | 'run' | 'info'
  title: string
  timeText: string
  durText?: string
  detail?: string
  minor?: boolean
}

// 正在跑的工位要每秒跳计时：producing 期间开一个 1s 心跳驱动 computed 重算
const now = ref(Date.now())
let timer: number | null = null
watch(() => props.producing, on => {
  if (on && timer == null) timer = window.setInterval(() => { now.value = Date.now() }, 1000)
  if (!on && timer != null) { clearInterval(timer); timer = null }
}, { immediate: true })
onBeforeUnmount(() => { if (timer != null) clearInterval(timer) })

function fmtDur(ms: number): string {
  if (ms < 1000) return '<1秒'
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s}秒`
  return `${Math.floor(s / 60)}分${String(s % 60).padStart(2, '0')}秒`
}
const fmtClock = (ms: number) => {
  const d = new Date(ms)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}
const firstLine = (s: string) => (s || '').split('\n', 1)[0].trim()

const rows = computed<Row[]>(() => {
  const out: Row[] = []
  let key = 0
  // 返回压入的行本身（同一引用）：check.repair 要回溯改它的 state
  const add = (r: Omit<Row, 'key'>) => {
    const row: Row = { ...r, key: key++ }
    out.push(row)
    return row
  }
  const rounds: Record<string, number> = {}
  const open: Record<string, { ms: number }> = {}
  let lastCheck: Row | null = null // check.repair 回溯标记「打回」的对象
  let roundStart = 0 // 本轮制作/重做的起点（终态行算总用时）

  // 终态时收口所有还开着的工位：done→绿色补时长；失败/取消/中断→红色，
  // 时长算到终态为止——不能让被打断的工位永远显示"运行中"。
  const flushOpen = (state: Row['state'], atMs: number) => {
    for (const stage of Object.keys(open)) {
      const st = open[stage]
      const n = rounds[stage] ?? 1
      add({
        state, title: stageNames[stage] + (n > 1 ? `（第${n}轮）` : ''),
        timeText: fmtClock(st.ms), durText: fmtDur(Math.max(0, atMs - st.ms)),
      })
    }
    for (const k of Object.keys(open)) delete open[k]
    lastCheck = null
  }
  // 新一轮开工：轮次号归零（重做里的检查是新一轮的第1轮，不背上一轮的账）
  const resetRun = () => {
    for (const k of Object.keys(open)) delete open[k]
    for (const k of Object.keys(rounds)) delete rounds[k]
    lastCheck = null
  }

  for (const ev of props.events) {
    const ms = evMs(ev)
    const t = fmtClock(ms)
    switch (ev.event) {
      case 'produce.start':
        resetRun()
        roundStart = ms
        add({ state: 'info', title: '▶ 开工制作', timeText: t })
        break
      case 'rework.start': {
        resetRun()
        roundStart = ms
        const i = ev.detail.indexOf(': ')
        const segs = i >= 0 ? ev.detail.slice(0, i) : ev.detail
        add({ state: 'info', title: `▶ 段级重做开工（${segs}）`, timeText: t, detail: i >= 0 ? ev.detail.slice(i + 2) : '' })
        break
      }
      case 'produce.done':
        flushOpen('ok', ms)
        add({
          state: 'ok', title: '✅ 全片制作完成', timeText: t,
          durText: roundStart ? fmtDur(ms - roundStart) : undefined,
        })
        break
      case 'rework.done':
        flushOpen('ok', ms)
        add({
          state: 'ok', title: `✅ 重做完成（${ev.detail}）`, timeText: t,
          durText: roundStart ? fmtDur(ms - roundStart) : undefined,
        })
        break
      case 'produce.error':
        flushOpen('fail', ms)
        add({ state: 'fail', title: '✗ 制作失败', timeText: t, detail: firstLine(ev.detail) })
        break
      case 'rework.error':
        flushOpen('fail', ms)
        add({ state: 'fail', title: '✗ 重做失败', timeText: t, detail: firstLine(ev.detail) })
        break
      case 'produce.cancelled':
        flushOpen('fail', ms)
        add({ state: 'info', title: '⏸ 已手动取消', timeText: t, detail: ev.detail })
        break
      case 'produce.interrupted':
        flushOpen('fail', ms)
        add({ state: 'info', title: '⏸ 服务重启，制作中断', timeText: t })
        break
      case 'rework.cancelled':
        flushOpen('fail', ms)
        add({ state: 'info', title: '⏸ 重做已取消', timeText: t })
        break
      case 'rework.interrupted':
        flushOpen('fail', ms)
        add({ state: 'info', title: '⏸ 服务重启，重做中断', timeText: t })
        break
      case 'produce.retry':
      case 'rework.retry':
        // 真失败自动重跑：死掉的工位按失败收口，重跑行说明流程自己在接着跑
        flushOpen('fail', ms)
        add({
          state: 'info', timeText: t, detail: firstLine(ev.detail),
          title: ev.event === 'produce.retry' ? '↻ 上次尝试失败，自动重跑' : '↻ 重做失败，自动重跑',
        })
        break
      case 'stage.start':
        if (!stageNames[ev.detail]) break
        open[ev.detail] = { ms }
        rounds[ev.detail] = (rounds[ev.detail] ?? 0) + 1
        if (ev.detail === 'check') lastCheck = null
        break
      case 'stage.finish': {
        const st = open[ev.detail]
        if (!stageNames[ev.detail] || !st) break
        delete open[ev.detail]
        const n = rounds[ev.detail]
        const row = add({
          state: 'ok',
          title: stageNames[ev.detail] + (n > 1 ? `（第${n}轮）` : ''),
          timeText: t, durText: fmtDur(Math.max(0, ms - st.ms)),
        })
        if (ev.detail === 'check') lastCheck = row
        break
      }
      case 'check.repair':
        if (lastCheck) lastCheck.state = 'reject'
        add({ state: 'reject', title: '检查打回：画面回炉重出', timeText: t, detail: firstLine(ev.detail) })
        break
      case 'check.contrast_fix':
        add({ state: 'info', title: '对比度自动回填', timeText: t, detail: ev.detail, minor: true })
        break
      case 'prereview.start':
        add({ state: 'info', title: '渲染前预审：' + ev.detail, timeText: t })
        break
      case 'prereview.pass':
        add({ state: 'ok', title: '预审通过', timeText: t, detail: ev.detail, minor: true })
        break
      case 'prereview.failed':
        add({ state: 'reject', title: '预审未过', timeText: t, detail: firstLine(ev.detail) })
        break
      case 'prereview.repair':
        add({ state: 'reject', title: `预审打回：重出 ${ev.detail}`, timeText: t })
        break
      case 'prereview.regen_error':
        add({ state: 'fail', title: '预审重出失败', timeText: t, detail: firstLine(ev.detail) })
        break
      case 'frameqa.start':
        add({ state: 'info', title: `画面审查：${ev.detail || '全片'}`, timeText: t })
        break
      case 'frameqa.pass':
        add({ state: 'ok', title: '画面审查通过', timeText: t, minor: true })
        break
      case 'frameqa.failed':
        add({ state: 'reject', title: '画面审查未过', timeText: t, detail: firstLine(ev.detail) })
        break
      case 'frameqa.rollback':
        add({ state: 'reject', title: '审查打回：回滚重出', timeText: t, detail: ev.detail })
        break
      case 'frameqa.skipped_by_prereview':
        add({ state: 'info', title: '预审全过，渲后免审', timeText: t, minor: true })
        break
      case 'render.seg':
        add({ state: 'info', title: `${ev.detail} 画面渲好`, timeText: t, minor: true })
        break
      case 'film.ready':
        add({ state: 'ok', title: '成片终装完成（后期/配乐）', timeText: t, minor: true })
        break
      case 'materials.result':
        add({ state: 'info', title: '素材入库 ' + ev.detail, timeText: t, minor: true })
        break
      default:
        break // llm.usage / *.api / stage.done 等技术性事件不上屏
    }
  }

  // 还没收口的工位 = 正在跑，实时计时（读 now.value，心跳驱动逐秒刷新）
  for (const stage of Object.keys(open)) {
    const st = open[stage]
    const n = rounds[stage] ?? 1
    add({
      state: 'run',
      title: stageNames[stage] + (n > 1 ? `（第${n}轮）` : '') + '…',
      timeText: fmtClock(st.ms),
      durText: fmtDur(Math.max(0, now.value - st.ms)),
    })
  }
  return out.reverse()
})
</script>

<template>
  <div class="feed">
    <div class="feed-head">
      制作事件流
      <span v-if="producing" class="feed-live"><i class="live-dot" />制作中</span>
    </div>
    <div v-if="rows.length" class="feed-list">
      <div v-for="r in rows" :key="r.key" class="row" :data-state="r.state" :data-minor="r.minor ? '1' : undefined">
        <span class="dot" />
        <span class="row-time">{{ r.timeText }}</span>
        <span class="row-main">
          <span class="row-title">{{ r.title }}</span>
          <span v-if="r.durText" class="row-dur">{{ r.durText }}</span>
          <span v-if="r.detail" class="row-detail">{{ r.detail }}</span>
        </span>
      </div>
    </div>
    <div v-else class="feed-empty">还没有制作记录——开工后这里会逐步记下每个工位的用时和打回情况。</div>
  </div>
</template>

<style scoped>
.feed { display: flex; flex-direction: column; min-height: 0; }
.feed-head {
  flex: none; display: flex; align-items: center; gap: 10px;
  font-weight: 700; font-size: 13px; color: #d9d9e0; letter-spacing: 1px;
  padding: 12px 16px 10px; border-bottom: 1px solid #232329;
}
.feed-live { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; font-weight: 400; color: #8fc7ff; }
.live-dot { width: 7px; height: 7px; border-radius: 50%; background: #8fc7ff; animation: pulse 1.2s infinite; }
.feed-list { overflow-y: auto; padding: 6px 8px 12px; }
.feed-empty { padding: 18px 16px; color: #55555f; font-size: 12.5px; line-height: 1.8; }

.row { display: flex; align-items: flex-start; gap: 9px; padding: 6px 8px; border-radius: 8px; }
.row:hover { background: #191920; }
.dot { width: 7px; height: 7px; border-radius: 50%; background: #3a3a45; flex: none; margin-top: 5px; }
.row[data-state="ok"] .dot { background: #7ee2a8; }
.row[data-state="run"] .dot { background: #8fc7ff; animation: pulse 1.2s infinite; }
.row[data-state="fail"] .dot, .row[data-state="reject"] .dot { background: #ff9d9d; }
.row[data-state="info"] .dot { background: #6a6a78; }
.row[data-minor] { opacity: .62; }

.row-time { flex: none; font-size: 11px; color: #5d5d6a; font-variant-numeric: tabular-nums; margin-top: 2px; width: 56px; }
.row-main { flex: 1; min-width: 0; display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 8px; }
.row-title { font-size: 12.5px; color: #c9c9d3; }
.row[data-state="reject"] .row-title { color: #ffb3a3; }
.row[data-state="fail"] .row-title { color: #ff9d9d; }
.row[data-state="run"] .row-title { color: #8fc7ff; }
.row-dur {
  font-size: 11px; color: #7ee2a8; background: rgba(126, 226, 168, .08);
  border: 1px solid rgba(126, 226, 168, .22); border-radius: 999px; padding: 0 8px;
  font-variant-numeric: tabular-nums;
}
.row[data-state="run"] .row-dur { color: #8fc7ff; background: rgba(143, 199, 255, .08); border-color: rgba(143, 199, 255, .25); }
.row[data-state="reject"] .row-dur { color: #f0c674; background: rgba(240, 198, 116, .07); border-color: rgba(240, 198, 116, .25); }
.row-detail {
  flex-basis: 100%; font-size: 11.5px; color: #8a8a96; line-height: 1.6;
  overflow: hidden; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical;
}
@keyframes pulse { 50% { opacity: .35; } }
</style>
