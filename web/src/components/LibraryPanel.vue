<script setup lang="ts">
import { ref } from 'vue'
import { NButton, NTag } from 'naive-ui'
import SampleFrame from './SampleFrame.vue'
import TemplateDetail from './TemplateDetail.vue'
import type { StyleInfo, StylePack, TemplateDetailData } from '../types'

const props = defineProps<{ packs: StylePack[]; registryStyles: StyleInfo[] }>()
const emit = defineEmits<{ publish: [id: string]; 'browse-elements': [styleId: string] }>()

// 点卡片开详情抽屉：tokens/layouts/关键词按 direction 对注册表 join（老项目包未命中则降级）
const show = ref(false)
const detail = ref<TemplateDetailData | null>(null)
function open(p: StylePack) {
  const reg = props.registryStyles.find(s => s.direction === p.direction)
  detail.value = {
    title: p.name,
    status: { label: p.published ? '已入库' : '草稿', type: p.published ? 'success' : 'warning' },
    direction: p.direction,
    genre: reg?.genre,
    keywords: reg?.keywords,
    photo: reg?.photo,
    layouts: reg?.layouts,
    tokens: reg?.tokens ?? null,
    samples: [{ tag: p.sample_tag || '样张', html: p.sample_html }],
    originLabel: '来源 ' + p.origin_project,
    createdAt: new Date(p.created_at).toLocaleDateString(),
    desc: p.desc,
    extraTags: [
      p.skin ? '风格文件' : '',
      p.example_specs?.length ? `示例spec×${p.example_specs.length}` : '',
      p.assets?.length ? `素材×${p.assets.length}` : '',
    ].filter(Boolean),
    registryStyleId: reg?.id,
    packId: p.id,
    published: p.published,
  }
  show.value = true
}
function onPublish(id: string) {
  show.value = false // 关抽屉再入库：入库后 packs 刷新，避免抽屉里状态过期
  emit('publish', id)
}
</script>

<template>
  <section class="lib">
    <header class="head">
      <h2>模板库</h2>
      <p>入库的风格 + 配套参考包（风格文件 / 样张 / 示例 spec / 素材清单）——皮肤定死、编排仿照、内容自由。点击卡片看样张与风格详情。</p>
    </header>
    <div v-if="!packs.length" class="none">
      还没有条目（冷启动零预置）。出一条片、点「出片」后，Agent 会自动起草风格包，到这里确认入库。
    </div>
    <div v-else class="grid">
      <figure v-for="p in packs" :key="p.id" class="card" :class="{ draft: !p.published }" @click="open(p)">
        <SampleFrame :html="p.sample_html" />
        <figcaption>
          <div class="row1">
            <b>{{ p.name }}</b>
            <NTag size="small" :bordered="false" :type="p.published ? 'success' : 'warning'">
              {{ p.published ? '已入库' : '草稿' }}
            </NTag>
          </div>
          <span class="desc">{{ p.desc }}</span>
          <div class="meta">
            <NTag v-if="p.skin" size="tiny" :bordered="false" type="info">风格文件</NTag>
            <NTag v-if="p.example_specs?.length" size="tiny" :bordered="false" type="info">
              示例spec×{{ p.example_specs.length }}
            </NTag>
            <NTag v-if="p.assets?.length" size="tiny" :bordered="false" type="info">
              素材×{{ p.assets.length }}
            </NTag>
          </div>
          <div class="row2">
            <span class="origin">来源 {{ p.origin_project }}</span>
            <NButton v-if="!p.published" size="small" type="primary" ghost @click.stop="emit('publish', p.id)">
              入库
            </NButton>
          </div>
        </figcaption>
      </figure>
    </div>

    <TemplateDetail :show="show" :detail="detail" @close="show = false"
      @publish="onPublish" @browse-elements="sid => emit('browse-elements', sid)" />
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
.desc { font-size: 11.5px; color: #8a8a96; }
.meta { display: flex; gap: 6px; flex-wrap: wrap; }
.row2 { display: flex; align-items: center; justify-content: space-between; }
.origin { font-size: 11px; color: #55555f; }
</style>
