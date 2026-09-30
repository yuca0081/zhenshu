// 段时间表：段长 = 配音时长 + 0.35s 尾垫 —— 与拼接口径一致（ai/assemble.py scene = duration_s + 0.35）。
// MP4 的段起始按此累计；此前按裸 duration_s 累计会导致第 N 段偏 0.35×N 秒。
import type { AudioMeta, Storyboard } from './types'

export const SEG_TAIL = 0.35

export interface SegCol {
  idx: number
  segId: string
  key: string
  dur: number // 段长（含尾垫，与 MP4 一致）
  start: number // 全局起始（秒）
}

export function segTable(sb: Storyboard | null, am: AudioMeta | null): SegCol[] {
  if (!sb) return []
  // xfade 成片：用后端实测边界（renders/timeline.json，每边界比裸累计早 xfadeDur）
  const tl = am?.timeline
  if (tl && tl.length === sb.segments.length) {
    return sb.segments.map((s, i) => ({
      idx: s.idx, segId: s.id, key: s.key,
      start: tl[i].start, dur: tl[i].end - tl[i].start,
    }))
  }
  let acc = 0
  return sb.segments.map((s, i) => {
    const v = am?.voices[i]
    const dur = v ? v.duration_s + SEG_TAIL : s.duration_hint || 3
    const col = { idx: s.idx, segId: s.id, key: s.key, dur, start: acc }
    acc += dur
    return col
  })
}

// 全局时刻 → 所在段（无匹配给首段）
export function segAt(table: SegCol[], t: number): SegCol | null {
  if (!table.length) return null
  for (let i = table.length - 1; i >= 0; i--) {
    if (t >= table[i].start) return table[i]
  }
  return table[0]
}

// 秒 → 0:45.2
export function fmtClock(t?: number): string {
  if (t == null || t < 0) return ''
  const m = Math.floor(t / 60)
  const s = (t % 60).toFixed(1).padStart(4, '0')
  return `${m}:${s}`
}
