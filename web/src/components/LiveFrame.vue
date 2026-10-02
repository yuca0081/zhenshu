<script setup lang="ts">
// 修改态活合成物（plan §4.3 预览双模）：
// 把磁盘上的单段帧 HTML（<template> 片段，惰性）包进可执行文档（srcdoc 壳，模板在 live-shell.ts），
// 壳内引项目本地 gsap（与渲染引擎同一份）+ 激活/交互脚本 —— 时间轴 seek、悬停高亮、点选命名元素。
// sandbox="allow-scripts"（无同源）：父子只走 postMessage，协议 live:ready / live:seek / live:pick / live:error。
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api } from '../api'
import { shellDoc } from '../live-shell'

const props = defineProps<{
  videoId: string
  segId: string // 'seg03'
  localTime: number // 段内时刻（秒）
  rev?: number // 重做完成后 bump，强制重载帧
  transition?: string // 成片转场模式（projectView）：fade=复刻页内黑场
  post?: { vignette?: number; grain?: number; chroma?: number } | null // stitch 后期妆（与 main.mp4 同源）
}>()
const emit = defineEmits<{
  ready: [segId: string, duration: number]
  pick: [p: { segId: string; id: string; name: string; t: number }]
  lasso: [p: { segId: string; t: number; picks: { id: string; name: string }[] }]
}>()

const iframeEl = ref<HTMLIFrameElement | null>(null)
const srcdoc = ref('')
const errText = ref('')
const readySeg = ref('')
const lasso = ref(false) // 圈选模式（父层按钮开关，壳内拖框 → live:lasso 上报）

let pendingT: number | null = null
let raf = 0
let loadSeq = 0 // 加载序号：检视态快速跨段拖动时旧响应迟到，不得覆盖新段 srcdoc

function postSeek(t: number) {
  pendingT = t
  if (!raf) raf = requestAnimationFrame(flush)
}
function postLassoMode() {
  if (readySeg.value && iframeEl.value?.contentWindow) {
    iframeEl.value.contentWindow.postMessage({ type: 'live:lasso-on', on: lasso.value }, '*')
  }
}
watch(lasso, postLassoMode)
function flush() {
  raf = 0
  if (pendingT == null) return
  if (readySeg.value !== props.segId || !iframeEl.value?.contentWindow) return // 未 ready：挂起，ready 后应用
  iframeEl.value.contentWindow.postMessage({ type: 'live:seek', t: pendingT }, '*')
  pendingT = null
}

async function load() {
  const seq = ++loadSeq
  readySeg.value = ''
  errText.value = ''
  srcdoc.value = ''
  const frag = await api
    .frameText(props.videoId, props.segId)
    .catch(() => null)
  if (seq !== loadSeq) return // 已切到别的段：丢弃旧响应
  if (frag == null) {
    errText.value = `段 ${props.segId} 合成物加载失败`
    return
  }
  srcdoc.value = shellDoc(props.videoId, props.segId, frag, props.transition ?? 'fade', props.post ?? null)
}

function onMessage(e: MessageEvent) {
  if (!iframeEl.value || e.source !== iframeEl.value.contentWindow) return
  const d = e.data as { type?: string; seg?: string; duration?: number; id?: string; name?: string; t?: number; error?: string; picks?: { id: string; name: string }[] }
  switch (d.type) {
    case 'live:ready':
      if (d.seg) readySeg.value = d.seg
      emit('ready', d.seg ?? props.segId, d.duration ?? 0)
      flush() // 应用挂起的 seek
      postLassoMode() // 重载后恢复圈选模式
      break
    case 'live:pick':
      emit('pick', { segId: d.seg ?? props.segId, id: d.id ?? '', name: d.name ?? '', t: d.t ?? 0 })
      break
    case 'live:lasso':
      emit('lasso', { segId: d.seg ?? props.segId, t: d.t ?? 0, picks: d.picks ?? [] })
      break
    case 'live:lasso-off':
      lasso.value = false // 壳内空拖取消：同步按钮态
      break
    case 'live:error':
      errText.value = d.error ?? '活合成物初始化失败'
      break
  }
}

// Esc 退出圈选（window 级监听只在圈选中挂）
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') lasso.value = false
}

onMounted(() => {
  window.addEventListener('message', onMessage)
  window.addEventListener('keydown', onKey)
})
onBeforeUnmount(() => {
  window.removeEventListener('message', onMessage)
  window.removeEventListener('keydown', onKey)
  if (raf) cancelAnimationFrame(raf)
})

watch(() => [props.segId, props.rev, props.videoId], load, { immediate: true })
// immediate：挂载时就把初始时刻挂起（pendingT），ready 后 flush 应用——
// 否则壳停在 t=0（段首黑场转场时整框近黑，正是检视与视频对不上的观感）
watch(() => props.localTime, postSeek, { immediate: true })
</script>

<template>
  <div v-if="errText" class="live-err">{{ errText }}</div>
  <template v-else-if="srcdoc">
    <iframe
      ref="iframeEl"
      :key="`${segId}-${rev ?? 0}`"
      class="live-frame"
      :srcdoc="srcdoc"
      sandbox="allow-scripts"
    />
    <!-- 圈选开关：检视态常驻左上（绝对定位于最近定位祖先 .player），开启后壳内拖框 → 命中元素批量进引用 -->
    <button v-if="readySeg && !lasso" class="lasso-btn" @click="lasso = true">▭ 圈选元素</button>
    <div v-else-if="readySeg && lasso" class="lasso-tip">
      拖框圈住要改的元素（可一次多个）；空点或 Esc 退出
    </div>
  </template>
  <div v-else class="live-loading">活合成物载入中…</div>
</template>

<style scoped>
.live-frame {
  width: 100%;
  border: 0;
  border-radius: 8px;
  background: #000;
  display: block;
  aspect-ratio: 16 / 9;
}
.lasso-btn, .lasso-tip {
  position: absolute; left: 10px; top: 10px; z-index: 4;
  font-size: 12px; padding: 4px 12px; border-radius: 999px;
  border: 1px solid rgba(240, 198, 116, .45); background: rgba(16, 14, 8, .78); color: #f0c674;
  backdrop-filter: blur(3px);
}
.lasso-btn { cursor: pointer; }
.lasso-btn:hover, .lasso-tip { background: rgba(42, 36, 22, .92); }
.live-loading,
.live-err {
  width: 100%;
  aspect-ratio: 16 / 9;
  border-radius: 8px;
  background: #0a0a0e;
  color: #7c7c88;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 13px;
}
.live-err { color: #ff9d9d; }
</style>
