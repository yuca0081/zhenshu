<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NButton, NInput, NModal, NRadioButton, NRadioGroup } from 'naive-ui'
import { api } from '../api'
import { errMsg } from '../format'

// 新建四件事：驱动模式 + 主题 + 项目名 + 画幅。文稿/分镜/风格三前置走「想法」
// 页签的准备工位自动完成；音乐驱动走 PV 管线（做曲→音乐地图→分镜，M1 开发中，
// 项目可先建占位——工作区进 PV 准备页签，制作入口前后端都拦）。
const show = defineModel<boolean>('show')
const emit = defineEmits<{ created: [id: string] }>()
const name = ref('')
const topic = ref('')
const aspect = ref('9:16')
const mode = ref<'script' | 'music'>('script')
const creating = ref(false)
const error = ref('')

// 文案随驱动模式换：音乐项目的主题是「情绪与画面想象」（分镜从曲子长出来），
// 不是「讲什么」——同一个输入框，两种世界观。
const topicHint = computed(() => mode.value === 'music'
  ? '一句话说清这支 PV 的情绪与画面想象——分镜从曲子长出来'
  : '一句话说清要讲什么——文稿、分镜都从它长出来')
const topicPlaceholder = computed(() => mode.value === 'music'
  ? '例：午夜城市霓虹下的孤独穿行' : '例：蚊子为什么嗡嗡叫')
const footHint = computed(() => mode.value === 'music'
  ? '音乐驱动管线开发中（做曲 → 音乐地图 → 分镜，原型已跑通）：项目可先建，准备工位上线前暂不能出片'
  : '文稿、分镜、风格进工作区后在对话栏上方补齐，齐了就能开工')

watch(show, v => {
  if (v) {
    name.value = ''; topic.value = ''; aspect.value = '9:16'
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
  if (!topic.value.trim()) { error.value = '主题必填——一句话说清要讲什么'; return }
  creating.value = true
  error.value = ''
  try {
    const r = await api.createProject(name.value.trim(), topic.value.trim(), aspect.value, mode.value)
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
        <span class="label">主题 <em>{{ topicHint }}</em></span>
        <NInput
          v-model:value="topic" maxlength="120"
          :placeholder="topicPlaceholder"
          @keydown.enter="onEnter"
        />
      </div>
      <div class="field">
        <span class="label">项目名 <em>留空取主题</em></span>
        <NInput v-model:value="name" maxlength="48" placeholder="例：蜂巢为什么是六边形" />
      </div>
      <div class="opts">
        <span class="label">驱动</span>
        <NRadioGroup v-model:value="mode" size="small">
          <NRadioButton value="script" title="从一句话主题长出文稿→分镜→风格，现行管线">文稿驱动</NRadioButton>
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
        <NButton type="primary" :disabled="!topic.trim()" :loading="creating" @click="create">创建</NButton>
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