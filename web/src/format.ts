// 共享格式化小件：项目状态映射 / 短日期 / tokens 渐变封面。
// 此前 HistoryRail/HomeView/GatesModal/LibraryPanel 各持一份（封面实现还有分叉），收敛于此。
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
