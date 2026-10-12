// 共享格式化小件：项目状态映射 / 短日期 / 相对时间等纯函数。
// 此前 HistoryRail/HomeView/LibraryPanel 各持一份，收敛于此（GatesModal 已退役）。
export const statusLabel: Record<string, string> = {
  created: '已创建', manuscript: '有文稿', storyboard: '有分镜', style: '风格定',
  producing: '制作中', video: '有成片',
}

// HistoryRail 侧栏状态圆点的附加 class（缺省 = 灰点）
export const statusDotCls: Record<string, string> = {
  storyboard: 's-draft', style: 's-draft', producing: 's-run', video: 's-done',
}

// iso → "10/02"（首页卡片 / 侧栏短日期）
export const dateOf = (iso: string) => (iso ?? '').slice(5, 10).replace('-', '/')

// 上次制作是否以失败/中断告终（列表标红依据；重做成功后 last_produce 变 done 自然消失）
export const lastProduceFailed = (p: { last_produce?: { event: string } | null }) =>
  !!p.last_produce && (p.last_produce.event.endsWith('.error') || p.last_produce.event.endsWith('.interrupted'))

// 特色类型 → 中文名（风格详情/视频解析详情共用一份，别再各抄各的）
export const kindLabel: Record<string, string> = {
  camera: '运镜', transition: '转场', motif: '母题', motion: '动效', material: '素材', palette: '配色',
}

// unknown 错误 → 人话字符串：catch (e) 的 e 是 unknown，各处手写收窄样式不一
// （e.message ?? String(e) / (e as Error).message / instanceof 三连），收敛于此
export function errMsg(e: unknown): string {
  if (e instanceof Error) return e.message
  if (typeof e === 'string') return e
  try { return JSON.stringify(e) ?? 'unknown' } catch { return 'unknown' } // 循环引用等序列化失败兜底
}

// 制作管线阶段 key → 中文名（此前 ActivityFeed/VideoPane 各抄一份且互有缺漏，
// 这里是并集；未收录的 key 回退原样——ActivityFeed 靠"回退=原样"判未知工位不上屏）
const stageNames: Record<string, string> = {
  tts: '配音', materials: '素材', compositions: '画面', assemble: '组装',
  check: '质量检查', render: '渲染', frameqa: '画面审查',
}
export function stageLabel(stage: string): string {
  return stageNames[stage] ?? stage
}
