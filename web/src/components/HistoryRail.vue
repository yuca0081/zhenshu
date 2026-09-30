<script setup lang="ts">
import { ref } from 'vue'
import { NPopover, NScrollbar } from 'naive-ui'
import type { ProjectRow } from '../types'

defineProps<{ projects: ProjectRow[]; current: string }>()
const emit = defineEmits<{ select: [id: string] }>()

// 折叠状态本地记忆：展开 = 宽栏（全名+状态），收起 = 56px 图标栏
const collapsed = ref(localStorage.getItem('rail-collapsed') === '1')
function toggle() {
  collapsed.value = !collapsed.value
  localStorage.setItem('rail-collapsed', collapsed.value ? '1' : '0')
}

const statusMap: Record<string, { label: string; cls: string }> = {
  created: { label: '已创建', cls: '' },
  manuscript: { label: '有文稿', cls: '' },
  storyboard: { label: '有分镜', cls: 's-draft' },
  style: { label: '风格定', cls: 's-draft' },
  producing: { label: '制作中', cls: 's-run' },
  video: { label: '有成片', cls: 's-done' },
}
const statusOf = (s: string) => statusMap[s] ?? { label: s, cls: '' }
const dateOf = (iso: string) => iso.slice(5, 10).replace('-', '/')
</script>

<template>
  <aside class="rail" :class="{ mini: collapsed }">
    <div class="head">
      <span v-if="!collapsed" class="head-title">项目 · {{ projects.length }}</span>
      <button class="fold" :title="collapsed ? '展开栏' : '收起栏'" @click="toggle">
        <svg v-if="!collapsed" viewBox="0 0 24 24" width="14" height="14"><path d="M15 18l-6-6 6-6" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>
        <svg v-else viewBox="0 0 24 24" width="14" height="14"><path d="M9 18l6-6-6-6" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>
      </button>
    </div>

    <NScrollbar class="list">
      <template v-if="!projects.length">
        <div class="empty">暂无项目</div>
      </template>
      <NPopover v-for="p in projects" :key="p.id" placement="right" :disabled="!collapsed" :delay="200" trigger="hover">
        <template #trigger>
          <div class="item" :class="{ active: p.id === current }" @click="emit('select', p.id)">
            <span class="ava">{{ p.name.slice(0, 2) }}</span>
            <span v-if="!collapsed" class="info">
              <span class="nm">{{ p.name }}</span>
              <span class="sub">
                <i class="st" :class="statusOf(p.status).cls" />{{ statusOf(p.status).label }}
                <em>{{ dateOf(p.created_at) }}</em>
              </span>
            </span>
          </div>
        </template>
        <div class="pop">
          <div class="pop-name">{{ p.name }}</div>
          <div class="pop-meta">{{ statusOf(p.status).label }}{{ p.has_video ? ' · 可下载成片' : '' }}</div>
        </div>
      </NPopover>
    </NScrollbar>
  </aside>
</template>

<style scoped>
.rail {
  width: 232px; flex: none; display: flex; flex-direction: column;
  background: linear-gradient(180deg, #0c0c11, #0a0a0e); border-right: 1px solid #1d1d24;
  transition: width .18s ease;
}
.rail.mini { width: 56px; align-items: center; }

.head { flex: none; display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 14px 14px 10px; min-height: 50px; }
.mini .head { padding: 14px 0 10px; justify-content: center; }
.head-title { font-size: 11px; color: #55555f; letter-spacing: 1px; }
.fold {
  border: 0; background: transparent; color: #55555f; cursor: pointer; padding: 4px; border-radius: 6px;
  display: flex; align-items: center;
}
.fold:hover { color: #c9c9d1; background: #1a1a21; }

.list { flex: 1; width: 100%; }
.empty { color: #55555f; font-size: 11px; text-align: center; padding-top: 20px; }
.item {
  margin: 2px 8px; padding: 8px 10px; border-radius: 10px; cursor: pointer;
  display: flex; align-items: center; gap: 10px; border: 1px solid transparent;
  transition: background .15s, border-color .15s;
}
.mini .item { margin: 4px auto; padding: 0; border-radius: 10px; width: 36px; height: 36px; justify-content: center; }
.item:hover { background: #17171e; }
.item.active { background: #1d1a12; border-color: rgba(240, 198, 116, .4); }
.ava {
  width: 32px; height: 32px; flex: none; border-radius: 9px; overflow: hidden;
  display: flex; align-items: center; justify-content: center;
  background: #1a1a21; color: #b9b9c4; font-size: 12px;
}
.item.active .ava { color: #f0c674; background: #241f14; }
.info { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.nm { font-size: 13px; color: #d5d5dc; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.item.active .nm { color: #f0c674; }
.sub { display: flex; align-items: center; gap: 5px; font-size: 11px; color: #6b6b77; }
.sub em { font-style: normal; color: #4c4c56; margin-left: auto; }
.st { width: 6px; height: 6px; border-radius: 50%; background: #3a3a45; flex: none; }
.st.s-draft { background: #8fc7ff; }
.st.s-run { background: #f0c674; animation: blink 1.2s infinite; }
.st.s-done { background: #7ee2a8; }
@keyframes blink { 50% { opacity: .3; } }

.pop { max-width: 220px; }
.pop-name { font-size: 13px; font-weight: 600; }
.pop-meta { font-size: 11px; color: #888; margin-top: 2px; }
.fade-enter-active, .fade-leave-active { transition: opacity .12s; }
.fade-enter-from, .fade-leave-to { opacity: 0; }
</style>
