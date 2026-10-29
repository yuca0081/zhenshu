<script setup lang="ts">
// three.js 书架的 Vue 薄包装（移植自 yuca-next ShelfCanvas）：
// 全权交给 ShelfScene，props 变化时转发；WebGL 不可用时 emit unsupported 由父级降级
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ShelfScene } from '../three/shelf-scene'
import type { ShelfItem } from '../three/shelf-scene'
import { useTheme } from '../theme'

const props = defineProps<{ items: ShelfItem[] }>()
const emit = defineEmits<{ select: [item: ShelfItem, index: number]; index: [index: number]; unsupported: [] }>()

const { theme } = useTheme()
const container = ref<HTMLDivElement | null>(null)
let scene: ShelfScene | null = null

onMounted(() => {
  if (!container.value) return
  try {
    scene = new ShelfScene(container.value, {
      onSelect: (item, i) => emit('select', item, i),
      onIndexChange: (i) => emit('index', i),
    })
    scene.setItems(props.items)
    if (import.meta.env.DEV) {
      (window as unknown as Record<string, unknown>).__shelfScene = scene // DEV 调试取证
    }
  } catch {
    scene = null
    emit('unsupported')
  }
})

watch(() => props.items, (items) => { scene?.setItems(items) })
watch(theme, () => { scene?.applyTheme() })

onBeforeUnmount(() => {
  scene?.dispose()
  scene = null
})

defineExpose({
  next: () => scene?.next(),
  prev: () => scene?.prev(),
  setIndex: (i: number) => scene?.setIndex(i),
})
</script>

<template>
  <div ref="container" class="shelf-fade shelf-box" aria-label="项目三维书架" />
</template>

<style scoped>
.shelf-box { position: relative; width: 100%; height: 100%; }
</style>
