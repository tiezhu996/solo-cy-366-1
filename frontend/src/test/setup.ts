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

// 每个用例前清理会话痕迹并恢复假后端初始种子数据，保证可连续运行。
beforeEach(() => {
  localStorage.clear()
  resetMockDB()
  document.body.innerHTML = ''
})

// 每个用例后卸载页面，避免 teleport/popup 跨用例残留。
afterEach(() => {
  disposeMountedPages()
  document.body.innerHTML = ''
})
