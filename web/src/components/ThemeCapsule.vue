<script setup lang="ts">
// 晨雾/月影 主题胶囊 —— 滑动拇指 + HUD 右上角标准切换器（移植自 yuca-next）
import { useTheme } from '../theme'
import RIcon from './RIcon.vue'

const { theme, setTheme } = useTheme()
</script>

<template>
  <div class="theme-capsule" role="group" aria-label="主题切换" :data-active="theme">
    <span class="theme-thumb" aria-hidden="true" />
    <button
      v-for="[value, icon, label] in ([['day', 'theme-morning', '晨雾'], ['night', 'theme-night', '月影']] as const)"
      :key="value" type="button" class="theme-btn capsule-btn"
      :aria-pressed="theme === value" :class="{ active: theme === value }"
      @click="setTheme(value)"
    >
      <RIcon :name="icon" :size="14" />
      {{ label }}
    </button>
  </div>
</template>

<style scoped>
.capsule-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 6px 14px;
  border: 0;
  background: transparent;
  font-family: inherit;
  font-size: 11px;
  letter-spacing: 0.14em;
  color: var(--muted);
  cursor: pointer;
  user-select: none;
  transition: color var(--dur-2) var(--ease);
}
.capsule-btn:hover { color: var(--ink); }
.capsule-btn.active { color: var(--accent); }
</style>
