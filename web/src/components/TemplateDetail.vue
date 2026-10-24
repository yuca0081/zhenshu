<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { NButton, NInput, NModal, NSelect, NSwitch, NTag } from 'naive-ui'
import { api, libraryMediaURL, referenceMediaURL } from '../api'
import { kindLabel } from '../format'
import type { RefVideoStyle, Signature, TemplateDetailData } from '../types'

// 风格详情弹窗（v3）：标签（词表名+brief）+ 特色清单（机制+证据帧缩略图）+ 打标证据
// + 参考片抽帧/原片。解析草稿（未入库）可增删改特色——编辑态存本地副本，
// 保存走 PUT /references/:id/draft（父组件接线）。
// 选风格模式（selectMode，风格页签用）：特色开关搬进查看态（看着截图点菜，默认全开），
// 底部「就选它」交回 disabled 清单——不再有藏在无图面板里的盲开关。
const props = defineProps<{ show: boolean; detail: TemplateDetailData | null; applying?: boolean }>()
const emit = defineEmits<{
  close: []
  publish: [packId: string]
  ingest: [refId: string]
  'save-draft': [refId: string, style: RefVideoStyle]
  apply: [packId: string, disabled: string[]]
}>()

// ── 媒体：条目快照（library）优先，引用根目录次之 ──
const frames = ref<string[]>([])
const hasVideo = ref(false)
// editing/draft/pickerFor 必须先于下方 watch 声明：immediate 回调在 setup 期同步
// 执行，用到未初始化的 const 会抛 TDZ ReferenceError（Vue 吞成 console 报错且
// 中断回调）——2026-10-19 中-37：老注释只提了 editing，pickerFor 漏了，每次
// 挂载都向全局 errorHandler 抛一次。
const editing = ref(false)
// 选风格模式的特色开关（关掉的不再要求吸收；换条目即重置=默认全开）
const selOff = ref<Record<string, boolean>>({})
const selDisabled = computed(() => Object.entries(selOff.value).filter(([, v]) => v).map(([id]) => id))
function doApply() {
  if (!props.detail?.packId || props.applying) return
  emit('apply', props.detail.packId, selDisabled.value)
}
// 证据帧挑选：默认只亮已选缩略图，点「挑证据帧」展开全部帧网格（缩略图点选，≤3）。
// pickerFor 记当前展开的挑选器（签名 id 或 'add'），一次只开一个。
const pickerFor = ref<string | null>(null)
// 草稿编辑态（startEdit 在 immediate 回调里就会被调——draft 同样必须先声明）
const draft = reactive<{
  name: string
  subject: string
  signatures: Signature[]
}>({ name: '', subject: '', signatures: [] })
const mediaURL = computed(() => {
  const d = props.detail
  if (d?.libMediaId) return (f: string) => libraryMediaURL(d.libMediaId!, f)
  if (d?.refId) return (f: string) => referenceMediaURL(d.refId!, f)
  return null
})
watch(
  () => [props.show, props.detail?.libMediaId, props.detail?.refId] as const,
  async ([show, libId, refId]) => {
    frames.value = []
    hasVideo.value = false
    editing.value = false
    pickerFor.value = null
    selOff.value = {}
    const d = props.detail
    if (!show || !d) return
    if (d.editable) startEdit() // 草稿态默认进编辑视角
    if (libId) {
      const r = await api.libraryFrames(libId).catch(() => null)
      // await 期间快速切换了条目：旧条目的抽帧不许写进新弹窗（数据串号）
      if (props.detail?.libMediaId !== libId || props.detail?.refId !== refId) return
      if (r) { frames.value = r.frames; hasVideo.value = r.has_video }
    } else if (refId) {
      const r = await api.referenceFrames(refId).catch(() => null)
      if (props.detail?.refId !== refId) return // 同上
      if (r) { frames.value = r.frames; hasVideo.value = r.has_video }
    }
  },
  { immediate: true },
)

// ── 草稿编辑（特色增删改）──────────────────────────────

function startEdit() {
  const d = props.detail
  if (!d?.draft) return
  editing.value = true
  draft.name = d.draft.name ?? ''
  draft.subject = d.draft.subject ?? ''
  draft.signatures = (d.draft.signatures ?? []).map(s => ({ ...s, frames: [...(s.frames ?? [])] }))
}
function removeSig(i: number) { draft.signatures.splice(i, 1) }

function toggleSigFrame(sg: Signature, f: string) {
  if (!sg.frames) { sg.frames = [f]; return }
  const i = sg.frames.indexOf(f)
  if (i >= 0) sg.frames.splice(i, 1)
  else if (sg.frames.length < 3) sg.frames.push(f)
}

function togglePicker(key: string) { pickerFor.value = pickerFor.value === key ? null : key }

const kindOptions = Object.entries(kindLabel).map(([value, label]) => ({ value, label }))
const adding = reactive<{ kind: string; name: string; mechanism: string; useHint: string; frames: string[] }>({
  kind: 'motif', name: '', mechanism: '', useHint: '', frames: [],
})
function toggleAddFrame(f: string) {
  const i = adding.frames.indexOf(f)
  if (i >= 0) adding.frames.splice(i, 1)
  else if (adding.frames.length < 3) adding.frames.push(f)
}
// 新增特色 id：独立递增计数。用 length+1 会先删一条再添加撞出重复 id
// （v-for :key 重复，patch 异常），且可能与草稿已有的 sigN 跨源冲突
let sigSeq = 0
function addSig() {
  if (!adding.name.trim() || !adding.mechanism.trim()) return
  draft.signatures.push({
    id: `sig-new-${Date.now().toString(36)}-${++sigSeq}`,
    kind: adding.kind as Signature['kind'],
    name: adding.name.trim(),
    mechanism: adding.mechanism.trim(),
    frames: [...adding.frames],
    use_hint: adding.useHint.trim() || undefined,
  })
  adding.name = adding.mechanism = adding.useHint = ''
  adding.frames = []
}
const saving = ref(false)
function saveDraft() {
  const d = props.detail
  if (!d?.draft || !d.refId) return
  const style = { ...d.draft, name: draft.name.trim() || d.draft.name, subject: draft.subject.trim(), signatures: draft.signatures }
  saving.value = true
  emit('save-draft', d.refId, style)
}
defineExpose({
  markSaved: () => { saving.value = false; editing.value = false },
  markFailed: () => { saving.value = false }, // 保存失败复位按钮，编辑态保留便于重试
})
</script>

<template>
  <NModal :show="props.show" :auto-focus="false" @update:show="v => !v && emit('close')">
    <div class="tdialog">
      <header class="tdialog-head">
        <h2>风格详情</h2>
        <button type="button" class="tdialog-x" @click="emit('close')">✕</button>
      </header>
      <div class="tdialog-body">
        <template v-if="detail">
          <!-- 头部 -->
          <header class="head">
            <div class="title-row">
              <h2 class="title-h2">{{ detail.title }}</h2>
              <NTag size="small" :bordered="false" :type="detail.status.type">{{ detail.status.label }}</NTag>
            </div>
            <div class="meta-row">
              <a v-if="detail.originHref" :href="detail.originHref" target="_blank" rel="noreferrer">{{ detail.originLabel }}↗</a>
              <span v-else class="origin">{{ detail.originLabel }}</span>
              <span v-if="detail.createdAt" class="time">{{ detail.createdAt }}</span>
            </div>
            <div class="act-row" v-if="!detail.selectMode">
              <NButton v-if="detail.packId && !detail.published" size="small" type="primary" ghost
                @click="emit('publish', detail.packId)">入库</NButton>
              <NButton v-if="detail.refId && !detail.confirmed" size="small" type="primary" ghost
                @click="emit('ingest', detail.refId)">入库</NButton>
            </div>
            <p v-if="detail.subject" class="subject">原片题材：{{ detail.subject }}（只供检索，不进生成提示词）</p>
          </header>

          <!-- 标签（词表名 + brief） -->
          <section class="sec">
            <h3>风格标签</h3>
            <div class="tags">
              <div v-for="(t, i) in detail.tags" :key="t.id" class="tagcard" :class="{ main: i === 0 }">
                <div class="tag-name">{{ i === 0 ? '主' : '辅' }} · {{ t.name }}</div>
                <p v-if="t.brief" class="tag-brief">{{ t.brief }}</p>
              </div>
            </div>
            <div v-if="!detail.tags.length" class="degrade">没有标签。</div>
          </section>

          <!-- 打标证据 -->
          <section v-if="detail.tagEvidence?.length" class="sec">
            <h3>打标证据</h3>
            <ul class="evidence">
              <li v-for="ev in detail.tagEvidence" :key="ev.tag + ev.note">
                <b>{{ ev.tag }}</b>
                <span>{{ ev.note }}</span>
                <div v-if="ev.frames?.length && mediaURL" class="ev-frames">
                  <img v-for="f in ev.frames" :key="f" :src="mediaURL(f)" loading="lazy" alt="" />
                </div>
              </li>
            </ul>
          </section>

          <!-- 特色清单（查看态；选风格模式=开关搬进抽屉，看着截图点菜） -->
          <section v-if="!editing && detail.signatures?.length" class="sec">
            <h3>特色清单 ×{{ detail.signatures.length }}<span v-if="detail.selectMode" class="sel-hint">（默认全开，不想要的关掉——关掉的不再要求吸收）</span></h3>
            <div v-for="sg in detail.signatures" :key="sg.id" class="sig" :class="{ off: detail.selectMode && selOff[sg.id] }">
              <div class="sig-head">
                <NSwitch v-if="detail.selectMode" size="small" :value="!selOff[sg.id]"
                  @update:value="(v: boolean) => (selOff[sg.id] = !v)" />
                <NTag size="tiny" :bordered="false" type="info">{{ kindLabel[sg.kind] ?? sg.kind }}</NTag>
                <b>{{ sg.name }}</b>
              </div>
              <p class="sig-mech">{{ sg.mechanism }}</p>
              <p v-if="sg.use_hint" class="sig-hint">适用：{{ sg.use_hint }}</p>
              <div v-if="sg.frames?.length && mediaURL" class="ev-frames">
                <img v-for="f in sg.frames" :key="f" :src="mediaURL(f)" loading="lazy" alt="" />
              </div>
            </div>
          </section>
          <section v-else-if="!editing" class="sec">
            <p class="degrade">没有提炼特色（公认气质之外无独门手法，或已被删光）。</p>
          </section>

          <!-- 特色清单（编辑态：草稿入库前增删改） -->
          <section v-if="editing" class="sec">
            <h3>特色清单 ×{{ draft.signatures.length }}（入库前可增删改）</h3>
            <div v-for="(sg, i) in draft.signatures" :key="sg.id" class="sig">
              <div class="sig-head">
                <NTag size="tiny" :bordered="false" type="info">{{ kindLabel[sg.kind] ?? sg.kind }}</NTag>
                <NInput v-model:value="sg.name" size="tiny" style="width: 200px" />
                <NButton size="tiny" quaternary type="error" @click="removeSig(i)">删除</NButton>
              </div>
              <NInput v-model:value="sg.mechanism" type="textarea" :rows="2" maxlength="300" />
              <NInput v-model:value="sg.use_hint" size="tiny" placeholder="适用节拍（可空）" />
              <!-- 证据帧：已选缩略图 + 展开式缩略图网格挑选（≤3） -->
              <div v-if="frames.length && mediaURL" class="fpick">
                <div class="fpick-sel">
                  <img v-for="f in sg.frames ?? []" :key="f" :src="mediaURL(f)" loading="lazy" alt="" />
                  <button type="button" class="fpick-btn" @click="togglePicker(sg.id)">
                    {{ pickerFor === sg.id ? '收起' : `挑证据帧${sg.frames?.length ? `（${sg.frames.length}/3）` : ''}` }}
                  </button>
                </div>
                <div v-if="pickerFor === sg.id" class="fpick-grid">
                  <button v-for="f in frames" :key="f" type="button" class="fcell"
                    :class="{ on: sg.frames?.includes(f) }" @click="toggleSigFrame(sg, f)">
                    <img :src="mediaURL(f)" loading="lazy" alt="" />
                  </button>
                </div>
              </div>
            </div>
            <div class="sig add">
              <div class="sig-head">
                <NSelect v-model:value="adding.kind" size="tiny" :options="kindOptions" style="width: 96px" />
                <NInput v-model:value="adding.name" size="tiny" style="width: 200px" placeholder="特色短名" />
              </div>
              <NInput v-model:value="adding.mechanism" type="textarea" :rows="2" maxlength="300"
                placeholder="机制描述：怎么做到的（节拍/方向/层次/比例），换题材照做仍成立——不写原片物件" />
              <NInput v-model:value="adding.useHint" size="tiny" placeholder="什么节拍适合用（可空）" />
              <div v-if="frames.length && mediaURL" class="fpick">
                <div class="fpick-sel">
                  <img v-for="f in adding.frames" :key="f" :src="mediaURL(f)" loading="lazy" alt="" />
                  <button type="button" class="fpick-btn" @click="togglePicker('add')">
                    {{ pickerFor === 'add' ? '收起' : `挑证据帧${adding.frames.length ? `（${adding.frames.length}/3）` : ''}` }}
                  </button>
                </div>
                <div v-if="pickerFor === 'add'" class="fpick-grid">
                  <button v-for="f in frames" :key="f" type="button" class="fcell"
                    :class="{ on: adding.frames.includes(f) }" @click="toggleAddFrame(f)">
                    <img :src="mediaURL(f)" loading="lazy" alt="" />
                  </button>
                </div>
              </div>
              <NButton size="tiny" :disabled="!adding.name.trim() || !adding.mechanism.trim()" @click="addSig">添加特色</NButton>
            </div>
            <div class="edit-foot">
              <NButton size="small" @click="editing = false">取消（回查看态）</NButton>
              <NButton size="small" type="primary" :loading="saving" @click="saveDraft">保存草稿</NButton>
            </div>
          </section>

          <!-- 参考片抽帧 + 原片 -->
          <section v-if="detail.refId || detail.libMediaId" class="sec">
            <h3>参考片抽帧{{ frames.length ? ` ×${frames.length}` : '' }}</h3>
            <div v-if="frames.length && mediaURL" class="frames">
              <img v-for="f in frames" :key="f" :src="mediaURL(f)" loading="lazy" alt="" />
            </div>
            <p v-else class="degrade">抽帧文件缺失。</p>
            <video v-if="hasVideo && mediaURL" class="ref-video" :src="mediaURL('video.mp4')"
              controls preload="none" />
          </section>

        </template>
      </div>
      <!-- 选风格模式底栏：看着截图点完菜，这里拍板 -->
      <footer v-if="detail?.selectMode" class="tdialog-foot">
        <span class="foot-hint">特色 {{ detail.signatures?.length ?? 0 }} 条{{ selDisabled.length ? `，关掉 ${selDisabled.length} 条` : '全开' }}；确认后按它「强参考」现发明设计系统并自动出样张</span>
        <NButton type="primary" size="large" :loading="applying" @click="doApply">就选它，出样张</NButton>
      </footer>
    </div>
  </NModal>
</template>

<style scoped>
.tdialog { width: min(880px, 94vw); max-height: 88vh; display: flex; flex-direction: column; background: #101016; border: 1px solid #232329; border-radius: 12px; overflow: hidden; }
.tdialog-head { display: flex; align-items: center; justify-content: space-between; padding: 12px 18px; border-bottom: 1px solid #232329; }
.tdialog-head h2 { font-size: 15px; color: #e8e8ee; margin: 0; }
.tdialog-x { background: none; border: none; color: #7c7c88; font-size: 14px; cursor: pointer; padding: 2px 8px; border-radius: 6px; }
.tdialog-x:hover { color: #e8e8ee; background: #1a1a22; }
.tdialog-body { overflow-y: auto; padding: 14px 18px 20px; color: #c9c9d1; }

/* 选风格模式底栏：特色开关的拍板位 */
.tdialog-foot { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 12px 18px; border-top: 1px solid #232329; background: #0d0d12; }
.foot-hint { font-size: 12px; color: #8a8a96; }
.sel-hint { font-size: 11px; color: #6f6f7c; font-weight: 400; margin-left: 6px; }
.sig.off { opacity: .45; }

.head { display: flex; flex-direction: column; gap: 8px; }
.title-row { display: flex; align-items: center; gap: 10px; }
.title-h2 { font-size: 17px; color: #e8e8ee; margin: 0; }
.meta-row { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; font-size: 12px; color: #7c7c88; }
.meta-row a { color: #7ea6d9; text-decoration: none; }
.act-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.subject { font-size: 12px; color: #8a8a96; margin: 0; }

.sec { margin-top: 22px; }
.sec h3 { font-size: 13px; color: #9a9aa6; margin: 0 0 10px; font-weight: 600; }
.degrade { font-size: 12px; color: #6f6f7c; margin: 0; }

.tags { display: flex; gap: 8px; flex-wrap: wrap; }
.tagcard { background: #14141a; border: 1px solid #232329; border-radius: 8px; padding: 8px 12px; max-width: 320px; }
.tagcard.main { border-color: #5c4d24; }
.tag-name { font-size: 12.5px; color: #e0c985; }
.tag-brief { margin: 4px 0 0; font-size: 11.5px; color: #8a8a96; line-height: 1.6; }

.evidence { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
.evidence li { font-size: 12px; display: flex; flex-direction: column; gap: 4px; }
.evidence b { color: #d9d9e0; font-weight: 600; }
.evidence span { color: #8a8a96; line-height: 1.6; }

.sig { background: #14141a; border: 1px solid #232329; border-radius: 8px; padding: 10px 12px; margin-bottom: 10px; display: flex; flex-direction: column; gap: 6px; }
.sig-head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.sig-head b { font-size: 12.5px; color: #e0c985; }
.sig-mech, .sig-hint { margin: 0; font-size: 12px; color: #a8a8b4; line-height: 1.7; }
.sig-hint { color: #6f6f7c; }
.sig.add { border-style: dashed; }
.edit-foot { display: flex; justify-content: flex-end; gap: 8px; }

.ev-frames { display: flex; gap: 6px; flex-wrap: wrap; }
.ev-frames img { width: 120px; height: auto; display: block; border-radius: 6px; border: 1px solid #232329; background: #0a0a10; }
.frames { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
.frames img { width: 100%; height: auto; display: block; border-radius: 6px; border: 1px solid #232329; background: #0a0a10; }

/* 证据帧挑选：已选缩略图 + 展开式网格（点选，≤3） */
.fpick { display: flex; flex-direction: column; gap: 6px; }
.fpick-sel { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
.fpick-sel img { width: 96px; height: auto; display: block; border-radius: 6px; border: 1px solid #5c4d24; background: #0a0a10; }
.fpick-btn { font-size: 11px; color: #7ea6d9; background: none; border: 1px dashed #2c2c38; border-radius: 6px; padding: 3px 9px; cursor: pointer; }
.fpick-btn:hover { border-color: #7ea6d9; }
.fpick-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(96px, 1fr)); gap: 6px; max-height: 264px; overflow-y: auto; padding: 6px; background: #0d0d13; border: 1px solid #232329; border-radius: 8px; }
.fcell { padding: 0; background: none; border: 1px solid #232329; border-radius: 6px; cursor: pointer; overflow: hidden; }
.fcell:hover { border-color: #3a3a46; }
.fcell img { width: 100%; height: auto; display: block; }
.fcell.on { border-color: #f0c674; box-shadow: 0 0 0 1px #f0c674; }
.ref-video { width: 100%; margin-top: 10px; border-radius: 8px; background: #000; max-height: 320px; }
</style>
