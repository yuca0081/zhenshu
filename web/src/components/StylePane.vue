<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useMessage, NButton, NInput, NScrollbar, NSelect, NTag } from 'naive-ui'
import { api, libraryMediaURL, styleClipURL } from '../api'
import type { PrepView, ProjectView, Style, StyleSamples, TemplateDetailData, VocabEntry } from '../types'
import { errMsg } from '../format'
import SampleFrame from './SampleFrame.vue'
import TemplateDetail from './TemplateDetail.vue'

// 风格页签（原 GatesModal 风格段 + StagePanel 样张段合并，弹窗退役）。
// 两态视图 + 迷你步骤条（① 选风格 → ② 出样张 → ③ 确认开工），先后顺序长在界面上：
// A 态（未出样张/正在换）= 风格库（翻页/标签筛选/搜索）+ 详情抽屉选风格 + 一句话方向；
// B 态（已出样张）= 样张大图预览（迷你真段 mp4，降级显示静态）+ 确认硬门 + 换风格入口。
// 两路同一交互：确认 → 自动出样张（聊天通道异步驱动 Agent）→ 用户在此页确认样张
// （唯一必须拍板的门；设计系统始终本片现发明）。
// 选风格交互：点卡片开详情抽屉（标签证据/特色截图都能看，特色开关在抽屉里默认全开）
// ——盲选退役：选之前先看见里面是什么。
const props = defineProps<{
  projectId: string
  view: ProjectView | null
  styleSamples: StyleSamples | null
  prep: PrepView | null
  videoStale: boolean // 成片生成后风格又改过（重做整片才会进新片）
}>()
const emit = defineEmits<{ saved: []; 'confirm-style': []; 'chat-sent': [] }>()

const message = useMessage()

// ── 步骤条 + 两态 ──
const step = computed(() => {
  const ss = props.styleSamples
  if (!ss?.samples?.length) return 1
  return ss.confirmed ? 3 : 2
})
const showPicker = ref(false) // B 态里点「换风格」→ 回 A 态（旧样张仍在，重确认即作废）
const requested = ref(false) // 已提交出样张、还没等来新样张——步骤条旁的等待提示
const reviewMode = computed(() => !!props.styleSamples?.samples?.length && !showPicker.value)
// 准备链在跑（②风格包由 Agent 自己做，样张落盘前这里没有可审的东西）：A 态选风格
// 入口整块让位给占位说明——旧态会把别的项目的库内推荐亮出来误导用户去手动选
// （2026-10-30 GUI 实测 t07：页面停在「①选风格」，推荐卡是另一项目的旧风格）。
const prepBusy = computed(() => !!props.prep?.running && !reviewMode.value)
// 上次触发复位的样张 JSON 签名：后台刷新会用新引用带同一份样张（loadProject 全量回写），
// 只看引用变化就把正在换风格的用户弹回样张页——内容没变就不复位
let lastSamplesSig: string | null = null
watch(() => props.styleSamples, nv => {
  const sig = nv == null ? '' : JSON.stringify(nv)
  if (sig === lastSamplesSig) return
  lastSamplesSig = sig
  if (nv) { requested.value = false; showPicker.value = false } // 新样张到手，回 B 态
  // 样张清空（切项目/换风格作废）同步复位等待态：不然「样张生成中」挂到 90s 超时
  else requested.value = false
})

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
const clipSrc = (video: string) => styleClipURL(props.projectId, video.split('/').pop() ?? video)

// ── 库内条目（懒加载一次；开页时拉，apply 后不重拉——库不常变）──
const packs = ref<Style[]>([])
const vocab = ref<VocabEntry[]>([])
const applying = ref(false)
const vEntry = (id: string) => vocab.value.find(v => v.id === id)

// 详情抽屉（选风格模式）：点卡片打开，看完点「就选它」
const showDetail = ref(false)
const detail = ref<TemplateDetailData | null>(null)
function openDetail(p: Style) {
  detail.value = {
    title: p.name,
    status: { label: '已入库', type: 'success' },
    subject: p.subject,
    tags: [p.tags.main, ...(p.tags.aux ?? [])]
      .filter(Boolean)
      .map(id => ({ id, name: vEntry(id)?.name ?? id, brief: vEntry(id)?.brief })),
    tagEvidence: p.tag_evidence ?? [],
    signatures: p.signatures ?? [],
    originLabel: '来源 ' + (p.origin_project || '—'),
    createdAt: new Date(p.created_at).toLocaleDateString(),
    libMediaId: p.origin_project?.startsWith('video:') ? p.id : undefined,
    packId: p.id,
    published: true,
    selectMode: true,
  }
  showDetail.value = true
}

async function apply(packId: string, disabled: string[]) {
  if (applying.value) return
  applying.value = true
  const pack = packs.value.find(p => p.id === packId)
  try {
    await api.applyStyle(props.projectId, packId, disabled)
    message.success('风格已确认，正在出样张——出好后回此页确认')
    emit('saved')
    requested.value = true
    // 确认即出样张：applyStyle 只落简报不触发生成，这里补一发聊天让 Agent 跑设计
    // 阶段（异步 202 秒回）；分镜没出时 Agent 会在对话里说明先出分镜
    const offNote = pack?.signatures?.length
      ? (disabled.length ? `特色关掉了 ${disabled.length} 条，其余全开` : '特色全开')
      : '该条目没有特色清单'
    await api.chat(props.projectId,
      `风格已选定：库内条目「${pack?.name ?? ''}」（${offNote}）。请按它出风格样张，出好后提醒我确认。`)
    showDetail.value = false
    emit('chat-sent')
  } catch (e) {
    message.error(errMsg(e))
  } finally {
    applying.value = false
  }
}

// 翻页 + 标签筛选 + 搜索（客户端做——库列表是全量小 JSON，几百条内无压力；
// 标签用下拉不用 chips——标签会越养越多，铺开放不下）
const PAGE_SIZE = 9
const page = ref(1)
const tagFilter = ref('')
const search = ref('')
const tagNames = computed(() => {
  const counts = new Map<string, number>()
  for (const p of packs.value) {
    for (const id of [p.tags.main, ...(p.tags.aux ?? [])]) counts.set(id, (counts.get(id) ?? 0) + 1)
  }
  return [...counts.entries()].sort((a, b) => b[1] - a[1]).map(([id]) => ({ id, name: vEntry(id)?.name ?? id }))
})
const tagOptions = computed(() => [
  { label: '全部标签', value: '' },
  ...tagNames.value.map(t => ({ label: t.name, value: t.id })),
])
const filteredPacks = computed(() => {
  const q = search.value.trim().toLowerCase()
  return packs.value.filter(p => {
    if (tagFilter.value && p.tags.main !== tagFilter.value && !(p.tags.aux ?? []).includes(tagFilter.value)) return false
    if (q && !p.name.toLowerCase().includes(q)) return false
    return true
  })
})
const pageCount = computed(() => Math.max(1, Math.ceil(filteredPacks.value.length / PAGE_SIZE)))
const pagedPacks = computed(() => filteredPacks.value.slice((page.value - 1) * PAGE_SIZE, page.value * PAGE_SIZE))
watch([tagFilter, search], () => { page.value = 1 })
watch(pageCount, n => { if (page.value > n) page.value = n })

onMounted(async () => {
  packs.value = (await api.listLibrary().catch(() => [] as Style[])).filter(p => p.published)
  vocab.value = await api.listVocab().catch(() => [] as VocabEntry[])
})

// ── 方向描述：确认 → Agent 出样张 ──
const direction = ref('')
const asking = ref(false)
async function draftStyle() {
  const d = direction.value.trim()
  if (!d || asking.value) return
  asking.value = true
  try {
    await api.chat(props.projectId, `我确认风格方向：${d}。请按它出风格样张，出好后提醒我确认。`)
    direction.value = ''
    requested.value = true
    emit('chat-sent')
  } catch (e) {
    message.error(errMsg(e))
  } finally {
    asking.value = false
  }
}
</script>

<template>
  <NScrollbar class="style-pane">
    <div class="inner">
      <!-- 迷你步骤条：一行 11px，顺序感在但不占地 -->
      <div class="steps">
        <span :class="{ on: step === 1, done: step > 1 }">① 选风格</span><i>→</i>
        <span :class="{ on: step === 2, done: step > 2 }">② 出样张</span><i>→</i>
        <span :class="{ on: step === 3, done: step === 3 }">③ 确认开工</span>
        <span v-if="requested" class="steps-note">样张生成中（几分钟）——好了一起自动回这页</span>
      </div>

      <!-- B 态：审样张 -->
      <template v-if="reviewMode">
        <div class="sec-head">
          风格样张 · {{ styleSamples!.direction }}
          <NTag size="small" :bordered="false" :type="styleSamples!.confirmed ? 'success' : 'warning'">
            {{ styleSamples!.confirmed ? '已确认' : '待确认' }}
          </NTag>
          <NButton v-if="!styleSamples!.confirmed" size="tiny" quaternary class="rebook" @click="showPicker = true">
            换风格
          </NButton>
        </div>
        <div v-if="styleSamples!.skin" class="skin-bar">
          <span class="skin-name">皮肤 {{ styleSamples!.skin.name }}</span>
          <span v-for="c in skinChips" :key="c.label" class="chip" :title="c.value">
            <i :style="{ background: c.value }" />{{ c.label }}
          </span>
          <code>{{ styleSamples!.skin.slots.font }}</code>
        </div>
        <div class="samples col">
          <figure v-for="s in styleSamples!.samples" :key="s.tag" class="sample">
            <!-- 迷你真段优先：与成片同一台渲染器渲出的 3s mp4；没渲出来降级静态 -->
            <video
              v-if="s.video" :src="clipSrc(s.video)" loop muted autoplay
              playsinline class="clip-video"
            />
            <SampleFrame v-else :html="s.html" />
            <figcaption>
              <!-- tag 含说明前缀（判卷/返工流的稳定 id），展示只露「样张 X」，完整说明走 desc -->
              <b>{{ s.tag.split('·')[0].trim() }}</b><span>{{ s.desc }}</span>
              <span v-if="s.video" class="clip-note ok">迷你真段 · 与成片同工序渲染（动效/字幕带即所得）</span>
              <span v-else-if="s.clip_note" class="clip-note">{{ s.clip_note }}</span>
            </figcaption>
          </figure>
        </div>
        <div class="confirm-bar">
          <p>样张与成片同一套皮肤、同一台渲染器，所见即所得。确认后风格文件锁定进项目（这是唯一必须你拍板的门）。</p>
          <NButton v-if="!styleSamples!.confirmed" type="primary" size="large" @click="emit('confirm-style')">
            确认风格，开工
          </NButton>
          <NButton v-else type="success" ghost size="large" disabled>已确认</NButton>
        </div>
        <p v-if="videoStale" class="stale-note">成片用的是旧风格——「视频」页签可按最新内容重做。</p>
      </template>
      <div v-else-if="showPicker && styleSamples" class="rebook-note">
        正在换风格——重新确认会作废现有样张，并按新选重出。
        <NButton size="tiny" quaternary @click="showPicker = false">返回样张</NButton>
      </div>

      <!-- 准备链在跑：风格由准备工位②自己做，选风格入口让位（防误导性手动入口） -->
      <div v-if="prepBusy" class="ms-hint prep-busy">
        <b>风格工位进行中</b>——准备链正在替你探索风格、渲染样张（进度在「主题」页签，
        样张多的轮次要十几分钟）。样张出好会自动出现在这里，届时确认即可开工。
      </div>

      <!-- A 态：选风格 -->
      <template v-if="!reviewMode && !prepBusy">
        <div v-if="!packs.length" class="ms-hint empty-lib">
          风格库还是空的——去「风格库」页上传参考视频解析入库，或直接用下面的文字描述方向。
        </div>
        <template v-else>
          <div class="sec-head picker-head">从风格库选 <span class="ph-sub">点卡片看截图和特色再决定；不想要的特色在详情里关掉</span></div>
          <div class="filter-bar">
            <NSelect v-model:value="tagFilter" :options="tagOptions" size="small" class="tag-select" />
            <NInput v-model:value="search" size="small" clearable placeholder="搜风格名" class="search" />
          </div>
          <div v-if="pagedPacks.length" class="style-cards">
            <button
              v-for="p in pagedPacks" :key="p.id" type="button"
              class="style-card" @click="openDetail(p)"
            >
              <!-- 封面 = 入库时原片代表帧；兜底文字在下垫层，图加载成功即盖住它 -->
              <div class="cover">
                <div class="cover-fallback"><b>{{ p.name }}</b></div>
                <img
                  v-if="p.origin_project?.startsWith('video:')"
                  :src="libraryMediaURL(p.id, 'cover.png')"
                  loading="lazy" alt=""
                  @error="($event.target as HTMLImageElement).style.display = 'none'"
                />
              </div>
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
          <div v-else class="ms-hint">没有匹配的风格——换个筛选词，或用下面一句话方向描述。</div>
          <div v-if="pageCount > 1" class="pager">
            <NButton size="tiny" :disabled="page <= 1" @click="page--">上一页</NButton>
            <span class="pg-num">{{ page }} / {{ pageCount }}（共 {{ filteredPacks.length }} 条）</span>
            <NButton size="tiny" :disabled="page >= pageCount" @click="page++">下一页</NButton>
          </div>
        </template>

        <div v-if="packs.length" class="foot">
          <span class="ms-hint">点卡片先看详情（截图/特色），抽屉里拍板；确认后自动出样张，出好后回此页再确认一次才能开工</span>
        </div>

        <!-- 选风格详情抽屉：特色开关+证据截图都在里面，看着点菜 -->
        <TemplateDetail :show="showDetail" :detail="detail" :applying="applying"
          @close="showDetail = false" @apply="apply" />

        <!-- 方向描述 -->
        <div class="sec-head picker-head">或者用一句话描述方向（AI 出样张后你确认）</div>
        <NInput
          v-model:value="direction" type="textarea" :rows="3" maxlength="400"
          placeholder="例：深夜电台感——暗底、暖金光、细衬线字，画面安静克制"
        />
        <div class="foot" style="margin-top: 8px;">
          <span class="ms-hint">确认后 AI 按这个方向出样张，出好后在此页确认</span>
          <NButton :disabled="!direction.trim()" :loading="asking" @click="draftStyle">确认方向，出样张</NButton>
        </div>
      </template>
    </div>
  </NScrollbar>
</template>

<style scoped>
.style-pane { flex: 1; }
.inner { padding: 14px 22px 22px; display: flex; flex-direction: column; gap: 12px; }
.sec-head { font-size: 13px; font-weight: 600; color: var(--ink); display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.picker-head { margin-top: 10px; }
.ph-sub { font-size: 11px; color: var(--muted); font-weight: 400; }
.stale-note { font-size: 12px; color: var(--warn); margin: 0; }

/* 迷你步骤条：一行 11px */
.steps { display: flex; align-items: center; gap: 8px; font-size: 11px; color: var(--muted); user-select: none; }
.steps i { color: var(--line); font-style: normal; }
.steps span.on { color: var(--accent); font-weight: 600; }
.steps span.done { color: var(--ok); }
.steps-note { margin-left: auto; color: var(--info); }

/* 筛选行：标签下拉 + 搜索框 */
.filter-bar { display: flex; align-items: center; gap: 10px; }
.tag-select { width: 170px; flex: none; }
.search { width: 170px; flex: none; margin-left: auto; }

/* 翻页 */
.pager { display: flex; align-items: center; justify-content: center; gap: 14px; }
.pg-num { font-size: 11.5px; color: var(--muted); font-variant-numeric: tabular-nums; }

/* 皮肤槽位条 */
.skin-bar { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; font-size: 12px; color: var(--muted); }
.skin-name { color: var(--ink); font-weight: 600; }
.skin-bar code { background: color-mix(in srgb, var(--ink) 4%, transparent); padding: 2px 8px; font-size: 11px; }
.chip { display: inline-flex; align-items: center; gap: 5px; border: 1px solid var(--line); padding: 2px 10px; }
.chip i { width: 12px; height: 12px; display: inline-block; border: 1px solid var(--line); }

/* 样张（B 态：一列大图；迷你真段 mp4 原生比例，静态兜底 16:9） */
.samples.col { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: 16px; }
.sample { width: 100%; max-width: 560px; margin: 0 auto; }
.clip-video { display: block; width: 100%; border: 1px solid var(--line-soft); background: #000; }
.sample :deep(.sf) { border: 1px solid var(--line-soft); }
figcaption { margin-top: 8px; display: flex; flex-direction: column; gap: 2px; }
figcaption b { font-size: 13px; color: var(--ink); }
figcaption span { font-size: 12px; color: var(--muted); }
.clip-note { font-size: 11px !important; color: var(--muted); opacity: .75; }
.clip-note.ok { color: var(--ok); opacity: 1; }
.confirm-bar { display: flex; align-items: center; justify-content: space-between; gap: 20px; flex-wrap: wrap; }
.confirm-bar p { color: var(--muted); font-size: 13px; max-width: 560px; margin: 0; }
.rebook { margin-left: auto; }
.rebook-note {
  font-size: 12px; color: var(--warn);
  background: color-mix(in srgb, var(--warn) 8%, transparent);
  border: 1px solid color-mix(in srgb, var(--warn) 35%, transparent);
  padding: 8px 12px; display: flex; align-items: center; gap: 10px;
}

/* 库内条目卡（点卡片开详情抽屉；Rhine 方角 + 悬停左侧强调刻线） */
.ms-hint { font-size: 11.5px; color: var(--muted); opacity: .8; line-height: 1.6; }
.empty-lib { background: var(--bg); border: 1px dashed var(--line); padding: 12px 16px; }
.style-cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(158px, 1fr)); gap: 10px; padding: 2px; }
.style-card {
  position: relative;
  display: flex; flex-direction: column; text-align: left; cursor: pointer; padding: 0;
  background: var(--surface); border: 1px solid var(--line-soft); overflow: hidden; color: var(--ink);
  font-family: inherit;
  transition: border-color var(--dur-1) var(--ease);
}
.style-card::before {
  content: ''; position: absolute; left: 0; top: 0; bottom: 0; width: 2px; z-index: 1;
  background: var(--accent);
  transform: scaleY(0); transform-origin: top center;
  transition: transform 300ms var(--ease);
}
.style-card:hover::before { transform: scaleY(1); }
.style-card:hover { border-color: var(--line); }
/* 封面随卡宽走 16:9；兜底文字垫底，真图盖上（同 LibraryPanel 卡片） */
.cover { position: relative; aspect-ratio: 16 / 9; background: color-mix(in srgb, var(--ink) 6%, var(--surface)); }
.cover img { position: absolute; inset: 0; width: 100%; height: 100%; object-fit: cover; }
.cover-fallback {
  position: absolute; inset: 0; display: grid; place-items: center; padding: 0 10px; text-align: center;
  background: color-mix(in srgb, var(--ink) 3%, var(--surface));
}
.cover-fallback b { color: var(--muted); font-size: 11px; letter-spacing: 0.1em; line-height: 1.4; }
.meta { padding: 7px 9px 9px; display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.meta b { font-size: 12px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.meta span { font-size: 10.5px; color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.tagline { display: flex; gap: 4px; flex-wrap: wrap; }
.foot { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
</style>
