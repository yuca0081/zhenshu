<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useMessage, NButton, NTag } from 'naive-ui'
import TemplateDetail from './TemplateDetail.vue'
import { api } from '../api'
import type { RefVideo, RefVideoSegment, RefVideoStyle, StyleTags, TemplateDetailData, VocabEntry } from '../types'

const emit = defineEmits<{ ingested: [] }>()

const refs = ref<RefVideo[]>([])
const vocab = ref<VocabEntry[]>([])
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
onMounted(() => { refresh(); api.listVocab().then(v => (vocab.value = v)).catch(() => {}) })
onUnmounted(() => { dead = true; window.clearTimeout(timer) })

const message = useMessage()
const vName = (id: string) => vocab.value.find(v => v.id === id)?.name ?? id

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
    emit('ingested') // 新条目出现在「风格库」tab
  } catch (e) {
    message.error(`入库失败：${(e as Error).message}`)
  }
}

// 保存草稿编辑（特色增删改），成功后刷新列表（ref.json 已变）
async function saveDraft(refId: string, seg: number, style: RefVideoStyle) {
  try {
    await api.updateReferenceDraft(refId, seg, style)
    message.success('草稿已保存')
    detailEl.value?.markSaved()
    await refresh()
  } catch (e) {
    message.error(`保存失败：${(e as Error).message}`)
  }
}

// ── 详情抽屉：single 用整片草稿；multi 点段行开该段草稿（可编辑） ──
const show = ref(false)
const detail = ref<TemplateDetailData | null>(null)
const detailEl = ref<InstanceType<typeof TemplateDetail> | null>(null)
function bvURL(name: string): { label: string; href: string } | null {
  const m = /^(BV[0-9A-Za-z]+)\.\w+$/.exec(name)
  return m ? { label: 'B站原片 ' + m[1], href: `https://www.bilibili.com/video/${m[1]}` } : null
}
function tagChips(tags: StyleTags) {
  return [tags.main, ...(tags.aux ?? [])].filter(Boolean)
    .map(id => ({ id, name: vName(id) }))
}
function buildDetail(r: RefVideo, style: RefVideoStyle, seg?: RefVideoSegment): TemplateDetailData {
  const bv = bvURL(r.name)
  const v = vocab.value
  return {
    title: style.name || seg?.name || r.name,
    status: r.confirmed
      ? { label: '已入库', type: 'success' }
      : { label: '已解析', type: 'success' },
    subject: style.subject,
    tags: [style.tags.main, ...(style.tags.aux ?? [])].filter(Boolean)
      .map(id => ({ id, name: v.find(x => x.id === id)?.name ?? id, brief: v.find(x => x.id === id)?.brief })),
    tagEvidence: style.tag_evidence ?? [],
    signatures: style.signatures ?? [],
    originLabel: bv?.label ?? r.name,
    originHref: bv?.href,
    createdAt: new Date(r.created_at).toLocaleString(),
    refId: r.id,
    segIdx: seg?.index ?? 0,
    framesInSeg: !!seg,
    confirmed: r.confirmed,
    editable: !r.confirmed,
    draft: style,
  }
}
function openSingle(r: RefVideo) {
  if (r.status !== 'done' || !r.style) return
  detail.value = buildDetail(r, r.style)
  show.value = true
}
function openSeg(r: RefVideo, seg: RefVideoSegment) {
  if (!seg.style) return
  detail.value = buildDetail(r, seg.style, seg)
  show.value = true
}
</script>

<template>
  <section class="rv">
    <header class="head">
      <div>
        <h2>视频解析</h2>
        <p>上传一支参考视频，AI 打公认风格标签、提炼它独有的手法特色（词表没有的标签自动申请新词）——确认入库后新建项目可选用。点卡片/段落看标签、特色与证据帧，入库前可增删改特色。</p>
      </div>
      <NButton type="primary" :loading="uploading" @click="pick">上传视频</NButton>
      <input ref="fileEl" type="file" accept="video/*" hidden @change="onFile" />
    </header>

    <div v-if="!refs.length" class="none">还没有解析记录。上传一支 mp4/mov/webm 试试（建议 ≤ 200MB）。</div>
    <div v-else class="grid">
      <figure v-for="r in refs" :key="r.id" class="card" :class="{ bad: r.status === 'error' }">
        <figcaption>
          <div class="row1">
            <b :title="r.name">{{ r.name }}</b>
            <NTag size="small" :bordered="false" :type="r.status === 'done' ? 'success' : r.status === 'error' ? 'error' : 'warning'">
              {{ r.status === 'done' ? '已解析' : r.status === 'error' ? '失败' : '解析中' }}
            </NTag>
          </div>

          <!-- single：整片一个草稿，点开详情 -->
          <template v-if="r.status === 'done' && r.style">
            <div class="chips">
              <NTag v-for="t in tagChips(r.style.tags)" :key="t.id" size="tiny" :bordered="false" type="warning">{{ t.name }}</NTag>
              <NTag v-if="r.style.signatures?.length" size="tiny" :bordered="false" type="info">特色×{{ r.style.signatures.length }}</NTag>
            </div>
            <button type="button" class="draft-line" @click="openSingle(r)">
              <span v-if="r.style.subject">{{ r.style.subject }}</span>
              <span class="more">看标签/特色/证据帧 →</span>
            </button>
          </template>

          <!-- multi：每段一行，点行开该段草稿 -->
          <template v-if="r.status === 'done' && r.segments?.length">
            <div v-for="seg in r.segments" :key="seg.index" class="segrow" :class="{ err: !seg.style }">
              <button type="button" class="seg-btn" :disabled="!seg.style" @click="openSeg(r, seg)">
                <b>{{ seg.index }}</b>
                <span class="seg-name">{{ seg.name }}</span>
                <span v-if="seg.style" class="seg-meta">
                  {{ vName(seg.style.tags.main) }}
                  <template v-if="seg.style.signatures?.length"> · 特色×{{ seg.style.signatures.length }}</template>
                </span>
                <span v-else class="seg-err">{{ seg.error || '解析失败' }}</span>
              </button>
            </div>
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

    <TemplateDetail ref="detailEl" :show="show" :detail="detail" @close="show = false"
      @ingest="id => { const r = refs.find(x => x.id === id); if (r) ingest(r) }"
      @save-draft="saveDraft" />
  </section>
</template>

<style scoped>
.rv { flex: 1; min-height: 0; overflow: auto; padding: 24px 28px; }
.head { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 18px; }
.head h2 { font-size: 16px; color: #d9d9e0; }
.head p { font-size: 12.5px; color: #7c7c88; margin: 4px 0 0; line-height: 1.6; }
.none { color: #55555f; font-size: 13px; padding: 40px 0; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); gap: 16px; }
.card {
  background: #14141a; border: 1px solid #232329; border-radius: 12px; overflow: hidden;
}
.card:hover { border-color: #3a3a44; }
.card.bad { border-color: #5c2424; }
figcaption { padding: 10px 12px 12px; display: flex; flex-direction: column; gap: 7px; }
.row1 { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.row1 b { font-size: 13px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.chips { display: flex; gap: 6px; flex-wrap: wrap; }
.draft-line {
  display: flex; flex-direction: column; gap: 2px; text-align: left; cursor: pointer;
  background: #101016; border: 1px solid #232329; border-radius: 8px; padding: 7px 9px; color: #8a8a96;
}
.draft-line:hover { border-color: #3a3a45; }
.draft-line span { font-size: 11.5px; line-height: 1.5; }
.draft-line .more { color: #7ea6d9; font-size: 11px; }
.segrow.err .seg-btn { cursor: default; }
.seg-btn {
  display: flex; align-items: baseline; gap: 8px; width: 100%; text-align: left; cursor: pointer;
  background: #101016; border: 1px solid #232329; border-radius: 8px; padding: 6px 9px; color: #c9c9d1;
}
.seg-btn:hover:not(:disabled) { border-color: #5c4d24; }
.seg-btn:disabled { opacity: .55; cursor: default; }
.seg-btn b { font-size: 11px; color: #f0c674; }
.seg-name { font-size: 12.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.seg-meta { margin-left: auto; font-size: 11px; color: #7c7c88; white-space: nowrap; }
.seg-err { margin-left: auto; font-size: 10.5px; color: #d98a8a; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 120px; }
.err { font-size: 11.5px; color: #d98a8a; word-break: break-all; }
.row2 { display: flex; align-items: center; justify-content: space-between; }
.origin { font-size: 11px; color: #55555f; }
.btns { display: flex; gap: 8px; align-items: center; }
</style>
