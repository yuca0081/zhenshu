<script setup lang="ts">
import { ref } from 'vue'
import { NPopover, NScrollbar } from 'naive-ui'
import { dateOf, lastProduceFailed, statusDotCls, statusLabel } from '../format'
import type { ProjectRow } from '../types'

defineProps<{ projects: ProjectRow[]; current: string }>()
const emit = defineEmits<{ select: [id: string]; delete: [id: string] }>()

// 折叠状态本地记忆：展开 = 宽栏（全名+状态），收起 = 56px 图标栏
const collapsed = ref(localStorage.getItem('rail-collapsed') === '1')
function toggle() {
  collapsed.value = !collapsed.value
  localStorage.setItem('rail-collapsed', collapsed.value ? '1' : '0')
}

const statusOf = (s: string) => ({ label: statusLabel[s] ?? s, cls: statusDotCls[s] ?? '' })

// 删除两段式确认（2026-10-19 中-36，替代 window.confirm——原生弹窗 2026-10-16
// 死按钮、Playwright 30s 超时同源，全站已弃用）：第一枪只亮确认态，3s 内再点
// 才真删；超时自动解除
const armed = ref<string | null>(null)
let armTimer: number | null = null
function clickDel(id: string) {
  if (armed.value === id) {
    if (armTimer) clearTimeout(armTimer)
    armed.value = null
    emit('delete', id)
    return
  }
  armed.value = id
  if (armTimer) clearTimeout(armTimer)
  armTimer = window.setTimeout(() => { armed.value = null }, 3000)
}
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
                <i class="st" :class="[statusOf(p.status).cls, { 's-fail': lastProduceFailed(p) }]" />{{ statusOf(p.status).label }}{{ p.local === false ? ' · 在云端' : '' }}{{ lastProduceFailed(p) ? ' · 上次制作失败' : '' }}
                <em>{{ dateOf(p.created_at) }}</em>
              </span>
            </span>
            <button v-if="!collapsed" class="del" :class="{ armed: armed === p.id }"
                    :title="armed === p.id ? '再点一次确认删除（不可恢复）' : '删除项目（不可恢复）'"
                    @click.stop="clickDel(p.id)">{{ armed === p.id ? '确认?' : '✕' }}</button>
          </div>
        </template>
        <div class="pop">
          <div class="pop-name">{{ p.name }}</div>
          <div class="pop-meta">{{ statusOf(p.status).label }}{{ p.local === false ? ' · 在云端' : '' }}{{ p.has_video ? ' · 可下载成片' : '' }}{{ lastProduceFailed(p) ? ' · 上次制作失败' : '' }}</div>
          <button class="pop-del" :class="{ armed: armed === p.id }" @click="clickDel(p.id)">
            {{ armed === p.id ? '再点一次，确认删除（不可恢复）' : '删除项目…' }}
          </button>
        </div>
      </NPopover>
    </NScrollbar>
  </aside>
</template>

<style scoped>
.rail {
  width: 232px; flex: none; display: flex; flex-direction: column;
  background: var(--bg); border-right: 1px solid var(--line-soft);
  transition: width .18s ease;
}
.rail.mini { width: 56px; align-items: center; }

.head { flex: none; display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 14px 14px 10px; min-height: 50px; }
.mini .head { padding: 14px 0 10px; justify-content: center; }
.head-title { font-size: 11px; color: var(--muted); letter-spacing: 0.14em; text-transform: uppercase; }
.fold {
  border: 0; background: transparent; color: var(--muted); cursor: pointer; padding: 4px;
  display: flex; align-items: center;
  transition: color var(--dur-1) var(--ease);
}
.fold:hover { color: var(--ink); }

.list { flex: 1; width: 100%; }
.empty { color: var(--muted); font-size: 11px; text-align: center; padding-top: 20px; }
.item {
  position: relative;
  margin: 2px 8px; padding: 8px 10px; cursor: pointer;
  display: flex; align-items: center; gap: 10px;
  color: var(--muted);
  transition: background var(--dur-2) var(--ease), color var(--dur-2) var(--ease);
}
/* 激活项左侧 2px 强调刻线自上而下展开（Rhine side-item 语法） */
.item::before {
  content: ''; position: absolute; left: 0; top: 7px; bottom: 7px; width: 2px;
  background: var(--accent);
  transform: scaleY(0); transform-origin: top center;
  transition: transform 240ms var(--ease);
}
.mini .item { margin: 4px auto; padding: 0; width: 36px; height: 36px; justify-content: center; }
.item:hover { background: color-mix(in srgb, var(--ink) 4%, transparent); color: var(--ink); }
.item.active { color: var(--ink); }
.item.active::before { transform: scaleY(1); }
/* 删除钮：hover 行才露出，平时不占视觉；红色一眼可见，悬停它本身再加红框红底（同首页卡片）；
   NPopover 气泡内常驻（折叠态唯一入口） */
.del {
  flex: none; border: 1px solid transparent; background: transparent; color: var(--err); cursor: pointer;
  font-size: 12px; width: 20px; height: 20px; line-height: 1; font-family: inherit;
  opacity: 0; transition: opacity var(--dur-1) var(--ease);
}
.item:hover .del { opacity: 1; }
.del:hover { border-color: var(--err); background: color-mix(in srgb, var(--err) 10%, transparent); }
.del.armed { opacity: 1; color: var(--accent-ink); background: var(--err); border-color: var(--err); }
.pop-del {
  border: 0; background: transparent; color: var(--err); cursor: pointer;
  font-size: 12px; padding: 6px 0 0; text-align: left; font-family: inherit;
}
.pop-del:hover { text-decoration: underline; }
.pop-del.armed { color: var(--err); font-weight: 600; }
.ava {
  width: 32px; height: 32px; flex: none; overflow: hidden;
  display: flex; align-items: center; justify-content: center;
  background: var(--surface); border: 1px solid var(--line-soft);
  color: var(--muted); font-size: 12px; letter-spacing: 0.06em;
}
.item.active .ava { color: var(--accent); border-color: color-mix(in srgb, var(--accent) 45%, transparent); }
.info { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.nm {
  font-size: 13px; color: var(--ink);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.item.active .nm { color: var(--accent); }
.sub { display: flex; align-items: center; gap: 5px; font-size: 11px; color: var(--muted); }
.sub em { font-style: normal; color: var(--muted); opacity: 0.65; margin-left: auto; }
.st { width: 6px; height: 6px; border-radius: 50%; background: var(--muted); opacity: 0.55; flex: none; }
.st.s-draft { background: var(--info); opacity: 1; }
.st.s-run { background: var(--accent); opacity: 1; animation: blink 1.2s infinite; }
.st.s-done { background: var(--ok); opacity: 1; }
.st.s-fail { background: var(--err); opacity: 1; }
@keyframes blink { 50% { opacity: .3; } }

.pop { max-width: 220px; }
.pop-name { font-size: 13px; font-weight: 600; }
.pop-meta { font-size: 11px; color: var(--muted); margin-top: 2px; }
</style>
