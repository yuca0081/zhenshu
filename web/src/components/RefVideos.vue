<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useMessage, NButton, NTag } from 'naive-ui'
import SampleFrame from './SampleFrame.vue'
import TemplateDetail from './TemplateDetail.vue'
import { api } from '../api'
import type { RefVideo, Style, TemplateDetailData } from '../types'

const props = defineProps<{ styles: Style[] }>()
const emit = defineEmits<{ ingested: [] }>()

const refs = ref<RefVideo[]>([])
const uploading = ref(false)
const fileEl = ref<HTMLInputElement | null>(null)
let timer: number | undefined
let dead = false // 卸载标志：refresh 的 await 期间销毁组件时，返回后不得再排下一轮

async function refresh() {
  const list = await api.listReferences().catch(() => refs.value)
  if (dead) return
  refs.value = list
  schedule()
}
function schedule() {
  window.clearTimeout(timer)
  const busy = refs.value.some(r => r.status === 'pending' || r.status === 'running')
  if (busy) timer = window.setTimeout(refresh, 3000) // 有在跑的才轮询
}
onMounted(refresh)
onUnmounted(() => { dead = true; window.clearTimeout(timer) })

const message = useMessage()

function pick() { fileEl.value?.click() }
async function onFile(e: Event) {
  const f = (e.target as HTMLInputElement).files?.[0]
  ;(e.target as HTMLInputElement).value = ''
  if (!f) return
  uploading.value = true
  try {
    await api.uploadReference(f)
    await refresh()
    // 上传后立刻起一次忙轮询（refresh 内部已按 status 判断）
  } catch (err) {
    message.error(`上传失败：${(err as Error).message}`)
  } finally {
    uploading.value = false
  }
}

async function retry(id: string) {
  await api.analyzeReference(id).catch(e => message.error(`重试失败：${e.message}`))
  await refresh()
}
async function ingest(r: RefVideo) {
  try {
    await api.confirmReference(r.id)
    await refresh()
    show.value = false // 入库后 refs 刷新，关抽屉避免状态过期
    emit('ingested') // 新风格出现在「风格库」tab
  } catch (e) {
    message.error(`入库失败：${(e as Error).message}`)
  }
}

// 点卡片开详情抽屉：ref.json 自带全套（tokens/双样张/5版式）；文件名是 BV 号则给原片跳转
const show = ref(false)
const detail = ref<TemplateDetailData | null>(null)
function bvURL(name: string): { label: string; href: string } | null {
  const m = /^(BV[0-9A-Za-z]+)\.\w+$/.exec(name)
  return m ? { label: 'B站原片 ' + m[1], href: `https://www.bilibili.com/video/${m[1]}` } : null
}
function open(r: RefVideo) {
  if (r.status !== 'done' || !r.style) return
  const bv = bvURL(r.name)
  const status = { done: { label: '已解析', type: 'success' }, error: { label: '失败', type: 'error' } } as const
  detail.value = {
    title: r.style.direction,
    status: status[r.status as 'done' | 'error'] ?? { label: '解析中', type: 'warning' },
    direction: r.style.direction,
    genre: r.style.genre,
    keywords: r.style.keywords,
    layouts: r.style.layouts,
    tokens: r.style.tokens ?? null,
    samples: r.style.samples,
    originLabel: bv?.label ?? r.name,
    originHref: bv?.href,
    createdAt: new Date(r.created_at).toLocaleString(),
    desc: `从参考视频「${r.name}」解析提炼`,
    refId: r.id,
    confirmed: r.confirmed,
  }
  show.value = true
}
</script>

<template>
  <section class="rv">
    <header class="head">
      <div>
        <h2>视频解析</h2>
        <p>上传一支参考视频，AI 抽帧分析构图/色彩/节奏，提炼成风格——确认入库后新建项目可选。点击卡片看完整风格与原片抽帧。</p>
      </div>
      <NButton type="primary" :loading="uploading" @click="pick">上传视频</NButton>
      <input ref="fileEl" type="file" accept="video/*" hidden @change="onFile" />
    </header>

    <div v-if="!refs.length" class="none">还没有解析记录。上传一支 mp4/mov/webm 试试（建议 ≤ 200MB）。</div>
    <div v-else class="grid">
      <figure v-for="r in refs" :key="r.id" class="card" :class="{ bad: r.status === 'error' }" @click="open(r)">
        <template v-if="r.status === 'done' && r.style">
          <SampleFrame :html="r.style.samples[0]?.html ?? ''" />
        </template>
        <div v-else class="ph">{{ r.status === 'error' ? '解析失败' : '解析中…' }}</div>
        <figcaption>
          <div class="row1">
            <b :title="r.name">{{ r.name }}</b>
            <NTag size="small" :bordered="false" :type="r.status === 'done' ? 'success' : r.status === 'error' ? 'error' : 'warning'">
              {{ r.status === 'done' ? '已解析' : r.status === 'error' ? '失败' : '解析中' }}
            </NTag>
          </div>
          <template v-if="r.status === 'done' && r.style">
            <span class="dir">{{ r.style.direction }}</span>
            <span class="desc">{{ r.style.genre }}</span>
            <ul class="layouts">
              <li v-for="l in r.style.layouts.slice(0, 3)" :key="l.id" :title="l.desc">{{ l.name }}：{{ l.desc }}</li>
              <li v-if="r.style.layouts.length > 3" class="more">…共 {{ r.style.layouts.length }} 个版式</li>
            </ul>
          </template>
          <span v-if="r.status === 'error'" class="err">{{ r.error }}</span>
          <div class="row2">
            <span class="origin">{{ new Date(r.created_at).toLocaleString() }}</span>
            <span class="btns">
              <NButton v-if="r.status === 'error'" size="small" ghost @click.stop="retry(r.id)">重试</NButton>
              <NButton v-if="r.status === 'done' && !r.confirmed" size="small" type="primary" ghost @click.stop="ingest(r)">
                入库
              </NButton>
              <NTag v-if="r.confirmed" size="small" :bordered="false" type="success">已入库</NTag>
            </span>
          </div>
        </figcaption>
      </figure>
    </div>

    <TemplateDetail :show="show" :detail="detail" @close="show = false"
      @ingest="id => { const r = refs.find(x => x.id === id); if (r) ingest(r) }" />
  </section>
</template>

<style scoped>
.rv { flex: 1; min-height: 0; overflow: auto; padding: 24px 28px; }
.head { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 18px; }
.head h2 { font-size: 16px; color: #d9d9e0; }
.head p { font-size: 12.5px; color: #7c7c88; margin: 4px 0 0; }
.none { color: #55555f; font-size: 13px; padding: 40px 0; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); gap: 16px; }
.card {
  background: #14141a; border: 1px solid #232329; border-radius: 12px; overflow: hidden;
  cursor: pointer; transition: border-color .15s;
}
.card:hover { border-color: #3a3a44; }
.card.bad { border-color: #5c2424; cursor: default; }
.ph { height: 169px; display: grid; place-items: center; color: #6f6f7c; font-size: 13px; background: #0d0d12; }
figcaption { padding: 10px 12px 12px; display: flex; flex-direction: column; gap: 6px; }
.row1 { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.row1 b { font-size: 13px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.dir { font-size: 12.5px; color: #e0c985; }
.desc { font-size: 11.5px; color: #8a8a96; }
.layouts { margin: 0; padding: 0; list-style: none; display: flex; flex-direction: column; gap: 2px; }
.layouts li { font-size: 11px; color: #7c7c88; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.layouts li.more { color: #55555f; }
.err { font-size: 11.5px; color: #d98a8a; word-break: break-all; }
.row2 { display: flex; align-items: center; justify-content: space-between; }
.origin { font-size: 11px; color: #55555f; }
.btns { display: flex; gap: 8px; align-items: center; }
</style>
