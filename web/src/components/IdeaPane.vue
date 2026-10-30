<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, ref, watch } from 'vue'
import { NButton, NCheckbox, NInput, useMessage } from 'naive-ui'
import { api } from '../api'
import type { MaterialFile, PrepView } from '../types'

// 主题（准备工位的启动页，docs/prep-agents-design.md §2 用户输入；2026-10-30
// 从「想法」升级为「主题」——新建弹窗不再收主题，这里成为完整输入位）：
// 一个可空的文本框说主题 + 参考资料（.txt/.md 材料，①文稿 read_source 与
// 原始稿同等对待）+ 一个按钮让 Agent 带着这些自动跑完 ①文稿 → ②风格 → ③分镜。
// 跑动中实时显示跑到哪个工位（SSE stage 事件直驱，轮询 /prep 兜底工件落地态）；
// 完成后引导去终审。工件已存在时提供「按新主题重跑全部」（force = 级联作废重跑）。
const props = defineProps<{
  projectId: string
  prep: PrepView | null
  // 各工位实时态（App 从 SSE stage 事件取：running/done/error；空=暂无事件）
  progress?: { script?: string; style?: string; storyboard?: string }
}>()
const emit = defineEmits<{ 'go-tab': [t: 'review']; refresh: [] }>()

const message = useMessage()
const idea = ref('')
const rerunOpen = ref(false)
watch(() => props.prep?.idea, v => { if (idea.value.trim() === '') idea.value = v ?? '' }, { immediate: true })

// ── 进度轮询：running 期间每 3s 拉 /prep（SSE 的兜底），跑完 notify App 全量刷新 ──
let pollTimer: number | null = null
const local = ref<PrepView | null>(props.prep)
watch(() => props.prep, v => { local.value = v })
function startPoll() {
  if (pollTimer != null) return
  pollTimer = window.setInterval(async () => {
    const pp = await api.prep(props.projectId).catch(() => null)
    if (pp) local.value = pp
    if (pp && !pp.running) {
      stopPoll()
      emit('refresh')
      if (pp.exists && pp.draft) message.success('准备阶段完成，去「终审」页签确认')
    }
  }, 3000)
}
function stopPoll() {
  if (pollTimer != null) { clearInterval(pollTimer); pollTimer = null }
}
watch(() => local.value?.running, running => {
  if (running) startPoll()
  if (!running) stopPoll()
}, { immediate: true })
onBeforeUnmount(() => {
  stopPoll()
  if (armTimer) clearTimeout(armTimer)
})
// KeepAlive 页签只是 deactivate 不 unmount（中-42）：切离「主题」页后 3s 轮询
// 照跑全程 15-25 分钟白烧——切走暂停、切回（running 仍在）恢复
let wantedPoll = false
onDeactivated(() => { wantedPoll = pollTimer != null; stopPoll() })
onActivated(() => { if (wantedPoll && local.value?.running) startPoll() })

const running = computed(() => !!local.value?.running)
const exists = computed(() => !!local.value?.exists)
const ready = computed(() => exists.value && !!local.value?.draft)

// 三工位实时态：SSE 事件优先（running/done/error），无事件时按工件落地态兜底
//（刷新后 SSE 事件流丢失的场景）。
type StationState = '' | 'running' | 'done' | 'error'
const stations = computed(() => {
  const st = (key: 'script' | 'style' | 'storyboard'): StationState => {
    const live = props.progress?.[key] as StationState | undefined
    if (live) return live
    if (key === 'script') return local.value?.script ? 'done' : ''
    if (key === 'style') return local.value?.stylepack ? 'done' : ''
    return local.value?.draft ? 'done' : ''
  }
  return [
    { key: 'script' as const, label: '① 文字稿', desc: '吃主题与材料，搜集改写成口播稿', state: st('script') },
    { key: 'style' as const, label: '② 风格包', desc: '探索风格库，出样张与设计系统', state: st('style') },
    { key: 'storyboard' as const, label: '③ 分镜稿', desc: '切段落，写场景与首末拍', state: st('storyboard') },
  ]
})
const stateTag = (s: StationState) =>
  s === 'running' ? '进行中' : s === 'done' ? '完成' : s === 'error' ? '出错' : '排队中'

// ── 参考材料：上传（.txt/.md 纯文本，前端读文本走 JSON）、删除；同名覆盖=更新。
// 增删在链路运行中被后端拒绝（材料是本链路的输入，中途不改口径），这里直接禁用。
const materials = ref<MaterialFile[]>([])
const matsBusy = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)
async function loadMaterials() {
  if (!props.projectId) return
  materials.value = await api.materials(props.projectId).catch(() => [])
}
watch(() => props.projectId, loadMaterials, { immediate: true })
async function onFiles(e: Event) {
  const input = e.target as HTMLInputElement
  const files = [...(input.files ?? [])]
  input.value = ''
  for (const f of files) {
    if (!/\.(txt|md|markdown)$/i.test(f.name)) {
      message.error(`只收纯文本材料（.txt / .md）：${f.name}`)
      continue
    }
    if (f.size > 300 * 1024) {
      message.error(`材料过大（>300KB），拆成几份再传：${f.name}`)
      continue
    }
    matsBusy.value = true
    try {
      const r = await api.saveMaterial(props.projectId, f.name, await f.text())
      materials.value = r.materials
    } catch (err) {
      message.error(`${f.name}：${(err as Error).message}`)
    } finally {
      matsBusy.value = false
    }
  }
}
async function removeMaterial(name: string) {
  if (matsBusy.value) return
  matsBusy.value = true
  try {
    const r = await api.deleteMaterial(props.projectId, name)
    materials.value = r.materials
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    matsBusy.value = false
  }
}

const starting = ref(false)
const forceRerun = ref(false)
// 两段式确认（自绘，替代 window.confirm——原生弹窗 2026-10-16 死按钮、10-18
// Playwright 点按 30s 超时同源，全站已弃用）：破坏性提交第一枪只亮确认态
const armed = ref(false)
let armTimer: number | null = null
async function start() {
  if (starting.value || running.value || !props.projectId) return
  // 换主题重跑模式 = 破坏性操作，必须带 force（2026-10-19 高-7：旧版该模式下
  // 勾选框被 v-if 隐藏、force 恒 false，工件已存在时必被后端 409 拒之门外）
  const force = rerunOpen.value || forceRerun.value
  if (force && !armed.value) {
    armed.value = true
    // 确认态限时解除（同 HistoryRail/PrepPane 3s 口径）：武装态无限期存活，
    // 用户被打断回来随手一点就级联作废全部工件（2026-10-19 审查低危）
    if (armTimer) clearTimeout(armTimer)
    armTimer = window.setTimeout(() => { armed.value = false }, 3000)
    return
  }
  armed.value = false
  starting.value = true
  try {
    await api.runPrep(props.projectId, idea.value.trim(), force)
    rerunOpen.value = false
    const pp = await api.prep(props.projectId).catch(() => null)
    if (pp) local.value = pp
    emit('refresh')
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    starting.value = false
  }
}
async function cancel() {
  if (!props.projectId) return
  try { await api.cancelPrep(props.projectId) } catch (e) { message.error((e as Error).message) }
}
</script>

<template>
  <div class="idea">
    <div class="wrap">
      <template v-if="running">
        <h3 class="live-title"><span class="live-dot" />准备阶段进行中</h3>
        <p class="sub">AI 正按链跑三个工位，一般 15~25 分钟（出样张的轮次多时更久）；每步的详细动作都在本页工位卡片上，制作阶段的账在「过程」页签里。</p>
        <ol class="stations">
          <li v-for="s in stations" :key="s.key" :class="s.state || 'pending'">
            <span class="mark">{{ s.state === 'done' ? '✓' : s.state === 'error' ? '✗' : '◐' }}</span>
            <span class="st-main">
              <b>{{ s.label }}</b>
              <i>{{ s.desc }}</i>
            </span>
            <span class="st-tag">{{ stateTag(s.state) }}</span>
          </li>
        </ol>
        <div class="row">
          <NButton size="small" quaternary @click="cancel">取消</NButton>
        </div>
      </template>

      <template v-else-if="ready && !rerunOpen">
        <h3>准备工件已就绪</h3>
        <p class="sub">
          文字稿 {{ local?.script?.sentences.length ?? 0 }} 句 · 风格「{{ local?.style_name || local?.stylepack?.style_id }}」 ·
          分镜 {{ local?.draft?.segments.length ?? 0 }} 段
        </p>
        <div class="row">
          <NButton type="primary" @click="emit('go-tab', 'review')">去终审确认</NButton>
          <NButton quaternary @click="rerunOpen = true">换个主题重跑</NButton>
        </div>
      </template>

      <template v-else>
        <h3>{{ rerunOpen ? '换个主题，重跑准备阶段' : '说说这个视频的主题' }}</h3>
        <p class="sub">
          想讲什么、给谁看、什么感觉——想到哪写到哪；<b>留空就让 AI 自由发挥</b>。
          点下面按钮，AI 会自动跑完 文稿 → 风格 → 分镜 三个工位，完成后在「终审」等你确认。
        </p>
        <NInput
          v-model:value="idea" type="textarea" placeholder="例：给上班族讲清楚「复利」是怎么回事，节奏快点，画面别太卡通…"
          :autosize="{ minRows: 4, maxRows: 10 }" :maxlength="2000" show-count
        />
        <!-- 参考材料：可选项，与主题一起喂给①文稿（改写时与原始稿同等对待） -->
        <div class="mats">
          <div class="mats-head">
            <span class="mats-title">参考资料<i>可选项——文章、笔记、资料稿，AI 改写时不改事实不增删观点</i></span>
            <button class="mat-add" :disabled="matsBusy || running" @click="fileInput?.click()">＋ 上传材料</button>
            <input ref="fileInput" type="file" accept=".txt,.md,.markdown,text/plain" multiple hidden @change="onFiles">
          </div>
          <ul v-if="materials.length" class="mat-list">
            <li v-for="m in materials" :key="m.name">
              <span class="mat-name" :title="m.name">{{ m.name }}</span>
              <span class="mat-chars">{{ m.chars }} 字</span>
              <button class="mat-del" :disabled="matsBusy || running" title="删除这份材料" @click="removeMaterial(m.name)">✕</button>
            </li>
          </ul>
          <p v-else class="mat-empty">还没传材料——一篇参考文章、几条笔记都行（.txt / .md）</p>
        </div>
        <div class="row">
          <NButton :type="armed ? 'error' : 'primary'" :loading="starting" @click="start">
            {{ armed ? '确认作废现有工件并重跑' : rerunOpen ? '按新主题重跑全部' : '让 AI 自动完成准备' }}
          </NButton>
          <NCheckbox v-if="!rerunOpen && exists" v-model:checked="forceRerun" @update:checked="armed = false">作废现有工件重跑</NCheckbox>
          <NButton v-if="rerunOpen" quaternary @click="rerunOpen = false; armed = false">返回</NButton>
        </div>
        <p v-if="exists" class="note">重跑 = 级联作废现有文字稿/风格包/分镜稿（含已确认的设计），按新主题与材料重新出齐。</p>
      </template>
    </div>
  </div>
</template>

<style scoped>
.idea { flex: 1; min-height: 0; display: flex; overflow-y: auto; }
.wrap { width: min(640px, 92%); margin: auto; display: flex; flex-direction: column; gap: 14px; padding: 28px 0; }
.wrap h3 { font-size: 17px; color: var(--ink); letter-spacing: 0.06em; }
.sub { font-size: 12.5px; color: var(--muted); line-height: 1.8; }
.sub b { color: var(--accent); font-weight: 600; }
.row { display: flex; align-items: center; gap: 14px; }
.note { font-size: 11.5px; color: var(--muted); opacity: .7; line-height: 1.7; }

/* 参考材料块：标题行 + 上传钮 + 清单（方角细边框，同站内卡片语言） */
.mats { display: flex; flex-direction: column; gap: 8px; }
.mats-head { display: flex; align-items: baseline; gap: 10px; }
.mats-title { flex: 1; min-width: 0; font-size: 12.5px; font-weight: 600; color: var(--ink); display: flex; align-items: baseline; gap: 8px; }
.mats-title i { font-style: normal; font-weight: 400; font-size: 11px; color: var(--muted); opacity: .75; }
.mat-add {
  cursor: pointer; flex: none; font-family: inherit; font-size: 11.5px; padding: 3px 10px;
  color: var(--muted); background: transparent; border: 1px solid var(--line);
  transition: color var(--dur-1) var(--ease), border-color var(--dur-1) var(--ease);
}
.mat-add:hover:not(:disabled) { color: var(--accent); border-color: var(--accent); }
.mat-add:disabled { opacity: .5; cursor: default; }
.mat-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 4px; }
.mat-list li {
  display: flex; align-items: center; gap: 10px;
  border: 1px solid var(--line-soft); padding: 6px 10px; font-size: 12px;
}
.mat-name { flex: 1; min-width: 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; color: var(--ink); }
.mat-chars { flex: none; color: var(--muted); font-size: 11px; font-variant-numeric: tabular-nums; }
.mat-del {
  cursor: pointer; flex: none; border: 0; background: transparent; color: var(--muted);
  font-size: 11px; padding: 2px 4px; font-family: inherit;
}
.mat-del:hover:not(:disabled) { color: var(--err); }
.mat-del:disabled { opacity: .4; cursor: default; }
.mat-empty { margin: 0; font-size: 11.5px; color: var(--muted); opacity: .7; line-height: 1.6; }

/* 运行中：标题带呼吸点 + 工位清单三态（完成绿 / 进行中琥珀转圈 / 排队灰） */
.live-title { display: flex; align-items: center; gap: 10px; }
.live-dot { width: 9px; height: 9px; border-radius: 50%; background: var(--info); animation: pulse 1.2s infinite; }
@keyframes pulse { 50% { opacity: .3; } }
.stations { list-style: none; margin: 0; padding: 4px 0; display: flex; flex-direction: column; gap: 6px; }
.stations li {
  display: flex; align-items: center; gap: 12px;
  border: 1px solid var(--line-soft); padding: 10px 14px; background: var(--surface);
  color: var(--muted);
}
.stations li .mark { width: 18px; text-align: center; font-size: 14px; }
.stations li .st-main { flex: 1; display: flex; flex-direction: column; gap: 2px; }
.stations li .st-main b { font-size: 13.5px; font-weight: 600; color: var(--ink); }
.stations li .st-main i { font-style: normal; font-size: 11.5px; color: var(--muted); opacity: .8; }
.stations li .st-tag { font-size: 11.5px; }
.stations li.done { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 30%, transparent); }
.stations li.done .st-tag { color: var(--ok); }
.stations li.running { color: var(--accent); border-color: color-mix(in srgb, var(--accent) 35%, transparent); }
.stations li.running .mark { animation: spin 1.1s linear infinite; display: inline-block; }
.stations li.running .st-tag { color: var(--accent); }
.stations li.error { color: var(--err); border-color: color-mix(in srgb, var(--err) 35%, transparent); }
.stations li.error .st-tag { color: var(--err); }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
