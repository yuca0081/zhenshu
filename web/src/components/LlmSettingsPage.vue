<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NButton, NCheckbox, NInput, NSelect, NTabPane, NTabs } from 'naive-ui'
import { api } from '../api'
import { errMsg } from '../format'
import type { LlmSettingsView, LlmStationMeta } from '../types'

// 模型设置页（仅管理员入口可见，docs/llm-settings-2026-10-29.md）：「大模型」页签
// 维护供应商台账（URL/Key/模型名，Key 只进不出）；「工位图」页签把每个工位绑到
// 台账条目，不绑 = 跟随所属档（对话/规划/视觉），档也不绑 = .env 兜底。保存即热生效。

const roleLabel: Record<string, string> = { dialogue: '对话', plan: '规划', visual: '视觉' }
const groupLabel: Record<string, string> = { prep: '准备', produce: '制作', library: '风格库', common: '通用' }
const effortOptions = [
  { label: '默认档', value: '' },
  { label: 'low（轻思考）', value: 'low' },
  { label: 'high（深思考）', value: 'high' },
  { label: 'disabled（关闭）', value: 'disabled' },
]

interface FormProv {
  id: string
  label: string
  base_url: string
  model: string
  key: string // 本轮新输入；不回显已存 Key
  clear: boolean // 显式清除已存 Key
  has_key: boolean
  key_tail: string
  saved: boolean // 已在台账（id 锁定，测试可按 id 取已存 Key）
  testing: boolean
  test: { ok: boolean; latency_ms: number; reply?: string; error?: string } | null
}

const view = ref<LlmSettingsView | null>(null)
const provs = ref<FormProv[]>([])
const roleDefault = ref<Record<string, { provider: string; effort: string }>>({})
const stations = ref<Record<string, { provider: string; effort: string }>>({})
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const savedTip = ref('')

onMounted(load)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const v = await api.llmSettings()
    view.value = v
    provs.value = v.providers.map(p => ({
      id: p.id, label: p.label, base_url: p.base_url, model: p.model,
      key: '', clear: false, has_key: p.has_key, key_tail: p.key_tail,
      saved: true, testing: false, test: null,
    }))
    const bOf = (src: Record<string, { provider: string; effort?: string }>, k: string) =>
      ({ provider: src[k]?.provider ?? '', effort: src[k]?.effort ?? '' })
    roleDefault.value = {
      dialogue: bOf(v.role_default, 'dialogue'),
      plan: bOf(v.role_default, 'plan'),
      visual: bOf(v.role_default, 'visual'),
    }
    stations.value = Object.fromEntries(v.station_meta.map(m => [m.id, bOf(v.stations, m.id)]))
  } catch (e) {
    error.value = `加载失败：${errMsg(e)}`
  } finally {
    loading.value = false
  }
}

function addProv() {
  provs.value.push({ id: '', label: '', base_url: '', model: '', key: '', clear: false, has_key: false, key_tail: '', saved: false, testing: false, test: null })
}

function removeProv(i: number) {
  const id = provs.value[i].id
  provs.value.splice(i, 1)
  if (!id) return
  // 台账删了，引用它的绑定同步摘掉（不摘会被服务端校验拒收）
  for (const base of Object.keys(roleDefault.value)) {
    if (roleDefault.value[base].provider === id) roleDefault.value[base].provider = ''
  }
  for (const k of Object.keys(stations.value)) {
    if (stations.value[k].provider === id) stations.value[k].provider = ''
  }
}

async function testProv(p: FormProv) {
  p.testing = true
  p.test = null
  try {
    // 表单值优先直传（没保存也能测）；已存条目带上 id 让服务端补空字段（含已存 Key）
    const payload: { provider?: string; base_url?: string; api_key?: string; model?: string } = {}
    if (p.saved && p.id) payload.provider = p.id
    if (p.base_url.trim()) payload.base_url = p.base_url.trim()
    if (p.model.trim()) payload.model = p.model.trim()
    if (p.key.trim()) payload.api_key = p.key.trim()
    p.test = await api.testLlmProvider(payload)
  } catch (e) {
    p.test = { ok: false, latency_ms: 0, error: errMsg(e) }
  } finally {
    p.testing = false
  }
}

function provById(id: string) {
  return provs.value.find(p => p.id === id)
}

// 工位当前生效模型（跟随链现场算：工位绑定 → 档位兜底 → 载入时的 env 生效值）
function effModel(meta: LlmStationMeta): string {
  const pid = stations.value[meta.id]?.provider || roleDefault.value[meta.base]?.provider || ''
  const p = pid ? provById(pid) : null
  if (p) return p.model || view.value?.env.model || ''
  return view.value?.effective[meta.id]?.model || view.value?.env.model || ''
}

const providerOptions = computed(() =>
  provs.value.map(p => ({ label: `${p.label || p.id} · ${p.model || '默认模型'}`, value: p.id })))

function followOption(meta: LlmStationMeta) {
  return { label: `跟随${roleLabel[meta.base]}（当前：${effModel(meta)}）`, value: '' }
}

function followRoleOption(base: string) {
  const m = view.value?.effective[base]?.model || view.value?.env.model || ''
  return { label: `未绑定（.env 兜底：${m}）`, value: '' }
}

const groups = computed(() => {
  if (!view.value) return []
  const by: Record<string, LlmStationMeta[]> = {}
  for (const m of view.value.station_meta) (by[m.group] ||= []).push(m)
  return ['prep', 'produce', 'library', 'common']
    .filter(g => by[g]?.length)
    .map(g => ({ key: g, label: groupLabel[g], items: by[g] }))
})

async function save() {
  const ids = new Set<string>()
  for (const p of provs.value) {
    p.id = p.id.trim().toLowerCase()
    if (!p.id) {
      error.value = '供应商 id 必填（小写字母/数字/-/_）'
      return
    }
    if (ids.has(p.id)) {
      error.value = `供应商 id 重复：${p.id}`
      return
    }
    ids.add(p.id)
  }
  saving.value = true
  error.value = ''
  savedTip.value = ''
  try {
    await api.saveLlmSettings({
      providers: provs.value.map(p => ({
        id: p.id, label: p.label.trim(), base_url: p.base_url.trim(), model: p.model.trim(),
        api_key: p.key, clear_api_key: p.clear,
      })),
      role_default: roleDefault.value,
      stations: stations.value,
    })
    await load() // 重拉掩码与生效值
    savedTip.value = '已保存，下一次模型作业即生效（无需重启）'
    setTimeout(() => { savedTip.value = '' }, 4000)
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="lm-page">
    <div class="lm-wrap">
      <header class="lm-head">
        <h2>模型设置</h2>
        <p>「大模型」定义可用端点；「工位图」给每个工位选一个。字段留空即沿用服务端 .env，保存后下一次模型作业生效（无需重启）。</p>
      </header>
      <div v-if="loading" class="lm-empty">加载中…</div>
      <template v-else-if="view">
        <NTabs type="line" animated default-value="providers">
          <NTabPane name="providers" tab="大模型">
            <p class="lm-hint">
              一个条目 = 一个可用端点。
              <template v-if="view.env.key_set">服务端 .env 已配 Key，新条目可只填模型名复用它。</template>
            </p>
            <div v-for="(p, i) in provs" :key="i" class="lm-prov">
              <div class="lm-row">
                <NInput v-model:value="p.id" :disabled="p.saved" :status="p.id ? undefined : 'error'"
                  placeholder="id（小写字母/数字/-/_）" class="lm-id" />
                <NInput v-model:value="p.label" placeholder="名称，如 GLM 编码套餐" class="lm-name" />
                <NButton size="small" :loading="p.testing" @click="testProv(p)">测试</NButton>
                <NButton size="small" quaternary type="error" @click="removeProv(i)">删除</NButton>
              </div>
              <div class="lm-row">
                <NInput v-model:value="p.base_url" placeholder="URL（留空 = .env 的 LLM_BASE_URL）" />
                <NInput v-model:value="p.model" placeholder="模型名（留空 = .env 的 LLM_MODEL）" class="lm-model" />
              </div>
              <div class="lm-row">
                <NInput v-model:value="p.key" type="password" show-password-on="click"
                  :placeholder="p.has_key ? `已保存 ****${p.key_tail}（留空保持）` : view.env.key_set ? 'API Key（留空 = 沿用 .env）' : 'API Key'" />
                <NCheckbox v-if="p.has_key" :checked="p.clear" class="lm-clear"
                  @update:checked="v => p.clear = v">清除已存 Key</NCheckbox>
              </div>
              <p v-if="p.test" class="lm-test" :class="{ ok: p.test.ok }">
                {{ p.test.ok ? `✅ 连通 ${p.test.latency_ms}ms「${(p.test.reply || '').trim().slice(0, 40)}」` : `❌ ${p.test.error}` }}
              </p>
            </div>
            <NButton size="small" dashed block @click="addProv">＋ 添加大模型</NButton>
          </NTabPane>

          <NTabPane name="stations" tab="工位图">
            <p class="lm-hint">每个工位选一个大模型；「跟随」= 用所属档的绑定，档也未绑定则用服务端 .env。</p>
            <div class="lm-roles">
              <div class="lm-role-title">档位兜底</div>
              <div v-for="base in ['dialogue', 'plan', 'visual']" :key="base" class="lm-row lm-role">
                <span class="lm-role-name">{{ roleLabel[base] }}档</span>
                <NSelect v-model:value="roleDefault[base].provider" :options="[followRoleOption(base), ...providerOptions]" />
                <NSelect v-model:value="roleDefault[base].effort" :options="effortOptions" class="lm-eff" />
              </div>
            </div>
            <div v-for="g in groups" :key="g.key" class="lm-group">
              <div class="lm-group-title">{{ g.label }}</div>
              <div v-for="m in g.items" :key="m.id" class="lm-row lm-st">
                <span class="lm-st-name" :title="m.id">{{ m.label }}</span>
                <NSelect v-model:value="stations[m.id].provider" :options="[followOption(m), ...providerOptions]" />
                <NSelect v-model:value="stations[m.id].effort" :options="effortOptions" class="lm-eff" />
                <span class="lm-st-eff">{{ effModel(m) }}</span>
              </div>
            </div>
          </NTabPane>
        </NTabs>
      </template>
      <div class="lm-foot">
        <span v-if="error" class="lm-error">{{ error }}</span>
        <span v-else-if="savedTip" class="lm-saved">{{ savedTip }}</span>
        <NButton type="primary" :loading="saving" :disabled="loading" @click="save">保存</NButton>
      </div>
    </div>
  </div>
</template>

<style scoped>
.lm-page { flex: 1; min-height: 0; overflow: auto; }
.lm-wrap { max-width: 880px; margin: 0 auto; padding: 26px 28px 40px; }
.lm-head h2 { font-size: 18px; letter-spacing: 0.12em; color: var(--ink); margin-bottom: 6px; }
.lm-head p { color: var(--muted); font-size: 13px; margin-bottom: 18px; line-height: 1.6; }
.lm-empty { color: var(--muted); font-size: 13px; padding: 48px 0; text-align: center; }
.lm-hint { color: var(--muted); font-size: 12.5px; margin: 2px 0 14px; line-height: 1.6; }
.lm-prov {
  border: 1px solid var(--line-soft); padding: 12px;
  display: flex; flex-direction: column; gap: 8px; margin-bottom: 10px;
}
.lm-row { display: flex; gap: 8px; align-items: center; }
.lm-id { width: 160px; flex: none; }
.lm-name { flex: 1; }
.lm-model { width: 220px; flex: none; }
.lm-clear { flex: none; }
.lm-test { font-size: 12.5px; color: var(--err); margin: 0; word-break: break-all; }
.lm-test.ok { color: var(--ok); }
.lm-roles { border: 1px solid var(--line-soft); padding: 10px 12px; margin-bottom: 14px; display: flex; flex-direction: column; gap: 8px; }
.lm-role-title { color: var(--muted); font-size: 11px; letter-spacing: 0.14em; text-transform: uppercase; }
.lm-role-name { width: 52px; flex: none; color: var(--accent); font-size: 13px; font-weight: 600; }
.lm-eff { width: 150px; flex: none; }
.lm-group { margin-bottom: 12px; }
.lm-group-title { color: var(--muted); font-size: 11px; margin: 6px 0; letter-spacing: 0.14em; text-transform: uppercase; }
.lm-st { margin-bottom: 6px; }
.lm-st-name { width: 96px; flex: none; font-size: 13px; }
.lm-st-eff { flex: none; color: var(--muted); opacity: .75; font-size: 12px; max-width: 150px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-variant-numeric: tabular-nums; }
.lm-foot { display: flex; align-items: center; justify-content: flex-end; gap: 14px; margin-top: 18px; }
.lm-error { color: var(--err); font-size: 12.5px; flex: 1; text-align: right; word-break: break-all; }
.lm-saved { color: var(--ok); font-size: 12.5px; flex: 1; text-align: right; }
</style>
