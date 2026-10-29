<script setup lang="ts">
import { computed, ref, onBeforeUnmount } from 'vue'
import { dateOf, lastProduceFailed, statusLabel } from '../format'
import type { ProjectRow } from '../types'
import ProjectShelf from './ProjectShelf.vue'
import RIcon from './RIcon.vue'
import type { ShelfItem } from '../three/shelf-scene'

// 首页：问候 + 项目玻璃书架 + 最近打开 + 快速开始（Rhine 版）
const props = defineProps<{ projects: ProjectRow[] }>()
const emit = defineEmits<{ new: []; open: [id: string]; materials: []; delete: [id: string] }>()

const recent = computed(() => props.projects.slice(0, 8))
const shelfItems = computed<ShelfItem[]>(() => recent.value.map(p => ({
  id: p.id,
  title: p.name,
  date: dateOf(p.created_at),
  status: statusLabel[p.status] ?? p.status,
})))

const hour = new Date().getHours()
const greet = hour < 5 ? '夜深了' : hour < 11 ? '早上好' : hour < 14 ? '午安' : hour < 19 ? '下午好' : '晚上好'
const pad = (n: number) => String(n).padStart(2, '0')

// 删除两段式确认（同 HistoryRail）：window.confirm 全站弃用（死按钮环境），
// 首页入口必须自带确认——2026-10-19 拆 confirm 时漏了这里，单击即永久删。
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
onBeforeUnmount(() => { if (armTimer) clearTimeout(armTimer) })

// 书架：焦点序号（控制条计数）+ WebGL 降级
const focusIdx = ref(0)
const shelfRef = ref<InstanceType<typeof ProjectShelf> | null>(null)
const webglOk = ref(true)
</script>

<template>
  <section class="home">
    <!-- 问候 + 快速开始 -->
    <div class="head rise">
      <div class="micro">STUDIO · 帧述</div>
      <h1>{{ greet }}。</h1>
      <p class="sub">聊天即创作 —— 粘贴一篇文章，还你一部带配音、字幕、动画的成片。</p>
      <div class="cta">
        <button class="btn btn-primary" @click="emit('new')"><RIcon name="plus" :size="14" />新建视频</button>
        <button class="btn" @click="emit('materials')"><RIcon name="palette" :size="14" />去风格库找灵感</button>
      </div>
    </div>

    <template v-if="recent.length">
      <!-- 项目玻璃书架（WebGL 不可用时降级为直角卡片网格） -->
      <div class="stage rise" style="animation-delay: 80ms">
        <ProjectShelf
          v-if="webglOk" ref="shelfRef" :items="shelfItems"
          @select="item => emit('open', item.id)" @index="i => (focusIdx = i)" @unsupported="webglOk = false"
        />
        <div v-else class="grid">
          <div
            v-for="(p, i) in recent" :key="p.id" class="module-card card" role="button" tabindex="0"
            :style="{ animationDelay: `${i * 60}ms` }"
            @click="emit('open', p.id)" @keydown.enter="emit('open', p.id)"
          >
            <div class="mc-top">
              <RIcon name="film" :size="22" class="tint" />
              <span class="num idx">{{ pad(i + 1) }}</span>
            </div>
            <div class="mc-title">{{ p.name }}</div>
            <div class="mc-desc num">{{ dateOf(p.created_at) }} · {{ statusLabel[p.status] ?? p.status }}</div>
          </div>
        </div>
      </div>
      <div v-if="webglOk" class="shelf-ctrl rise" style="animation-delay: 140ms">
        <button class="arrow" aria-label="上一个" @click="shelfRef?.prev()"><RIcon name="back" :size="15" /></button>
        <span class="num counter">{{ pad(focusIdx + 1) }} / {{ pad(recent.length) }}</span>
        <button class="arrow" aria-label="下一个" @click="shelfRef?.next()"><RIcon name="open" :size="15" /></button>
      </div>

      <!-- 最近打开 -->
      <div class="sec rise" style="animation-delay: 200ms">
        <div class="hairline"><span class="micro">RECENT · 最近打开</span></div>
        <div class="rows">
          <div
            v-for="(p, i) in recent" :key="p.id" class="data-row proj-row" role="button" tabindex="0"
            @click="emit('open', p.id)" @keydown.enter="emit('open', p.id)"
          >
            <span class="num idx">{{ pad(i + 1) }}</span>
            <span class="name">{{ p.name }}</span>
            <i v-if="p.mode === 'music'" class="pv" title="音乐驱动 PV（做曲→音乐地图→分镜）">PV</i>
            <i :class="['st', p.status === 'video' ? 'done' : p.status === 'producing' ? 'run' : '', { fail: lastProduceFailed(p) }]">
              {{ statusLabel[p.status] ?? p.status }}{{ lastProduceFailed(p) ? ' · 上次制作失败' : '' }}
            </i>
            <em class="date num">{{ dateOf(p.created_at) }}</em>
            <button
              class="del" :class="{ armed: armed === p.id }"
              :title="armed === p.id ? '再点一次确认删除（不可恢复）' : '删除项目（不可恢复）'"
              @click.stop="clickDel(p.id)"
            >{{ armed === p.id ? '确认?' : '✕' }}</button>
          </div>
        </div>
      </div>
    </template>

    <!-- 快速开始 -->
    <div class="sec rise" style="animation-delay: 260ms">
      <div class="hairline"><span class="micro">MODULES · 模块</span></div>
      <div class="mods">
        <button class="module-card" @click="emit('new')">
          <div class="mc-top">
            <RIcon name="film" :size="26" class="tint" />
            <span class="num idx">01</span>
          </div>
          <div>
            <div class="mc-title"><span class="micro mc-en">NEW</span>新建视频</div>
            <div class="mc-desc">从想法开始，一键起草文稿与分镜</div>
          </div>
        </button>
        <button class="module-card" @click="emit('materials')">
          <div class="mc-top">
            <RIcon name="palette" :size="26" class="tint" />
            <span class="num idx">02</span>
          </div>
          <div>
            <div class="mc-title"><span class="micro mc-en">LIBRARY</span>风格库</div>
            <div class="mc-desc">逛画风样张 · 解析参考视频</div>
          </div>
        </button>
      </div>
    </div>

    <div class="foot">
      <div class="hairline"><span>ONE FILM AT A TIME · 把想法存进玻璃放映室</span></div>
    </div>
  </section>
</template>

<style scoped>
.home {
  height: 100%;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  padding: 10px 40px 28px;
}

/* ── 问候 ── */
.head { width: min(1080px, 100%); margin: 0 auto; }
.head h1 { margin: 8px 0 0; font-size: 32px; font-weight: 600; letter-spacing: 0.04em; }
.sub { margin-top: 8px; color: var(--muted); font-size: 13px; }
.cta { margin-top: 20px; display: flex; gap: 12px; }

/* ── 书架舞台 ── */
.stage {
  width: min(1080px, 100%);
  height: 46vh;
  min-height: 300px;
  margin: 10px auto 0;
  position: relative;
}
.shelf-ctrl { display: flex; align-items: center; justify-content: center; gap: 18px; margin-top: 8px; }
.arrow {
  width: 34px; height: 34px;
  display: flex; align-items: center; justify-content: center;
  border: 1px solid var(--line); background: transparent; color: var(--muted);
  cursor: pointer;
  transition: color var(--dur-1) var(--ease), border-color var(--dur-1) var(--ease), transform var(--dur-1) var(--ease);
}
.arrow:hover { color: var(--ink); border-color: var(--ink); transform: translateY(-1px); }
.counter { font-size: 12px; color: var(--muted); letter-spacing: 0.12em; }

/* ── 通用分区 ── */
.sec { width: min(1080px, 100%); margin: 26px auto 0; }
.rows { border-top: 1px solid var(--line-soft); }
.proj-row { cursor: pointer; }
.idx { width: 28px; flex: none; font-size: 12px; color: var(--muted); }
.name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 14px; }
.pv {
  font-style: normal; font-size: 10px; color: var(--accent); flex: none;
  border: 1px solid color-mix(in srgb, var(--accent) 40%, transparent);
  padding: 0 5px; letter-spacing: 0.1em;
}
.st { font-style: normal; font-size: 11px; color: var(--muted); flex: none; }
.st.done { color: var(--ok); }
.st.run { color: var(--accent); }
.st.fail { color: var(--err); }
.date { font-style: normal; font-size: 11px; color: var(--muted); flex: none; }
/* 删除钮：hover 行才露出；红色一眼可见，armed 展开成「确认?」 */
.del {
  border: 1px solid transparent; background: transparent; color: var(--err); cursor: pointer;
  font-size: 12px; font-family: inherit; min-width: 24px; height: 24px; line-height: 1;
  opacity: 0; transition: opacity var(--dur-1) var(--ease);
}
.proj-row:hover .del, .del.armed { opacity: 1; }
.del:hover, .del.armed { border-color: var(--err); background: color-mix(in srgb, var(--err) 10%, transparent); }
.del.armed { padding: 0 6px; font-weight: 600; opacity: 1; }

/* ── 模块卡网格：gap-px 缝 1px 分隔线 ── */
.mods {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: 1px; border: 1px solid var(--line-soft); background: var(--line-soft);
}
.mc-top { display: flex; align-items: flex-start; justify-content: space-between; }
.tint { color: var(--muted); transition: color var(--dur-2) var(--ease); }
.module-card:hover .tint { color: var(--accent); }
.mc-title { font-size: 15px; font-weight: 600; letter-spacing: 0.04em; }
.mc-en { margin-right: 8px; }
.mc-desc { margin-top: 4px; font-size: 12px; color: var(--muted); }

/* ── WebGL 降级卡片 ── */
.grid {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  gap: 1px; border: 1px solid var(--line-soft); background: var(--line-soft);
}
.card { animation: rhine-rise 380ms var(--ease) both; }

.foot { width: min(1080px, 100%); margin: 36px auto 0; font-size: 11px; padding-bottom: 6px; }
</style>
