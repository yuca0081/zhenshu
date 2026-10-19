<script setup lang="ts">
import { ref } from 'vue'
import { NButton, NInput } from 'naive-ui'
import { api } from '../api'
import { errMsg } from '../format'
import type { UserInfo } from '../types'

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
    <div class="login-card">
      <div class="lg-brand">
        <span class="lg-logo">帧</span>
        <div>
          <b>帧述</b>
          <p>AI 视频生产平台</p>
        </div>
      </div>
      <form @submit.prevent="submit">
        <span class="field-label">用户名</span>
        <NInput v-model:value="username" maxlength="32" placeholder="用户名" autofocus @keydown.enter="onEnter" />
        <span class="field-label">密码</span>
        <NInput
          v-model:value="password" type="password" show-password-on="click"
          placeholder="密码" @keydown.enter="onEnter"
        />
        <p v-if="error" class="lg-error">{{ error }}</p>
        <NButton type="primary" attr-type="submit" block :loading="busy" class="lg-btn">登 录</NButton>
      </form>
    </div>
  </div>
</template>

<style scoped>
.login-page {
  height: 100vh; display: flex; align-items: center; justify-content: center;
  background: radial-gradient(1200px 600px at 70% -10%, #1c1a14 0%, #0e0e12 55%);
}
.login-card {
  width: 340px; padding: 34px 32px 30px; border-radius: 16px;
  background: #14141a; border: 1px solid #26262e;
  box-shadow: 0 18px 60px rgba(0, 0, 0, .45);
}
.lg-brand { display: flex; align-items: center; gap: 12px; margin-bottom: 26px; }
.lg-logo {
  width: 40px; height: 40px; border-radius: 12px; display: flex; align-items: center; justify-content: center;
  background: linear-gradient(135deg, #f0c674, #c89b4b); color: #14140f; font-weight: 700; font-size: 20px;
  box-shadow: 0 2px 10px rgba(240, 198, 116, .3);
}
.lg-brand b { color: #f0c674; font-size: 18px; letter-spacing: 3px; display: block; }
.lg-brand p { color: #7c7c88; font-size: 12px; margin-top: 2px; }
.field-label { display: block; color: #9a9aa6; font-size: 12.5px; margin: 14px 0 6px; }
.lg-error { color: #ff8484; font-size: 12.5px; margin-top: 12px; }
.lg-btn { margin-top: 20px; font-weight: 700; letter-spacing: 6px; }
</style>
