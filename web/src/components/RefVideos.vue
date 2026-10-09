<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useMessage, NButton, NTag } from 'naive-ui'
import TemplateDetail from './TemplateDetail.vue'
import { api, referenceMediaURL } from '../api'
import type { RefVideo, RefVideoStyle, Signature, TemplateDetailData, VocabEntry } from '../types'

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

function pick() { fileEl.value?.click() }
// 批量解析：多选逐支提交，服务端排队解析（并发上限），列表轮询跟进
async function onFile(e: Event) {
  const files = Array.from((e.target as HTMLInputElement).files ?? [])
  ;(e.target as HTMLInputElement).value = ''
  if (!files.length) return
  uploading.value = true
  let ok = 0
  for (const f of files) {
    try {
      await api.uploadReference(f)
      ok++
    } catch (err) {
      message.error(`「${f.name}」上传失败：${(err as Error).message}`)
    }
  }
  uploading.value = false
  if (ok > 1) message.success(`已提交 ${ok} 支视频，解析按序进行`)
  await refresh()
}

async function retry(id: string) {
  await api.analyzeReference(id).catch(e => message.error(`重试失败：${e.message}`))
  await refresh()
}

// 保存草稿编辑（特色增删改），成功后刷新列表（ref.json 已变）
async function saveDraft(refId: string, style: RefVideoStyle) {
  try {
    await api.updateReferenceDraft(refId, style)
    message.success('草稿已保存')
    detailEl.value?.markSaved()
    await refresh()
  } catch (e) {
    message.error(`保存失败：${(e as Error).message}`)
  }
}

// ── 解析结果长方块：一视频一块；折叠显名称/标签，展开看详情 ──
const kindLabel: Record<string, string> = {
  camera: '运镜', transition: '转场', motif: '母题', motion: '动效', material: '素材', palette: '配色',
}
interface Unit {
  name: string
  subject: string                   // 内容行：题材一句话，缺省退特色名
  tagChips: { id: string; name: string }[] // 折叠态标签（最多 3 个）
  tagMore: number
  tagsFull: { id: string; name: string; brief?: string }[]
  sigs: Signature[]
  confirmed: boolean
}
const vEntry = (id?: string) => (id ? vocab.value.find(v => v.id === id) : undefined)
const vName = (id?: string) => (id ? vEntry(id)?.name ?? id : '')
const tagIds = (style?: RefVideoStyle) =>
  [style?.tags?.main, ...(style?.tags?.aux ?? [])].filter((x): x is string => !!x)

// done 但没有草稿 = 旧版拆段数据（已退役），不出块，视频行给「重新解析」
function unit(r: RefVideo): Unit | null {
  if (r.status !== 'done' || !r.style) return null
  const style = r.style
  const ids = tagIds(style)
  const sigs = style.signatures ?? []
  const subject = style.subject
    || (sigs.length ? `特色：${sigs.slice(0, 3).map(s => s.name).join('、')}${sigs.length > 3 ? ' 等' : ''}` : '')
  return {
    name: style.name || vName(ids[0]) || r.name,
    subject,
    tagChips: ids.slice(0, 3).map(id => ({ id, name: vName(id) })),
    tagMore: Math.max(0, ids.length - 3),
    tagsFull: ids.map(id => ({ id, name: vName(id), brief: vEntry(id)?.brief })),
    sigs,
    confirmed: r.confirmed,
  }
}
const rows = computed(() => refs.value.map(r => ({ r, u: unit(r) })))

const expanded = ref<Set<string>>(new Set())
function toggle(r: RefVideo) {
  const s = new Set(expanded.value)
  if (s.has(r.id)) s.delete(r.id)
  else s.add(r.id)
  expanded.value = s
}

// 入库（幂等，重复入库服务端同 seed upsert）
async function ingest(r: RefVideo, name: string) {
  try {
    await api.confirmReference(r.id)
    message.success(`「${name}」已入库，新建项目可选用`)
    show.value = false // 弹窗里点的入库：关弹窗避免状态过期
    await refresh()
    emit('ingested') // 新条目出现在「风格库」tab
  } catch (e) {
    message.error(`入库失败：${(e as Error).message}`)
  }
}

// ── 详情弹窗（完整数据 + 草稿编辑 + 原片/抽帧回看）──
const show = ref(false)
const detail = ref<TemplateDetailData | null>(null)
const detailEl = ref<InstanceType<typeof TemplateDetail> | null>(null)
function bvURL(name: string): { label: string; href: string } | null {
  const m = /^(BV[0-9A-Za-z]+)\.\w+$/.exec(name)
  return m ? { label: 'B站原片 ' + m[1], href: `https://www.bilibili.com/video/${m[1]}` } : null
}
function buildDetail(r: RefVideo, style: RefVideoStyle): TemplateDetailData {
  const bv = bvURL(r.name)
  return {
    title: style.name || r.name,
    status: r.confirmed
      ? { label: '已入库', type: 'success' }
      : { label: '已解析', type: 'success' },
    subject: style.subject,
    tags: tagIds(style)
      .map(id => ({ id, name: vEntry(id)?.name ?? id, brief: vEntry(id)?.brief })),
    tagEvidence: style.tag_evidence ?? [],
    signatures: style.signatures ?? [],
    originLabel: bv?.label ?? r.name,
    originHref: bv?.href,
    createdAt: new Date(r.created_at).toLocaleString(),
    refId: r.id,
    confirmed: r.confirmed,
    editable: !r.confirmed,
    draft: style,
  }
}
function openDetail(r: RefVideo) {
  if (!r.style) return
  detail.value = buildDetail(r, r.style)
  show.value = true
}
const frameURL = (r: RefVideo, f: string) => referenceMediaURL(r.id, f)
</script>

<template>
  <section class="rv">
    <header class="head">
      <div>
        <h2>视频解析</h2>
        <p>上传参考视频（支持多选批量），AI 打公认风格标签、提炼它独有的手法特色（词表没有的标签自动申请新词）。一个视频解析出一个风格条目：点开看标签、特色与证据帧，右上角入库，入库后新建项目可选用。</p>
      </div>
      <NButton type="primary" :loading="uploading" @click="pick">上传视频</NButton>
      <input ref="fileEl" type="file" accept="video/*" multiple hidden @change="onFile" />
    </header>

    <div v-if="!refs.length" class="none">还没有解析记录。上传 mp4/mov/webm 试试（建议 ≤ 200MB）。</div>
    <div v-else class="list">
      <article v-for="row in rows" :key="row.r.id" class="refitem" :class="{ bad: row.r.status === 'error' }">
        <!-- 视频行：文件名 + 状态 + 时间 -->
        <div class="vhead">
          <b class="vname" :title="row.r.name">{{ row.r.name }}</b>
          <NTag size="small" :bordered="false"
            :type="row.r.status === 'done' ? 'success' : row.r.status === 'error' ? 'error' : 'warning'">
            {{ row.r.status === 'done' ? '已解析' : row.r.status === 'error' ? '失败' : '解析中' }}
          </NTag>
          <span class="vmeta">{{ new Date(row.r.created_at).toLocaleString() }}</span>
          <span class="vgap" />
          <NButton v-if="row.r.status === 'error'" size="tiny" ghost @click="retry(row.r.id)">重试</NButton>
          <NButton v-else-if="row.r.status === 'done' && !row.u" size="tiny" ghost
            title="旧版拆段数据已退役，重新解析为一个条目" @click="retry(row.r.id)">重新解析</NButton>
        </div>
        <p v-if="row.r.status === 'error'" class="verr">{{ row.r.error }}</p>

        <!-- 解析结果长方块 -->
        <div v-if="row.u" class="block" :class="{ open: expanded.has(row.r.id) }">
          <div class="bhead click" @click="toggle(row.r)">
            <span class="caret">▶</span>
            <div class="binfo">
              <div class="brow1">
                <b class="bname">{{ row.u.name }}</b>
                <NTag v-for="t in row.u.tagChips" :key="t.id" size="tiny" :bordered="false" type="warning">{{ t.name }}</NTag>
                <span v-if="row.u.tagMore" class="tagmore">+{{ row.u.tagMore }}</span>
                <span v-if="row.u.sigs.length" class="bmeta">特色×{{ row.u.sigs.length }}</span>
              </div>
              <p v-if="row.u.subject" class="bsubject">{{ row.u.subject }}</p>
            </div>
            <div class="bact" @click.stop>
              <NButton size="tiny" quaternary @click="openDetail(row.r)">
                {{ row.u.confirmed ? '详情' : '编辑' }}
              </NButton>
              <NButton v-if="!row.u.confirmed" size="tiny" type="primary" ghost
                @click="ingest(row.r, row.u.name)">入库</NButton>
              <NTag v-else size="small" :bordered="false" type="success">已入库</NTag>
            </div>
          </div>

          <!-- 展开态：标签 brief + 特色机制 + 证据帧缩略图 -->
          <div v-if="expanded.has(row.r.id)" class="bbody">
            <div v-if="row.u.tagsFull.length" class="xrow">
              <span class="xlab">标签</span>
              <div class="xtags">
                <div v-for="t in row.u.tagsFull" :key="t.id" class="xtag">
                  <b>{{ t.name }}</b>
                  <span v-if="t.brief">{{ t.brief }}</span>
                </div>
              </div>
            </div>
            <div v-if="row.u.sigs.length" class="xrow">
              <span class="xlab">特色</span>
              <div class="xsigs">
                <div v-for="sg in row.u.sigs" :key="sg.id" class="xsig">
                  <div class="xsig-head">
                    <NTag size="tiny" :bordered="false" type="info">{{ kindLabel[sg.kind] ?? sg.kind }}</NTag>
                    <b>{{ sg.name }}</b>
                    <span v-if="sg.use_hint" class="xsig-hint">适用：{{ sg.use_hint }}</span>
                  </div>
                  <p class="xsig-mech">{{ sg.mechanism }}</p>
                  <div v-if="sg.frames?.length" class="evframes">
                    <img v-for="f in sg.frames" :key="f" :src="frameURL(row.r, f)" loading="lazy" alt="" />
                  </div>
                </div>
              </div>
            </div>
            <div v-if="!row.u.tagsFull.length && !row.u.sigs.length" class="xempty">
              这份草稿没有标签/特色数据（旧版解析产物），只有名称可入库参考。
            </div>
          </div>
        </div>
      </article>
    </div>

    <TemplateDetail ref="detailEl" :show="show" :detail="detail" @close="show = false"
      @ingest="id => { const r = refs.find(x => x.id === id); if (r) ingest(r, detail?.title ?? '') }"
      @save-draft="saveDraft" />
  </section>
</template>

<style scoped>
.rv { flex: 1; min-height: 0; overflow: auto; padding: 24px 28px; }
.head { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 18px; }
.head h2 { font-size: 16px; color: #d9d9e0; }
.head p { font-size: 12.5px; color: #7c7c88; margin: 4px 0 0; line-height: 1.6; }
.none { color: #55555f; font-size: 13px; padding: 40px 0; }

.list { display: flex; flex-direction: column; gap: 22px; max-width: 1080px; }
.refitem { display: flex; flex-direction: column; gap: 8px; }
.vhead { display: flex; align-items: center; gap: 10px; }
.vname { font-size: 14px; color: #e8e8ee; max-width: 40%; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.vmeta { font-size: 11.5px; color: #55555f; white-space: nowrap; }
.vgap { flex: 1; }
.verr { margin: 0; font-size: 11.5px; color: #d98a8a; word-break: break-all; }

.block { background: #14141a; border: 1px solid #232329; border-radius: 10px; overflow: hidden; }
.block.open { border-color: #3a3a46; }
.bhead { display: flex; align-items: center; gap: 10px; padding: 10px 14px; }
.bhead.click { cursor: pointer; }
.bhead.click:hover { background: #17171f; }
.caret { font-size: 9px; color: #7c7c88; transition: transform .15s; flex-shrink: 0; }
.block.open .caret { transform: rotate(90deg); }
.binfo { flex: 1; min-width: 0; }
.brow1 { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.bname { font-size: 13.5px; color: #e8e8ee; }
.tagmore { font-size: 11px; color: #7c7c88; }
.bmeta { margin-left: auto; padding-left: 10px; font-size: 11px; color: #55555f; white-space: nowrap; }
.bsubject { margin: 3px 0 0; font-size: 12px; color: #8a8a96; line-height: 1.5; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.bact { flex-shrink: 0; display: flex; align-items: center; }

.bbody { border-top: 1px dashed #232329; padding: 12px 14px 12px 33px; display: flex; flex-direction: column; gap: 12px; background: #101016; }
.xrow { display: flex; gap: 10px; }
.xlab { flex-shrink: 0; width: 30px; font-size: 11.5px; color: #6f6f7c; padding-top: 2px; }
.xtags { display: flex; flex-direction: column; gap: 5px; }
.xtag { font-size: 12px; line-height: 1.6; }
.xtag b { color: #e0c985; font-weight: 600; margin-right: 8px; }
.xtag span { color: #8a8a96; }
.xsigs { flex: 1; display: flex; flex-direction: column; gap: 8px; }
.xsig { background: #14141a; border: 1px solid #232329; border-radius: 8px; padding: 8px 10px; display: flex; flex-direction: column; gap: 5px; }
.xsig-head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.xsig-head b { font-size: 12.5px; color: #e0c985; }
.xsig-hint { font-size: 11px; color: #6f6f7c; }
.xsig-mech { margin: 0; font-size: 12px; color: #a8a8b4; line-height: 1.7; }
.evframes { display: flex; gap: 6px; flex-wrap: wrap; }
.evframes img { width: 110px; height: auto; display: block; border-radius: 6px; border: 1px solid #232329; background: #0a0a10; }
.xempty { font-size: 12px; color: #6f6f7c; }
</style>
