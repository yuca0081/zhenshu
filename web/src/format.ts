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
