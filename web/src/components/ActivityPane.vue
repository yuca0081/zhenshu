<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, ref, watch } from 'vue'
import type { ActivityEvent, TranscriptMsg } from '../types'
import { api, artifactFileURL } from '../api'
import { cachePct, fmtTok, stageLabel } from '../format'

// 「过程」页（2026-10-18 替代旧事件流弹窗）：manifest 事件 → 两条线——
// ① 普通事件行（开工/工位用时/打回/终态，人话翻译沿用原 ActivityFeed 口径）；
// ② 智能体任务卡（agent.start/agent.done 聚合）：标题+用时+token 账，
//    点开懒加载对话留痕（GET /transcript），运行中的卡片每 5 秒刷新看它干活。
// 卡片身份键 = task#第几次开工：重跑/冷重试同任务号会开新卡片，展开态不串卡。
// 头部两本账：「全程」= 项目里所有智能体任务累计（projectView 的 tokens，
// manifest 全量扫描，不受活动流尾部窗口限制）；「本轮」= 最近一次开工以来。

const props = defineProps<{
  projectId: string
  events: ActivityEvent[]
  producing: boolean
  totals?: { prompt: number; completion: number; calls: number; cached?: number } | null
}>()

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
  cachedTokens: number // 平台前缀缓存命中（cached_tokens；旧账目无此字段=0 不显示）
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

const items = computed<Item[]>(() => {
  const out: Item[] = []
  const occ: Record<string, number> = {}
  const openTasks: TaskGroup[] = []
  // 稳定 key（中-L5）：按行自身内容（timeText+title）定位；同刻同名重复加序号。
  // 旧版自增序号做 key，manifest 超 2000 条后尾窗一滑动、每轮轮询全量 key 平移
  // → 2000 行全量重 patch。task 组的 key 本就带 task 号，不受影响。
  const stableKey = (base: string) => {
    const n = occ[base] ?? 0
    occ[base] = n + 1
    return n ? `${base}#${n}` : base
  }
  const add = (r: Omit<PlainRow, 'kind' | 'key'>) => {
    out.push({ kind: 'plain', key: stableKey(`t-${r.timeText}-${r.title}`), ...r })
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
        promptTokens: 0, completionTokens: 0, cachedTokens: 0, calls: 0,
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
        g.cachedTokens = ev.cached_tokens || 0
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
          kind: 'plain', key: stableKey(`t-${t}-${stageLabel(ev.detail)}${n > 1 ? `#${n}` : ''}`), state: 'ok',
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
      // prereview.*：legacy 栅栏时代事件，段级流水线起后端已无发射点——翻译留着
      // 给旧项目 manifest 回放，新项目不会再走到这五个分支。
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
      // ── 段级流水线审核账（review.*：段审/边界审/全片终审，segpipe.go）──
      case 'review.rework': // 两种形态：段审打回（segXX rN: …）/ 边界打回（bNN → segXX）
        add({ state: 'reject', title: '审核打回：段画面重写', timeText: t, detail: firstLine(ev.detail) })
        break
      case 'review.seg_pass_degraded':
        add({ state: 'reject', title: '段审打回超限，带伤放行', timeText: t, detail: firstLine(ev.detail) })
        break
      case 'review.bnd_pass_degraded':
        add({ state: 'reject', title: '边界审打回超限，带伤放行', timeText: t, detail: firstLine(ev.detail) })
        break
      case 'review.seg_error':
        add({ state: 'fail', title: '段审审查器故障（降级放行）', timeText: t, detail: firstLine(ev.detail) })
        break
      case 'review.bnd_error':
        add({ state: 'fail', title: '边界审审查器故障（降级放行）', timeText: t, detail: firstLine(ev.detail) })
        break
      case 'review.final_error':
        add({ state: 'fail', title: '终审审查器故障，降级放行', timeText: t, detail: firstLine(ev.detail) })
        break
      case 'review.brake':
        add({ state: 'fail', title: '刹车停机（可重跑，产物按段保留）', timeText: t, detail: firstLine(ev.detail) })
        break
      case 'review.seg_audit_off':
        add({ state: 'info', title: '段审已关（机检直过）', timeText: t, minor: true })
        break
      case 'review.stitch_fix_applied':
        add({ state: 'ok', title: '拼接修正已应用', timeText: t, detail: ev.detail, minor: true })
        break
      case 'review.final_recheck':
        add({ state: 'info', title: '终审复核轮（帧格瘦身）', timeText: t, detail: ev.detail, minor: true })
        break
      case 'review.verdict_write_error':
        add({ state: 'fail', title: '审核意见单落盘失败', timeText: t, detail: firstLine(ev.detail), minor: true })
        break
      // ── 段级门禁与停机补充账 ──
      case 'check.seam':
        add({ state: 'ok', title: '接缝亮度闸通过', timeText: t, detail: ev.detail, minor: true })
        break
      case 'check.seam_degraded':
        add({ state: 'info', title: '接缝亮度闸降级放行', timeText: t, detail: firstLine(ev.detail), minor: true })
        break
      case 'check.tdz_recheck':
        add({ state: 'info', title: '运行时崩溃本地复检（不烧修复轮）', timeText: t, detail: firstLine(ev.detail), minor: true })
        break
      case 'budget.exhausted':
        add({ state: 'fail', title: '预算耗尽，制作停机', timeText: t, detail: firstLine(ev.detail) })
        break
      case 'segpipe.watchdog':
        add({ state: 'fail', title: '管线静默超限，看门狗刹车', timeText: t, detail: firstLine(ev.detail) })
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
      case 'frameqa.skipped_by_prereview': // legacy 栅栏时代联动，现管线无发射点
        add({ state: 'info', title: '预审全过，渲后免审', timeText: t, minor: true })
        break
      case 'render.seg':
        add({ state: 'info', title: `${ev.detail} 画面渲好`, timeText: t, minor: true })
        break
      case 'film.ready':
        add({ state: 'ok', title: '成片终装完成（后期/配乐）', timeText: t, minor: true })
        break
      // 样张阶段自采披露：装了什么库/字体，过程页同步可见（终审页有汇总）
      case 'prep.vend':
        add({ state: 'ok', title: '样张自采库入库', timeText: t, detail: ev.detail, minor: true })
        break
      case 'prep.style_fonts':
        add({ state: 'ok', title: '样张字体已备（目录）', timeText: t, detail: ev.detail, minor: true })
        break
      case 'prep.style_font_custom':
        add({ state: 'ok', title: '样张自装字体', timeText: t, detail: ev.detail, minor: true })
        break
      case 'prep.style_font_degrade':
        add({ state: 'info', title: '字体降级（回退渲染，不挡样张）', timeText: t, detail: firstLine(ev.detail), minor: true })
        break
      default:
        break // llm.usage / seg.* 状态机账 / fonts.* / produce.tts 等记账类与技术性事件不上屏
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
// cd = 平台前缀缓存命中合计（agent.done 的 cached_tokens；旧账目无此字段=0）
const roundTotals = computed(() => {
  let start = -1
  props.events.forEach((ev, i) => {
    if (ev.event === 'produce.start' || ev.event === 'rework.start') start = i
  })
  const t = { p: 0, c: 0, n: 0, cd: 0 }
  for (let i = start + 1; i < props.events.length; i++) {
    const ev = props.events[i]
    if (ev.event === 'agent.done') {
      t.p += ev.prompt_tokens || 0
      t.c += ev.completion_tokens || 0
      t.n += ev.calls || 0
      t.cd += ev.cached_tokens || 0
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
  if (on) startTimer()
  if (!on) stopTimer()
}, { immediate: true })
function startTimer() {
  if (timer == null) timer = window.setInterval(onTick, 5000)
}
function stopTimer() {
  if (timer != null) { clearInterval(timer); timer = null }
}
onBeforeUnmount(stopTimer)
// KeepAlive 页签切走只是 deactivate（中-42）：展开中任务的 5s transcript 轮询
// 不该在用户不看「过程」页时空转——切走暂停、切回恢复
let wantedTimer = false
onDeactivated(() => { wantedTimer = timer != null; stopTimer() })
onActivated(() => { if (wantedTimer && props.producing) startTimer() })

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

// ── 注入图缩略图 + 点击放大（灯箱）：images = 留痕里记的项目内路径清单 ──
const lightbox = ref<{ src: string; label: string } | null>(null)
const openLightbox = (path: string, label?: string) => {
  lightbox.value = { src: artifactFileURL(props.projectId, path), label: label || path }
}
</script>

<template>
  <div class="ap">
    <div class="ap-head">
      制作过程
      <span v-if="producing" class="ap-live"><i class="live-dot" />制作中</span>
      <span v-if="totals?.calls" class="ap-totals ap-total-all" title="这个项目全部智能体任务的 token 消耗累计（manifest 全量累计，含此前每一轮）">
        全程：输入 {{ fmtTok(totals.prompt) }} · 输出 {{ fmtTok(totals.completion) }} tokens · {{ totals.calls }} 次调用<template v-if="totals.cached"> · 缓存命中 {{ cachePct(totals.cached, totals.prompt) }}</template>
      </span>
      <span v-if="roundTotals.n" class="ap-totals" :class="{ 'ap-totals-push': totals?.calls }" title="最近一次开工以来全部智能体任务的 token 消耗合计">
        本轮：输入 {{ fmtTok(roundTotals.p) }} · 输出 {{ fmtTok(roundTotals.c) }} tokens · {{ roundTotals.n }} 次调用<template v-if="roundTotals.cd"> · 缓存命中 {{ cachePct(roundTotals.cd, roundTotals.p) }}</template>
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
            <span v-if="it.calls" class="task-tok">输入 {{ fmtTok(it.promptTokens) }} · 输出 {{ fmtTok(it.completionTokens) }}<template v-if="it.cachedTokens"> · 缓存 {{ cachePct(it.cachedTokens, it.promptTokens) }}</template> · {{ it.calls }} 次</span>
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
              <!-- 注入图缩略图：点击放大看原文件 -->
              <div v-if="m.images?.length" class="msg-imgs">
                <figure
                  v-for="(im, ii) in m.images" :key="ii" class="msg-img"
                  :title="`${im.label || im.path}——点击放大`" @click.stop="openLightbox(im.path, im.label)"
                >
                  <img :src="artifactFileURL(projectId, im.path)" :alt="im.label || im.path" loading="lazy" />
                  <figcaption>{{ im.label || im.path }}</figcaption>
                </figure>
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
    <!-- 灯箱：点击任意处关闭 -->
    <div v-if="lightbox" class="lightbox" @click="lightbox = null">
      <img :src="lightbox.src" :alt="lightbox.label" />
      <div class="lightbox-label">{{ lightbox.label }}</div>
      <div class="lightbox-hint">点击任意处关闭</div>
    </div>
  </div>
</template>

<style scoped>
.ap { display: flex; flex-direction: column; min-height: 0; flex: 1; }
.ap-head {
  flex: none; display: flex; align-items: center; gap: 12px; flex-wrap: wrap;
  font-weight: 600; font-size: 13px; color: var(--ink); letter-spacing: 0.06em;
  padding: 12px 18px 10px; border-bottom: 1px solid var(--line-soft);
}
.ap-live { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; font-weight: 400; color: var(--info); }
.live-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--info); animation: pulse 1.2s infinite; }
.ap-totals {
  font-size: 12px; font-weight: 400; color: var(--muted);
  font-variant-numeric: tabular-nums;
}
/* 全程账永远最右（它是主账）；本轮账随其后；只有本轮时本轮自己推到右边 */
.ap-total-all { margin-left: auto; color: var(--ink); }
.ap-totals-push { margin-left: auto; }
.ap-scroll { flex: 1; min-height: 0; overflow-y: auto; padding: 6px 10px 16px; }
.ap-empty { padding: 18px; color: var(--muted); font-size: 12.5px; line-height: 1.8; }

/* 普通事件行 */
.row { display: flex; align-items: flex-start; gap: 9px; padding: 6px 8px; }
.row:hover { background: color-mix(in srgb, var(--ink) 4%, transparent); }
.dot { width: 7px; height: 7px; border-radius: 50%; background: var(--muted); opacity: .55; flex: none; margin-top: 5px; }
.row[data-state="ok"] .dot { background: var(--ok); opacity: 1; }
.row[data-state="run"] .dot { background: var(--info); opacity: 1; animation: pulse 1.2s infinite; }
.row[data-state="fail"] .dot, .row[data-state="reject"] .dot { background: var(--err); opacity: 1; }
.row[data-state="info"] .dot { background: var(--muted); opacity: .8; }
.row[data-minor] { opacity: .62; }
.row-time { flex: none; font-size: 11px; color: var(--muted); opacity: .8; font-variant-numeric: tabular-nums; margin-top: 2px; width: 56px; }
.row-main { flex: 1; min-width: 0; display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 8px; }
.row-title { font-size: 12.5px; color: var(--ink); }
.row[data-state="reject"] .row-title { color: var(--warn); }
.row[data-state="fail"] .row-title { color: var(--err); }
.row[data-state="run"] .row-title { color: var(--info); }
.row-dur {
  font-size: 11px; color: var(--ok);
  background: color-mix(in srgb, var(--ok) 8%, transparent);
  border: 1px solid color-mix(in srgb, var(--ok) 22%, transparent); padding: 0 8px;
  font-variant-numeric: tabular-nums;
}
.row[data-state="run"] .row-dur { color: var(--info); background: color-mix(in srgb, var(--info) 8%, transparent); border-color: color-mix(in srgb, var(--info) 25%, transparent); }
.row[data-state="reject"] .row-dur { color: var(--warn); background: color-mix(in srgb, var(--warn) 7%, transparent); border-color: color-mix(in srgb, var(--warn) 25%, transparent); }
.row-detail {
  flex-basis: 100%; font-size: 11.5px; color: var(--muted); line-height: 1.6;
  overflow: hidden; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical;
}

/* 智能体任务卡 */
.task { border: 1px solid var(--line-soft); margin: 6px 2px; background: var(--bg); overflow: hidden; }
.task:hover { border-color: var(--line); }
.task[data-state="run"] { border-color: color-mix(in srgb, var(--info) 35%, transparent); }
.task[data-state="fail"] { border-color: color-mix(in srgb, var(--err) 35%, transparent); }
.task-head { display: flex; align-items: center; gap: 9px; padding: 9px 12px; cursor: pointer; user-select: none; }
.task-head:hover { background: color-mix(in srgb, var(--ink) 3%, transparent); }
.chev { flex: none; color: var(--muted); font-size: 11px; width: 12px; }
.task .dot { margin-top: 0; }
.task[data-state="run"] .dot { background: var(--info); opacity: 1; animation: pulse 1.2s infinite; }
.task[data-state="ok"] .dot { background: var(--ok); opacity: 1; }
.task[data-state="fail"] .dot { background: var(--err); opacity: 1; }
.task-title { font-size: 13px; color: var(--ink); font-weight: 600; }
.task[data-state="run"] .task-title { color: var(--info); }
.task[data-state="fail"] .task-title { color: var(--err); }
.task-dur {
  font-size: 11px; color: var(--ok);
  background: color-mix(in srgb, var(--ok) 8%, transparent);
  border: 1px solid color-mix(in srgb, var(--ok) 22%, transparent); padding: 0 8px;
  font-variant-numeric: tabular-nums;
}
.task-dur.run { color: var(--info); background: color-mix(in srgb, var(--info) 8%, transparent); border-color: color-mix(in srgb, var(--info) 25%, transparent); }
.task-tok {
  margin-left: auto; font-size: 11px; color: var(--muted); background: color-mix(in srgb, var(--ink) 3%, transparent);
  border: 1px solid var(--line-soft); padding: 1px 9px;
  font-variant-numeric: tabular-nums; white-space: nowrap;
}
.task-tok.running { color: var(--info); }
.task-body { border-top: 1px solid var(--line-soft); padding: 8px 12px 10px; background: color-mix(in srgb, var(--ink) 2%, var(--bg)); }
.task-loading, .task-end { padding: 6px 4px; color: var(--muted); opacity: .7; font-size: 11.5px; }
.task-err { padding: 6px 4px; color: var(--err); font-size: 12px; display: flex; gap: 8px; align-items: center; }

/* 对话消息 */
.msg { padding: 7px 8px; margin: 4px 0; background: var(--surface); border: 1px solid var(--line-soft); }
.msg[data-role="user"] { border-left: 2px solid var(--accent); }
.msg[data-role="note"] { opacity: .7; background: transparent; border-style: dashed; }
.msg-head { display: flex; align-items: center; gap: 8px; margin-bottom: 3px; }
.msg-role { font-size: 11px; color: var(--info); font-weight: 600; }
.msg[data-role="system"] .msg-role { color: var(--muted); }
.msg[data-role="tool"] .msg-role { color: var(--accent); }
.msg[data-role="note"] .msg-role { color: var(--muted); }
.msg-ts { font-size: 10.5px; color: var(--muted); opacity: .65; font-variant-numeric: tabular-nums; }
.msg-toggle {
  margin-left: auto; cursor: pointer; border: 0; background: transparent; font-family: inherit;
  color: var(--muted); font-size: 11px; padding: 0 2px;
}
.msg-toggle:hover { color: var(--ink); }
.msg-pre {
  margin: 2px 0 0; white-space: pre-wrap; word-break: break-word;
  font-size: 12px; line-height: 1.65; color: var(--ink);
  font-family: ui-monospace, 'Cascadia Code', Consolas, monospace;
  max-height: 420px; overflow-y: auto;
}
.msg-pre.dim { color: var(--muted); }
.toolcall { border: 1px solid var(--line-soft); margin: 4px 0; padding: 6px 8px; background: color-mix(in srgb, var(--ink) 2%, var(--surface)); }
.toolcall-head { display: flex; align-items: center; gap: 8px; }
.toolcall-name { font-size: 11.5px; color: var(--accent); font-weight: 600; }

/* 注入图缩略图条 + 灯箱 */
.msg-imgs { display: flex; flex-wrap: wrap; gap: 6px; margin: 6px 0 2px; }
.msg-img {
  width: 96px; margin: 0; cursor: zoom-in; border: 1px solid var(--line-soft);
  overflow: hidden; background: var(--bg);
}
.msg-img:hover { border-color: var(--accent); }
.msg-img img { display: block; width: 100%; height: 54px; object-fit: cover; }
.msg-img figcaption {
  font-size: 9.5px; color: var(--muted); padding: 2px 4px; line-height: 1.3;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.lightbox {
  position: fixed; inset: 0; z-index: 300; cursor: zoom-out;
  background: rgb(var(--veil) / 0.88); backdrop-filter: blur(4px);
  display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 10px;
  padding: 32px;
}
.lightbox img { max-width: 92vw; max-height: 82vh; object-fit: contain; box-shadow: 0 8px 48px rgba(0, 0, 0, .6); }
.lightbox-label { color: var(--ink); font-size: 13px; }
.lightbox-hint { color: var(--muted); font-size: 11px; }
@keyframes pulse { 50% { opacity: .35; } }
</style>
