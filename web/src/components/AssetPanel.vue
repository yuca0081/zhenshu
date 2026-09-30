<script setup lang="ts">
// 素材库（asset-library.md §4/§5）：catalog 统一检索——type 圈范围 + tags/desc 匹配。
// 入库走管线（fetch 打标 / gen prompt / BGM builtin），这里只读：检索 + 预览 + 标签。
import { onMounted, ref } from 'vue'
import { NInput, NTag } from 'naive-ui'
import { api, assetFileURL } from '../api'
import type { AssetEntry } from '../types'

const q = ref('')
const type = ref<'image' | 'audio' | ''>('image')
const entries = ref<AssetEntry[]>([])
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    const r = await api.assets(type.value, q.value.trim())
    entries.value = r.entries ?? []
  } catch {
    entries.value = []
  } finally {
    loading.value = false
  }
}

onMounted(load)

function pick(t: 'image' | 'audio' | '') {
  type.value = t
  load()
}

const originName: Record<string, string> = {
  fetch: '搜图', gen: '生图', upload: '上传', builtin: '内置', film: '成片',
}
</script>

<template>
  <section class="assets">
    <header class="bar">
      <div class="types">
        <button v-for="t in (['image', 'audio', ''] as const)" :key="t" class="tbtn" :class="{ on: type === t }" @click="pick(t)">
          {{ t === 'image' ? '图片' : t === 'audio' ? '音频' : '全部' }}
        </button>
      </div>
      <NInput v-model:value="q" size="small" placeholder="按标签/描述检索（如：实验室、冷色调、BGM）" clearable @keyup.enter="load" @clear="load" />
      <button class="go" @click="load">检索</button>
    </header>

    <div v-if="!entries.length && !loading" class="none">
      素材库还是空的。制作项目时配图（搜图/生图）会自动打标入库，这里会越来越厚。
    </div>

    <div v-else class="grid">
      <figure v-for="e in entries" :key="e.id" class="card">
        <img v-if="e.type === 'image'" :src="assetFileURL(e.id)" loading="lazy" alt="" />
        <div v-else class="audio">
          <audio :src="assetFileURL(e.id)" controls preload="none" />
        </div>
        <figcaption>
          <div class="row">
            <b>{{ e.id }}</b>
            <NTag size="tiny" :bordered="false">{{ originName[e.origin] ?? e.origin }}</NTag>
          </div>
          <span class="desc">{{ e.desc }}</span>
          <div class="tags">
            <span v-for="t in e.tags.slice(0, 6)" :key="t" class="tag">{{ t }}</span>
          </div>
          <span v-if="e.meta?.w" class="dim">{{ e.meta.w }}×{{ e.meta.h }}</span>
        </figcaption>
      </figure>
    </div>
  </section>
</template>

<style scoped>
.assets { flex: 1; min-height: 0; overflow: auto; padding: 16px 20px; }
.bar { display: flex; gap: 10px; align-items: center; margin-bottom: 14px; }
.bar :deep(.n-input) { max-width: 360px; }
.types { display: flex; gap: 6px; }
.tbtn {
  border: 1px solid #2b2b33; background: #14141a; color: #8a8a96; cursor: pointer;
  font-size: 12px; padding: 4px 14px; border-radius: 999px;
}
.tbtn.on { color: #f0c674; border-color: #5c4d24; }
.go {
  border: 1px solid #2b4a66; background: #16222c; color: #8fc7ff; cursor: pointer;
  font-size: 12px; padding: 4px 16px; border-radius: 999px;
}
.none { color: #55555f; font-size: 13px; padding: 40px 0; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 14px; align-content: start; }
.card { background: #14141a; border: 1px solid #232329; border-radius: 10px; overflow: hidden; }
.card img { width: 100%; height: 140px; object-fit: cover; display: block; background: #0a0a10; }
.audio { padding: 14px 12px; }
figcaption { padding: 8px 10px 10px; display: flex; flex-direction: column; gap: 4px; }
.row { display: flex; align-items: center; justify-content: space-between; }
.row b { font-size: 12px; }
.desc { font-size: 11.5px; color: #8a8a96; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
.tags { display: flex; flex-wrap: wrap; gap: 4px; }
.tag { font-size: 10.5px; color: #7c7c88; background: #1b1b22; border-radius: 4px; padding: 1px 6px; }
.dim { font-size: 10.5px; color: #55555f; }
</style>
