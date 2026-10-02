<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import { useMessage, NButton, NInput, NScrollbar, NSpin } from 'naive-ui'
import { api } from '../api'
import { fmtClock } from '../segtime'
import type { ChatRef, Msg, ProjectView } from '../types'

const props = defineProps<{ projectId: string; msgs: Msg[]; busy: boolean; refs: ChatRef[]; view: ProjectView | null }>()
const draft = defineModel<string>('draft')
const emit = defineEmits<{ sent: []; 'remove-ref': [ref: ChatRef]; 'clear-refs': []; 'open-gate': [g: 'manuscript' | 'storyboard' | 'style'] }>()

const message = useMessage()
const listRef = ref<InstanceType<typeof NScrollbar> | null>(null)

async function send() {
  const text = (draft.value ?? '').trim()
  if (!text || !props.projectId) return
  // 引用卡片结构化随消息发出（落库 refs、Agent 拿精确上下文）；
  // 清稿/清引用只在发送成功后做——先清后发会在失败时让整段输入无声蒸发
  try {
    await api.chat(props.projectId, text, props.refs)
  } catch (e) {
    message.error(`发送失败：${e instanceof Error ? e.message : e}（内容仍保留在输入框）`)
    return
  }
  draft.value = ''
  emit('clear-refs')
  emit('sent')
}

watch(() => props.msgs.length, () => nextTick(() => listRef.value?.scrollTo({ top: 1e9, behavior: 'smooth' })))

const fmtTime = (iso: string) => (iso ? iso.slice(11, 16) : '')

// 引用三态：段级 / 时刻级（段 + 时间码）/ 元素级（段 + 时刻 + 人话名）
const refLabel = (r: ChatRef) =>
  r.elementName
    ? `段${r.idx} · ${fmtClock(r.t)} ·「${r.elementName}」`
    : r.t != null
      ? `段${r.idx} · ${fmtClock(r.t)}`
      : `段${r.idx}「${r.key}」`
const refKey = (r: ChatRef) => r.elementId ?? (r.t != null ? `s${r.idx}t${r.t}` : `s${r.idx}`)
</script>

<template>
  <aside class="chat">
    <div class="head">
      对话
      <span v-if="busy" class="busy"><NSpin :size="12" /> Agent 工作中</span>
      <!-- 三前置硬门：同名标签即入口，点开补内容，齐了灯亮才能开工 -->
      <div v-if="view" class="gates">
        <button class="chip" :class="{ ok: view.gates.manuscript }" title="文稿——点开编辑 / AI 起草" @click="emit('open-gate', 'manuscript')">文稿</button>
        <button class="chip" :class="{ ok: view.gates.storyboard }" title="分镜——点开查看整份分镜文档" @click="emit('open-gate', 'storyboard')">
          分镜{{ view.gates.storyboard ? `·${view.seg_count}段` : '' }}
        </button>
        <button
          class="chip" :class="{ ok: view.gates.style_confirmed, wait: view.gates.style_draft && !view.gates.style_confirmed }"
          title="风格——点开选库内风格或描述方向" @click="emit('open-gate', 'style')"
        >风格{{ view.gates.style_confirmed ? '已确认' : '' }}</button>
      </div>
    </div>
    <NScrollbar ref="listRef" class="list">
      <div v-if="!msgs.length" class="none">
        把文稿粘进对话、或点上方「文稿」标签填写——<br />我会出分镜、出风格样张，你确认风格后开工出片。
      </div>
      <div v-for="m in msgs" :key="m.id" class="row" :class="m.role">
        <template v-if="m.type === 'tool_call'">
          <div class="tool">
            <span class="tname">{{ m.content }}</span>
          </div>
        </template>
        <template v-else-if="m.type === 'error'">
          <div class="bubble err">{{ m.content }}</div>
        </template>
        <template v-else>
          <div class="meta">{{ m.role === 'user' ? '我' : '帧述' }} · {{ fmtTime(m.created_at) }}</div>
          <div class="bubble" :class="m.role">{{ m.content }}</div>
          <div v-if="m.role === 'user' && m.refs?.length" class="msg-refs">
            <span v-for="r in m.refs" :key="refKey(r)" class="msg-ref">{{ refLabel(r) }}</span>
          </div>
        </template>
      </div>
      <div v-if="busy && !msgs.length" class="none"><NSpin :size="14" /></div>
    </NScrollbar>
    <!-- 引用列表：时间轴/分镜/检视点选加入，可单删；随下一条消息结构化发出 -->
    <div v-if="refs.length" class="refs">
      <span class="refs-label">引用 {{ refs.length }}</span>
      <div class="ref-chips">
        <span v-for="r in refs" :key="refKey(r)" class="ref-chip">
          {{ refLabel(r) }}
          <button class="rm" title="移除" @click="emit('remove-ref', r)">×</button>
        </span>
      </div>
    </div>
    <div class="input">
      <NInput
        v-model:value="draft" type="textarea" :rows="3" :maxlength="4000"
        placeholder="对话或下指令（Enter 发送）｜Ctrl+点时间轴加引用"
        @keydown.enter.exact.prevent="send"
      />
      <NButton type="primary" :disabled="!draft?.trim() || !projectId" @click="send">发送</NButton>
    </div>
  </aside>
</template>

<style scoped>
.chat {
  width: 400px; flex: none; display: flex; flex-direction: column;
  background: #121217; border-left: 1px solid #1d1d24;
}
.head {
  flex: none; height: 44px; display: flex; align-items: center; gap: 10px;
  padding: 0 16px; font-weight: 600; color: #d9d9e0; border-bottom: 1px solid #1d1d24;
}
.busy { display: flex; align-items: center; gap: 6px; font-size: 12px; font-weight: 400; color: #8fc7ff; }
/* 三前置标签（硬门进度）：就在标题行里，点开即入口 */
.gates { margin-left: auto; display: flex; gap: 6px; flex: none; }
.chip {
  font-size: 11px; padding: 2px 9px; border-radius: 999px; cursor: pointer;
  background: #1c1c23; color: #7c7c88; border: 1px solid #2b2b33;
}
.chip:hover { border-color: #4a4a56; color: #b9b9c4; }
.chip.ok { color: #7ee2a8; border-color: #2c5c40; background: #14211a; }
.chip.ok:hover { border-color: #3d7a56; }
.chip.wait { color: #f0c674; border-color: #5c4d24; background: #211d12; }
.chip.run { color: #8fc7ff; border-color: #2b4a66; background: #121b24; }
.list { flex: 1; padding: 14px 12px; }
.none { color: #55555f; font-size: 13px; line-height: 1.9; text-align: center; padding: 40px 20px; }
.row { margin-bottom: 12px; }
.meta { font-size: 11px; color: #55555f; margin: 0 6px 3px; }
.row.agent .meta { text-align: left; }
.row.user .meta { text-align: right; }
.bubble {
  padding: 9px 13px; border-radius: 12px; font-size: 13.5px; line-height: 1.75;
  white-space: pre-wrap; word-break: break-word; max-width: 92%;
}
.bubble.user { background: #2b3a52; color: #e8eef8; margin-left: auto; border-bottom-right-radius: 4px; }
.bubble.agent { background: #1c1c23; color: #c9c9d1; border-bottom-left-radius: 4px; }
.bubble.err { background: #2a1a1a; color: #ff9d9d; }
.tool { text-align: center; }
.tname {
  font-size: 11px; color: #8a8a96; background: #17171d; border: 1px dashed #2b2b33;
  border-radius: 999px; padding: 3px 12px;
}
.refs { flex: none; display: flex; gap: 8px; padding: 8px 10px 0; align-items: flex-start; }
.refs-label { flex: none; font-size: 11px; color: #f0c674; padding-top: 5px; }
/* 最多露出 5 行，超出滚轮滚动 */
.ref-chips {
  flex: 1; min-width: 0; display: flex; flex-wrap: wrap; gap: 5px; align-content: flex-start;
  max-height: 145px; overflow-y: auto; scrollbar-width: thin; scrollbar-color: #3a3a45 transparent;
}
.ref-chips::-webkit-scrollbar { width: 5px; }
.ref-chips::-webkit-scrollbar-thumb { background: #3a3a45; border-radius: 3px; }
.ref-chip {
  display: inline-flex; align-items: center; gap: 4px; max-width: 100%;
  font-size: 12px; color: #f0c674; background: #241f14; border: 1px solid rgba(240, 198, 116, .35);
  border-radius: 999px; padding: 3px 6px 3px 10px;
}
.rm {
  border: 0; background: transparent; color: #8a7a4d; cursor: pointer; font-size: 13px;
  width: 16px; height: 16px; line-height: 1; border-radius: 50%; padding: 0;
}
.rm:hover { color: #fff; background: rgba(240, 198, 116, .3); }
/* 历史消息内的引用 chips（结构化 refs 回显） */
.msg-refs { display: flex; flex-wrap: wrap; gap: 4px; margin: 3px 6px 0 auto; justify-content: flex-end; }
.msg-ref {
  font-size: 11px; color: #c9a75f; background: #1d1a12; border: 1px solid rgba(240, 198, 116, .25);
  border-radius: 999px; padding: 1px 8px;
}
.input { flex: none; display: flex; gap: 8px; padding: 10px; border-top: 1px solid #1d1d24; align-items: flex-end; }
</style>
