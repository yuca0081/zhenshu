<script setup lang="ts">
import { computed } from 'vue'
import type { ProjectRow } from '../types'

// 首页：品牌落地 + 快速开始（新建/继续最近项目/逛素材区）
const props = defineProps<{ projects: ProjectRow[] }>()
const emit = defineEmits<{ new: []; open: [id: string]; materials: [] }>()

const statusLabel: Record<string, string> = {
  created: '已创建', manuscript: '有文稿', storyboard: '有分镜', style: '风格定',
  producing: '制作中', video: '有成片',
}
const recent = computed(() => props.projects.slice(0, 6))
const dateOf = (iso: string) => (iso ?? '').slice(5, 10).replace('-', '/')
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
        <button class="ghost" @click="emit('materials')">去素材区找灵感</button>
      </div>
    </div>

    <div v-if="recent.length" class="recent">
      <div class="sec">
        <span>最近项目</span>
        <span class="count">{{ projects.length }} 个</span>
      </div>
      <div class="grid">
        <button v-for="p in recent" :key="p.id" class="proj" @click="emit('open', p.id)">
          <b>{{ p.name }}</b>
          <span class="meta">
            <i :class="['st', p.status === 'video' ? 'done' : p.status === 'producing' ? 'run' : '']">
              {{ statusLabel[p.status] ?? p.status }}
            </i>
            <em>{{ dateOf(p.created_at) }}</em>
          </span>
        </button>
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
  display: flex; flex-direction: column; gap: 10px; text-align: left; cursor: pointer;
  background: #14141a; border: 1px solid #232329; border-radius: 12px; padding: 14px 16px; color: #d9d9e0;
}
.proj:hover { border-color: #f0c674; }
.proj b { font-size: 14px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.meta { display: flex; justify-content: space-between; align-items: center; }
.meta i { font-style: normal; font-size: 11px; color: #8a8a96; }
.meta i.done { color: #7ed491; }
.meta i.run { color: #f0c674; }
.meta em { font-style: normal; font-size: 11px; color: #55555f; }
</style>
