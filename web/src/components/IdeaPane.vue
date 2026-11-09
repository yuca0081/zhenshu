<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, ref, watch } from 'vue'
import { NButton, NCheckbox, NInput, useMessage } from 'naive-ui'
import { api } from '../api'
import type { ImageMaterial, MaterialFile, PrepStation, PrepView } from '../types'

// 主题（准备工位的启动页，docs/prep-agents-design.md §2/§9；2026-10-30
// 从「想法」升级为「主题」——新建弹窗不再收主题，这里成为完整输入位）：
// 主题文本框 + 参考资料 + 画面素材 + 三个勾选项（文稿/风格/分镜）+ 生成按钮。
// 勾选 = 本次让 AI 生成/重做哪些工件（不是三个独立开关）：缺的前置自动补跑，
// 勾了下游而上游也勾着时中间站必须跟跑；只勾上游则下游在成功后级联作废留空。
// 跑动期间 Runner.active 对应的页签在 StagePanel 里只读（见 prepLockLabel），
// 链收口后 active 清空、自动解锁，用户可以自己改。工件已就绪时不隐藏表单，
// 只把默认勾选切到「缺的」；三件齐时默认全不勾，等用户点名重做。
const props = defineProps<{
  projectId: string
  prep: PrepView | null
  // 各工位实时态（App 从 SSE stage 事件取：running/done/error；空=暂无事件）
  progress?: { script?: string; style?: string; storyboard?: string }
}>()
const emit = defineEmits<{ 'go-tab': [t: 'review']; refresh: [] }>()

const message = useMessage()
const idea = ref('')
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
const ready = computed(() => exists.value && !!local.value?.stylepack && !!local.value?.draft)

// ── 勾选集：这次要让 AI 生成/重做哪些工件 ─────────────────────────
// 三工件不是三个独立开关，而是一条链（①→②→③）：
//   · 勾了下游：上游必须可用，缺件自动补跑（③ 需要 ①+②）；
//   · 勾了上游且下游也在勾选里：中间那站必须跟跑（新稿不能配旧风格）；
//   · 只勾上游：下游工件在成功后级联作废、留空，等下次勾选再补。
// 前端复算一遍只为让用户在点按钮前看见真实执行计划；后端 planStations 同口径。
type PickMap = Record<PrepStation, boolean>
const picked = ref<PickMap>({ script: true, style: true, storyboard: true })
const has = computed<Record<PrepStation, boolean>>(() => ({
  script: !!local.value?.script,
  style: !!local.value?.stylepack,
  storyboard: !!local.value?.draft,
}))
const ORDER: PrepStation[] = ['script', 'style', 'storyboard']
const STATION_LABEL: Record<PrepStation, string> = { script: '① 文稿', style: '② 风格', storyboard: '③ 分镜' }
// 统一链（rev2 实验档）：单 agent 一条循环产三件，顺序由 AI 定——勾选集被
// 服务端收敛为三件全选（三件互相影响，部分重做没有意义），前端同口径展示。
const unified = computed(() => !!local.value?.unified)

const plan = computed<PrepStation[]>(() => {
  if (unified.value) return [...ORDER]
  const want: PickMap = { ...picked.value }
  if (want.storyboard && (want.script || !has.value.script || !has.value.style)) want.style = true
  if ((want.style || want.storyboard) && !has.value.script) want.script = true
  return ORDER.filter(k => want[k])
})
const planText = computed(() => plan.value.map(k => STATION_LABEL[k]).join(unified.value ? '、' : ' → '))
const autoAdded = computed(() => unified.value ? [] : plan.value.filter(k => !picked.value[k]))
const willReplace = computed(() => plan.value.some(k => has.value[k]))
// 计划没跑到③ = 有下游会被本次上游重做连累作废（只重做③则没有下游）。
const dropsDownstream = computed(() => plan.value.length > 0 && plan.value[plan.value.length - 1] !== 'storyboard')

// 两段式确认（自绘，替代 window.confirm——原生弹窗 2026-10-16 死按钮、10-18
// Playwright 点按 30s 超时同源，全站已弃用）：破坏性提交第一枪只亮确认态
const armed = ref(false)
let armTimer: number | null = null

let pickedInit = false
// 默认只勾缺的：新项目=全勾；跑到一半=接着补缺；三件齐=全不勾（等用户点名重做）。
// 重跑完成后按新状态重算，避免把用户上一轮的勾选带进下一轮。
function initPicked() {
  picked.value = {
    script: !has.value.script,
    style: !has.value.style,
    storyboard: !has.value.storyboard,
  }
  pickedInit = true
  armed.value = false
}
watch(() => local.value, () => { if (!pickedInit) initPicked() }, { immediate: true })
watch(() => props.projectId, () => {
  pickedInit = false
  if (props.prep) { local.value = props.prep; initPicked() }
})
watch(running, (now, was) => { if (was && !now) initPicked() })
watch(picked, () => { armed.value = false }, { deep: true })

// 三工位实时态：SSE 事件优先（running/done/error），无事件时按工件落地态兜底
//（刷新后 SSE 事件流丢失的场景）。inRun=本次计划是否包含它——不含的工位
// 显示「本次不动」，不参与锁区、也不会被链重写。
type StationState = '' | 'running' | 'done' | 'error'
const active = computed<PrepStation[]>(() => {
  const a = local.value?.active
  if (a) return a // [] = 链已收口（释放锁），不是缺字段
  // 旧后端/对话侧重跑没有 active：保守地按全链锁，避免链在写而工作区可编辑。
  return running.value ? [...ORDER] : []
})
const stations = computed(() => {
  const activeSet = new Set(active.value)
  const st = (key: PrepStation): StationState => {
    const live = props.progress?.[key] as StationState | undefined
    if (live) return live
    if (key === 'script') return local.value?.script ? 'done' : ''
    if (key === 'style') return local.value?.stylepack ? 'done' : ''
    return local.value?.draft ? 'done' : ''
  }
  return [
    { key: 'script' as const, label: '① 文字稿', desc: '吃主题与材料，搜集改写成口播稿', state: st('script'), inRun: activeSet.has('script') },
    { key: 'style' as const, label: '② 风格包', desc: '探索风格库，出样张与设计系统', state: st('style'), inRun: activeSet.has('style') },
    { key: 'storyboard' as const, label: '③ 分镜稿', desc: '切段落，写场景与首末拍', state: st('storyboard'), inRun: activeSet.has('storyboard') },
  ]
})
// 跑动中的「本次生成」以服务端 active 为准：刷新/换页回来后勾选集会被默认值重置，
// 但链实际在跑什么只有 active 知道。统一链没有站序——不画箭头链。
const activeText = computed(() =>
  unified.value ? '三件一起做（AI 自定顺序）' : active.value.map(k => STATION_LABEL[k]).join(' → '))
const stateTag = (s: StationState) =>
  s === 'running' ? '进行中' : s === 'done' ? '完成' : s === 'error' ? '出错' : (unified.value ? '待产出' : '排队中')
// 内容链已返回、但 Async 运行器还在补传/收尾（active 已清空）：锁已放，进度卡收一下，
// 不把三站全标成「本次不动」误导用户。
const finishing = computed(() => running.value && active.value.length === 0)

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

// ── 真图素材：实拍图 / 截图 / 官方图表（不是上面的纯文本参考资料）─────────
// 会作为画面里的真图出现。「必需」= 分镜阶段必须被某一镜认领、成片里必须出现。
// 2026-11-02：此前素材只能手工往 assets/materials.json 塞条目，且写作期对素材
// 的口径是「可选加分」，模型 10 段 9 段整段忽略，成片一张截图都没有——这个开关
// 与分镜认领校验就是那次事故补的。
const images = ref<ImageMaterial[]>([])
const imgBusy = ref(false)
const imgInput = ref<HTMLInputElement | null>(null)
async function loadImages() {
  if (!props.projectId) return
  images.value = (await api.images(props.projectId).catch(() => ({ images: [] }))).images
}
watch(() => props.projectId, loadImages, { immediate: true })
async function onImages(e: Event) {
  const input = e.target as HTMLInputElement
  const files = [...(input.files ?? [])]
  input.value = ''
  for (const f of files) {
    if (!/\.(jpe?g|png)$/i.test(f.name)) {
      message.error(`只收 JPEG / PNG 图片：${f.name}`)
      continue
    }
    if (f.size > 20 * 1024 * 1024) {
      message.error(`图片过大（>20MB）：${f.name}`)
      continue
    }
    imgBusy.value = true
    try {
      // 默认勾「必需」：上传画面素材的本意就是让它出现在成片里，
      // 静默不用是更糟的失败（事故形态）。
      const r = await api.uploadImage(props.projectId, f, { title: f.name.replace(/\.[^.]+$/, ''), required: true })
      images.value = r.materials
    } catch (err) {
      message.error(`${f.name}：${(err as Error).message}`)
    } finally {
      imgBusy.value = false
    }
  }
}
async function toggleRequired(m: ImageMaterial, required: boolean) {
  if (imgBusy.value) return
  imgBusy.value = true
  try {
    const r = await api.updateImage(props.projectId, m.file, { required })
    images.value = r.materials
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    imgBusy.value = false
  }
}
async function removeImage(m: ImageMaterial) {
  if (imgBusy.value) return
  imgBusy.value = true
  try {
    const r = await api.deleteImage(props.projectId, m.file)
    images.value = r.materials
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    imgBusy.value = false
  }
}

const starting = ref(false)
const buttonLabel = computed(() => {
  if (!plan.value.length) return '先勾选要生成的工件'
  if (armed.value) return '再点一次，确认重做'
  if (willReplace.value) return '生成（会重做勾选的工件）'
  return plan.value.length === 3 ? '让 AI 自动完成准备' : '让 AI 生成勾选的工件'
})
async function start() {
  if (starting.value || running.value || !props.projectId) return
  const stations = plan.value
  if (!stations.length) {
    message.warning('先勾选至少一个工件——勾选 = 让 AI 生成/重做它')
    return
  }
  // 勾选里有已落盘工件 = 覆盖性重做：两段式确认（3s 内再点一次才真跑）。
  if (willReplace.value && !armed.value) {
    armed.value = true
    if (armTimer) clearTimeout(armTimer)
    armTimer = window.setTimeout(() => { armed.value = false }, 3000)
    return
  }
  armed.value = false
  if (armTimer) { clearTimeout(armTimer); armTimer = null }
  starting.value = true
  try {
    await api.runPrep(props.projectId, idea.value.trim(), willReplace.value, stations)
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
        <p class="sub">
          本次生成：<b>{{ activeText || planText || '—' }}</b>。一般 15~25 分钟（出样张的轮次多时更久）；
          跑动期间对应工作区只读，AI 结束会自动解锁，届时可以自己改。
        </p>
        <ol v-if="!finishing" class="stations">
          <li v-for="s in stations" :key="s.key" :class="s.inRun ? (s.state || 'pending') : 'idle'">
            <span class="mark">{{ !s.inRun ? '·' : s.state === 'done' ? '✓' : s.state === 'error' ? '✗' : '◐' }}</span>
            <span class="st-main">
              <b>{{ s.label }}</b>
              <i>{{ s.desc }}</i>
            </span>
            <span class="st-tag">{{ s.inRun ? stateTag(s.state) : '本次不动' }}</span>
          </li>
        </ol>
        <p v-else class="sub">AI 已结束，正在收尾——工作区已解锁，可以先去改起来。</p>
        <div class="row">
          <NButton size="small" quaternary @click="cancel">取消</NButton>
        </div>
      </template>

      <template v-else>
        <h3>说说这个视频的主题</h3>
        <p class="sub">
          想讲什么、给谁看、什么感觉——想到哪写到哪；<b>留空就让 AI 自由发挥</b>。
          <template v-if="unified">
            这是<b>统一链</b>项目（实验档）：三件由同一个 AI 一起做，<b>顺序与返工节奏由 AI 自己定</b>。
          </template>
          <template v-else>
            勾选要生成的工件，AI 会按 <b>① 文稿 → ② 风格 → ③ 分镜</b> 的顺序自动跑；缺的前置会自动补跑。
          </template>
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
        <!-- 真图素材：会作为画面里的实拍图/截图出现；「必需」= 分镜必须认领、成片必须出现 -->
        <div class="mats">
          <div class="mats-head">
            <span class="mats-title">画面素材<i>实拍图 / 截图 / 官方图表——会作为画面里的真图出现；勾「必需」就一定会进成片</i></span>
            <button class="mat-add" :disabled="imgBusy || running" @click="imgInput?.click()">＋ 上传图片</button>
            <input ref="imgInput" type="file" accept="image/jpeg,image/png" multiple hidden @change="onImages">
          </div>
          <ul v-if="images.length" class="mat-list">
            <li v-for="m in images" :key="m.file">
              <img class="mat-thumb" :src="`/api/projects/${projectId}/file/${m.file}`" :alt="m.title ?? m.file" loading="lazy">
              <span class="mat-name" :title="m.file">{{ m.title || m.file }}</span>
              <label class="mat-req" :class="{ on: m.required }">
                <input
                  type="checkbox" :checked="!!m.required" :disabled="imgBusy || running"
                  @change="toggleRequired(m, ($event.target as HTMLInputElement).checked)"
                >
                必需
              </label>
              <button class="mat-del" :disabled="imgBusy || running" title="删除这张素材" @click="removeImage(m)">✕</button>
            </li>
          </ul>
          <p v-else class="mat-empty">还没传图片——GitHub 截图、官方架构图、数据表格都行；勾了「必需」的会被分镜写进对应镜头</p>
        </div>

        <!-- 勾选集 + 生成按钮（按钮旁三个选项：勾谁生成谁；勾选即重做） -->
        <div class="gen">
          <div class="gen-opts">
            <span class="gen-title">本次生成</span>
            <NCheckbox v-model:checked="picked.script">① 文稿</NCheckbox>
            <NCheckbox v-model:checked="picked.style">② 风格</NCheckbox>
            <NCheckbox v-model:checked="picked.storyboard">③ 分镜</NCheckbox>
          </div>
          <NButton
            :type="armed ? 'error' : 'primary'" :loading="starting" :disabled="!plan.length" @click="start"
          >{{ buttonLabel }}</NButton>
        </div>
        <p class="plan-line" :class="{ 'plan-warn': willReplace }">
          <template v-if="plan.length">
            将生成：<b>{{ planText }}</b><template v-if="unified">（统一链：三件一锅端，AI 自定顺序与返工节奏）</template><template v-else-if="autoAdded.length">（{{ autoAdded.map(k => STATION_LABEL[k]).join('、') }}自动带上：缺件补跑，或上游重做后必须跟跑）</template>。运行期间对应工作区锁定只读，结束后自动解锁。
          </template>
          <template v-else>先勾选至少一个工件——勾选 = 让 AI 生成/重做它。</template>
        </p>
        <p v-if="willReplace && plan.length" class="note">
          <template v-if="dropsDownstream">
            勾选里有已就绪的工件：重做上游会级联作废它的下游（例如重做文稿会让风格/分镜失效）。
            未勾的下游不会自动重做，会留在空白，等下次再勾。
          </template>
          <template v-else>
            勾选里有已就绪的工件：本次会覆盖重做它（两段式确认，第一枪只亮确认态）。
          </template>
        </p>
        <div v-if="ready" class="row ready-row">
          <span class="ok-line">✓ 三工件已就绪</span>
          <NButton size="small" @click="emit('go-tab', 'review')">去终审确认</NButton>
        </div>
        <p v-else-if="exists" class="note">已有部分工件——默认只勾缺的；想重做已就绪的，直接勾上再点生成。</p>
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

/* 勾选行：三个选项贴着生成按钮；窄屏自动换行 */
.gen { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; }
.gen-opts { flex: 1; min-width: 260px; display: flex; align-items: center; gap: 14px; flex-wrap: wrap; }
.gen-title { font-size: 12px; font-weight: 600; color: var(--ink); letter-spacing: .04em; }
.plan-line { font-size: 12px; color: var(--muted); line-height: 1.7; }
.plan-line b { color: var(--accent); font-weight: 600; }
.plan-line.plan-warn b { color: var(--warn); }
.ready-row { justify-content: space-between; border-top: 1px solid var(--line-soft); padding-top: 10px; }
.ok-line { font-size: 12.5px; color: var(--ok); font-weight: 600; }

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
/* 画面素材行：缩略图 + 标题 + 「必需」开关（必需=分镜必须认领，成片必须出现） */
.mat-thumb {
  flex: none; width: 48px; height: 36px; object-fit: cover;
  border: 1px solid var(--line-soft); background: var(--surface);
}
.mat-req {
  flex: none; display: inline-flex; align-items: center; gap: 4px;
  font-size: 11px; color: var(--muted); cursor: pointer; user-select: none;
  border: 1px solid var(--line-soft); padding: 2px 6px;
}
.mat-req.on { color: var(--accent); border-color: var(--accent); font-weight: 600; }
.mat-req input { accent-color: var(--accent); cursor: pointer; margin: 0; }
.mat-req input:disabled { cursor: default; }

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
/* 本次不动的工位：留在清单里做对照，但压暗——用户要知道「保留 ≠ 重写」 */
.stations li.idle { opacity: .48; }
.stations li.idle .mark { color: var(--muted); }
.stations li.idle .st-tag { color: var(--muted); }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
