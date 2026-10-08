<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NButton, NInput, NModal, NSelect, NSwitch, NTag } from 'naive-ui'
import { api } from '../api'
import type { ProjectView, Storyboard, Style, VocabEntry } from '../types'

// 三前置硬门弹窗（点对话栏上方的 文稿/分镜/风格 标签打开）：
// - 文稿：编辑保存（PUT manuscript，article.txt 为真源）+ AI 按主题起草
// - 分镜：只读展示整份分镜文档（改分镜走对话让 AI 重新生成）
// - 风格（v3）：库内条目 = 标签气质 + 特色开关（默认全开），点开工即写「强参考」
//   简报——不再即时生效：设计系统本片现发明，样张确认硬门照走；或文字描述交给 AI
const props = defineProps<{ projectId: string; gate: 'manuscript' | 'storyboard' | 'style' | null; view: ProjectView | null }>()
const emit = defineEmits<{ close: []; saved: []; 'chat-sent': [] }>()

const title = computed(() =>
  props.gate === 'manuscript' ? '文稿'
    : props.gate === 'storyboard' ? `分镜${props.view?.seg_count ? `（${props.view.seg_count} 段）` : ''}`
    : '风格')

// ── 文稿 ─────────────────────────────────────────────
const content = ref('')
const wordCount = computed(() => content.value.trim().length)
const savedMsg = ref('')
const saveErr = ref('')
const stale = ref(false) // 保存时后端告知分镜基于旧稿
const minutes = ref(1)
const drafting = ref(false)

async function draft() {
  const topic = props.view?.topic?.trim()
  if (!topic || drafting.value) return
  drafting.value = true
  saveErr.value = ''
  try {
    const r = await api.draftManuscript(topic, minutes.value)
    content.value = r.content
  } catch (e: any) {
    saveErr.value = e.message ?? String(e)
  } finally {
    drafting.value = false
  }
}

// 链接导入：readability 抽正文 → 标题+正文填入（覆盖前给确认，避免误删已写内容）
const linkURL = ref('')
const extracting = ref(false)
async function extract() {
  const u = linkURL.value.trim()
  if (!u || extracting.value) return
  extracting.value = true
  saveErr.value = ''
  try {
    const r = await api.extractLink(u)
    const text = r.title ? `${r.title}\n\n${r.content}` : r.content
    if (content.value.trim() && !confirm('文本框已有内容，用提取的正文覆盖？')) return
    content.value = text
  } catch (e: any) {
    saveErr.value = e.message ?? String(e)
  } finally {
    extracting.value = false
  }
}

async function save() {
  if (!content.value.trim()) { saveErr.value = '文稿不能为空'; return }
  saveErr.value = savedMsg.value = ''
  try {
    const r = await api.saveManuscript(props.projectId, content.value)
    stale.value = r.storyboard_stale
    savedMsg.value = `已保存（${r.word_count} 字）`
    emit('saved')
  } catch (e: any) {
    saveErr.value = e.message ?? String(e)
  }
}

// ── 分镜 ─────────────────────────────────────────────
const sb = ref<Storyboard | null>(null)

// ── 风格（v3：条目 + 特色开关）─────────────────────────
const packs = ref<Style[]>([])
const vocab = ref<VocabEntry[]>([])

const picked = ref('')
const sigOff = ref<Record<string, boolean>>({}) // 关掉的特色 id（默认全开）
const applying = ref(false)
const direction = ref('')

const vEntry = (id: string) => vocab.value.find(v => v.id === id)

function pick(id: string) {
  picked.value = picked.value === id ? '' : id
  sigOff.value = {} // 换条目重置开关（默认全开）
}

const pickedPack = computed(() => packs.value.find(p => p.id === picked.value))

async function apply() {
  if (!picked.value || applying.value) return
  applying.value = true
  saveErr.value = ''
  try {
    const disabled = Object.entries(sigOff.value).filter(([, off]) => off).map(([id]) => id)
    await api.applyStyle(props.projectId, picked.value, disabled)
    emit('saved')
    emit('close')
  } catch (e: any) {
    saveErr.value = e.message ?? String(e)
  } finally {
    applying.value = false
  }
}

async function draftStyle() {
  const d = direction.value.trim()
  if (!d) return
  saveErr.value = ''
  try {
    await api.chat(props.projectId, `我想用这个风格方向：${d}。请帮我出风格样张，出好后提醒我确认。`)
    emit('chat-sent')
    emit('close')
  } catch (e: any) {
    saveErr.value = e.message ?? String(e)
  }
}

// 打开时按类型懒加载
watch(() => props.gate, async g => {
  savedMsg.value = saveErr.value = ''
  stale.value = false
  if (!g || !props.projectId) return
  if (g === 'manuscript') {
    const m = await api.manuscript(props.projectId).catch(() => null)
    content.value = m?.content ?? ''
  } else if (g === 'storyboard') {
    sb.value = await api.storyboard(props.projectId).catch(() => null)
  } else {
    packs.value = (await api.listLibrary().catch(() => [] as Style[])).filter(p => p.published)
    vocab.value = await api.listVocab().catch(() => [] as VocabEntry[])
    picked.value = ''
    sigOff.value = {}
    direction.value = ''
  }
})
</script>

<template>
  <NModal
    :show="!!gate" preset="card" :title="title" class="app-modal gm-modal"
    @update:show="(v: boolean) => { if (!v) emit('close') }"
  >
    <!-- 文稿：编辑 + AI 起草 -->
    <div v-if="gate === 'manuscript'" class="body">
      <div class="hint">旁白只来自文稿，忠实原文。AI 已生成的分镜基于当前文稿。</div>
      <NInput
        v-model:value="content" type="textarea" :rows="14" :maxlength="200000"
        :placeholder="view?.topic ? `可以直接粘贴文稿，或点下方「AI 起草」（主题：${view.topic}）` : '把文章全文粘进来——这就是视频文稿'"
      />
      <div class="opts">
        <NSelect
          v-model:value="minutes" size="small" style="width: 110px"
          :options="[{ label: '约 1 分钟', value: 1 }, { label: '约 2 分钟', value: 2 }, { label: '约 3 分钟', value: 3 }, { label: '约 5 分钟', value: 5 }]"
        />
        <NButton size="small" :disabled="!view?.topic" :loading="drafting" @click="draft">AI 起草文稿</NButton>
        <span class="hint">{{ view?.topic ? '起草后填入上方文本框，可随意改' : '没有主题（老项目），直接粘贴文稿' }}</span>
      </div>
      <!-- 链接导入（readability 正文提取）：文章页 → 标题+正文填入文本框 -->
      <div class="opts">
        <NInput v-model:value="linkURL" size="small" style="flex: 1" placeholder="或粘贴文章链接（公众号/专栏/博客），自动提取正文" @keyup.enter="extract" />
        <NButton size="small" :disabled="!linkURL.trim()" :loading="extracting" @click="extract">提取正文</NButton>
      </div>
      <div class="foot">
        <!-- 语速按 Edge TTS 实测 4.7 字/秒（苏格拉底片 1207 字 / 256s 语音） -->
        <span class="count">{{ wordCount }} 字 · 预计 {{ Math.max(1, Math.round(wordCount / 4.7 / 60)) }} 分钟</span>
        <NButton type="primary" :disabled="!content.trim()" @click="save">保存</NButton>
      </div>
      <div v-if="stale" class="warn">分镜是基于旧稿生成的——改稿后请在对话里让我重新生成分镜。</div>
    </div>

    <!-- 分镜：只读整份文档 -->
    <div v-else-if="gate === 'storyboard'" class="body">
      <template v-if="sb">
        <div v-if="sb.plan" class="hint">编排思路：{{ sb.plan }}</div>
        <div class="segs">
          <div v-for="s in sb.segments" :key="s.id" class="seg">
            <div class="seg-head">
              <b>段{{ s.idx }}</b>
              <span class="key">{{ s.key }}</span>
              <span class="dur">约 {{ s.duration_hint }}s</span>
            </div>
            <p class="narr">{{ s.narration }}</p>
            <p v-if="s.visual_brief" class="brief">{{ s.visual_brief }}</p>
          </div>
        </div>
      </template>
      <div v-else class="none">
        还没有分镜。先补好文稿，然后在对话里说「生成分镜」——我会拆段、写画面、标时长。
      </div>
    </div>

    <!-- 风格：库内条目（标签+特色开关） / 文字描述 -->
    <div v-else-if="gate === 'style'" class="body">
      <div class="sec-label">从风格库选（标签定气质，特色清单逐条吸收；不想要的特色可关掉）</div>
      <div v-if="packs.length" class="style-cards">
        <button
          v-for="p in packs" :key="p.id" type="button"
          class="style-card" :class="{ on: picked === p.id }" @click="pick(p.id)"
        >
          <div class="meta">
            <b>{{ p.name }}</b>
            <span class="tagline">
              <NTag size="tiny" :bordered="false" type="warning">{{ vEntry(p.tags.main)?.name ?? p.tags.main }}</NTag>
              <NTag v-for="a in p.tags.aux ?? []" :key="a" size="tiny" :bordered="false">{{ vEntry(a)?.name ?? a }}</NTag>
              <NTag v-if="p.signatures?.length" size="tiny" :bordered="false" type="info">特色×{{ p.signatures.length }}</NTag>
            </span>
          </div>
        </button>
      </div>
      <div v-else class="hint">风格库还是空的——在「风格库」页上传参考视频解析入库，或直接用文字描述。</div>

      <!-- 选中条目：气质 brief + 特色开关面板（默认全开） -->
      <div v-if="pickedPack" class="sig-panel">
        <p v-if="vEntry(pickedPack.tags.main)?.brief" class="panel-brief">
          {{ vEntry(pickedPack.tags.main)!.name }}：{{ vEntry(pickedPack.tags.main)!.brief }}
        </p>
        <template v-if="pickedPack.signatures?.length">
          <div class="sec-label">特色开关（关掉的不再要求吸收）</div>
          <div v-for="sg in pickedPack.signatures" :key="sg.id" class="sig-row">
            <NSwitch size="small" :value="!sigOff[sg.id]" @update:value="(v: boolean) => (sigOff[sg.id] = !v)" />
            <div class="sig-txt">
              <b>{{ sg.name }} <NTag size="tiny" :bordered="false" type="info">{{ sg.kind }}</NTag></b>
              <span>{{ sg.mechanism }}</span>
            </div>
          </div>
        </template>
        <p v-else class="panel-brief">该条目没有特色清单——只对齐气质基调。</p>
      </div>

      <div class="foot">
        <span class="hint">选定后设计阶段按它「强参考」现发明设计系统，样张仍需在舞台确认</span>
        <NButton type="primary" :disabled="!picked" :loading="applying" @click="apply">选定开工</NButton>
      </div>
      <div class="sec-label" style="margin-top: 18px;">或者用一句话描述方向（AI 出样张后你确认）</div>
      <NInput
        v-model:value="direction" type="textarea" :rows="3" maxlength="400"
        placeholder="例：深夜电台感——暗底、暖金光、细衬线字，画面安静克制"
      />
      <div class="foot" style="margin-top: 10px;">
        <span class="hint">提交后回到对话区，AI 会出样张并提醒你确认</span>
        <NButton :disabled="!direction.trim()" @click="draftStyle">让 AI 出样张</NButton>
      </div>
    </div>

    <div v-if="saveErr" class="err">{{ saveErr }}</div>
    <div v-if="savedMsg" class="okmsg">{{ savedMsg }}</div>
  </NModal>
</template>

<style>
/* 通用皮在 App.vue 的 .app-modal；此处只留三前置弹窗的尺寸差异 */
.gm-modal { width: 680px; }
.gm-modal .n-card__content { max-height: 72vh; overflow: auto; }
</style>

<style scoped>
.body { display: flex; flex-direction: column; gap: 12px; }
.hint { font-size: 11.5px; color: #55555f; line-height: 1.6; }
.opts { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.foot { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
.count { color: #7c7c88; font-size: 12px; }
.warn { color: #f0c674; font-size: 12px; background: #211d12; border: 1px solid #5c4d24; border-radius: 8px; padding: 8px 12px; }
.err { color: #ff9d9d; font-size: 13px; margin-top: 10px; }
.okmsg { color: #7ee2a8; font-size: 13px; margin-top: 10px; }

/* 分镜列表 */
.segs { display: flex; flex-direction: column; gap: 10px; }
.seg { background: #101016; border: 1px solid #232329; border-radius: 10px; padding: 10px 14px; }
.seg-head { display: flex; align-items: baseline; gap: 10px; }
.seg-head b { font-size: 13px; color: #f0c674; }
.key { font-size: 12px; color: #c9c9d1; }
.dur { margin-left: auto; font-size: 11px; color: #55555f; }
.narr { margin-top: 6px; font-size: 13px; line-height: 1.7; color: #d9d9e0; white-space: pre-wrap; }
.brief { margin-top: 4px; font-size: 12px; line-height: 1.6; color: #8a8a96; white-space: pre-wrap; }
.none { color: #55555f; font-size: 13px; line-height: 1.9; text-align: center; padding: 40px 20px; }

/* 风格卡（与新建弹窗同款观感） */
.sec-label { font-size: 12px; color: #9a9aa6; }
.style-cards {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(158px, 1fr)); gap: 10px;
  max-height: 300px; overflow: auto; padding: 2px;
}
.style-card {
  display: flex; flex-direction: column; text-align: left; cursor: pointer; padding: 0;
  background: #101016; border: 1px solid #232329; border-radius: 10px; overflow: hidden; color: #c9c9d1;
}
.style-card:hover { border-color: #3a3a45; }
.style-card.on { border-color: #f0c674; box-shadow: 0 0 0 1px #f0c674; }
.meta { padding: 7px 9px 9px; display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.meta b { font-size: 12px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.meta span {
  font-size: 10.5px; color: #6f6f7c; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.style-card.on .meta b { color: #f0c674; }
.tagline { display: flex; gap: 4px; flex-wrap: wrap; }

/* 特色开关面板 */
.sig-panel { background: #101016; border: 1px solid #232329; border-radius: 10px; padding: 10px 14px; display: flex; flex-direction: column; gap: 8px; }
.panel-brief { margin: 0; font-size: 12px; color: #8a8a96; line-height: 1.6; }
.sig-row { display: flex; gap: 10px; align-items: flex-start; }
.sig-txt { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.sig-txt b { font-size: 12px; color: #d9d9e0; display: flex; align-items: center; gap: 6px; }
.sig-txt span { font-size: 11.5px; color: #7c7c88; line-height: 1.6; }
</style>
