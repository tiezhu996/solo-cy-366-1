import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createRouter, createMemoryHistory } from 'vue-router'
import Vant from 'vant'
import type { Component } from 'vue'
import { nextTick } from 'vue'
import { mockDB } from './mockBackend'

// 已挂载页面，由 setup.ts 在用例后统一卸载，避免 teleport/popup 跨用例残留。
const mountedWrappers: VueWrapper<any>[] = []

/**
 * 移除游离在 body 的 Vant teleport 叠层（toast/popup/dialog 等）。
 * 这些节点可能已不属于当前组件树，若不先移除，组件卸载时其内部过渡的
 * removeChild 会在 jsdom 下报 NotFoundError（真实浏览器容忍该顺序）。
 */
function detachOrphanOverlays(root: Element): void {
  const overlaySel = ['.van-toast', '.van-overlay', '.van-popup', '.van-dialog', '.van-action-sheet'].join(',')
  document.body.querySelectorAll(overlaySel).forEach((node) => {
    if (!root.contains(node) && node.parentNode) {
      node.parentNode.removeChild(node)
    }
  })
}

export async function disposeMountedPages(): Promise<void> {
  // 先排空在途请求与响应式更新，避免组件在卸载过程中触发更新/teleport 清理异常。
  await flushPromises()
  await nextTick()
  while (mountedWrappers.length) {
    const w = mountedWrappers.pop()
    const root = w?.element as Element | undefined
    if (root) detachOrphanOverlays(root)
    try {
      w?.unmount()
    } catch {
      /* 已随 body 清理 */
    }
  }
  await nextTick()
}

export type Role = 'admin' | 'staff' | 'member'

const ROLE_STORAGE: Record<Role, { token: string; user: { id: number; username: string; role: string; nickname: string } }> = {
  admin: { token: 'token-admin', user: { id: 101, username: 'admin', role: 'admin', nickname: '管理员' } },
  staff: { token: 'token-staff', user: { id: 202, username: 'clerk', role: 'staff', nickname: '店员' } },
  member: { token: 'token-member', user: { id: 303, username: 'gamer', role: 'member', nickname: '会员' } },
}

/** 注入登录会话（页面 RBAC 由 localStorage 中的 user.role 驱动）。 */
export function loginAs(role: Role): void {
  const s = ROLE_STORAGE[role]
  localStorage.setItem('token', s.token)
  localStorage.setItem('user', JSON.stringify(s.user))
}

/** 以真实 Pinia + Router + Vant 挂载页面；弹窗 teleport 到 body，断言时查 document.body。 */
export async function mountPage(component: Component, routePath = '/stations'): Promise<VueWrapper<any>> {
  setActivePinia(createPinia())
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: { template: '<div/>' } }, { path: '/stations', component }, { path: '/stations/repairs', component }],
  })
  router.push(routePath)
  await router.isReady()
  const wrapper = mount(component, {
    global: {
      plugins: [router, Vant],
    },
    attachTo: document.body,
  })
  await flushPromises()
  await nextTick()
  mountedWrappers.push(wrapper)
  return wrapper
}

/** 推进微任务（等待页面请求与弹窗渲染）。 */
export async function settle(times = 1): Promise<void> {
  for (let i = 0; i < times; i += 1) {
    await flushPromises()
    await nextTick()
  }
}

/** 在弹层（teleport 到 body）中按精确文本查找按钮并原生点击。 */
export async function clickPopupButton(text: string): Promise<void> {
  const btn = findByText(document.body, 'button', text)
  if (!btn) throw new Error(`弹层中找不到按钮「${text}」`)
  ;(btn as HTMLElement).click()
  await settle(2)
}

/** 列表中按文本查找可点击行（van-cell）。 */
export function clickCellByText(text: string): void {
  const cell = findByText(document.body, '.van-cell', text)
  if (!cell) throw new Error(`列表中找不到机位行「${text}」`)
  ;(cell as HTMLElement).click()
}

export function findByText(root: ParentNode, selector: string, text: string): Element | null {
  const nodes = Array.from(root.querySelectorAll(selector))
  return nodes.find((el) => (el.textContent || '').includes(text)) ?? null
}

/** 按 placeholder 找弹层中的文本域（页面存在多个 popup，借此区分原因/结果表单）。 */
export function textareaByPlaceholder(placeholder: string): HTMLTextAreaElement {
  const el = document.body.querySelector(`textarea[placeholder*="${placeholder}"]`) as HTMLTextAreaElement | null
  if (!el) throw new Error(`找不到文本域 placeholder=${placeholder}`)
  return el
}

/** 用原生 setter 赋值并派发 input 事件，兼容 Vant Field 的 v-model。 */
export function setNativeValue(el: HTMLInputElement | HTMLTextAreaElement, value: string): void {
  const proto = Object.getPrototypeOf(el)
  const desc = Object.getOwnPropertyDescriptor(proto, 'value')
  desc?.set?.call(el, value)
  el.dispatchEvent(new Event('input', { bubbles: true }))
}

/** 读取假后端机位/报修当前状态（接口侧断言）。 */
export function dbState(stationID: number) {
  const station = mockDB().stations.find((s) => s.id === stationID) ?? null
  const repairs = mockDB().repairs.filter((r) => r.station_id === stationID)
  const open = repairs.find((r) => r.status === 'pending') ?? null
  return { station, repairs, open }
}
