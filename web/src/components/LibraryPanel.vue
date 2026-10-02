<script setup lang="ts">
import { ref } from 'vue'
import { NButton, NTag } from 'naive-ui'
import { coverStyle } from '../format'
import SampleFrame from './SampleFrame.vue'
import TemplateDetail from './TemplateDetail.vue'
import type { Style, TemplateDetailData } from '../types'

const props = defineProps<{ packs: Style[] }>()
const emit = defineEmits<{ publish: [id: string] }>()

// 点卡片开详情抽屉：条目本体自带 tokens/layouts/全套样张，直接规范化
const show = ref(false)
const detail = ref<TemplateDetailData | null>(null)
function open(s: Style) {
  detail.value = {
    title: s.name,
    status: { label: s.published ? '已入库' : '草稿', type: s.published ? 'success' : 'warning' },
    direction: s.direction,
    genre: s.genre,
    keywords: s.keywords,
    layouts: s.layouts,
    tokens: s.tokens ?? null,
    samples: (s.samples ?? []).map(x => ({ tag: x.tag, desc: x.desc, html: x.html })),
    originLabel: '来源 ' + (s.origin_project || '—'),
    createdAt: new Date(s.created_at).toLocaleDateString(),
    desc: s.desc,
    extraTags: [
      s.skin ? '风格文件' : '',
      s.layouts?.length ? `版式×${s.layouts.length}` : '',
      s.tokens ? '色板' : '',
    ].filter(Boolean),
    libMediaId: s.origin_project?.startsWith('video:') ? s.id : undefined,
    packId: s.id,
    published: s.published,
  }
  show.value = true
}
function onPublish(id: string) {
  show.value = false // 关抽屉再入库：入库后 packs 刷新，避免抽屉里状态过期
  emit('publish', id)
}

// 卡片副行：题材 + 关键词摘要
function subline(s: Style): string {
  const kw = (s.keywords ?? []).slice(0, 3).join(' · ')
  return [s.genre, kw].filter(Boolean).join('　')
}
</script>

<template>
  <section class="lib">
    <header class="head">
      <h2>风格库</h2>
      <p>可复用的视觉风格：色板 tokens 定死、版式仿照、内容自由——用同一风格出的片子同一套皮肤、相似气质。点击卡片看样张与色板详情。</p>
    </header>
    <div v-if="!packs.length" class="none">
      风格库还是空的。出一条片、成片出片后 Agent 会自动起草风格，到这里确认入库；或在「视频解析」上传参考视频提炼。
    </div>
    <div v-else class="grid">
      <figure v-for="s in packs" :key="s.id" class="card" :class="{ draft: !s.published }" @click="open(s)">
        <SampleFrame v-if="s.samples?.[0]?.html" :html="s.samples![0].html" />
        <div v-else class="cover" :style="coverStyle(s)">
          <b>{{ s.name }}</b>
        </div>
        <figcaption>
          <div class="row1">
            <b>{{ s.name }}</b>
            <NTag size="small" :bordered="false" :type="s.published ? 'success' : 'warning'">
              {{ s.published ? '已入库' : '草稿' }}
            </NTag>
          </div>
          <span v-if="subline(s)" class="sub">{{ subline(s) }}</span>
          <span v-if="s.desc" class="desc">{{ s.desc }}</span>
          <div class="meta">
            <NTag v-if="s.skin" size="tiny" :bordered="false" type="info">风格文件</NTag>
            <NTag v-if="s.layouts?.length" size="tiny" :bordered="false" type="info">版式×{{ s.layouts.length }}</NTag>
            <NTag v-if="(s.samples?.length ?? 0) > 1" size="tiny" :bordered="false" type="info">样张×{{ s.samples!.length }}</NTag>
          </div>
          <div class="row2">
            <span class="origin">{{ s.id }}</span>
            <NButton v-if="!s.published" size="small" type="primary" ghost @click.stop="emit('publish', s.id)">
              入库
            </NButton>
          </div>
        </figcaption>
      </figure>
    </div>

    <TemplateDetail :show="show" :detail="detail" @close="show = false" @publish="onPublish" />
  </section>
</template>

<style scoped>
.lib { flex: 1; min-height: 0; overflow: auto; padding: 24px 28px; }
.head h2 { font-size: 16px; color: #d9d9e0; }
.head p { font-size: 12.5px; color: #7c7c88; margin: 4px 0 18px; }
.none { color: #55555f; font-size: 13px; padding: 40px 0; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 16px; }
.card {
  background: #14141a; border: 1px solid #232329; border-radius: 12px; overflow: hidden;
  cursor: pointer; transition: border-color .15s;
}
.card:hover { border-color: #3a3a44; }
.card.draft { border-color: #5c4d24; }
figcaption { padding: 10px 12px 12px; display: flex; flex-direction: column; gap: 6px; }
.row1 { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.row1 b { font-size: 13px; }
.sub { font-size: 11.5px; color: #e0c985; }
.desc { font-size: 11.5px; color: #8a8a96; }
.meta { display: flex; gap: 6px; flex-wrap: wrap; }
.row2 { display: flex; align-items: center; justify-content: space-between; }
.origin { font-size: 11px; color: #55555f; font-family: monospace; }
.cover {
  height: 169px; display: grid; place-items: center; padding: 0 18px; text-align: center;
}
.cover b { color: #fff; font-size: 16px; letter-spacing: 1px; text-shadow: 0 1px 6px rgba(0,0,0,.45); }
</style>
