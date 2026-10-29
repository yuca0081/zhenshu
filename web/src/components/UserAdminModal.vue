<script setup lang="ts">
import { ref, watch } from 'vue'
import { NButton, NInput, NModal, NSelect } from 'naive-ui'
import { api } from '../api'
import { errMsg } from '../format'
import type { UserRow } from '../types'

// 用户管理（仅管理员）：列出账号 + 新建（固定账号模型，无自助注册/无改密——重置密码
// 靠删掉同名重建，账号少时够用）。
const show = defineModel<boolean>('show')
const emit = defineEmits<{ changed: [] }>()

const users = ref<UserRow[]>([])
const loading = ref(false)
const username = ref('')
const password = ref('')
const role = ref<'admin' | 'user'>('user')
const creating = ref(false)
const error = ref('')

watch(show, v => {
  if (v) {
    username.value = ''; password.value = ''; role.value = 'user'; error.value = ''
    load()
  }
})

async function load() {
  loading.value = true
  try {
    users.value = await api.listUsers()
  } catch (e) {
    error.value = `加载用户失败：${errMsg(e)}`
  } finally {
    loading.value = false
  }
}

async function create() {
  if (!username.value.trim() || password.value.length < 6) {
    error.value = '用户名必填；密码至少 6 位'
    return
  }
  creating.value = true
  error.value = ''
  try {
    await api.createUser(username.value.trim(), password.value, role.value)
    username.value = ''; password.value = ''
    await load()
    emit('changed')
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    creating.value = false
  }
}

const roleLabel = (r: string) => (r === 'admin' ? '管理员' : '用户')
const fmtDate = (iso: string) => iso.replace('T', ' ').slice(0, 16)
</script>

<template>
  <NModal v-model:show="show" preset="card" title="用户管理" class="app-modal ua-modal">
    <div class="ua-list">
      <div v-if="loading" class="ua-empty">加载中…</div>
      <div v-else-if="!users.length" class="ua-empty">还没有账号</div>
      <div v-for="u in users" v-else :key="u.id" class="ua-row">
        <span class="ua-avatar">{{ u.username.slice(0, 1).toUpperCase() }}</span>
        <span class="ua-name">{{ u.username }}</span>
        <span class="ua-role" :class="{ adm: u.role === 'admin' }">{{ roleLabel(u.role) }}</span>
        <span class="ua-date">{{ fmtDate(u.created_at) }}</span>
      </div>
    </div>
    <div class="ua-form">
      <span class="ua-label">新建账号</span>
      <div class="ua-inputs">
        <NInput v-model:value="username" maxlength="32" placeholder="用户名（字母/数字/_/-）" class="ua-un" />
        <NInput
          v-model:value="password" type="password" show-password-on="click"
          placeholder="密码（至少 6 位）" @keydown.enter="create"
        />
        <NSelect
          v-model:value="role" class="ua-role-sel"
          :options="[{ label: '普通用户', value: 'user' }, { label: '管理员', value: 'admin' }]"
        />
        <NButton type="primary" :loading="creating" @click="create">创建</NButton>
      </div>
      <p v-if="error" class="ua-error">{{ error }}</p>
    </div>
  </NModal>
</template>

<style scoped>
.ua-list { min-height: 120px; max-height: 320px; overflow: auto; margin-bottom: 18px; }
.ua-empty { color: var(--muted); font-size: 13px; padding: 28px 0; text-align: center; }
.ua-row {
  display: flex; align-items: center; gap: 10px; padding: 8px 4px;
  border-bottom: 1px solid var(--line-soft); font-size: 13.5px;
}
.ua-row:last-child { border-bottom: 0; }
.ua-avatar {
  width: 26px; height: 26px; flex: none;
  display: flex; align-items: center; justify-content: center;
  border: 1px solid color-mix(in srgb, var(--accent) 45%, transparent);
  color: var(--accent); font-weight: 600; font-size: 12px;
}
.ua-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ua-role { color: var(--muted); font-size: 12px; }
.ua-role.adm { color: var(--accent); }
.ua-date { color: var(--muted); opacity: .7; font-size: 12px; font-variant-numeric: tabular-nums; }
.ua-label { display: block; color: var(--muted); font-size: 12.5px; margin-bottom: 8px; }
.ua-inputs { display: flex; gap: 8px; }
.ua-un { width: 180px; flex: none; }
.ua-role-sel { width: 110px; flex: none; }
.ua-error { color: var(--err); font-size: 12.5px; margin-top: 10px; }
</style>
