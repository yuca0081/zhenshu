<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NButton, NInput, NModal, NRadioButton, NRadioGroup } from 'naive-ui'
import { api } from '../api'
import { errMsg } from '../format'

// 新建三件事：项目名 + 驱动 + 画幅。主题不在这里填（2026-10-30）：它挪进了
// 工作区第一个页签「主题」——那里能写想法、传材料，是比单行框完整的输入位；
// 文稿/风格/分镜三前置由主题页的一键链路（准备工位）完成。音乐驱动走 PV
// 管线（做曲→音乐地图→分镜），项目可先建占位。
const show = defineModel<boolean>('show')
const emit = defineEmits<{ created: [id: string] }>()
const name = ref('')
const aspect = ref('9:16')
const mode = ref<'script' | 'music'>('script')
const creating = ref(false)
const error = ref('')

const nameHint = computed(() => mode.value === 'music'
  ? '给这支 PV 起个名字'
  : '起个名字就行——讲什么、什么感觉，进工作区在「主题」页慢慢说')
const footHint = computed(() => mode.value === 'music'
  ? '音乐驱动管线开发中（做曲 → 音乐地图 → 分镜，原型已跑通）：项目可先建，准备工位上线前暂不能出片'
  : '创建后进「主题」页签：写下想做什么、传点参考资料，AI 自动跑完 文稿 → 风格 → 分镜')

watch(show, v => {
  if (v) {
    name.value = ''; aspect.value = '9:16'
    mode.value = 'script'; error.value = ''
  }
})

// IME 组合期（候选未上屏）的 Enter 是选词不是提交；.prevent 会连选词的 Enter 一起吞，
// 只在真提交时手动阻止默认行为
function onEnter(e: KeyboardEvent) {
  if (e.isComposing || e.keyCode === 229) return
  e.preventDefault()
  create()
}

async function create() {
  if (!name.value.trim()) { error.value = '先给项目起个名字'; return }
  creating.value = true
  error.value = ''
  try {
    const r = await api.createProject(name.value.trim(), '', aspect.value, mode.value)
    show.value = false // 创建成功即关：留着极易误点再建一个重复项目（2026-10-16/17 两轮实测踩过）
    emit('created', r.id)
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    creating.value = false
  }
}
</script>

<template>
  <NModal v-model:show="show" preset="card" title="新建视频" class="app-modal np-modal" :mask-closable="!creating">
    <div class="body">
      <div class="field">
        <span class="label">项目名 <em>{{ nameHint }}</em></span>
        <NInput
          v-model:value="name" maxlength="48" autofocus
          placeholder="例：蚊子为什么嗡嗡叫"
          @keydown.enter="onEnter"
        />
      </div>
      <div class="opts">
        <span class="label">驱动</span>
        <NRadioGroup v-model:value="mode" size="small">
          <NRadioButton value="script" title="从主题与材料长出文稿→分镜→风格，现行管线">文稿驱动</NRadioButton>
          <NRadioButton value="music" title="曲先行：做曲→音乐地图→分镜跟拍走（PV，开发中）">音乐驱动 · PV</NRadioButton>
        </NRadioGroup>
      </div>
      <div class="opts">
        <span class="label">画幅</span>
        <NRadioGroup v-model:value="aspect" size="small">
          <NRadioButton value="9:16" title="竖屏，手机优先">9:16 竖屏</NRadioButton>
          <NRadioButton value="16:9" title="横屏，PC/大屏">16:9 横屏</NRadioButton>
        </NRadioGroup>
      </div>
      <div v-if="error" class="err">{{ error }}</div>
      <div class="foot">
        <span class="hint">{{ footHint }}</span>
        <NButton type="primary" :disabled="!name.trim()" :loading="creating" @click="create">创建</NButton>
      </div>
    </div>
  </NModal>
</template>

<!-- modal 挂在 body 下，样式需全局作用域 -->
<style>
.np-modal { width: 560px; }
</style>

<style scoped>
/* 表单布局样式：8850e10「弹窗全局皮合并」时误删过（全局皮只合并了 .app-modal 壳，
   漏了这半），画幅行挤作一团——恢复自 642c01d，勿再并入全局块 */
.body { display: flex; flex-direction: column; gap: 14px; }
.field { display: flex; flex-direction: column; gap: 6px; }
.label { font-size: 12px; color: var(--muted); display: flex; align-items: baseline; gap: 8px; letter-spacing: 0.04em; }
.label em { font-style: normal; font-size: 11px; color: var(--muted); opacity: 0.65; }
.opts { display: flex; align-items: center; gap: 12px; }
.err { color: var(--err); font-size: 13px; }
.foot { display: flex; justify-content: space-between; align-items: center; margin-top: 2px; gap: 12px; }
.hint { color: var(--muted); opacity: 0.7; font-size: 11.5px; line-height: 1.5; }
</style>
