<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { NButton, NDrawer, NDrawerContent, NInput, NSelect, NTag } from 'naive-ui'
import { api, libraryMediaURL, referenceMediaURL, referenceSegMediaURL } from '../api'
import type { RefVideoStyle, Signature, TemplateDetailData } from '../types'

// 风格详情抽屉（v3）：标签（词表名+brief）+ 特色清单（机制+证据帧缩略图）+ 打标证据
// + 参考片抽帧/原片。解析草稿（未入库）可增删改特色——编辑态存本地副本，
// 保存走 PUT /references/:id/draft（父组件接线）。
const props = defineProps<{ show: boolean; detail: TemplateDetailData | null }>()
const emit = defineEmits<{
  close: []
  publish: [packId: string]
  ingest: [refId: string]
  'save-draft': [refId: string, seg: number, style: RefVideoStyle]
}>()

// ── 媒体：条目快照（library）优先，引用根目录次之，multi 段子目录最后 ──
const frames = ref<string[]>([])
const hasVideo = ref(false)
const mediaURL = computed(() => {
  const d = props.detail
  if (d?.libMediaId) return (f: string) => libraryMediaURL(d.libMediaId!, f)
  if (d?.refId && d.framesInSeg && d.segIdx) return (f: string) => referenceSegMediaURL(d.refId!, d.segIdx!, f)
  if (d?.refId) return (f: string) => referenceMediaURL(d.refId!, f)
  return null
})
watch(
  () => [props.show, props.detail?.libMediaId, props.detail?.refId, props.detail?.segIdx] as const,
  async ([show, libId, refId, segIdx]) => {
    frames.value = []
    hasVideo.value = false
    editing.value = false
    const d = props.detail
    if (!show || !d) return
    if (d.editable) startEdit() // 草稿态默认进编辑视角
    if (libId) {
      const r = await api.libraryFrames(libId).catch(() => null)
      if (r) { frames.value = r.frames; hasVideo.value = r.has_video }
    } else if (refId && d.framesInSeg && segIdx) {
      const r = await api.referenceSegFrames(refId, segIdx).catch(() => null)
      if (r) frames.value = r.frames
    } else if (refId) {
      const r = await api.referenceFrames(refId).catch(() => null)
      if (r) { frames.value = r.frames; hasVideo.value = r.has_video }
    }
  },
  { immediate: true },
)

const kindLabel: Record<string, string> = {
  camera: '运镜', transition: '转场', motif: '母题', motion: '动效', material: '素材', palette: '配色',
}

// ── 草稿编辑（特色增删改）──────────────────────────────
const editing = ref(false)
const draft = reactive<{
  name: string
  subject: string
  signatures: Signature[]
}>({ name: '', subject: '', signatures: [] })

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

const kindOptions = Object.entries(kindLabel).map(([value, label]) => ({ value, label }))
const adding = reactive<{ kind: string; name: string; mechanism: string; useHint: string; frames: string[] }>({
  kind: 'motif', name: '', mechanism: '', useHint: '', frames: [],
})
function toggleAddFrame(f: string) {
  const i = adding.frames.indexOf(f)
  if (i >= 0) adding.frames.splice(i, 1)
  else if (adding.frames.length < 3) adding.frames.push(f)
}
function addSig() {
  if (!adding.name.trim() || !adding.mechanism.trim()) return
  draft.signatures.push({
    id: `sig${draft.signatures.length + 1}`,
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
  emit('save-draft', d.refId, d.segIdx ?? 0, style)
}
defineExpose({ markSaved: () => { saving.value = false; editing.value = false } })
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
            <a v-if="detail.originHref" :href="detail.originHref" target="_blank" rel="noreferrer">{{ detail.originLabel }}↗</a>
            <span v-else class="origin">{{ detail.originLabel }}</span>
            <span v-if="detail.createdAt" class="time">{{ detail.createdAt }}</span>
          </div>
          <div class="act-row">
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

        <!-- 特色清单（查看态） -->
        <section v-if="!editing && detail.signatures?.length" class="sec">
          <h3>特色清单 ×{{ detail.signatures.length }}</h3>
          <div v-for="sg in detail.signatures" :key="sg.id" class="sig">
            <div class="sig-head">
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
            <div v-if="frames.length" class="frame-pick">
              <button v-for="f in frames" :key="f" type="button" class="pick"
                :class="{ on: sg.frames?.includes(f) }" @click="toggleSigFrame(sg, f)">
                {{ f }}
              </button>
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
            <div v-if="frames.length" class="frame-pick">
              <button v-for="f in frames" :key="f" type="button" class="pick"
                :class="{ on: adding.frames.includes(f) }" @click="toggleAddFrame(f)">
                {{ f }}
              </button>
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
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.tdrawer :deep(.n-drawer-header) { background: #101016; border-color: #232329; }
.tdrawer { background: #101016; color: #c9c9d1; }
.head { display: flex; flex-direction: column; gap: 8px; }
.title-row { display: flex; align-items: center; gap: 10px; }
.title-row h2 { font-size: 17px; color: #e8e8ee; margin: 0; }
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
.frame-pick { display: flex; gap: 6px; flex-wrap: wrap; }
.pick { font-size: 10.5px; font-family: monospace; color: #7c7c88; background: #101016; border: 1px solid #232329; border-radius: 6px; padding: 2px 8px; cursor: pointer; }
.pick.on { color: #f0c674; border-color: #5c4d24; background: #211d12; }
.ref-video { width: 100%; margin-top: 10px; border-radius: 8px; background: #000; max-height: 320px; }
</style>
