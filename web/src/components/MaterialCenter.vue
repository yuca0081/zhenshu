<script setup lang="ts">
import { ref } from 'vue'
import { NTabs, NTabPane } from 'naive-ui'
import LibraryPanel from './LibraryPanel.vue'
import RefVideos from './RefVideos.vue'
import type { Style } from '../types'

const props = defineProps<{ packs: Style[] }>()
const emit = defineEmits<{ publish: [id: string]; ingested: [] }>()

const tab = ref<'styles' | 'videos'>('styles')
</script>

<template>
  <section class="mc">
    <NTabs v-model:value="tab" type="line" size="small" class="tabs">
      <NTabPane name="styles" tab="风格库">
        <LibraryPanel :packs="props.packs" @publish="emit('publish', $event)" />
      </NTabPane>
      <NTabPane name="videos" tab="视频解析">
        <RefVideos :styles="props.packs" @ingested="emit('ingested')" />
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
</style>
