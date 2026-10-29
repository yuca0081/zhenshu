<script setup lang="ts">
import { ref } from 'vue'
import { api } from '../api'
import { errMsg } from '../format'
import type { UserInfo } from '../types'
import ThemeCapsule from './ThemeCapsule.vue'

const emit = defineEmits<{ 'logged-in': [user: UserInfo] }>()
const username = ref('')
const password = ref('')
const busy = ref(false)
const error = ref('')

// IME 组合期（候选未上屏）的 Enter 是选词不是提交（与 NewProjectModal 同款判断）。
// 必须 preventDefault：keydown 走 submit 后，表单默认提交行为会经 @submit.prevent
// 再触发一次 submit——双发两次登录请求（2026-10-19 中-L4）。
function onEnter(e: KeyboardEvent) {
  if (e.isComposing || e.keyCode === 229) return
  e.preventDefault()
  submit()
}

async function submit() {
  if (!username.value.trim() || !password.value) { error.value = '请输入用户名和密码'; return }
  busy.value = true
  error.value = ''
  try {
    const u = await api.login(username.value.trim(), password.value)
    emit('logged-in', u)
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="login-page">
    <!-- 背景装饰：网格缓慢漂移 + 光带 -->
    <div
      class="grid-drift bg-grid" aria-hidden="true"
    />
    <div class="light-band" aria-hidden="true" />

    <div class="capsule-slot">
      <ThemeCapsule />
    </div>

    <div class="wrap">
      <!-- 左：品牌 -->
      <div class="brand rise">
        <div class="brand-row">
          <span class="brand-name">帧述</span>
          <span class="micro">ZHENSU</span>
        </div>
        <div class="micro brand-sub">VIDEO STUDIO · AI 短视频制作台</div>

        <div class="hairline brand-line"><span class="micro">ACCESS</span></div>
        <p class="brand-copy">
          聊天即创作——粘贴一篇文章，<br />还你一部带配音、字幕、动画的成片。
        </p>
      </div>

      <!-- 右：表单面板 -->
      <form class="panel form rise" style="animation-delay: 80ms" @submit.prevent="submit">
        <div class="micro">IDENTITY VERIFY</div>
        <h1 class="form-title">登录</h1>

        <label class="micro form-label" for="lg-user">账号 / ACCOUNT</label>
        <input
          id="lg-user" v-model="username" class="field" maxlength="32"
          placeholder="用户名" autocomplete="username" autofocus @keydown.enter="onEnter"
        />

        <label class="micro form-label" for="lg-pass">密码 / PASSWORD</label>
        <input
          id="lg-pass" v-model="password" class="field" type="password"
          placeholder="密码" autocomplete="current-password" @keydown.enter="onEnter"
        />

        <p v-if="error" class="lg-error">{{ error }}</p>

        <button type="submit" class="btn btn-primary lg-btn" :disabled="busy || !username.trim() || !password">
          {{ busy ? '验证中…' : '进入制作台' }}
        </button>

        <div class="hairline foot-line"><span>ZHENSU · VIDEO STUDIO</span></div>
      </form>
    </div>
  </div>
</template>

<style scoped>
.login-page {
  position: relative; height: 100vh; display: flex; align-items: center; justify-content: center;
  background: var(--bg); color: var(--ink); overflow: hidden; padding: 24px;
}
.bg-grid {
  position: absolute; inset: 0; pointer-events: none; opacity: 0.35;
  background-image:
    linear-gradient(var(--line-soft) 1px, transparent 1px),
    linear-gradient(90deg, var(--line-soft) 1px, transparent 1px);
  background-size: 72px 72px;
}
.light-band {
  position: absolute; inset-inline: 0; top: 0; height: 160px; pointer-events: none;
  background: linear-gradient(180deg, var(--light-band), transparent);
}
.capsule-slot { position: absolute; right: 24px; top: 24px; }

.wrap {
  position: relative; width: 100%; max-width: 880px;
  display: grid; gap: 40px; grid-template-columns: 1fr;
}
@media (min-width: 760px) {
  .wrap { grid-template-columns: 1fr 380px; gap: 64px; }
}

/* 左：品牌 */
.brand { display: none; }
@media (min-width: 760px) {
  .brand { display: block; }
}
.brand-row { display: flex; align-items: baseline; gap: 12px; }
.brand-name { font-size: 30px; font-weight: 600; letter-spacing: 0.32em; }
.brand-sub { margin-top: 8px; }
.brand-line { margin-top: 56px; }
.brand-copy { margin-top: 32px; max-width: 320px; font-size: 13px; line-height: 1.9; color: var(--muted); }

/* 右：表单 */
.form { padding: 32px; }
.form-title { margin-top: 8px; font-size: 24px; font-weight: 600; letter-spacing: 0.04em; }
.form-label { display: block; margin-top: 28px; }
.form .field { margin-top: 8px; }
.lg-error {
  margin-top: 16px; color: var(--err); font-size: 12px;
  border-left: 2px solid var(--err); padding: 2px 0 2px 10px;
}
.lg-btn { margin-top: 28px; width: 100%; }
.foot-line { margin-top: 32px; font-size: 10px; }
</style>
