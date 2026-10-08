<script setup lang="ts">
import { ref } from 'vue'
import { NButton, NPopconfirm, NTag, useMessage } from 'naive-ui'
import { api, libraryMediaURL } from '../api'
import TemplateDetail from './TemplateDetail.vue'
import type { Style, TemplateDetailData, VocabEntry } from '../types'

const props = defineProps<{ packs: Style[] }>()
const emit = defineEmits<{ publish: [id: string]; deleted: [] }>()
const message = useMessage()

// 词表（条目标签 id → 名称/简介 join 用；加载失败退裸 id）
const vocab = ref<VocabEntry[]>([])
async function loadVocab() {
  vocab.value = await api.listVocab().catch(() => [] as VocabEntry[])
}
loadVocab()
const vEntry = (id: string) => vocab.value.find(v => v.id === id)

// 删除条目（库里没有撤销；已出片项目不受影响——简报在选用时已拷走）
async function onDelete(id: string) {
  try {
    await api.deleteLibrary(id)
    message.success('已删除')
  } catch (e) {
    message.error(`删除失败：${(e as Error).message}`)
    return
  }
  show.value = false // 详情抽屉开着就一并关掉，避免展示已删条目
  emit('deleted')
}

// 点卡片开详情抽屉：标签 join 词表
const show = ref(false)
const detail = ref<TemplateDetailData | null>(null)
function open(s: Style) {
  detail.value = {
    title: s.name,
    status: { label: s.published ? '已入库' : '草稿', type: s.published ? 'success' : 'warning' },
    subject: s.subject,
    tags: [s.tags.main, ...(s.tags.aux ?? [])]
      .filter(Boolean)
      .map(id => ({ id, name: vEntry(id)?.name ?? id, brief: vEntry(id)?.brief })),
    tagEvidence: s.tag_evidence ?? [],
    signatures: s.signatures ?? [],
    originLabel: '来源 ' + (s.origin_project || '—'),
    createdAt: new Date(s.created_at).toLocaleDateString(),
    libMediaId: s.origin_project?.startsWith('video:') ? s.id : undefined,
    packId: s.id,
    published: s.published,
  }
  show.value = true
}
function onPublish(id: string) {
  show.value = false // 关抽屉再入库：入库后 packs 刷新，避免抽屉里状态过期
  emit('publish', id)
}
</script>

<template>
  <section class="lib">
    <header class="head">
      <h2>风格库</h2>
      <p>策展目录 + 绝活收藏夹：条目只打公认风格标签，配参考片独有手法清单——选用即「强参考」，设计阶段现发明设计系统并逐条吸收。点卡片看标签、特色与证据帧。</p>
    </header>
    <div v-if="!packs.length" class="none">
      风格库还是空的。在「视频解析」上传参考视频解析入库（词表会自动长出第一批词条）；出片后 Agent 也会自动起草。
    </div>
    <div v-else class="grid">
      <figure v-for="s in packs" :key="s.id" class="card" :class="{ draft: !s.published }" @click="open(s)">
        <div class="cover">
          <img v-if="s.origin_project?.startsWith('video:')" :src="libraryMediaURL(s.id, 'cover.png')"
            loading="lazy" alt="" @error="($event.target as HTMLImageElement).style.display = 'none'" />
          <div class="cover-fallback">
            <b>{{ s.name }}</b>
          </div>
        </div>
        <figcaption>
          <div class="row1">
            <b>{{ s.name }}</b>
            <NTag size="small" :bordered="false" :type="s.published ? 'success' : 'warning'">
              {{ s.published ? '已入库' : '草稿' }}
            </NTag>
          </div>
          <div class="chips">
            <NTag size="tiny" :bordered="false" type="warning">{{ vEntry(s.tags.main)?.name ?? s.tags.main }}</NTag>
            <NTag v-for="a in s.tags.aux ?? []" :key="a" size="tiny" :bordered="false">{{ vEntry(a)?.name ?? a }}</NTag>
            <NTag v-if="s.signatures?.length" size="tiny" :bordered="false" type="info">特色×{{ s.signatures.length }}</NTag>
          </div>
          <span v-if="s.subject" class="sub" :title="s.subject">{{ s.subject }}</span>
          <div class="row2">
            <span class="origin">{{ s.id }}</span>
            <span class="btns">
              <NPopconfirm @positive-click="onDelete(s.id)">
                <template #trigger>
                  <NButton size="small" quaternary type="error" @click.stop>删除</NButton>
                </template>
                删除后新建项目不能再选它；已出片项目不受影响。
              </NPopconfirm>
              <NButton v-if="!s.published" size="small" type="primary" ghost @click.stop="emit('publish', s.id)">
                入库
              </NButton>
            </span>
          </div>
        </figcaption>
      </figure>
    </div>

    <TemplateDetail :show="show" :detail="detail" @close="show = false" @publish="onPublish" />
  </section>
</template>

<style scoped>
.lib { flex: 1; min-height: 0; overflow: auto; padding: 24px 28px; }
.head h2 { font-size: 16px; color: #d9d9e0; }
.head p { font-size: 12.5px; color: #7c7c88; margin: 4px 0 18px; line-height: 1.6; }
.none { color: #55555f; font-size: 13px; padding: 40px 0; line-height: 1.8; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 16px; }
.card {
  background: #14141a; border: 1px solid #232329; border-radius: 12px; overflow: hidden;
  cursor: pointer; transition: border-color .15s;
}
.card:hover { border-color: #3a3a44; }
.card.draft { border-color: #5c4d24; }
figcaption { padding: 10px 12px 12px; display: flex; flex-direction: column; gap: 6px; }
.row1 { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.row1 b { font-size: 13px; }
.chips { display: flex; gap: 6px; flex-wrap: wrap; }
.sub { font-size: 11.5px; color: #8a8a96; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.row2 { display: flex; align-items: center; justify-content: space-between; }
.btns { display: flex; gap: 6px; align-items: center; }
.origin { font-size: 11px; color: #55555f; font-family: monospace; }
.cover { position: relative; height: 150px; background: #0d0d12; }
.cover img { position: absolute; inset: 0; width: 100%; height: 100%; object-fit: cover; }
.cover-fallback {
  position: absolute; inset: 0; display: grid; place-items: center; padding: 0 18px; text-align: center;
  background: linear-gradient(135deg, #1a1a22, #23232e);
}
.cover-fallback b { color: #d9d9e0; font-size: 15px; letter-spacing: 1px; }
</style>
