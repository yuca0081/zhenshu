<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NTag, NTabs, NTabPane } from 'naive-ui'
import LibraryPanel from './LibraryPanel.vue'
import RefVideos from './RefVideos.vue'
import AssetPanel from './AssetPanel.vue'
import { api, galleryURL } from '../api'
import type { ElementInfo, StyleInfo, StylePack } from '../types'

const props = defineProps<{ packs: StylePack[] }>()
const emit = defineEmits<{ publish: [id: string]; ingested: [] }>()

// 元素库状态：注册表（elements/styles 顺序即权威顺序）+ 样张矩阵清单
const tab = ref<'styles' | 'elements'>('styles')
const elements = ref<ElementInfo[]>([])
const styles = ref<StyleInfo[]>([])
const gallery = ref<Record<string, string[]>>({})
const curStyle = ref('')

onMounted(async () => {
  const r = await api.registry().catch(() => null)
  if (r) { elements.value = r.elements; styles.value = r.styles }
  gallery.value = await api.gallery().catch(() => ({}))
  if (!curStyle.value) curStyle.value = styleCols.value[0]?.id ?? ''
})

// 风格列 = 注册表顺序在前，样张矩阵里有而注册表没有的（hand/flat 等引擎列）殿后
const styleCols = computed(() => {
  const known = new Set(styles.value.map(s => s.id))
  const extras = Object.keys(gallery.value).filter(id => !known.has(id))
  return [...styles.value, ...extras.map(id => ({ id, direction: id, genre: '', keywords: [], photo: '' }))]
})

// 选中风格列下的元素卡片：按注册表顺序，只显示已有样张的 kind
const cards = computed(() => {
  const have = new Set(gallery.value[curStyle.value] ?? [])
  return elements.value.filter(e => have.has(e.kind))
})
const styleCover = (id: string) => (gallery.value[id]?.includes('_overview') ? galleryURL(id, '_overview') : '')
const empty = computed(() => !Object.keys(gallery.value).length)

// 详情抽屉「去元素库」联动：切 tab 并选中该风格列
function showInElements(styleId: string) {
  curStyle.value = styleId
  tab.value = 'elements'
}
</script>

<template>
  <section class="mc">
    <NTabs v-model:value="tab" type="line" size="small" class="tabs">
      <NTabPane name="styles" tab="模板库">
        <LibraryPanel :packs="props.packs" :registry-styles="styles" @publish="emit('publish', $event)"
          @browse-elements="showInElements" />
      </NTabPane>
      <NTabPane name="assets" tab="素材库">
        <AssetPanel />
      </NTabPane>
      <NTabPane name="videos" tab="视频解析">
        <RefVideos :registry-styles="styles" @ingested="emit('ingested')" @browse-elements="showInElements" />
      </NTabPane>
      <NTabPane name="elements" tab="元素库" class="pane">
        <div v-if="empty" class="none">
          样张矩阵还没渲染。在仓库根跑 <code>python ai/element_gallery.py all</code>，
          会为每个风格 × 每个元素各出一张定妆 PNG（可重入，已完成的自动跳过）。
        </div>
        <div v-else class="wrap">
          <aside class="cols">
            <button
              v-for="s in styleCols" :key="s.id"
              class="col" :class="{ on: s.id === curStyle }"
              :title="s.direction"
              @click="curStyle = s.id"
            >
              <img v-if="styleCover(s.id)" :src="styleCover(s.id)" loading="lazy" alt="" />
              <b>{{ s.id }}</b>
              <span>{{ s.genre || s.direction }}</span>
            </button>
          </aside>
          <div class="grid">
            <figure v-for="e in cards" :key="e.kind" class="card" :title="e.scene">
              <img :src="galleryURL(curStyle, e.kind)" loading="lazy" alt="" />
              <figcaption>
                <div class="row">
                  <b>{{ e.name }}</b>
                  <NTag size="tiny" :bordered="false">{{ e.category }}</NTag>
                </div>
                <code>{{ e.kind }}</code>
              </figcaption>
            </figure>
          </div>
        </div>
      </NTabPane>
    </NTabs>
  </section>
</template>

<style scoped>
.mc { flex: 1; min-height: 0; padding: 14px 28px 14px; }
/* 高度链打通到 tab pane，滚动条才出得来（naive-ui 实际类名是单数 n-tab-pane） */
.mc :deep(.n-tabs) { height: 100%; display: flex; flex-direction: column; }
.mc :deep(.n-tabs-nav) { flex: none; }
.mc :deep(.n-tabs-content) { flex: 1; min-height: 0; }
.mc :deep(.n-tab-pane) { height: 100%; overflow: auto; }
.none { color: #55555f; font-size: 13px; padding: 40px 0; }
.none code { background: #1b1b22; padding: 2px 8px; border-radius: 6px; font-size: 12px; }
.wrap { display: flex; gap: 16px; align-items: flex-start; }
.cols { flex: none; width: 132px; display: flex; flex-direction: column; gap: 8px; position: sticky; top: 0; }
.col {
  display: flex; flex-direction: column; gap: 3px; text-align: left; cursor: pointer;
  background: #14141a; border: 1px solid #232329; border-radius: 10px; padding: 6px;
  color: #c9c9d1; overflow: hidden;
}
.col img { width: 100%; height: auto; display: block; border-radius: 6px; }
.col b { font-size: 12px; letter-spacing: .5px; }
.col span {
  font-size: 10.5px; color: #6f6f7c; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.col.on { border-color: #f0c674; }
.col.on b { color: #f0c674; }
.grid { flex: 1; display: grid; grid-template-columns: repeat(auto-fill, minmax(240px, 1fr)); gap: 14px; align-content: start; }
.card { background: #14141a; border: 1px solid #232329; border-radius: 10px; overflow: hidden; }
.card img { width: 100%; height: auto; display: block; background: #0a0a10; }
figcaption { padding: 8px 10px 10px; display: flex; flex-direction: column; gap: 3px; }
.row { display: flex; align-items: center; justify-content: space-between; gap: 6px; }
.row b { font-size: 12.5px; }
figcaption code { font-size: 10.5px; color: #6f6f7c; }
</style>
