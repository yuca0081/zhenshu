// 共享格式化小件：项目状态映射 / 短日期 / tokens 渐变封面。
// 此前 HistoryRail/HomeView/GatesModal/LibraryPanel 各持一份（封面实现还有分叉），收敛于此。
import type { Style } from './types'

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

// 无样张条目的封面回退：tokens.bg 渐变打底（风格门弹窗 / 风格库卡片同款）
export function coverStyle(s: Style): string {
  const bg = s.tokens?.bg as { from?: string; to?: string; angle?: number } | string | undefined
  const from = (typeof bg === 'string' ? bg : bg?.from) || '#1a1a22'
  const to = (typeof bg === 'object' ? bg?.to : undefined) || from
  const ang = typeof bg === 'object' ? (bg.angle ?? 135) : 135
  return `background: linear-gradient(${ang}deg, ${from}, ${to})`
}
