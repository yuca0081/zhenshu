<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NButton, NInput, NInputNumber, NSpin } from 'naive-ui'
import { api } from '../api'
import type { Manuscript } from '../types'
import { errMsg } from '../format'

// 文稿页签：整页编辑器（原 GatesModal 文稿弹窗迁入，弹窗退役）。
// article.txt 是唯一真源；manuscript.json 是管线产物（保存即删防旧稿重放）。
// AI 起草按目标字数直驱（时长概念归分镜：分镜页签定总时长，逐段规划）。
// AI 写稿期间（本页起草 or 对话栏 Agent 轮）编辑区整体锁定：防手写与 AI 落盘
// 互相写穿，编辑区浮层上给取消/中断出口。
const props = defineProps<{
  projectId: string
  topic: string
  manuscript: Manuscript | null
  videoStale: boolean // 成片生成后文稿又改过（重做整片才会进新片）
  agentBusy: boolean // 对话栏 Agent 轮进行中（App 的 turnActive，整轮不断锁）
}>()
const emit = defineEmits<{ saved: []; 'cancel-agent': [] }>()

const content = ref('')
const dirty = ref(false) // 编辑中不回灌服务器稿（chat 侧 Agent 改稿触发的刷新不打断手写）
watch(
  () => props.manuscript,
  m => { if (!dirty.value) content.value = m?.content ?? '' },
  { immediate: true },
)
const wordCount = computed(() => content.value.trim().length)

const stale = ref(false) // 分镜基于旧稿（保存响应带回）
const prepInvalidated = ref(false) // 准备工件按新稿级联作废（保存响应带回）
const savedMsg = ref('')
const saveErr = ref('')
const saving = ref(false)
async function save() {
  if (!content.value.trim() || saving.value) { if (!content.value.trim()) saveErr.value = '文稿不能为空'; return }
  saveErr.value = savedMsg.value = ''
  saving.value = true
  try {
    const r = await api.saveManuscript(props.projectId, content.value)
    stale.value = r.storyboard_stale
    prepInvalidated.value = !!(r as { prep_invalidated?: boolean }).prep_invalidated
    savedMsg.value = `已保存（${r.word_count} 字）`
    dirty.value = false
    emit('saved')
  } catch (e) {
    saveErr.value = errMsg(e)
  } finally {
    saving.value = false
  }
}

// ── AI 起草（目标字数）────────────────────────────────
const words = ref(500)
const drafting = ref(false)
let draftAbort: AbortController | null = null
// AI 占用编辑区的两种来源：本页起草 / 对话栏 Agent 轮（可能 save_manuscript 改稿）
const locked = computed(() => drafting.value || props.agentBusy)
async function draft() {
  const topic = props.topic.trim()
  if (!topic || drafting.value) return
  // 覆盖确认提前到发起前：生成要几分钟，结束后才问等于白等一场再挨一巴掌
  if (content.value.trim() && !window.confirm('文本框已有内容，AI 草稿将覆盖它，继续？')) return
  saveErr.value = savedMsg.value = ''
  drafting.value = true
  draftAbort = new AbortController()
  try {
    const r = await api.draftManuscript(topic, words.value, draftAbort.signal)
    content.value = r.content
    dirty.value = true
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') savedMsg.value = '已取消 AI 起草'
    else saveErr.value = errMsg(e)
  } finally {
    drafting.value = false
    draftAbort = null
  }
}
function cancelDraft() { draftAbort?.abort() }

// ── 链接导入（readability 正文提取）──────────────────
const linkURL = ref('')
const extracting = ref(false)
async function extract() {
  const u = linkURL.value.trim()
  if (!u || extracting.value || locked.value) return
  extracting.value = true
  saveErr.value = ''
  try {
    const r = await api.extractLink(u)
    if (content.value.trim() && !window.confirm('文本框已有内容，用提取的正文覆盖？')) return
    content.value = r.title ? `${r.title}\n\n${r.content}` : r.content
    dirty.value = true
  } catch (e) {
    saveErr.value = errMsg(e)
  } finally {
    extracting.value = false
  }
}
</script>

<template>
  <div class="ms-pane">
    <div class="ms-toolbar">
      <span class="ms-hint">旁白只来自文稿，忠实原文。可直接粘贴 / AI 起草 / 链接导入，改完点保存。</span>
      <span class="ms-count">{{ wordCount }} 字</span>
      <NButton type="primary" size="small" :loading="saving" :disabled="!content.trim() || locked" @click="save">保存</NButton>
    </div>
    <!-- AI 占用编辑区：不再单独占一行横幅，提示+出口浮在编辑区内部（覆盖层不挤压布局）。
         本页起草给「取消」，对话轮给「中断 AI」——出口永远在明面上 -->
    <div class="ms-editor">
      <NInput
        v-model:value="content" class="ms-input" type="textarea" :maxlength="200000" :disabled="locked"
        placeholder="把文章全文粘进来——这就是视频文稿"
        @update:value="dirty = true"
      />
      <div v-if="locked" class="ms-lock-cover">
        <NSpin :size="14" />
        <span>{{ drafting ? '正在 AI 生成稿件…生成完成前编辑区已锁定' : 'AI 正在工作中——本轮可能改写文稿，编辑区已临时锁定' }}</span>
        <NButton size="tiny" quaternary @click="drafting ? cancelDraft() : emit('cancel-agent')">
          {{ drafting ? '取消生成' : '中断 AI' }}
        </NButton>
      </div>
    </div>
    <div class="ms-tools">
      <div class="tool-row">
        <NInputNumber v-model:value="words" size="small" :min="100" :max="5000" :step="100" :disabled="agentBusy" style="width: 120px">
          <template #suffix>字</template>
        </NInputNumber>
        <NButton size="small" :disabled="!topic || agentBusy" :loading="drafting" @click="draft">AI 起草文稿</NButton>
        <span class="ms-hint">{{ topic ? `按主题起草约 ${words} 字，填入上方可改` : '没有主题（老项目），直接粘贴文稿' }}</span>
        <span v-if="words > 2500" class="ms-warn-inline">长稿一次起草后半段易重复注水，建议分段</span>
      </div>
      <div class="tool-row">
        <NInput v-model:value="linkURL" size="small" style="flex: 1; min-width: 220px" :disabled="locked"
          placeholder="或粘贴文章链接（公众号/专栏/博客），自动提取正文" @keyup.enter="extract" />
        <NButton size="small" :disabled="!linkURL.trim() || locked" :loading="extracting" @click="extract">提取正文</NButton>
      </div>
    </div>
    <div v-if="stale" class="ms-warn">分镜是基于旧稿生成的——改稿后请在对话里让我重新生成分镜。</div>
    <div v-if="prepInvalidated" class="ms-warn">准备工件（文字稿/风格包/分镜稿）已按新稿级联作废——在对话里说「重跑文稿工位」重新出齐，然后在「终审」页签重新确认。</div>
    <div v-if="videoStale" class="ms-warn">成片还是旧稿的版本——去「视频」页签点「按最新内容重做」才会进新片。</div>
    <div v-if="saveErr" class="ms-err">{{ saveErr }}</div>
    <div v-if="savedMsg" class="ms-ok">{{ savedMsg }}</div>
  </div>
</template>

<style scoped>
.ms-pane { flex: 1; min-height: 0; display: flex; flex-direction: column; gap: 10px; padding: 14px 20px 16px; }
.ms-toolbar { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; }
.ms-hint { font-size: 11.5px; color: #55555f; line-height: 1.6; flex: 1; min-width: 200px; }
.ms-count { font-size: 12px; color: #7c7c88; margin-left: auto; }
/* 编辑框吃满剩余高度：NInput textarea 需要高度穿透 */
.ms-editor { position: relative; flex: 1; min-height: 120px; display: flex; }
.ms-input { flex: 1; min-height: 120px; }
.ms-input :deep(.n-input-wrapper) { height: 100%; }
.ms-input :deep(textarea) { height: 100%; resize: none; }
/* AI 占用编辑区：覆盖在编辑框内部的琥珀提示 + 内联出口按钮（不单独占一行） */
.ms-lock-cover {
  position: absolute; top: 0; right: 0; bottom: 0; left: 0; z-index: 5;
  display: flex; align-items: center; justify-content: center; gap: 12px;
  background: rgba(20, 20, 26, .66); border-radius: 8px;
  color: #f0c674; font-size: 12.5px; text-align: center; padding: 0 16px;
}
.ms-lock-cover :deep(.n-button) { color: #f0c674; flex: none; }
.ms-lock-cover :deep(.n-button:hover) { color: #ffdd9a; }
.ms-tools { display: flex; flex-direction: column; gap: 8px; }
.tool-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.ms-warn-inline { font-size: 11px; color: #f0c674; }
.ms-warn { color: #f0c674; font-size: 12px; background: #211d12; border: 1px solid #5c4d24; border-radius: 8px; padding: 8px 12px; }
.ms-err { color: #ff9d9d; font-size: 12.5px; }
.ms-ok { color: #7ee2a8; font-size: 12.5px; }
</style>
