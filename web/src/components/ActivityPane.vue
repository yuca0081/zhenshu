<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import type { ActivityEvent, TranscriptMsg } from '../types'
import { api } from '../api'
import { stageLabel } from '../format'

// 「过程」页（2026-10-18 替代旧事件流弹窗）：manifest 事件 → 两条线——
// ① 普通事件行（开工/工位用时/打回/终态，人话翻译沿用原 ActivityFeed 口径）；
// ② 智能体任务卡（agent.start/agent.done 聚合）：标题+用时+token 账，
//    点开懒加载对话留痕（GET /transcript），运行中的卡片每 5 秒刷新看它干活。
// 卡片身份键 = task#第几次开工：重跑/冷重试同任务号会开新卡片，展开态不串卡。

const props = defineProps<{ projectId: string; events: ActivityEvent[]; producing: boolean }>()

const evMs = (e: ActivityEvent) => e.ms ?? Date.parse(e.ts)
const knownStage = (s: string) => stageLabel(s) !== s

interface PlainRow {
  kind: 'plain'
  key: string
  state: 'ok' | 'fail' | 'reject' | 'run' | 'info'
  title: string
  timeText: string
  durText?: string
  detail?: string
  minor?: boolean
}
interface TaskGroup {
  kind: 'task'
  key: string // task#n，n = 同任务号第几次开工
  task: string
  label: string
  startMs: number
  doneMs?: number
  state: 'run' | 'ok' | 'fail'
  promptTokens: number
  completionTokens: number
  calls: number
}
type Item = PlainRow | TaskGroup

// 正在跑的工位/任务要跳计时：producing 期间心跳驱动重算（5s 粒度，理由同旧面板）
const now = ref(Date.now())
let timer: number | null = null

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
const shortTime = (ts?: string) => (ts ? fmtClock(Date.parse(ts) || 0) : '')
const firstLine = (s: string) => (s || '').split('\n', 1)[0].trim()
const fmtTok = (n: number) => (n >= 10000 ? (n / 10000).toFixed(1).replace(/\.0$/, '') + '万' : n.toLocaleString())

const items = computed<Item[]>(() => {
  const out: Item[] = []
  let pk = 0
  const occ: Record<string, number> = {}
  const openTasks: TaskGroup[] = []
  const add = (r: Omit<PlainRow, 'kind' | 'key'>) => {
    out.push({ kind: 'plain', key: `p${pk++}`, ...r })
  }
  const rounds: Record<string, number> = {}
  const open: Record<string, { ms: number }> = {}
  let lastCheck: PlainRow | null = null
  let roundStart = 0

  const flushOpen = (state: PlainRow['state'], atMs: number) => {
    for (const stage of Object.keys(open)) {
      const st = open[stage]
      const n = rounds[stage] ?? 1
      add({
        state, title: stageLabel(stage) + (n > 1 ? `（第${n}轮）` : ''),
        timeText: fmtClock(st.ms), durText: fmtDur(Math.max(0, atMs - st.ms)),
      })
    }
    for (const k of Object.keys(open)) delete open[k]
    lastCheck = null
  }
  const resetRun = () => {
    for (const k of Object.keys(open)) delete open[k]
    for (const k of Object.keys(rounds)) delete rounds[k]
    lastCheck = null
  }

  for (const ev of props.events) {
    const ms = evMs(ev)
    const t = fmtClock(ms)
    // ── 智能体任务卡（优先于普通行翻译）──
    if (ev.event === 'agent.start' && ev.task) {
      const n = (occ[ev.task] ?? 0) + 1
      occ[ev.task] = n
      openTasks.push({
        kind: 'task', key: `${ev.task}#${n}`, task: ev.task,
        label: ev.label || ev.task, startMs: ms, state: 'run',
        promptTokens: 0, completionTokens: 0, calls: 0,
      })
      out.push(openTasks[openTasks.length - 1])
      continue
    }
    if (ev.event === 'agent.done' && ev.task) {
      const g = [...openTasks].reverse().find(x => x.task === ev.task && x.state === 'run')
      if (g) {
        g.doneMs = ms
        g.state = ev.state === 'fail' ? 'fail' : 'ok'
        g.promptTokens = ev.prompt_tokens || 0
        g.completionTokens = ev.completion_tokens || 0
        g.calls = ev.calls || 0
      }
      continue
    }
    // ── 普通事件行 ──
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
        add({ state: 'ok', title: '✅ 全片制作完成', timeText: t, durText: roundStart ? fmtDur(ms - roundStart) : undefined })
        break
      case 'rework.done':
        flushOpen('ok', ms)
        add({ state: 'ok', title: `✅ 重做完成（${ev.detail}）`, timeText: t, durText: roundStart ? fmtDur(ms - roundStart) : undefined })
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
        flushOpen('fail', ms)
        add({
          state: 'info', timeText: t, detail: firstLine(ev.detail),
          title: ev.event === 'produce.retry' ? '↻ 上次尝试失败，自动重跑' : '↻ 重做失败，自动重跑',
        })
        break
      case 'stage.start':
        if (!knownStage(ev.detail)) break
        open[ev.detail] = { ms }
        rounds[ev.detail] = (rounds[ev.detail] ?? 0) + 1
        if (ev.detail === 'check') lastCheck = null
        break
      case 'stage.finish': {
        const st = open[ev.detail]
        if (!knownStage(ev.detail) || !st) break
        delete open[ev.detail]
        const n = rounds[ev.detail]
        const row: PlainRow = {
          kind: 'plain', key: `p${pk++}`, state: 'ok',
          title: stageLabel(ev.detail) + (n > 1 ? `（第${n}轮）` : ''),
          timeText: t, durText: fmtDur(Math.max(0, ms - st.ms)),
        }
        out.push(row)
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
      default:
        break // llm.usage / seg.* / stage.done 等技术性事件不上屏
    }
  }

  // 还没收口的工位 = 正在跑，实时计时；还开着的任务卡保持 run 态
  for (const stage of Object.keys(open)) {
    const st = open[stage]
    const n = rounds[stage] ?? 1
    add({
      state: 'run',
      title: stageLabel(stage) + (n > 1 ? `（第${n}轮）` : '') + '…',
      timeText: fmtClock(st.ms),
      durText: fmtDur(Math.max(0, now.value - st.ms)),
    })
  }
  return out
})

// 最新在最上
const itemsReversed = computed(() => [...items.value].reverse())

// ── 本轮合计：最近一次开工（produce/rework）之后的全部 agent.done ──
const totals = computed(() => {
  let start = -1
  props.events.forEach((ev, i) => {
    if (ev.event === 'produce.start' || ev.event === 'rework.start') start = i
  })
  const t = { p: 0, c: 0, n: 0 }
  for (let i = start + 1; i < props.events.length; i++) {
    const ev = props.events[i]
    if (ev.event === 'agent.done') {
      t.p += ev.prompt_tokens || 0
      t.c += ev.completion_tokens || 0
      t.n += ev.calls || 0
    }
  }
  return t
})

// ── 对话留痕：懒加载 + 运行中 5s 刷新 ────────────────────────────
const openKeys = ref<Record<string, boolean>>({})
const msgCache = ref<Record<string, TranscriptMsg[]>>({})
const loading = ref<Record<string, boolean>>({})
const loadErr = ref<Record<string, string>>({})

async function loadMsgs(g: TaskGroup) {
  if (loading.value[g.key]) return
  loading.value = { ...loading.value, [g.key]: true }
  try {
    const msgs = await api.transcript(props.projectId, g.task)
    msgCache.value = { ...msgCache.value, [g.key]: msgs }
    loadErr.value = { ...loadErr.value, [g.key]: '' }
  } catch (e) {
    loadErr.value = { ...loadErr.value, [g.key]: (e as Error).message }
  } finally {
    loading.value = { ...loading.value, [g.key]: false }
  }
}

async function toggle(g: TaskGroup) {
  openKeys.value = { ...openKeys.value, [g.key]: !openKeys.value[g.key] }
  if (openKeys.value[g.key] && !msgCache.value[g.key]) await loadMsgs(g)
}

function onTick() {
  now.value = Date.now()
  for (const it of items.value) {
    if (it.kind === 'task' && openKeys.value[it.key] && it.state === 'run') loadMsgs(it)
  }
}

watch(() => props.producing, on => {
  if (on && timer == null) timer = window.setInterval(onTick, 5000)
  if (!on && timer != null) { clearInterval(timer); timer = null }
}, { immediate: true })
onBeforeUnmount(() => { if (timer != null) clearInterval(timer) })

// ── 消息渲染：超长内容默认折叠成预览（600 字），短内容直接全显不给按钮 ──
const closedMap = ref<Record<string, boolean>>({})
const msgLong = 600
const argsLong = 240
const isOpenKey = (k: string, defOpen: boolean) => closedMap.value[k] ?? defOpen
const toggleKey = (k: string, defOpen: boolean) => {
  closedMap.value = { ...closedMap.value, [k]: !isOpenKey(k, defOpen) }
}
const roleLabel = (m: TranscriptMsg) => {
  if (m.role === 'system') return '系统提示'
  if (m.role === 'user') return '用户'
  if (m.role === 'assistant') return '模型'
  if (m.role === 'note') return '备注'
  return `工具·${m.name || m.tool_call_id || ''}`
}
const clampText = (s: string, n: number) => (s.length > n ? s.slice(0, n) + '…' : s)
</script>

<template>
  <div class="ap">
    <div class="ap-head">
      制作过程
      <span v-if="producing" class="ap-live"><i class="live-dot" />制作中</span>
      <span v-if="totals.n" class="ap-totals" title="最近一次开工以来全部智能体任务的 token 消耗合计">
        本轮模型消耗：输入 {{ fmtTok(totals.p) }} · 输出 {{ fmtTok(totals.c) }} tokens · {{ totals.n }} 次调用
      </span>
    </div>
    <div v-if="itemsReversed.length" class="ap-scroll">
      <template v-for="it in itemsReversed" :key="it.key">
        <!-- 普通事件行 -->
        <div v-if="it.kind === 'plain'" class="row" :data-state="it.state" :data-minor="it.minor ? '1' : undefined">
          <span class="dot" />
          <span class="row-time">{{ it.timeText }}</span>
          <span class="row-main">
            <span class="row-title">{{ it.title }}</span>
            <span v-if="it.durText" class="row-dur">{{ it.durText }}</span>
            <span v-if="it.detail" class="row-detail">{{ it.detail }}</span>
          </span>
        </div>
        <!-- 智能体任务卡：点开看对话 -->
        <div v-else class="task" :data-state="it.state">
          <div class="task-head" :title="`${it.task}——点开看这个智能体的完整对话`" @click="toggle(it)">
            <span class="chev">{{ openKeys[it.key] ? '▾' : '▸' }}</span>
            <span class="dot" />
            <span class="task-title">{{ it.label }}</span>
            <span v-if="it.state === 'run'" class="task-dur run">{{ fmtDur(Math.max(0, now - it.startMs)) }}…</span>
            <span v-else-if="it.doneMs" class="task-dur">{{ fmtDur(it.doneMs - it.startMs) }}</span>
            <span v-if="it.calls" class="task-tok">输入 {{ fmtTok(it.promptTokens) }} · 输出 {{ fmtTok(it.completionTokens) }} · {{ it.calls }} 次</span>
            <span v-else-if="it.state === 'run'" class="task-tok running">运行中</span>
          </div>
          <div v-if="openKeys[it.key]" class="task-body">
            <div v-if="loadErr[it.key]" class="task-err">
              读取对话失败：{{ loadErr[it.key] }}
              <button class="msg-toggle" @click.stop="loadMsgs(it)">重试</button>
            </div>
            <div v-else-if="loading[it.key] && !msgCache[it.key]" class="task-loading">读取对话…</div>
            <div v-for="(m, mi) in msgCache[it.key] || []" :key="mi" class="msg" :data-role="m.role">
              <div class="msg-head">
                <span class="msg-role">{{ roleLabel(m) }}</span>
                <span v-if="m.ts" class="msg-ts">{{ shortTime(m.ts) }}</span>
                <button
                  v-if="m.content && m.content.length > msgLong"
                  class="msg-toggle"
                  @click.stop="toggleKey(`${it.key}:${mi}`, false)"
                >{{ isOpenKey(`${it.key}:${mi}`, false) ? '收起' : '展开' }}</button>
              </div>
              <template v-for="(tc, ci) in m.tool_calls || []" :key="ci">
                <div class="toolcall">
                  <div class="toolcall-head">
                    <span class="toolcall-name">🔧 {{ tc.name }}</span>
                    <button
                      v-if="tc.args.length > argsLong"
                      class="msg-toggle"
                      @click.stop="toggleKey(`${it.key}:${mi}:tc${ci}`, false)"
                    >{{ isOpenKey(`${it.key}:${mi}:tc${ci}`, false) ? '收起参数' : '展开参数' }}</button>
                  </div>
                  <pre v-if="isOpenKey(`${it.key}:${mi}:tc${ci}`, false) || tc.args.length <= argsLong" class="msg-pre">{{ tc.args }}</pre>
                  <pre v-else class="msg-pre dim">{{ clampText(tc.args, argsLong) }}</pre>
                </div>
              </template>
              <template v-if="m.content">
                <pre v-if="isOpenKey(`${it.key}:${mi}`, false) || m.content.length <= msgLong" class="msg-pre">{{ m.content }}</pre>
                <pre v-else class="msg-pre dim">{{ clampText(m.content, msgLong) }}</pre>
              </template>
            </div>
            <div v-if="msgCache[it.key]?.length" class="task-end">— 对话到此 —</div>
          </div>
        </div>
      </template>
    </div>
    <div v-else class="ap-empty">还没有制作记录——开工后这里会逐步记下每个工位的用时、智能体的对话和 token 消耗。</div>
  </div>
</template>

<style scoped>
.ap { display: flex; flex-direction: column; min-height: 0; flex: 1; }
.ap-head {
  flex: none; display: flex; align-items: center; gap: 12px; flex-wrap: wrap;
  font-weight: 700; font-size: 13px; color: #d9d9e0; letter-spacing: 1px;
  padding: 12px 18px 10px; border-bottom: 1px solid #232329;
}
.ap-live { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; font-weight: 400; color: #8fc7ff; }
.live-dot { width: 7px; height: 7px; border-radius: 50%; background: #8fc7ff; animation: pulse 1.2s infinite; }
.ap-totals {
  margin-left: auto; font-size: 12px; font-weight: 400; color: #9a9aa8;
  font-variant-numeric: tabular-nums;
}
.ap-scroll { flex: 1; min-height: 0; overflow-y: auto; padding: 6px 10px 16px; }
.ap-empty { padding: 18px; color: #55555f; font-size: 12.5px; line-height: 1.8; }

/* 普通事件行（与旧弹窗同款视觉） */
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

/* 智能体任务卡 */
.task { border: 1px solid #232329; border-radius: 10px; margin: 6px 2px; background: #101016; overflow: hidden; }
.task:hover { border-color: #2e2e38; }
.task[data-state="run"] { border-color: rgba(143, 199, 255, .35); }
.task[data-state="fail"] { border-color: rgba(255, 157, 157, .35); }
.task-head { display: flex; align-items: center; gap: 9px; padding: 9px 12px; cursor: pointer; user-select: none; }
.task-head:hover { background: #16161d; }
.chev { flex: none; color: #5d5d6a; font-size: 11px; width: 12px; }
.task .dot { margin-top: 0; }
.task[data-state="run"] .dot { background: #8fc7ff; animation: pulse 1.2s infinite; }
.task[data-state="ok"] .dot { background: #7ee2a8; }
.task[data-state="fail"] .dot { background: #ff9d9d; }
.task-title { font-size: 13px; color: #d9d9e0; font-weight: 600; }
.task[data-state="run"] .task-title { color: #8fc7ff; }
.task[data-state="fail"] .task-title { color: #ff9d9d; }
.task-dur {
  font-size: 11px; color: #7ee2a8; background: rgba(126, 226, 168, .08);
  border: 1px solid rgba(126, 226, 168, .22); border-radius: 999px; padding: 0 8px;
  font-variant-numeric: tabular-nums;
}
.task-dur.run { color: #8fc7ff; background: rgba(143, 199, 255, .08); border-color: rgba(143, 199, 255, .25); }
.task-tok {
  margin-left: auto; font-size: 11px; color: #9a9aa8; background: #17171e;
  border: 1px solid #26262e; border-radius: 999px; padding: 1px 9px;
  font-variant-numeric: tabular-nums; white-space: nowrap;
}
.task-tok.running { color: #8fc7ff; }
.task-body { border-top: 1px solid #1d1d24; padding: 8px 12px 10px; background: #0d0d12; }
.task-loading, .task-end { padding: 6px 4px; color: #55555f; font-size: 11.5px; }
.task-err { padding: 6px 4px; color: #ff9d9d; font-size: 12px; display: flex; gap: 8px; align-items: center; }

/* 对话消息 */
.msg { padding: 7px 8px; border-radius: 8px; margin: 4px 0; background: #12121a; border: 1px solid #1d1d24; }
.msg[data-role="user"] { background: #14161c; }
.msg[data-role="note"] { opacity: .7; background: transparent; border-style: dashed; }
.msg-head { display: flex; align-items: center; gap: 8px; margin-bottom: 3px; }
.msg-role { font-size: 11px; color: #8fc7ff; font-weight: 600; }
.msg[data-role="system"] .msg-role { color: #9a9aa8; }
.msg[data-role="tool"] .msg-role { color: #c3a6ff; }
.msg[data-role="note"] .msg-role { color: #7c7c88; }
.msg-ts { font-size: 10.5px; color: #4c4c56; font-variant-numeric: tabular-nums; }
.msg-toggle {
  margin-left: auto; cursor: pointer; border: 0; background: transparent;
  color: #6a6a78; font-size: 11px; padding: 0 2px;
}
.msg-toggle:hover { color: #d9d9e0; }
.msg-pre {
  margin: 2px 0 0; white-space: pre-wrap; word-break: break-word;
  font-size: 12px; line-height: 1.65; color: #c9c9d3;
  font-family: ui-monospace, 'Cascadia Code', Consolas, monospace;
  max-height: 420px; overflow-y: auto;
}
.msg-pre.dim { color: #7c7c88; }
.toolcall { border: 1px solid #232331; border-radius: 8px; margin: 4px 0; padding: 6px 8px; background: #15121d; }
.toolcall-head { display: flex; align-items: center; gap: 8px; }
.toolcall-name { font-size: 11.5px; color: #c3a6ff; font-weight: 600; }
@keyframes pulse { 50% { opacity: .35; } }
</style>
