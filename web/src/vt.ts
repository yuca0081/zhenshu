// 页面切换过渡助手：document.startViewTransition 包装（移植自 yuca-next 的
// react-router viewTransition 用法；zhenshu 是手写 hash 路由，切换处手动包一层）。
// 回调里改 ref 后要等 Vue 的 DOM patch 完才截「新」帧，所以补一个 nextTick。
// 特性不支持 / 用户偏好减少动态时直接执行，不掉帧不报错。
import { nextTick } from 'vue'

export function startVt(update: () => void) {
  const doc = document as Document & { startViewTransition?: (cb: () => Promise<void>) => unknown }
  const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches
  if (!doc.startViewTransition || reduced) {
    update()
    return
  }
  doc.startViewTransition(async () => {
    update()
    await nextTick()
  })
}
