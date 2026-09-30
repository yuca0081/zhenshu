<script setup lang="ts">
import { ref, watch } from 'vue'
import { NButton, NInput, NModal, NRadioButton, NRadioGroup } from 'naive-ui'
import { api } from '../api'

// 新建三件事：名称 + 主题 + 画幅。文稿/分镜/风格是三前置硬门，
// 进工作区后点对话栏上方的同名标签补齐（文稿弹窗可 AI 起草，风格弹窗可选库内或描述方向）。
const show = defineModel<boolean>('show')
const emit = defineEmits<{ created: [id: string] }>()
const name = ref('')
const topic = ref('')
const aspect = ref('9:16')
const creating = ref(false)
const error = ref('')

watch(show, v => {
  if (v) {
    name.value = ''; topic.value = ''; aspect.value = '9:16'; error.value = ''
  }
})

async function create() {
  if (!topic.value.trim()) { error.value = '主题必填——一句话说清要讲什么'; return }
  creating.value = true
  error.value = ''
  try {
    const r = await api.createProject(name.value.trim(), topic.value.trim(), aspect.value)
    emit('created', r.id)
  } catch (e: any) {
    error.value = e.message ?? String(e)
  } finally {
    creating.value = false
  }
}
</script>

<template>
  <NModal v-model:show="show" preset="card" title="新建视频" class="np-modal" :mask-closable="!creating">
    <div class="body">
      <div class="field">
        <span class="label">主题 <em>一句话说清要讲什么——文稿、分镜都从它长出来</em></span>
        <NInput
          v-model:value="topic" maxlength="120"
          placeholder="例：蚊子为什么嗡嗡叫"
          @keydown.enter.prevent="create"
        />
      </div>
      <div class="field">
        <span class="label">项目名 <em>留空取主题</em></span>
        <NInput v-model:value="name" maxlength="48" placeholder="例：蜂巢为什么是六边形" />
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
        <span class="hint">文稿、分镜、风格进工作区后在对话栏上方补齐，齐了就能开工</span>
        <NButton type="primary" :disabled="!topic.trim()" :loading="creating" @click="create">创建</NButton>
      </div>
    </div>
  </NModal>
</template>

<!-- modal 挂在 body 下，样式需全局作用域 -->
<style>
.np-modal {
  width: 560px; background: #14141a; border: 1px solid #26262e; border-radius: 14px;
  --n-padding-left: 22px; --n-padding-right: 22px; --n-padding-top: 20px; --n-padding-bottom: 20px;
}
.np-modal .n-card-header { padding: 18px 22px 0; }
.np-modal .n-card-header__main { font-weight: 700; letter-spacing: 2px; color: #f0c674; font-size: 15px; }
.np-modal .n-card__content { padding: 14px 22px 20px; }
.np-modal .n-base-close { color: #7c7c88; top: 16px; right: 16px; }
.np-modal .n-base-close:hover { color: #e6e6ea; }
.np-modal .n-input { background: #101016; border: 1px solid #232329; border-radius: 10px; }
.np-modal .n-input:hover { border-color: #3a3a45; }
.np-modal .n-input--focus { border-color: rgba(240, 198, 116, .55); }
</style>

<style scoped>
.body { display: flex; flex-direction: column; gap: 14px; }
.field { display: flex; flex-direction: column; gap: 6px; }
.label { font-size: 12px; color: #9a9aa6; display: flex; align-items: baseline; gap: 8px; }
.label em { font-style: normal; font-size: 11px; color: #55555f; }
.opts { display: flex; align-items: center; gap: 12px; }
.err { color: #ff9d9d; font-size: 13px; }
.foot { display: flex; justify-content: space-between; align-items: center; margin-top: 2px; gap: 12px; }
.hint { color: #55555f; font-size: 11.5px; line-height: 1.5; }
</style>
