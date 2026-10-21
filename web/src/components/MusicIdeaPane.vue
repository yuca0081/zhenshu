<script setup lang="ts">
import type { ProjectView } from '../types'

// 音乐驱动 PV 项目的「想法」页签（M1 上线前的准备占位页）：项目可建、主题已
// 定，做曲→地图→分镜的准备工位上线后此处直接开工。文稿链的三前置（文稿/
// 分镜/风格确认）对 PV 项目不适用——页签集在 StagePanel 按 mode 裁剪。
defineProps<{ view: ProjectView }>()
</script>

<template>
  <div class="mip">
    <div class="head">
      <h2>音乐驱动 · PV</h2>
      <p class="topic">主题：<b>{{ view.topic || '（未填）' }}</b></p>
      <p class="sub">曲先行——分镜跟着曲子的节拍走，不是稿子念到哪画面跟到哪</p>
    </div>

    <ol class="flow">
      <li><i>①</i><div><b>做曲</b><span>一句话主题 → 曲 brief → 整曲多段落＋能量弧（曲库 v2 后端）</span></div></li>
      <li><i>②</i><div><b>音乐地图</b><span>节拍网格 / 段落 / 能量 JSON——分镜唯一认的物理事实</span></div></li>
      <li><i>③</i><div><b>分镜</b><span>镜头边界钉在节拍上，段落判据从「话题完整」换成「音乐段落」</span></div></li>
      <li><i>④</i><div><b>制作</b><span>写画面 ×N → 卡点机械闸 → 镜头审 → 拼接挂原曲</span></div></li>
    </ol>

    <div class="note">
      <p><b>准备工位开发中（M1）</b>——项目已建好占位，做曲工位上线后这里直接开工。</p>
      <p class="dim">原型闸已跑通（2026-10-21）：80s 整曲＋谱面地图＋40s 副歌全链，切点卡拍 0ms 偏差。方案见 <code>docs/pv-music-driven-design.md</code>，样片在 <code>demo/pv-prototype/</code>。</p>
    </div>
  </div>
</template>

<style scoped>
.mip { max-width: 640px; margin: 40px auto; padding: 0 24px; display: flex; flex-direction: column; gap: 26px; }
.head h2 { font-size: 20px; letter-spacing: 4px; color: #f0c674; margin: 0 0 10px; }
.topic { color: #d8d8e0; font-size: 14px; margin: 0 0 6px; }
.topic b { color: #fff; }
.sub { color: #9a9aa6; font-size: 12.5px; margin: 0; }
.flow { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 12px; }
.flow li { display: flex; gap: 14px; align-items: flex-start; padding: 12px 16px; border: 1px solid #2a2a33; border-radius: 10px; background: rgba(255, 255, 255, .02); }
.flow i { font-style: normal; color: #f0c674; font-size: 15px; line-height: 1.5; }
.flow b { display: block; color: #e8e8ee; font-size: 14px; margin-bottom: 3px; }
.flow span { color: #9a9aa6; font-size: 12.5px; line-height: 1.6; }
.note { border-left: 3px solid #f0c674; padding: 4px 0 4px 14px; display: flex; flex-direction: column; gap: 6px; }
.note p { margin: 0; color: #c8c8d2; font-size: 13px; line-height: 1.7; }
.note .dim { color: #7c7c88; font-size: 12px; }
.note code { color: #f0c674; font-size: 11.5px; }
</style>
