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
  background: transparent; padding: 4px 8px; border-radius: 999px; color: #c9c9d4;
}
.acct:hover { background: #1b1b22; }
.acct-avatar {
  width: 26px; height: 26px; border-radius: 50%; display: flex; align-items: center; justify-content: center;
  background: linear-gradient(135deg, #f0c674, #c89b4b); color: #14140f; font-weight: 700; font-size: 13px;
}
.acct-name { font-size: 13px; max-width: 120px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.acct-role {
  font-size: 11px; color: #f0c674; border: 1px solid rgba(240, 198, 116, .45);
  padding: 1px 7px; border-radius: 999px;
}
</style>
