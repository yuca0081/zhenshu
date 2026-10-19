<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NButton, NInput, NSpin } from 'naive-ui'
import { api } from '../api'
import type { Manuscript } from '../types'
import { errMsg } from '../format'

// 文稿页签：整页编辑器。article.txt 是唯一真源；manuscript.json 是管线产物
// （保存即删防旧稿重放）。AI 成稿的入口在「想法」页签（准备工位①）——这里只管
// 粘贴/手改/保存。Agent 对话轮改稿期间（可能 save_manuscript）编辑区锁定，浮层
// 上给中断出口。
const props = defineProps<{
  projectId: string
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
    prepInvalidated.value = r.prep_invalidated // 类型已如实声明，不再 cast 嗅探（中-41）
    savedMsg.value = `已保存（${r.word_count} 字）`
    dirty.value = false
    emit('saved')
  } catch (e) {
    saveErr.value = errMsg(e)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="ms-pane">
    <div class="ms-toolbar">
      <span class="ms-hint">旁白只来自文稿，忠实原文。直接粘贴或修改，改完点保存；让 AI 写稿去「想法」页签。</span>
      <span class="ms-count">{{ wordCount }} 字</span>
      <NButton type="primary" size="small" :loading="saving" :disabled="!content.trim() || agentBusy" @click="save">保存</NButton>
    </div>
    <!-- Agent 占用编辑区：提示+出口浮在编辑区内部（覆盖层不挤压布局） -->
    <div class="ms-editor">
      <NInput
        v-model:value="content" class="ms-input" type="textarea" :maxlength="200000" :disabled="agentBusy"
        placeholder="把文章全文粘进来——这就是视频文稿"
        @update:value="dirty = true"
      />
      <div v-if="agentBusy" class="ms-lock-cover">
        <NSpin :size="14" />
        <span>AI 正在工作中——本轮可能改写文稿，编辑区已临时锁定</span>
        <NButton size="tiny" quaternary @click="emit('cancel-agent')">中断 AI</NButton>
      </div>
    </div>
    <div v-if="stale" class="ms-warn">分镜是基于旧稿生成的——改稿后请在对话里让我重新生成分镜。</div>
    <div v-if="prepInvalidated" class="ms-warn">准备工件（文字稿/风格包/分镜稿）已按新稿级联作废——在「想法」页签重跑或对话里说「重跑文稿工位」，然后在「终审」页签重新确认。</div>
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
/* Agent 占用编辑区：覆盖在编辑框内部的琥珀提示 + 内联出口按钮（不单独占一行） */
.ms-lock-cover {
  position: absolute; top: 0; right: 0; bottom: 0; left: 0; z-index: 5;
  display: flex; align-items: center; justify-content: center; gap: 12px;
  background: rgba(20, 20, 26, .66); border-radius: 8px;
  color: #f0c674; font-size: 12.5px; text-align: center; padding: 0 16px;
}
.ms-lock-cover :deep(.n-button) { color: #f0c674; flex: none; }
.ms-lock-cover :deep(.n-button:hover) { color: #ffdd9a; }
.ms-warn { color: #f0c674; font-size: 12px; background: #211d12; border: 1px solid #5c4d24; border-radius: 8px; padding: 8px 12px; }
.ms-err { color: #ff9d9d; font-size: 12.5px; }
.ms-ok { color: #7ee2a8; font-size: 12.5px; }
</style>
