/**
 * 机位报修页面可重复测试（Vitest + jsdom + 真实页面组件 Stations.vue / Repairs.vue）。
 *
 * 覆盖：
 *  P1 管理员正常报修：标记故障（原因）-> 详情显示状态/原因/登记角色/时间；
 *     填写结果恢复 -> 详情空闲，历史已关闭记录显示原因/结果/处理角色/时间
 *  P2 无报修单的故障机位（历史遗留）：详情无“当前报修”，可直接填结果恢复，
 *     页面展示补录的已关闭记录（结果/处理角色/时间，原因留痕）
 *  P3 会员只读：列表/详情/历史可见，所有写操作按钮与管理入口隐藏；
 *     直接调用登记/关闭/管理列表接口均 403
 *  P4 接口拒绝在页面侧可复现：重复登记 40010、使用中/已预约登记 40900、非法枚举 42200
 *  P5 报修管理页（Repairs.vue）展示正常记录与补录记录的原因/结果/角色/时间
 *
 * 失败归属：断言信息以【页面】或【接口】开头，便于区分是页面渲染问题还是接口/数据问题。
 * 每个用例前 setup 自动清空 localStorage/body 并 resetMockDB()，可连续反复运行。
 */
import { describe, it, expect, beforeEach, vi } from 'vitest'

// 命令式 Toast/Dialog 会动态挂载到 document.body，jsdom 下随用例清理产生
// removeChild 未捕获异常；测试不关注 Toast 表现，替换为无副作用桩（组件仍用真实 Vant）。
vi.mock('vant', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vant')>()
  return {
    ...actual,
    showToast: vi.fn(),
    showSuccessToast: vi.fn(),
    showFailToast: vi.fn(),
    showConfirmDialog: vi.fn(() => Promise.resolve()),
  }
})

import Stations from '@/pages/Stations.vue'
import Repairs from '@/pages/Repairs.vue'
import { reportRepair, closeRepair, listRepairs } from '@/api/repair'
import { updateStationStatus } from '@/api/station'
import {
  mountPage, settle, loginAs, clickCellByText, clickPopupButton, findByText,
  textareaByPlaceholder, setNativeValue, dbState, type Role,
} from './pageHelpers'

// 固定时间（mockBackend 基准 08:00 UTC，每次写操作 +5s）
// 页面经 formatTime 渲染为 "YYYY-MM-DD HH:mm:ss"；接口原始值为 ISO 8601
const ISO_TIME_RE = /2026-01-01T08:00:\d{2}\.\d{3}Z/
const FIRST_TIME = '2026-01-01 08:00:05'

function pageMustContain(text: string, where: string) {
  const hit = findByText(document.body, '*', text)
  expect(hit, `【页面】${where} 应可见文本「${text}」`).toBeTruthy()
}

function pageMustNotContain(text: string, where: string) {
  const hit = findByText(document.body, '*', text)
  expect(hit, `【页面】${where} 不应出现「${text}」（会员只读/按钮隐藏失败）`).toBeFalsy()
}

async function openStationDetail(name: string) {
  clickCellByText(name)
  await settle(2)
}

/** 弹窗内填写文本域并点击确认。 */
async function fillAndConfirm(placeholder: string, value: string, confirmText: string) {
  const ta = textareaByPlaceholder(placeholder)
  setNativeValue(ta, value)
  await settle(1)
  await clickPopupButton(confirmText)
}

async function apiErrorCode(p: Promise<unknown>): Promise<number> {
  try {
    await p
    return 0
  } catch (e: any) {
    return e?.response?.data?.code ?? -1
  }
}

describe('P1 管理员正常报修闭环（页面）', () => {
  it('标记故障填原因、恢复填结果，详情与历史展示状态/原因/结果/角色/时间', async () => {
    loginAs('admin')
    await mountPage(Stations)

    await openStationDetail('A区-01')
    pageMustContain('空闲', '机位详情状态徽标')
    pageMustContain('标记故障并登记报修', '空闲机位的操作按钮')

    // 登记报修
    const reportBtn = findByText(document.body, 'button', '标记故障并登记报修') as HTMLElement
    expect(reportBtn, '【页面】登记按钮应存在').toBeTruthy()
    reportBtn.click()
    await settle(2)
    await fillAndConfirm('故障现象', '耳机没有声音，麦克风无声', '确认登记')

    // 详情：故障 + 当前待处理报修
    pageMustContain('故障', '登记后机位状态')
    pageMustContain('待处理', '报修状态徽标')
    pageMustContain('当前报修', '当前报修区块')
    pageMustContain('故障原因', '原因字段名')
    pageMustContain('耳机没有声音，麦克风无声', '故障原因内容')
    pageMustContain('管理员 · admin', '登记操作角色与用户名')
    pageMustContain('登记时间', '登记时间字段名')
    const timeHit = findByText(document.body, '.van-cell', FIRST_TIME)
    expect(timeHit, `【页面】登记时间 ${FIRST_TIME} 应可见`).toBeTruthy()

    // 接口侧状态一致
    let state = dbState(1)
    expect(state.station?.status, '【接口】登记后机位应为 fault').toBe('fault')
    expect(state.open?.status, '【接口】应有 pending 报修单').toBe('pending')
    expect(state.open?.reason, '【接口】报修原因一致').toBe('耳机没有声音，麦克风无声')
    expect(state.open?.report_role, '【接口】登记角色为 admin').toBe('admin')

    // 恢复空闲
    const closeBtn = findByText(document.body, 'button', '恢复空闲') as HTMLElement
    expect(closeBtn, '【页面】故障机位应显示恢复按钮').toBeTruthy()
    closeBtn.click()
    await settle(2)
    await fillAndConfirm('维修/处理结果', '更换耳机接口，现场试听正常', '确认恢复')

    // 详情：空闲 + 报修历史（已关闭）
    pageMustContain('空闲', '恢复后机位状态')
    pageMustContain('已关闭', '历史记录报修状态')
    pageMustContain('报修记录', '历史区块标题')
    pageMustContain('耳机没有声音，麦克风无声', '历史中的故障原因')
    pageMustContain('更换耳机接口，现场试听正常', '历史中的处理结果')
    pageMustContain('处理：管理员 admin', '历史中的处理角色与用户名')
    pageMustContain('登记：管理员 admin', '历史中的登记角色与用户名')
    const closedTime = findByText(document.body, '.van-cell', '2026-01-01 08:00:10')
    expect(closedTime, '【页面】处理时间 2026-01-01 08:00:10 应在历史中可见').toBeTruthy()

    state = dbState(1)
    expect(state.station?.status, '【接口】恢复后机位应为 idle').toBe('idle')
    expect(state.open, '【接口】恢复后不应有待处理单').toBeNull()
    expect(state.repairs[0]?.status, '【接口】报修单应为 closed').toBe('closed')
    expect(state.repairs[0]?.handle_result, '【接口】处理结果一致').toBe('更换耳机接口，现场试听正常')
    expect(state.repairs[0]?.handled_at, '【接口】处理时间应落库').toBeTruthy()
  })
})

describe('P2 无报修单的故障机位恢复（历史遗留补录，页面）', () => {
  it('故障机位无当前报修也可填结果恢复，历史展示补录的已关闭记录', async () => {
    loginAs('staff')
    // 预置：4 号机位 fault 且无任何报修单（mock 初始种子即此状态）
    let state = dbState(4)
    expect(state.station?.status, '【测试基建】4 号机位应为 fault').toBe('fault')
    expect(state.repairs.length, '【测试基建】4 号机位应无报修单').toBe(0)

    await mountPage(Stations)
    await openStationDetail('B区-02')

    pageMustContain('故障', '遗留故障机位状态')
    // 无当前报修块，给出缺单提示
    pageMustContain('缺少待处理报修记录', '遗留故障的缺单提示')
    expect(findByText(document.body, '*', '当前报修'), '【页面】遗留故障不应显示“当前报修”区块').toBeFalsy()
    // 关键：恢复按钮不依赖待处理单，故障机位即可见（修复前此入口缺失导致卡死）
    const closeBtn = findByText(document.body, 'button', '恢复空闲') as HTMLElement
    expect(closeBtn, '【页面】无报修单的故障机位也必须显示“恢复空闲”按钮').toBeTruthy()

    closeBtn.click()
    await settle(2)
    await fillAndConfirm('维修/处理结果', '主板松动已紧固，复测正常', '确认恢复')

    pageMustContain('空闲', '补录恢复后机位状态')
    pageMustContain('已关闭', '补录记录状态')
    pageMustContain('历史故障遗留', '补录记录原因留痕')
    pageMustContain('主板松动已紧固，复测正常', '补录记录处理结果')
    pageMustContain('处理：店员 clerk', '补录处理角色与用户名')
    const t = findByText(document.body, '.van-cell', FIRST_TIME)
    expect(t, `【页面】补录处理时间 ${FIRST_TIME} 应可见`).toBeTruthy()

    state = dbState(4)
    expect(state.station?.status, '【接口】补录恢复后机位应为 idle').toBe('idle')
    expect(state.open, '【接口】不应有待处理单').toBeNull()
    expect(state.repairs.length, '【接口】应补录恰好 1 条记录').toBe(1)
    const rec = state.repairs[0]
    expect(rec.status, '【接口】补录记录应为 closed').toBe('closed')
    expect(rec.handle_result, '【接口】补录处理结果一致').toBe('主板松动已紧固，复测正常')
    expect(rec.handle_role, '【接口】补录处理角色为 staff').toBe('staff')
    expect(rec.handle_by_name, '【接口】补录处理人为 clerk').toBe('clerk')
    expect(rec.handled_at, '【接口】补录处理时间应落库').toBeTruthy()
    expect(rec.report_user_id, '【接口】遗留补录不应有登记人').toBe(0)
    expect(ISO_TIME_RE.test(rec.handled_at || ''), '【接口】补录时间应为 ISO 格式').toBe(true)
  })
})

describe('P3 会员只读入口（页面 + 接口越权）', () => {
  it('会员可查看机位详情与历史，但看不到任何写操作入口', async () => {
    loginAs('member')
    await mountPage(Stations)

    // 列表可见（只读）
    pageMustContain('A区-01', '会员可见机位列表')
    // 管理入口隐藏
    expect(findByText(document.body, 'button', '报修记录'), '【页面】会员不应看到报修管理入口').toBeFalsy()

    await openStationDetail('A区-01')
    pageMustContain('空闲', '会员可查看机位状态')
    pageMustNotContain('标记故障并登记报修', '会员机位详情')
    pageMustNotContain('恢复空闲', '会员机位详情')
    pageMustNotContain('编辑机位', '会员机位详情')
    pageMustNotContain('删除机位', '会员机位详情')

    // 故障机位（4 号）同样只读，且无恢复入口
    await openStationDetail('B区-02')
    pageMustContain('故障', '会员可查看故障机位')
    pageMustNotContain('恢复空闲', '会员故障机位详情')
  })

  it.each<[Role, string, () => Promise<unknown>]>([
    ['member', '登记报修', () => reportRepair(1, '越权登记')],
    ['member', '关闭报修', () => closeRepair(4, '越权恢复')],
    ['member', '报修管理列表', () => listRepairs({ page: 1, page_size: 10 })],
  ])('会员调用%s接口返回 403/40300', async (_role, label, fn) => {
    loginAs('member')
    const code = await apiErrorCode(fn())
    expect(code, `【接口】会员${label}应返回 40300`).toBe(40300)
  })

  it('未登录调用写接口返回 401（页面请求带 Authorization 但会话失效场景）', async () => {
    // 不注入会话
    const code = await apiErrorCode(reportRepair(1, '未登录'))
    expect(code, '【接口】未登录登记应返回 40100').toBe(40100)
  })
})

describe('P4 页面侧可复现的接口拒绝（重复登记/非法状态/非法枚举）', () => {
  beforeEach(() => {
    loginAs('staff')
  })

  it('同一机位重复登记返回 40010', async () => {
    expect(await apiErrorCode(reportRepair(1, '第一次故障')), '【接口】首次登记应成功').toBe(0)
    const code = await apiErrorCode(reportRepair(1, '重复登记'))
    expect(code, '【接口】重复登记应返回 40010').toBe(40010)
    const { repairs } = dbState(1)
    expect(repairs.filter((r) => r.status === 'pending').length, '【接口】拒绝重复后仍仅 1 条待处理').toBe(1)
  })

  it.each([
    [2, '使用中机位'],
    [3, '已预约机位'],
  ])('%s登记返回 40900', async (stationID, label) => {
    const code = await apiErrorCode(reportRepair(stationID, '故障'))
    expect(code, `【接口】${label}登记应返回 40900`).toBe(40900)
    expect(dbState(stationID).repairs.length, `【接口】${label}被拒后不应落报修单`).toBe(0)
  })

  it('旧状态接口非法枚举返回 42200，故障/恢复旁路返回 40010/40011', async () => {
    expect(await apiErrorCode(updateStationStatus(1, 'broken' as unknown as string)), '【接口】非法状态枚举应 42200').toBe(42200)
    expect(await apiErrorCode(updateStationStatus(1, 'fault')), '【接口】旧接口标记故障应 40010').toBe(40010)
    await reportRepair(1, '鼠标失灵')
    expect(await apiErrorCode(updateStationStatus(1, 'idle')), '【接口】旧接口故障恢复应 40011').toBe(40011)
  })

  it('无报修单故障机位经正常补录接口可恢复（与页面 P2 同规则）', async () => {
    const code = await apiErrorCode(closeRepair(4, '补录恢复'))
    expect(code, '【接口】遗留故障恢复应成功 code=0').toBe(0)
    expect(dbState(4).station?.status, '【接口】机位应 idle').toBe('idle')
  })
})

describe('P5 报修管理页 Repairs.vue（页面）', () => {
  it('管理员可见正常记录与补录记录的原因/结果/角色/时间', async () => {
    loginAs('admin')
    // 自建数据：1 号机位正常闭环；4 号机位遗留补录
    await reportRepair(1, '正常故障：显示器花屏')
    await closeRepair(1, '正常处理：更换视频线')
    await closeRepair(4, '遗留处理：紧固主板')

    await mountPage(Repairs, '/stations/repairs')

    pageMustContain('正常故障：显示器花屏', '正常记录原因')
    pageMustContain('正常处理：更换视频线', '正常记录结果')
    pageMustContain('遗留处理：紧固主板', '补录记录结果')
    pageMustContain('历史故障遗留', '补录记录原因留痕')
    pageMustContain('已关闭', '管理页记录状态')
    pageMustContain('登记：管理员 admin', '正常记录登记角色')
    pageMustContain('处理：管理员 admin', '处理角色与用户名')
    const anyTime = findByText(document.body, '.van-cell', FIRST_TIME)
    expect(anyTime, `【页面】管理页应可见时间（如 ${FIRST_TIME}）`).toBeTruthy()
  })
})
