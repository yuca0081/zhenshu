<script setup lang="ts">
import { computed, ref, onBeforeUnmount } from 'vue'
import { dateOf, lastProduceFailed, statusLabel } from '../format'
import type { ProjectRow } from '../types'

// 首页：品牌落地 + 快速开始（新建/继续最近项目/逛风格库）
const props = defineProps<{ projects: ProjectRow[] }>()
const emit = defineEmits<{ new: []; open: [id: string]; materials: []; delete: [id: string] }>()

const recent = computed(() => props.projects.slice(0, 6))

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
</script>

<template>
  <section class="home">
    <!-- 背景层：细网格 + 三色光斑缓慢漂移（纯 CSS，transform 合成不卡） -->
    <div class="bg" aria-hidden="true">
      <div class="blob b-gold" />
      <div class="blob b-cyan" />
      <div class="blob b-violet" />
    </div>
    <div class="hero">
      <h1>帧述</h1>
      <p class="slogan">聊天即创作 —— 粘贴一篇文章，还你一部带配音、字幕、动画的成片</p>
      <div class="cta">
        <button class="primary" @click="emit('new')">＋ 新建视频</button>
        <button class="ghost" @click="emit('materials')">去风格库找灵感</button>
      </div>
    </div>

    <div v-if="recent.length" class="recent">
      <div class="sec">
        <span>最近项目</span>
        <span class="count">{{ projects.length }} 个</span>
      </div>
      <div class="grid">
        <!-- 卡片用 div+button 组合（button 里不能再套 button）；删除钮 hover 露出，同左侧项目栏 -->
        <div v-for="p in recent" :key="p.id" class="proj" role="button" tabindex="0" @click="emit('open', p.id)" @keydown.enter="emit('open', p.id)">
          <b>{{ p.name }}</b>
          <button class="del" :class="{ armed: armed === p.id }" :title="armed === p.id ? '再点一次确认删除（不可恢复）' : '删除项目（不可恢复）'" @click.stop="clickDel(p.id)">{{ armed === p.id ? '确认?' : '✕' }}</button>
          <span class="meta">
            <i v-if="p.mode === 'music'" class="pv" title="音乐驱动 PV（做曲→音乐地图→分镜）">♪ PV</i>
            <i :class="['st', p.status === 'video' ? 'done' : p.status === 'producing' ? 'run' : '', { fail: lastProduceFailed(p) }]">
              {{ statusLabel[p.status] ?? p.status }}{{ lastProduceFailed(p) ? ' · 上次制作失败' : '' }}
            </i>
            <em>{{ dateOf(p.created_at) }}</em>
          </span>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.home { position: relative; flex: 1; min-height: 0; overflow: auto; display: flex; flex-direction: column; align-items: center; }
.hero, .recent { position: relative; z-index: 1; }

/* ── 背景动效：网格 + 光斑 ─────────────────── */
.bg {
  position: absolute; inset: 0; overflow: hidden; pointer-events: none; z-index: 0;
  background-image:
    linear-gradient(rgba(230, 230, 234, .035) 1px, transparent 1px),
    linear-gradient(90deg, rgba(230, 230, 234, .035) 1px, transparent 1px);
  background-size: 44px 44px;
  mask-image: radial-gradient(1200px 640px at 50% 36%, #000 30%, transparent 78%);
}
.blob { position: absolute; border-radius: 50%; filter: blur(90px); will-change: transform; }
.b-gold {
  width: 560px; height: 560px; left: 8%; top: -12%;
  background: radial-gradient(circle, rgba(240, 198, 116, .16), transparent 65%);
  animation: drift-a 34s ease-in-out infinite alternate;
}
.b-cyan {
  width: 640px; height: 640px; right: -6%; top: 6%;
  background: radial-gradient(circle, rgba(77, 157, 224, .13), transparent 65%);
  animation: drift-b 42s ease-in-out infinite alternate;
}
.b-violet {
  width: 480px; height: 480px; left: 34%; bottom: -16%;
  background: radial-gradient(circle, rgba(179, 136, 235, .11), transparent 65%);
  animation: drift-c 38s ease-in-out infinite alternate;
}
@keyframes drift-a { from { transform: translate(0, 0) scale(1); } to { transform: translate(160px, 90px) scale(1.18); } }
@keyframes drift-b { from { transform: translate(0, 0) scale(1.1); } to { transform: translate(-140px, 120px) scale(.94); } }
@keyframes drift-c { from { transform: translate(0, 0) scale(.95); } to { transform: translate(-110px, -80px) scale(1.15); } }

.hero {
  margin-top: 9vh; text-align: center; padding: 0 24px;
}
.hero h1 { font-size: 44px; letter-spacing: 10px; color: #f0c674; text-indent: 10px; }
.slogan { margin-top: 14px; color: #9a9aa6; font-size: 15px; }
.cta { margin-top: 30px; display: flex; gap: 14px; justify-content: center; }
.cta .primary {
  border: 0; background: linear-gradient(135deg, #f0c674, #d9ae5b); color: #14140f;
  font-size: 15px; font-weight: 700; padding: 11px 30px; border-radius: 999px; cursor: pointer;
}
.cta .primary:hover { filter: brightness(1.06); }
.cta .ghost {
  border: 1px solid #3a3a45; background: transparent; color: #c9c9d1;
  font-size: 14px; padding: 11px 24px; border-radius: 999px; cursor: pointer;
}
.cta .ghost:hover { border-color: #f0c674; color: #f0c674; }
.recent { width: min(880px, 92vw); margin-top: 8vh; }
.sec {
  display: flex; justify-content: space-between; align-items: baseline;
  font-size: 13px; color: #7c7c88; margin-bottom: 12px;
}
.sec .count { font-size: 11.5px; color: #55555f; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 12px; }
.proj {
  position: relative; display: flex; flex-direction: column; gap: 10px; text-align: left; cursor: pointer;
  background: #14141a; border: 1px solid #232329; border-radius: 12px; padding: 14px 16px; color: #d9d9e0;
}
.proj:hover { border-color: #f0c674; }
.proj b { font-size: 14px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; padding-right: 22px; }
/* 删除钮：hover 卡片才露出；红色一眼可见，悬停它本身再加红框红底强化 */
.del {
  position: absolute; top: 9px; right: 9px;
  border: 1px solid transparent; background: transparent; color: #ff7a7a; cursor: pointer;
  font-size: 13px; width: 24px; height: 24px; line-height: 1; border-radius: 6px;
  opacity: 0; transition: opacity .12s;
}
.proj:hover .del, .del.armed { opacity: 1; }
.del:hover, .del.armed { color: #ff6b6b; background: #2a1518; border-color: #ff7a7a; }
.del.armed { width: auto; padding: 0 6px; font-weight: 600; opacity: 1; }
.meta { display: flex; justify-content: space-between; align-items: center; }
.meta i { font-style: normal; font-size: 11px; color: #8a8a96; }
.meta i.pv { color: #c9a24b; border: 1px solid rgba(240, 198, 116, .35); border-radius: 4px; padding: 0 4px; flex: none; }
.meta i.done { color: #7ed491; }
.meta i.run { color: #f0c674; }
.meta i.fail { color: #ff7a7a; }
.meta em { font-style: normal; font-size: 11px; color: #55555f; }
</style>
