<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NButton, NDrawer, NDrawerContent, NModal, NTag } from 'naive-ui'
import SampleFrame from './SampleFrame.vue'
import { api, libraryMediaURL, referenceMediaURL } from '../api'
import type { StyleTokens, TemplateDetailData } from '../types'

// 风格详情抽屉：风格库/视频解析卡片点开共用。纯展示——数据由父组件
// 规范化成 TemplateDetailData（tokens/layouts 已 join 好），这里只渲染与联动。
const props = defineProps<{ show: boolean; detail: TemplateDetailData | null }>()
const emit = defineEmits<{
  close: []
  publish: [packId: string]
  ingest: [refId: string]
}>()

// 抽帧截图：风格自带媒体（libMediaId）优先，参考引用（refId）次之
const frames = ref<string[]>([])
const hasVideo = ref(false)
const mediaURL = computed(() =>
  props.detail?.libMediaId ? libraryMediaURL : referenceMediaURL,
)
watch(
  () => [props.show, props.detail?.libMediaId, props.detail?.refId] as const,
  async ([show, libId, refId]) => {
    frames.value = []
    hasVideo.value = false
    if (!show || (!libId && !refId)) return
    const r = libId
      ? await api.libraryFrames(libId).catch(() => null)
      : await api.referenceFrames(refId!).catch(() => null)
    if (r) { frames.value = r.frames; hasVideo.value = r.has_video }
  },
  { immediate: true },
)

// 样张点击放大：iframe 原生 960 宽展示
const zoom = ref<{ tag: string; html: string } | null>(null)

const t = computed<StyleTokens | null>(() => props.detail?.tokens ?? null)
const bgGrad = computed(() => {
  const bg = t.value?.bg
  if (!bg?.from) return ''
  const ang = bg.angle ?? 0
  return bg.type === 'solid' || !bg.to
    ? bg.from
    : `linear-gradient(${ang}deg, ${bg.from}, ${bg.to})`
})
const swatches = computed(() => {
  const s = t.value
  if (!s) return []
  const out: { hex: string; label: string }[] = []
  if (s.ink) out.push({ hex: s.ink, label: 'ink' })
  if (s.txt) out.push({ hex: s.txt, label: 'txt' })
  if (s.primary) out.push({ hex: s.primary, label: 'primary' })
  if (s.surface) out.push({ hex: s.surface, label: 'surface' })
  for (const [i, hex] of (s.accent ?? []).entries()) out.push({ hex, label: `accent${i + 1}` })
  return out
})

const copied = ref('')
let copiedTimer: number | undefined
async function copyHex(hex: string) {
  try { await navigator.clipboard.writeText(hex) } catch { /* 剪贴板不可用就算了 */ }
  copied.value = hex
  window.clearTimeout(copiedTimer)
  copiedTimer = window.setTimeout(() => (copied.value = ''), 1200)
}
</script>

<template>
  <NDrawer :show="props.show" :width="720" placement="right" @update:show="v => !v && emit('close')">
    <NDrawerContent title="风格详情" closable :native-scrollbar="false" class="tdrawer">
      <template v-if="detail">
        <!-- 头部 -->
        <header class="head">
          <div class="title-row">
            <h2>{{ detail.title }}</h2>
            <NTag size="small" :bordered="false" :type="detail.status.type">{{ detail.status.label }}</NTag>
          </div>
          <div class="meta-row">
            <span v-if="detail.direction" class="direction">{{ detail.direction }}</span>
            <a v-if="detail.originHref" :href="detail.originHref" target="_blank" rel="noreferrer">{{ detail.originLabel }}↗</a>
            <span v-else class="origin">{{ detail.originLabel }}</span>
            <span v-if="detail.createdAt" class="time">{{ detail.createdAt }}</span>
          </div>
          <div class="act-row">
            <NTag v-for="x in detail.extraTags ?? []" :key="x" size="tiny" :bordered="false" type="info">{{ x }}</NTag>
            <NButton v-if="detail.packId && !detail.published" size="small" type="primary" ghost
              @click="emit('publish', detail.packId)">入库</NButton>
            <NButton v-if="detail.refId && !detail.confirmed" size="small" type="primary" ghost
              @click="emit('ingest', detail.refId)">入库</NButton>
          </div>
          <p v-if="detail.desc" class="desc">{{ detail.desc }}</p>
        </header>

        <!-- 样张（点击放大） -->
        <section class="sec">
          <h3>风格样张{{ detail.samples.length > 1 ? ` ×${detail.samples.length}` : '' }}</h3>
          <div class="samples">
            <figure v-for="s in detail.samples" :key="s.tag" class="sample" @click="zoom = s">
              <SampleFrame :html="s.html" />
              <figcaption>
                <b>{{ s.tag }}</b>
                <span v-if="s.desc">{{ s.desc }}</span>
                <span class="zoom-hint">点击放大</span>
              </figcaption>
            </figure>
          </div>
        </section>

        <!-- 风格 tokens -->
        <section v-if="t" class="sec">
          <h3>风格 tokens</h3>
          <div v-if="bgGrad" class="grad" :style="{ background: bgGrad }">
            <span class="grad-hex">{{ t.bg?.from }}{{ t.bg?.to ? ` → ${t.bg.to}` : '' }}</span>
          </div>
          <div class="chips">
            <button v-for="sw in swatches" :key="sw.label" class="swatch" :title="`点击复制 ${sw.hex}`"
              @click="copyHex(sw.hex)">
              <i :style="{ background: sw.hex }" />
              <em>
                {{ sw.hex }}
                <small>{{ sw.label }}</small>
              </em>
              <transition name="fade">
                <u v-if="copied === sw.hex">已复制</u>
              </transition>
            </button>
          </div>
          <div class="tok-rows">
            <div v-if="t.font" class="tok-row"><label>字体</label><code :style="{ fontFamily: t.font }">{{ t.font }}</code></div>
            <div v-if="t.bg?.fx?.length" class="tok-row"><label>背景特效</label>
              <NTag v-for="f in t.bg.fx" :key="f" size="tiny" :bordered="false">{{ f }}</NTag>
            </div>
            <div v-if="t.cap" class="tok-row"><label>字幕条</label>
              <NTag size="tiny" :bordered="false" type="info">{{ t.cap.mode || 'plain' }}</NTag>
              <i v-if="t.cap.bg" class="dot" :style="{ background: t.cap.bg }" :title="`字幕底 ${t.cap.bg}`" />
              <code class="mini">{{ t.cap.color }}</code>
            </div>
            <div v-if="t.radius != null || t.border_w != null" class="tok-row"><label>形制</label>
              <code class="mini">radius {{ t.radius ?? '-' }} · border {{ t.border_w ?? 0 }}</code>
            </div>
          </div>
        </section>
        <section v-else-if="detail.samples.length" class="sec">
          <p class="degrade">此包未挂注册表 tokens（项目提炼的旧包），以样张气质为准。</p>
        </section>

        <!-- 设计系统（视频解析新条目才有——神似心得：概念/字体三角色/动效/装置/负面清单） -->
        <section v-if="detail.concept || detail.fonts || detail.motion || detail.device || detail.anti_patterns?.length" class="sec">
          <h3>设计系统</h3>
          <p v-if="detail.concept" class="concept">{{ detail.concept }}</p>
          <div v-if="detail.fonts" class="tok-rows">
            <div v-if="detail.fonts.display?.stack" class="tok-row"><label>大标题</label>
              <code :style="{ fontFamily: detail.fonts.display.stack }">{{ detail.fonts.display.stack }}</code>
            </div>
            <div v-if="detail.fonts.body?.stack" class="tok-row"><label>正文</label>
              <code :style="{ fontFamily: detail.fonts.body.stack }">{{ detail.fonts.body.stack }}</code>
            </div>
            <div v-if="detail.fonts.data?.stack" class="tok-row"><label>数据</label>
              <code :style="{ fontFamily: detail.fonts.data.stack }">{{ detail.fonts.data.stack }}</code>
            </div>
          </div>
          <div v-if="detail.motion" class="tok-rows">
            <div class="tok-row"><label>动效语言</label>
              <code class="mini">{{ detail.motion.ease }} · 进场 {{ detail.motion.dur_in }}s / 落定 {{ detail.motion.dur_settle }}s / 错峰 {{ detail.motion.stagger }}s</code>
            </div>
          </div>
          <p v-if="detail.motion?.notes" class="notes">{{ detail.motion.notes }}</p>
          <div v-if="detail.device" class="device">
            <p class="device-name">贯穿装置 · {{ detail.device.name }}</p>
            <p v-if="detail.device.desc" class="device-desc">{{ detail.device.desc }}</p>
            <p v-if="detail.device.per_seg" class="device-desc">逐段：{{ detail.device.per_seg }}</p>
          </div>
          <div v-if="detail.anti_patterns?.length" class="tok-row anti-row">
            <label>负面清单</label>
            <NTag v-for="a in detail.anti_patterns" :key="a" size="tiny" :bordered="false" type="warning">{{ a }}</NTag>
          </div>
        </section>

        <!-- 版式 -->
        <section v-if="detail.layouts?.length" class="sec">
          <h3>构图版式 ×{{ detail.layouts.length }}</h3>
          <ul class="layouts">
            <li v-for="l in detail.layouts" :key="l.id">
              <b>{{ l.name }}</b>
              <span>{{ l.desc }}</span>
            </li>
          </ul>
        </section>

        <!-- 关键词 / 题材 / 配图 -->
        <section v-if="detail.genre || detail.keywords?.length" class="sec">
          <h3>题材与关键词</h3>
          <p v-if="detail.genre" class="kv"><label>适用题材</label>{{ detail.genre }}</p>
          <div v-if="detail.keywords?.length" class="chips kw">
            <NTag v-for="k in detail.keywords" :key="k" size="small" :bordered="false">{{ k }}</NTag>
          </div>
        </section>

        <!-- 参考片抽帧 + 原片 -->
        <section v-if="detail.refId || detail.libMediaId" class="sec">
          <h3>参考片抽帧{{ frames.length ? ` ×${frames.length}` : '' }}</h3>
          <div v-if="frames.length" class="frames">
            <img v-for="f in frames" :key="f" :src="mediaURL(detail.libMediaId ?? detail.refId!, f)" loading="lazy" alt="" />
          </div>
          <p v-else class="degrade">抽帧文件缺失。</p>
          <video v-if="hasVideo" class="ref-video" :src="mediaURL(detail.libMediaId ?? detail.refId!, 'video.mp4')"
            controls preload="none" />
        </section>

      </template>
    </NDrawerContent>
  </NDrawer>

  <!-- 样张放大：原生 960 宽 -->
  <NModal :show="!!zoom" @update:show="v => !v && (zoom = null)">
    <div class="zoombox" @click="zoom = null">
      <div v-if="zoom" class="zoom-inner">
        <SampleFrame :html="zoom.html" />
        <p>{{ zoom.tag }}（点击任意处关闭）</p>
      </div>
    </div>
  </NModal>
</template>

<style scoped>
.tdrawer :deep(.n-drawer-header) { background: #101016; border-color: #232329; }
.tdrawer { background: #101016; color: #c9c9d1; }
.head { display: flex; flex-direction: column; gap: 8px; }
.title-row { display: flex; align-items: center; gap: 10px; }
.title-row h2 { font-size: 17px; color: #e8e8ee; margin: 0; }
.meta-row { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; font-size: 12px; color: #7c7c88; }
.meta-row .direction { color: #e0c985; }
.meta-row a { color: #7ea6d9; text-decoration: none; }
.act-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.desc { font-size: 12.5px; color: #8a8a96; margin: 0; }

.sec { margin-top: 22px; }
.sec h3 { font-size: 13px; color: #9a9aa6; margin: 0 0 10px; font-weight: 600; }
.samples { display: grid; gap: 14px; }
.sample { margin: 0; cursor: zoom-in; border: 1px solid #232329; border-radius: 10px; overflow: hidden; }
.sample:hover { border-color: #3a3a44; }
.sample figcaption { display: flex; gap: 10px; align-items: baseline; padding: 8px 10px; font-size: 11.5px; color: #7c7c88; }
.sample figcaption b { color: #c9c9d1; }
.zoom-hint { margin-left: auto; color: #55555f; }

.grad { height: 44px; border-radius: 8px; display: flex; align-items: flex-end; padding: 6px 10px; }
.grad-hex { font-size: 11px; color: #fff; text-shadow: 0 1px 3px rgba(0,0,0,.8); font-family: monospace; }
.chips { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 10px; }
.swatch { display: flex; align-items: center; gap: 7px; background: #14141a; border: 1px solid #232329; border-radius: 8px; padding: 5px 9px 5px 6px; cursor: copy; position: relative; }
.swatch:hover { border-color: #3a3a44; }
.swatch i { width: 22px; height: 22px; border-radius: 5px; border: 1px solid rgba(255,255,255,.12); display: block; }
.swatch em { font-style: normal; font-size: 11px; font-family: monospace; color: #c9c9d1; display: flex; flex-direction: column; line-height: 1.25; }
.swatch em small { color: #6f6f7c; font-family: inherit; }
.swatch u { position: absolute; top: -8px; right: -6px; text-decoration: none; font-size: 10px; background: #2b2b35; color: #9fd99f; border-radius: 6px; padding: 1px 6px; }
.fade-enter-active, .fade-leave-active { transition: opacity .3s; }
.fade-enter-from, .fade-leave-to { opacity: 0; }

.tok-rows { display: flex; flex-direction: column; gap: 7px; margin-top: 12px; }
.tok-row { display: flex; align-items: center; gap: 8px; font-size: 12px; }
.tok-row label { flex: none; width: 58px; color: #6f6f7c; }
.tok-row code { font-size: 11.5px; color: #a8b6c9; background: #17171e; padding: 2px 8px; border-radius: 6px; }
.tok-row code.mini { color: #8a8a96; }
.tok-row .dot { width: 16px; height: 16px; border-radius: 4px; border: 1px solid rgba(255,255,255,.12); display: block; }

.layouts { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
.layouts li { display: flex; gap: 10px; font-size: 12px; align-items: baseline; }
.layouts li b { flex: none; color: #d9d9e0; min-width: 72px; }
.layouts li span { color: #8a8a96; }

.concept { font-size: 12.5px; color: #c9c9d1; margin: 0; line-height: 1.6; }
.notes { font-size: 12px; color: #8a8a96; margin: 6px 0 0; line-height: 1.6; }
.device { background: #14141a; border: 1px solid #232329; border-radius: 8px; padding: 10px 12px; margin-top: 10px; }
.device-name { margin: 0; font-size: 12.5px; color: #e0c985; }
.device-desc { margin: 4px 0 0; font-size: 12px; color: #8a8a96; line-height: 1.6; }
.anti-row { flex-wrap: wrap; margin-top: 10px; }
.kv { display: flex; gap: 10px; font-size: 12.5px; margin: 0 0 6px; color: #c9c9d1; }
.kv label { flex: none; width: 58px; color: #6f6f7c; }
.chips.kw { margin-top: 4px; }

.frames { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
.frames img { width: 100%; height: auto; display: block; border-radius: 6px; border: 1px solid #232329; background: #0a0a10; }
.ref-video { width: 100%; margin-top: 10px; border-radius: 8px; background: #000; max-height: 320px; }
.degrade { font-size: 12px; color: #6f6f7c; margin: 0; }

.zoombox { position: fixed; inset: 0; display: grid; place-items: center; cursor: zoom-out; }
.zoom-inner { width: min(960px, 92vw); background: #101016; border: 1px solid #2a2a34; border-radius: 12px; padding: 14px; }
.zoom-inner p { text-align: center; font-size: 12px; color: #7c7c88; margin: 10px 0 0; }
</style>
