<script setup lang="ts">
import { computed } from 'vue'
import { NDropdown } from 'naive-ui'
import type { UserInfo } from '../types'

// 右上角账号菜单：用户名 + 角色，下拉给「用户管理」（仅管理员）与「退出登录」。
const props = defineProps<{ user: UserInfo }>()
const emit = defineEmits<{ logout: []; manage: [] }>()

const options = computed(() => {
  const opts: { label: string; key: string }[] = []
  if (props.user.role === 'admin') opts.push({ label: '用户管理', key: 'manage' })
  opts.push({ label: '退出登录', key: 'logout' })
  return opts
})

function onSelect(key: string) {
  if (key === 'logout') emit('logout')
  if (key === 'manage') emit('manage')
}
</script>

<template>
  <NDropdown :options="options" trigger="click" @select="onSelect">
    <button class="acct" :title="user.role === 'admin' ? '管理员' : '用户'">
      <span class="acct-avatar">{{ user.username.slice(0, 1).toUpperCase() }}</span>
      <span class="acct-name">{{ user.username }}</span>
      <span v-if="user.role === 'admin'" class="acct-role">管理员</span>
    </button>
  </NDropdown>
</template>

<style scoped>
.acct {
  display: flex; align-items: center; gap: 8px; border: 0; cursor: pointer;
  background: transparent; padding: 4px 8px; color: var(--muted); font-family: inherit;
  transition: color var(--dur-1) var(--ease);
}
.acct:hover { color: var(--ink); }
.acct-avatar {
  width: 24px; height: 24px; display: flex; align-items: center; justify-content: center;
  border: 1px solid var(--accent); color: var(--accent); font-weight: 600; font-size: 12px;
}
.acct-name {
  font-size: 12px; letter-spacing: 0.06em;
  max-width: 120px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.acct-role {
  font-size: 10px; color: var(--accent); font-family: inherit;
  border: 1px solid color-mix(in srgb, var(--accent) 45%, transparent);
  padding: 0 6px; letter-spacing: 0.1em;
}
</style>
