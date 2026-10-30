<script setup lang="ts">
import { ref, watch } from 'vue'
import { NButton, NInput, useMessage } from 'naive-ui'
import type { ProjectView } from '../types'
import { fmtClock } from '../segtime'
import { api, videoURL } from '../api'

// 封面页签（2026-10-30 从视频页右栏拆出）：出片封面预览 + 标题 + 重做。
// 「用当前画面」需要指一帧——页内自带取帧播放器，拖到想要的位置再点按钮。
const props = defineProps<{
  view: ProjectView
  videoId: string
  videoRev: number
}>()
const message = useMessage()

// 本地账面优先于 props.view.cover（重做成功即刷新，不等下一轮轮询回填）
const cover = ref<{ t: number; title: string } | null>(null)
const coverTitle = ref('')
const coverDirty = ref(false)
const coverBusy = ref(false)
const coverRev = ref(0)
const coverMissing = ref(false)
watch(() => props.view, v => {
  if (!coverDirty.value) {
    cover.value = v?.cover ?? null
    if (v?.cover?.title) coverTitle.value = v.cover.title
  }
}, { immediate: true })

async function redoCover(useCurrentFrame: boolean) {
  if (coverBusy.value || props.view.producing) return
  coverBusy.value = true
  try {
    const body: { t?: number; title?: string } = { title: coverTitle.value }
    if (useCurrentFrame) body.t = frameEl.value?.currentTime ?? 0
    const r = await api.regenCover(props.videoId, body)
    cover.value = { t: r.t, title: r.title }
    coverTitle.value = r.title
    coverDirty.value = false
    coverMissing.value = false
    coverRev.value++
    message.success(useCurrentFrame ? `封面已换成 ${fmtClock(r.t)} 的画面` : '封面已重做')
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    coverBusy.value = false
  }
}

// 取帧播放器：只负责选位置（不与时间轴联动）
const frameEl = ref<HTMLVideoElement | null>(null)
const frameT = ref(0)
</script>

<template>
  <div class="cover-pane">
    <template v-if="view.has_video">
      <div class="cover-grid">
        <!-- 当前封面：预览 + 标题 + 重做（制作中隐藏动作——收尾自动出封面） -->
        <div class="card">
          <div class="card-head">封面<span v-if="cover" class="card-meta" title="封面帧在成片里的时刻">{{ fmtClock(cover.t) }} 帧</span></div>
          <img
            v-if="!coverMissing" class="cover-thumb" :class="{ vertical: view.aspect === '9:16' }"
            :src="api.coverURL(videoId, coverRev)" alt="封面预览"
            @error="coverMissing = true"
          />
          <div v-else class="cover-thumb none" :class="{ vertical: view.aspect === '9:16' }">尚未生成</div>
          <NInput
            v-model:value="coverTitle" size="small" :disabled="coverBusy"
            placeholder="封面标题（留空用项目名）" @update:value="coverDirty = true"
          />
          <div v-if="view.producing" class="card-hint">制作管线运行中——收尾会自动重出封面</div>
          <div v-else class="card-btns">
            <NButton size="small" :loading="coverBusy" :disabled="coverBusy" @click="redoCover(false)">重做封面</NButton>
            <NButton size="small" type="primary" :loading="coverBusy" :disabled="coverBusy" @click="redoCover(true)">用当前画面</NButton>
          </div>
        </div>
        <!-- 取帧播放器：拖到想要的一帧，配合「用当前画面」抽此刻画面 -->
        <div class="card">
          <div class="card-head">从成片取画面</div>
          <video
            ref="frameEl" class="frame-player" :class="{ vertical: view.aspect === '9:16' }"
            :src="videoURL(videoId, videoRev)" controls preload="metadata"
            @timeupdate="frameT = frameEl!.currentTime"
          />
          <p class="card-hint">把播放器拖到想要的一帧，点左侧「用当前画面」，封面就换成 {{ fmtClock(frameT) }} 处的画面。</p>
        </div>
      </div>
    </template>
    <div v-else class="empty">还没有成片——封面在制作收尾时自动产出。</div>
  </div>
</template>

<style scoped>
.cover-pane {
  flex: 1; min-height: 0; overflow-y: auto;
  display: flex; align-items: flex-start; justify-content: center; padding: 24px 20px;
}
.cover-grid { display: flex; gap: 16px; align-items: stretch; justify-content: center; flex-wrap: wrap; }
.card {
  display: flex; flex-direction: column; gap: 10px; width: 380px;
  border: 1px solid var(--line-soft); padding: 12px 14px 14px;
}
.card-head {
  font-size: 12px; font-weight: 600; color: var(--ink);
  display: flex; align-items: baseline; gap: 8px;
}
.card-meta { font-weight: 400; font-size: 11.5px; color: var(--muted); font-variant-numeric: tabular-nums; }
.card-hint { margin: 0; font-size: 12px; line-height: 1.8; color: var(--muted); }
.card-btns { display: flex; align-items: center; gap: 8px; }
.cover-thumb {
  width: 100%; aspect-ratio: 16 / 9; object-fit: cover; display: block;
  border: 1px solid var(--line); background: #000;
}
.cover-thumb.vertical { width: auto; height: 300px; aspect-ratio: 9 / 16; margin: 0 auto; }
.cover-thumb.none {
  display: flex; align-items: center; justify-content: center;
  color: var(--muted); font-size: 12px; background: color-mix(in srgb, var(--ink) 4%, var(--surface));
}
.frame-player { width: 100%; aspect-ratio: 16 / 9; background: #000; display: block; }
.frame-player.vertical { width: auto; height: 300px; aspect-ratio: 9 / 16; margin: 0 auto; }
.empty { flex: 1; display: flex; align-items: center; justify-content: center; color: var(--muted); font-size: 13px; }
</style>
