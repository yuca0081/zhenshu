<script setup lang="ts">
import { computed } from 'vue'
import { ICON_BODIES } from '../icons.data'

// Rhine 线性图标载体（移植自 yuca-next YucaIcon）：
// 外层 span.ric[data-icon] 承担动效状态，svg 内部结构由 icon-motion.css 按 data-icon 驱动
const props = withDefaults(defineProps<{
  name: string
  size?: number
  /** 挂到 .ric 上的状态类：is-busy / is-done / is-entering 等 */
  state?: string
  title?: string
}>(), { size: 18 })

const body = computed(() => ICON_BODIES[props.name])
</script>

<template>
  <span v-if="body" class="ric" :class="state" :data-icon="name" :title="title || undefined">
    <svg
      :width="size" :height="size" viewBox="0 0 24 24" fill="none" stroke="currentColor"
      stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"
      :aria-hidden="title ? undefined : 'true'" :role="title ? 'img' : undefined"
      v-html="title ? `<title>${title}</title>${body}` : body"
    />
  </span>
</template>

<style>
.ric {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  line-height: 1;
  flex: none;
}
</style>
