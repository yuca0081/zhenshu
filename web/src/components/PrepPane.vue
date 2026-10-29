<script setup lang="ts">
import { computed, ref } from 'vue'
import { NButton, NInput, useMessage } from 'naive-ui'
import { api } from '../api'
import type { PrepDraftSeg, PrepView } from '../types'
import SampleFrame from './SampleFrame.vue'

// 打包终审（全流程唯一人工门，docs/prep-agents-design.md §2）：
// 文字稿 + 风格包 + 分镜稿 三工件一次确认，拦在昂贵的制作之前。
// 反馈按工件路由：改文字→重①（②③级联）/ 改风格→重②③ / 改结构场景→重③——
// 快捷按钮把要求转成对话消息，由主管 Agent 调 rerun_prep 执行。
const props = defineProps<{
  projectId: string
  prep: PrepView | null
  confirmed: boolean
  agentBusy: boolean
}>()
const emit = defineEmits<{ confirmed: []; 'chat-sent': [] }>()

const message = useMessage()
const confirming = ref(false)
// 页内两段式确认（替代 window.confirm）：原生弹窗在部分浏览器/自动化环境被拦
// 成静默假死（2026-10-16 死按钮、10-18 实测 Playwright 点按 30s 超时同源）。
const armed = ref(false)
let armTimer: number | null = null
function disarm() {
  armed.value = false
  if (armTimer != null) { clearTimeout(armTimer); armTimer = null }
}
async function confirm() {
  if (!props.projectId || confirming.value) return
  if (!armed.value) {
    armed.value = true
    if (armTimer != null) clearTimeout(armTimer)
    armTimer = window.setTimeout(disarm, 3000)
    return
  }
  disarm()
  confirming.value = true
  try {
    const r = await api.confirmPrep(props.projectId)
    message.success(`打包终审通过：${r.direction}`)
    emit('confirmed')
  } catch (e) {
    message.error((e as Error).message)
  } finally {
    confirming.value = false
  }
}

// ── 反馈路由：要求转对话消息，主管 Agent 据此调 rerun_prep ──
const feedback = ref('')
const feedbackTarget = ref<'script' | 'style' | 'storyboard'>('storyboard')
const targets = [
  { id: 'script', label: '改文字', hint: '重跑文稿工位（风格分镜级联重做）' },
  { id: 'style', label: '改风格', hint: '重跑风格工位（分镜级联重做）' },
  { id: 'storyboard', label: '改结构/场景', hint: '只重跑分镜工位' },
] as const
// IME 组合期的 Enter 是选词不是发送（中-38：主用户群中文输入，漏这个守卫会把
// 半截反馈发出去触发工位重跑——同仓 ChatPanel/NewProjectModal/LoginView 三处
// 同款）
function onEnter(e: KeyboardEvent) {
  if (e.isComposing || e.keyCode === 229) return
  e.preventDefault()
  sendFeedback()
}
async function sendFeedback() {
  const note = feedback.value.trim()
  if (!note || !props.projectId || props.agentBusy) return
  const t = targets.find(x => x.id === feedbackTarget.value)!
  try {
    await api.chat(props.projectId, `打包终审反馈（${t.label}）：${note}`)
    feedback.value = ''
    emit('chat-sent')
  } catch (e) {
    message.error((e as Error).message)
  }
}

const samples = computed(() => props.prep?.design?.samples ?? [])
const clipSrc = (video: string) => `/api/projects/${props.projectId}/style/clips/${video.split('/').pop() ?? video}`
const segs = computed(() => props.prep?.draft?.segments ?? [])
const paragraphs = computed(() => props.prep?.draft?.paragraphs ?? [])
const sentences = computed(() => props.prep?.script?.sentences ?? [])

// ── ③ 段落分组行（工件 C 段落层）：段落主题作分界行插进段表 ──
// draft 缺段落（老项目）或段落与段表对不齐时退回平铺，不硬凑。
interface ParaRow {
  para: { title: string; segs: string[] } | null
  no: number
  seg?: PrepDraftSeg
}
const paraRows = computed<ParaRow[]>(() => {
  const ss = segs.value
  const flat = () => ss.map(s => ({ para: null, no: 0, seg: s }))
  const ps = paragraphs.value
  if (!ps.length || !ss.length) return flat()
  const byId = new Map(ss.map(s => [s.id, s]))
  const rows: ParaRow[] = []
  let cursor = 0
  for (let i = 0; i < ps.length; i++) {
    rows.push({ para: ps[i], no: i + 1 })
    for (const id of ps[i].segs) {
      const s = byId.get(id)
      if (!s || ss[cursor]?.id !== id) return flat()
      cursor++
      rows.push({ para: null, no: 0, seg: s })
    }
  }
  if (cursor !== ss.length) return flat()
  return rows
})
const paraSecs = (p: { segs: string[] }) =>
  p.segs.reduce((a, id) => a + (segs.value.find(s => s.id === id)?.duration_hint ?? 0), 0)

// ── 自采依赖（终审披露）：样张阶段模型装了哪些库、哪些字体，一次列清 ──
const vendLibs = computed(() => props.prep?.design?.vend_libs ?? [])
const customFonts = computed(() => props.prep?.design?.custom_fonts ?? [])
// 设计字体栈拆 token（去重）：目录/系统/自装混在一起的「本片用了什么字体」全览
const designFonts = computed(() => {
  const f = props.prep?.design?.fonts
  if (!f) return []
  const out: string[] = []
  const seen = new Set<string>()
  for (const role of [f.display, f.body, f.data]) {
    if (!role?.stack) continue
    for (let tok of role.stack.split(',')) {
      tok = tok.trim().replace(/^['"]|['"]$/g, '')
      const k = tok.toLowerCase()
      if (tok && !seen.has(k)) { seen.add(k); out.push(tok) }
    }
  }
  return out
})
const hasDeps = computed(() => vendLibs.value.length > 0 || customFonts.value.length > 0 || designFonts.value.length > 0)
</script>

<template>
  <div class="prep">
    <template v-if="prep?.exists && prep.script && prep.stylepack && prep.draft">
      <header class="head">
        <div class="head-main">
          <h3>打包终审</h3>
          <p class="sub">
            文字稿 {{ sentences.length }} 句 · 风格「{{ prep.stylepack.direction || prep.style_name || prep.stylepack.style_id }}」 ·
            分镜 {{ segs.length }} 段<template v-if="paragraphs.length"> · {{ paragraphs.length }} 段落</template> / 约 {{ Math.round(segs.reduce((a, s) => a + s.duration_hint, 0)) }} 秒
          </p>
        </div>
        <div v-if="confirmed" class="ok-badge">✓ 已确认，可开工</div>
        <NButton
          v-else :type="armed ? 'warning' : 'primary'" :loading="confirming" @click="confirm"
          :title="armed ? '再点一次即锁定设计系统并允许开工' : '点击后需再确认一次（防误触）'"
        >{{ armed ? '再点一次，确认开工' : '确认打包，允许开工' }}</NButton>
      </header>

      <div class="scroll">
        <!-- ① 文字稿 -->
        <section class="card">
          <h4>① 文字稿<span class="meta">{{ prep.script.word_count }} 字 / 目标约 {{ Math.round(prep.script.target_seconds) }} 秒</span></h4>
          <ol class="sentences">
            <li v-for="s in sentences" :key="s.id"><i>{{ s.id }}</i>{{ s.text }}</li>
          </ol>
          <details v-if="prep.script.fact_sheet?.length" class="facts">
            <summary>事实清单（{{ prep.script.fact_sheet.length }} 条，逐条可溯源）</summary>
            <ul>
              <li v-for="(f, i) in prep.script.fact_sheet" :key="i">
                {{ f.claim }} <a v-if="f.url" :href="f.url" target="_blank" rel="noopener">来源 ↗</a>
              </li>
            </ul>
          </details>
        </section>

        <!-- ② 风格包 -->
        <section class="card">
          <h4>② 风格包<span class="meta">{{ prep.stylepack.direction }}</span></h4>
          <p class="desc">{{ prep.stylepack.description }}</p>
          <div class="grammar">
            <span>切分密度：{{ prep.stylepack.grammar.density }}</span>
            <span>段长带：{{ prep.stylepack.grammar.band_min }}–{{ prep.stylepack.grammar.band_max }} 秒</span>
            <span>同舞台延续：{{ prep.stylepack.grammar.continuity }}</span>
          </div>
          <!-- 自采依赖：样张阶段装了什么在此披露——确认门的人工验收面 -->
          <div v-if="hasDeps" class="deps">
            <span class="dep-label">自采依赖</span>
            <div class="dep-rows">
              <p v-if="vendLibs.length">
                <b>库（{{ vendLibs.length }}，正式段货架复用）</b>
                <code v-for="v in vendLibs" :key="v">{{ v }}</code>
              </p>
              <p v-if="customFonts.length">
                <b>自装字体（{{ customFonts.length }}，来源可核对）</b>
                <span v-for="c in customFonts" :key="c.family" class="dep-font">
                  {{ c.family }}<i v-if="c.license">{{ c.license }}</i><a v-if="c.url" :href="c.url" target="_blank" rel="noopener">源 ↗</a>
                </span>
              </p>
              <p v-if="designFonts.length">
                <b>本片字体</b>
                <code v-for="f in designFonts" :key="f">{{ f }}</code>
              </p>
            </div>
          </div>
          <div v-if="samples.length" class="samples">
            <figure v-for="(s, i) in samples" :key="i" class="sample">
              <!-- 动态样张优先：mp4（prep② 直渲/迷你真段）；没有再降级静态 -->
              <video
                v-if="s.video" :src="clipSrc(s.video)" loop muted autoplay
                playsinline controls class="clip-video"
              />
              <SampleFrame v-else :html="s.html" />
              <figcaption>{{ s.tag }} — {{ s.desc }}</figcaption>
            </figure>
          </div>
        </section>

        <!-- ③ 分镜稿 -->
        <section class="card">
          <h4>③ 分镜稿<span v-if="paragraphs.length" class="meta">{{ paragraphs.length }} 个段落——段落内是同一话题的画面延续，段落之间换话题</span></h4>
          <table class="sb">
            <thead><tr><th style="width:44px">段</th><th style="width:120px">标题</th><th style="width:80px">旁白</th><th>场景 / 首拍 / 末拍</th><th style="width:56px">秒</th></tr></thead>
            <tbody>
              <template v-for="r in paraRows" :key="r.seg ? r.seg.id : 'para' + r.no">
                <tr v-if="r.para" class="para-row">
                  <td colspan="5"><b>段落 {{ r.no }}</b>{{ r.para.title }}<i>{{ r.para.segs.length }} 段 · 约 {{ Math.round(paraSecs(r.para)) }} 秒</i></td>
                </tr>
                <tr v-else-if="r.seg">
                  <td class="c">{{ r.seg.idx }}</td>
                  <td>{{ r.seg.key }}</td>
                  <td class="c refs">{{ r.seg.narration_refs }}</td>
                  <td class="brief">
                    <p><b>场景</b> {{ r.seg.visual_brief.scene }}</p>
                    <p><b>首拍</b> {{ r.seg.visual_brief.first_frame }}</p>
                    <p><b>末拍</b> {{ r.seg.visual_brief.last_frame }}</p>
                  </td>
                  <td class="c">{{ Math.round(r.seg.duration_hint) }}</td>
                </tr>
              </template>
            </tbody>
          </table>
        </section>
      </div>

      <!-- 反馈路由 -->
      <footer v-if="!confirmed" class="feedback">
        <div class="fb-tabs">
          <button
            v-for="t in targets" :key="t.id" class="fb-tab" :class="{ on: feedbackTarget === t.id }"
            :title="t.hint" @click="feedbackTarget = t.id"
          >{{ t.label }}</button>
        </div>
        <NInput
          v-model:value="feedback" type="text" size="small" :disabled="agentBusy"
          placeholder="不满意的点写在这里，按工件路由重跑对应的准备工位…"
          @keydown.enter="onEnter"
        />
        <NButton size="small" :disabled="!feedback.trim() || agentBusy" :loading="agentBusy" @click="sendFeedback">发给我改</NButton>
      </footer>
    </template>

    <div v-else class="empty">
      <template v-if="prep?.exists">准备工位还没跑完（缺工件）——在对话里说「继续」或等它跑完。</template>
      <template v-else>这个项目没走准备工位（老项目/无 AI 模式）——风格在「风格」页签确认。</template>
    </div>
  </div>
</template>

<style scoped>
.prep { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.head {
  flex: none; display: flex; align-items: center; gap: 14px;
  padding: 14px 18px; border-bottom: 1px solid #202027;
}
.head-main { flex: 1; min-width: 0; }
.head h3 { font-size: 15px; color: #d9d9e0; letter-spacing: 1px; }
.head .sub { font-size: 12px; color: #7c7c88; margin-top: 3px; }
.ok-badge { color: #7ee2a8; font-size: 13px; font-weight: 600; }
.scroll { flex: 1; min-height: 0; overflow-y: auto; padding: 14px 18px; display: flex; flex-direction: column; gap: 14px; }
.card { background: #101016; border: 1px solid #232329; border-radius: 10px; padding: 14px 16px; }
.card h4 { font-size: 13.5px; color: #f0c674; margin-bottom: 10px; letter-spacing: 1px; }
.card .meta { font-size: 12px; color: #7c7c88; font-weight: 400; margin-left: 10px; }
.sentences { margin: 0; padding-left: 0; list-style: none; font-size: 13px; line-height: 1.8; color: #c9c9d3; }
.sentences i { font-style: normal; color: #6b6b78; font-size: 11.5px; margin-right: 8px; font-family: Consolas, monospace; }
.facts { margin-top: 10px; font-size: 12.5px; color: #9a9aa6; }
.facts summary { cursor: pointer; }
.facts ul { margin: 8px 0 0; padding-left: 18px; line-height: 1.7; }
.facts a { color: #8fc7ff; margin-left: 6px; }
.desc { font-size: 13px; line-height: 1.8; color: #c9c9d3; margin-bottom: 10px; }
.grammar { display: flex; gap: 18px; flex-wrap: wrap; font-size: 12.5px; color: #9a9aa6; margin-bottom: 12px; }
/* 自采依赖：库/字体 chips 披露区（终审的人工验收面） */
.deps { display: flex; gap: 12px; font-size: 12.5px; margin-bottom: 12px; }
.dep-label { flex: none; color: #f0c674; font-weight: 600; letter-spacing: .5px; padding-top: 2px; }
.dep-rows { min-width: 0; display: flex; flex-direction: column; gap: 6px; }
.dep-rows p { margin: 0; display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
.dep-rows b { color: #7c7c88; font-weight: 600; font-size: 11.5px; white-space: nowrap; }
.dep-rows code {
  background: #191920; border: 1px solid #2b2b33; border-radius: 5px;
  padding: 2px 8px; color: #c9c9d3; font-size: 11.5px;
}
.dep-font { display: inline-flex; align-items: baseline; gap: 5px; }
.dep-font i {
  font-style: normal; color: #7ee2a8; font-size: 10.5px;
  border: 1px solid rgba(126, 226, 168, .35); border-radius: 4px; padding: 1px 5px;
}
.dep-font a { color: #8fc7ff; font-size: 11px; }
.samples { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: 12px; }
.sample figcaption { font-size: 11.5px; color: #7c7c88; margin-top: 6px; line-height: 1.5; }
.sb { width: 100%; border-collapse: collapse; font-size: 12.5px; }
.sb th { text-align: left; padding: 8px 10px; color: #7c7c88; font-weight: 600; border-bottom: 1px solid #232329; }
.sb td { padding: 8px 10px; border-bottom: 1px solid #1d1d24; vertical-align: top; color: #c9c9d3; line-height: 1.6; }
.para-row td {
  background: #17171f; padding: 7px 12px; font-size: 12.5px; color: #f0c674;
  border-bottom: 1px solid #232329; letter-spacing: .3px;
}
.para-row b { font-weight: 600; margin-right: 8px; opacity: .75; }
.para-row i { font-style: normal; color: #7c7c88; margin-left: 10px; font-size: 11.5px; }
.c { color: #7c7c88; text-align: center; }
.refs { font-family: Consolas, monospace; font-size: 11.5px; }
.brief b { color: #6b6b78; font-weight: 600; margin-right: 6px; font-size: 11.5px; }
.brief p { margin: 0 0 4px; }
.feedback {
  flex: none; display: flex; align-items: center; gap: 10px;
  padding: 10px 18px; border-top: 1px solid #202027; background: #121217;
}
.fb-tabs { display: flex; gap: 4px; }
.fb-tab {
  border: 1px solid #2b2b33; background: #191920; color: #9a9aa6; font-size: 12px;
  padding: 4px 12px; border-radius: 999px; cursor: pointer; white-space: nowrap;
}
.fb-tab:hover { color: #e6e6ea; }
.fb-tab.on { border-color: rgba(240, 198, 116, .45); color: #f0c674; background: #1e1a12; }
.empty { flex: 1; display: flex; align-items: center; justify-content: center; color: #55555f; font-size: 13px; padding: 0 40px; text-align: center; line-height: 1.9; }
</style>
