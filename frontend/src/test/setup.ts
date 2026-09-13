import { beforeEach, afterEach } from 'vitest'
import request from '@/utils/request'
import { mockAdapter, resetMockDB } from './mockBackend'
import { disposeMountedPages } from './pageHelpers'

// 所有页面请求走内存假后端（与真实后端同业务规则、同响应体与错误码）。
;(request.defaults as unknown as { adapter: unknown }).adapter = mockAdapter

// ---- jsdom 环境补全 Vant 依赖的浏览器 API ----
if (!window.matchMedia) {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener() {},
    removeListener() {},
    addEventListener() {},
    removeEventListener() {},
    dispatchEvent() {
      return false
    },
  })) as unknown as typeof window.matchMedia
}
window.scrollTo = (() => {}) as unknown as typeof window.scrollTo
if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {}
}
if (!Element.prototype.scrollTo) {
  Element.prototype.scrollTo = (() => {}) as unknown as typeof Element.prototype.scrollTo
}
if (!Element.prototype.setPointerCapture) {
  Element.prototype.setPointerCapture = () => {}
}
if (!Element.prototype.releasePointerCapture) {
  Element.prototype.releasePointerCapture = () => {}
}
if (!(window as unknown as { ResizeObserver?: unknown }).ResizeObserver) {
  ;(window as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}
if (!(Element.prototype as unknown as { animate?: unknown }).animate) {
  ;(Element.prototype as unknown as { animate: () => unknown }).animate = () => ({
    finished: Promise.resolve(),
    cancel() {},
    pause() {},
    play() {},
    onfinish: null,
  })
}

// jsdom 下 Vant 弹层/下拉（teleport + 过渡）在更新或卸载时，可能对已不属于
// 当前父节点的锚点调用 insertBefore、或重复 removeChild，抛 NotFoundError
// （真实浏览器对该清理顺序更宽容）。仅在这种“游离”情形做幂等兜底，不改变正常 DOM 行为。
;(function guardTeleportDomOps() {
  const proto = Node.prototype
  const rawRemoveChild = proto.removeChild
  proto.removeChild = function patchedRemoveChild<T extends Node>(child: T): T {
    if (child && child.parentNode !== this) {
      return child
    }
    return rawRemoveChild.call(this, child) as T
  }
  const rawInsertBefore = proto.insertBefore
  proto.insertBefore = function patchedInsertBefore<T extends Node>(newNode: T, referenceNode: Node | null): T {
    if (referenceNode && referenceNode.parentNode !== this) {
      // 锚点已游离：等价于追加到末尾
      return rawInsertBefore.call(this, newNode, null) as T
    }
    return rawInsertBefore.call(this, newNode, referenceNode) as T
  }
})()

// 每个用例前清理会话痕迹并恢复假后端初始种子数据，保证可连续运行。
beforeEach(() => {
  localStorage.clear()
  resetMockDB()
  document.body.innerHTML = ''
})

// 每个用例后由 Vue 正常卸载（含 teleport 到 body 的弹层），不再手动清空 body，
// 避免 Vant 弹层过渡的异步 removeChild 撞上已清空的父节点产生 jsdom NotFoundError。
afterEach(async () => {
  await disposeMountedPages()
})
