<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'

// 样张 HTML 固定 960×540 画幅（seed_styles 产物），塞进自适应宽的卡片必须整体缩放，
// 否则 iframe 视口只显示左上局部（大字被切）。容器 16:9，iframe 原生尺寸 + transform scale。
const props = defineProps<{ html: string }>()
const wrap = ref<HTMLElement | null>(null)
let ro: ResizeObserver | null = null

const apply = () => {
  const w = wrap.value
  const f = w?.querySelector('iframe')
  if (w && f) f.style.transform = `scale(${w.clientWidth / 960})`
}
onMounted(() => {
  ro = new ResizeObserver(apply)
  if (wrap.value) ro.observe(wrap.value)
  apply()
})
onBeforeUnmount(() => ro?.disconnect())
</script>

<template>
  <div ref="wrap" class="sf"><iframe :srcdoc="props.html" sandbox="" tabindex="-1" title="风格样张" /></div>
</template>

<style scoped>
.sf { width: 100%; aspect-ratio: 16/9; position: relative; overflow: hidden; background: #FDF6E3; display: block; }
.sf iframe { position: absolute; top: 0; left: 0; width: 960px; height: 540px; border: 0; transform-origin: 0 0; }
</style>
