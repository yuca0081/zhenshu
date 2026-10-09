<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useMessage, NButton, NInput, NScrollbar, NSwitch, NTag } from 'naive-ui'
import { api } from '../api'
import type { ProjectView, Style, StyleSamples, VocabEntry } from '../types'
import SampleFrame from './SampleFrame.vue'

// 风格页签（原 GatesModal 风格段 + StagePanel 样张段合并，弹窗退役）：
// 上半 = 已产出的样张（皮肤色板 + 确认硬门）；下半 = 库内条目选择（标签气质 +
// 特色开关）或一句话方向描述。选定不即时生效：设计系统本片现发明，样张照走确认门。
const props = defineProps<{
  projectId: string
  view: ProjectView | null
  styleSamples: StyleSamples | null
  videoStale: boolean // 成片生成后风格又改过（重做整片才会进新片）
}>()
const emit = defineEmits<{ saved: []; 'confirm-style': []; 'chat-sent': [] }>()

const message = useMessage()

// ── 样张（皮肤色板 chips 非颜色槽位不进，字体单独展示）──
const skinChips = computed(() => {
  const sl = props.styleSamples?.skin?.slots
  if (!sl) return []
  const out = [
    { label: '底色', value: sl.bg },
    { label: '文字', value: sl.txt },
    { label: '强调', value: sl.accent },
  ]
  if (sl.mute) out.push({ label: '次级', value: sl.mute })
  if (sl.surface) out.push({ label: '面板', value: sl.surface })
  return out.filter(c => c.value)
})

// ── 库内条目（懒加载一次；开页时拉，apply 后不重拉——库不常变）──
const packs = ref<Style[]>([])
const vocab = ref<VocabEntry[]>([])
const picked = ref('')
const sigOff = ref<Record<string, boolean>>({}) // 关掉的特色 id（默认全开）
const applying = ref(false)
const applyErr = ref('')
const vEntry = (id: string) => vocab.value.find(v => v.id === id)
const pickedPack = computed(() => packs.value.find(p => p.id === picked.value))

onMounted(async () => {
  packs.value = (await api.listLibrary().catch(() => [] as Style[])).filter(p => p.published)
  vocab.value = await api.listVocab().catch(() => [] as VocabEntry[])
})

function pick(id: string) {
  picked.value = picked.value === id ? '' : id
  sigOff.value = {} // 换条目重置开关（默认全开）
}

async function apply() {
  if (!picked.value || applying.value) return
  applying.value = true
  applyErr.value = ''
  try {
    await api.applyStyle(props.projectId, picked.value,
      Object.entries(sigOff.value).filter(([, off]) => off).map(([id]) => id))
    picked.value = ''
    message.success('风格已选定，样张出来后在此页确认')
    emit('saved')
  } catch (e: any) {
    applyErr.value = e.message ?? String(e)
  } finally {
    applying.value = false
  }
}

// ── 方向描述 → Agent 出样张 ──
const direction = ref('')
const asking = ref(false)
async function draftStyle() {
  const d = direction.value.trim()
  if (!d || asking.value) return
  asking.value = true
  try {
    await api.chat(props.projectId, `我想用这个风格方向：${d}。请帮我出风格样张，出好后提醒我确认。`)
    direction.value = ''
    emit('chat-sent')
  } catch (e: any) {
    message.error(e.message ?? String(e))
  } finally {
    asking.value = false
  }
}
</script>

<template>
  <NScrollbar class="style-pane">
    <div class="inner">
      <!-- 样张：已产出才显示（皮肤条 + 样张卡 + 确认硬门） -->
      <template v-if="styleSamples">
        <div class="sec-head">
          风格样张 · {{ styleSamples.direction }}
          <NTag size="small" :bordered="false" :type="styleSamples.confirmed ? 'success' : 'warning'">
            {{ styleSamples.confirmed ? '已确认' : '待确认' }}
          </NTag>
        </div>
        <div v-if="styleSamples.skin" class="skin-bar">
          <span class="skin-name">皮肤 {{ styleSamples.skin.name }}</span>
          <span v-for="c in skinChips" :key="c.label" class="chip" :title="c.value">
            <i :style="{ background: c.value }" />{{ c.label }}
          </span>
          <code>{{ styleSamples.skin.slots.font }}</code>
        </div>
        <div class="samples">
          <figure v-for="s in styleSamples.samples" :key="s.tag" class="sample">
            <!-- 样张 HTML 固定 960×540，卡片只有半宽——必须整体缩放（SampleFrame） -->
            <SampleFrame :html="s.html" />
            <figcaption><b>{{ s.tag }}</b><span>{{ s.desc }}</span></figcaption>
          </figure>
        </div>
        <div class="confirm-bar">
          <p>样张与成片同一套皮肤，所见即所得。确认后风格文件锁定进项目（这是唯一必须你拍板的门）。</p>
          <NButton v-if="!styleSamples.confirmed" type="primary" size="large" @click="emit('confirm-style')">
            确认风格，开工
          </NButton>
          <NButton v-else type="success" ghost size="large" disabled>已确认</NButton>
        </div>
        <p v-if="videoStale" class="stale-note">成片用的是旧风格——「视频」页签可按最新内容重做。</p>
      </template>
      <div v-else class="sec-head none-samples">
        还没有样张。选库内条目「选定开工」，或用一句话描述方向让 AI 出样张——出好后回此页确认。
      </div>

      <!-- 库内条目选择 -->
      <div class="sec-head picker-head">从风格库选（标签定气质，特色清单逐条吸收；不想要的特色可关掉）</div>
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
      <div v-else class="ms-hint">风格库还是空的——去「风格库」页上传参考视频解析入库，或直接用文字描述。</div>

      <!-- 选中条目：气质 brief + 特色开关面板（默认全开） -->
      <div v-if="pickedPack" class="sig-panel">
        <p v-if="vEntry(pickedPack.tags.main)?.brief" class="panel-brief">
          {{ vEntry(pickedPack.tags.main)!.name }}：{{ vEntry(pickedPack.tags.main)!.brief }}
        </p>
        <template v-if="pickedPack.signatures?.length">
          <div class="picker-label">特色开关（关掉的不再要求吸收）</div>
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
        <span class="ms-hint">选定后设计阶段按它「强参考」现发明设计系统，样张仍需确认</span>
        <NButton type="primary" :disabled="!picked" :loading="applying" @click="apply">选定开工</NButton>
      </div>
      <div v-if="applyErr" class="ms-err">{{ applyErr }}</div>

      <!-- 方向描述 -->
      <div class="sec-head picker-head">或者用一句话描述方向（AI 出样张后你确认）</div>
      <NInput
        v-model:value="direction" type="textarea" :rows="3" maxlength="400"
        placeholder="例：深夜电台感——暗底、暖金光、细衬线字，画面安静克制"
      />
      <div class="foot" style="margin-top: 8px;">
        <span class="ms-hint">提交后 AI 会出样张并提醒你确认</span>
        <NButton :disabled="!direction.trim()" :loading="asking" @click="draftStyle">让 AI 出样张</NButton>
      </div>
    </div>
  </NScrollbar>
</template>

<style scoped>
.style-pane { flex: 1; }
.inner { padding: 16px 22px 22px; display: flex; flex-direction: column; gap: 12px; }
.sec-head { font-size: 13px; font-weight: 600; color: #d9d9e0; display: flex; align-items: center; gap: 10px; }
.none-samples { color: #8a8a96; font-weight: 400; background: #101016; border: 1px dashed #2b2b33; border-radius: 10px; padding: 12px 16px; line-height: 1.8; }
.picker-head { margin-top: 10px; }
.stale-note { font-size: 12px; color: #f0c674; margin: 0; }

/* 皮肤槽位条 */
.skin-bar { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; font-size: 12px; color: #8a8a96; }
.skin-name { color: #c3c3cd; font-weight: 600; }
.skin-bar code { background: #1b1b22; padding: 2px 8px; border-radius: 6px; font-size: 11px; }
.chip { display: inline-flex; align-items: center; gap: 5px; border: 1px solid #2b2b33; border-radius: 999px; padding: 2px 10px; }
.chip i { width: 12px; height: 12px; border-radius: 3px; display: inline-block; border: 1px solid rgba(255,255,255,.18); }

/* 样张 */
.samples { display: flex; gap: 16px; flex-wrap: wrap; }
.sample { flex: 1; min-width: 320px; max-width: 480px; }
.sample :deep(.sf) { border: 1px solid #2b2b33; border-radius: 8px; }
figcaption { margin-top: 8px; display: flex; flex-direction: column; gap: 2px; }
figcaption b { font-size: 13px; }
figcaption span { font-size: 12px; color: #8a8a96; }
.confirm-bar { display: flex; align-items: center; justify-content: space-between; gap: 20px; flex-wrap: wrap; }
.confirm-bar p { color: #8a8a96; font-size: 13px; max-width: 560px; margin: 0; }

/* 库内条目卡 + 特色开关面板（自 GatesModal 迁入） */
.ms-hint { font-size: 11.5px; color: #55555f; line-height: 1.6; }
.style-cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(158px, 1fr)); gap: 10px; padding: 2px; }
.style-card {
  display: flex; flex-direction: column; text-align: left; cursor: pointer; padding: 0;
  background: #101016; border: 1px solid #232329; border-radius: 10px; overflow: hidden; color: #c9c9d1;
}
.style-card:hover { border-color: #3a3a45; }
.style-card.on { border-color: #f0c674; box-shadow: 0 0 0 1px #f0c674; }
.meta { padding: 7px 9px 9px; display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.meta b { font-size: 12px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.meta span { font-size: 10.5px; color: #6f6f7c; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.style-card.on .meta b { color: #f0c674; }
.tagline { display: flex; gap: 4px; flex-wrap: wrap; }
.sig-panel { background: #101016; border: 1px solid #232329; border-radius: 10px; padding: 10px 14px; display: flex; flex-direction: column; gap: 8px; }
.picker-label { font-size: 12px; color: #9a9aa6; }
.panel-brief { margin: 0; font-size: 12px; color: #8a8a96; line-height: 1.6; }
.sig-row { display: flex; gap: 10px; align-items: flex-start; }
.sig-txt { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.sig-txt b { font-size: 12px; color: #d9d9e0; display: flex; align-items: center; gap: 6px; }
.sig-txt span { font-size: 11.5px; color: #7c7c88; line-height: 1.6; }
.foot { display: flex; justify-content: space-between; align-items: center; gap: 12px; flex-wrap: wrap; }
.ms-err { color: #ff9d9d; font-size: 12.5px; }
</style>
