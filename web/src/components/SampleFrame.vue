<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

// 样张 HTML 塞进自适应宽的卡片必须整体缩放，否则 iframe 视口只显示左上局部
//（大字被切）。画幅从根元素 data-width/data-height 读：横版风格样张 960×540、
// 竖版段落样张 1080×1920 共用本组件——旧实现写死 16:9 容器，竖版被横向裁切、
// 文字截断没法评判（2026-10-30 GUI 实测 t10）。读不到属性退横版默认。
const props = defineProps<{ html: string }>()

const dims = computed(() => {
  const w = Number(/data-width="(\d+)"/.exec(props.html)?.[1] ?? 0)
  const h = Number(/data-height="(\d+)"/.exec(props.html)?.[1] ?? 0)
  return w > 0 && h > 0 ? { w, h } : { w: 960, h: 540 }
})

const wrap = ref<HTMLElement | null>(null)
let ro: ResizeObserver | null = null

const apply = () => {
  const w = wrap.value
  const f = w?.querySelector('iframe')
  if (w && f) f.style.transform = `scale(${w.clientWidth / dims.value.w})`
}
onMounted(() => {
  ro = new ResizeObserver(apply)
  if (wrap.value) ro.observe(wrap.value)
  apply()
})
onBeforeUnmount(() => ro?.disconnect())
</script>

<template>
  <div ref="wrap" class="sf" :style="{ aspectRatio: `${dims.w} / ${dims.h}` }"><iframe :srcdoc="props.html" sandbox="" tabindex="-1" title="风格样张" :style="{ width: dims.w + 'px', height: dims.h + 'px' }" /></div>
</template>

<style scoped>
.sf { width: 100%; position: relative; overflow: hidden; background: #FDF6E3; display: block; }
.sf iframe { position: absolute; top: 0; left: 0; border: 0; transform-origin: 0 0; }
</style>
