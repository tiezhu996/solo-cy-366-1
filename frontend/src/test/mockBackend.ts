/**
 * 机位报修页面测试的内存假后端（axios adapter）。
 *
 * 实现与后端 internal/service/repair_service.go、station_service.go 相同的业务规则，
 * 页面测试无需真实服务即可覆盖：正常报修闭环、历史遗留故障补录恢复、会员只读/RBAC、
 * 重复登记/非法状态拒绝。数据全部驻留内存，resetMockDB() 后恢复初始种子，
 * 时间戳使用固定 UTC 时钟，保证用例可连续反复运行。
 *
 * 失败归属：适配器返回与真实后端一致的 {code,message}，并记录请求日志（含 HTTP 状态），
 * 接口类断言失败可与页面渲染类断言区分。
 */

export interface StationRow {
  id: number
  name: string
  area: string
  station_type: string
  price_per_hour: number
  status: string
  description: string
}

export interface RepairRow {
  id: number
  station_id: number
  reason: string
  status: string // pending / closed
  report_user_id: number
  report_by_name: string
  report_role: string
  reported_at: string
  handle_result: string
  handle_user_id: number
  handle_by_name: string
  handle_role: string
  handled_at: string | null
  created_at: string
  updated_at: string
}

interface UserRow {
  id: number
  username: string
  role: string
  nickname: string
}

export interface MockRequestLog {
  method: string
  url: string
  status: number
  code: number
  role: string
}

interface DB {
  stations: StationRow[]
  repairs: RepairRow[]
  users: UserRow[]
  seq: number
  repairID: number
  clock: number // 自固定基准起的秒数
}

const LEGACY_REASON = '历史故障遗留（无待处理报修登记）'

// 与后端错误码一致（internal/constants/error_codes.go）
const CODE = {
  OK: 0,
  UNAUTHORIZED: 40100,
  FORBIDDEN: 40300,
  NOT_FOUND: 40400,
  CONFLICT: 40900,
  VALIDATION: 42200,
  REPAIR_OPEN: 40010,
  REPAIR_NONE: 40011,
}

let db: DB

function freshDB(): DB {
  return {
    seq: 0,
    repairID: 0,
    clock: 0,
    users: [
      { id: 101, username: 'admin', role: 'admin', nickname: '管理员' },
      { id: 202, username: 'clerk', role: 'staff', nickname: '店员' },
      { id: 303, username: 'gamer', role: 'member', nickname: '会员' },
    ],
    stations: [
      { id: 1, name: 'A区-01', area: 'A区', station_type: 'seat', price_per_hour: 8, status: 'idle', description: '空闲机位' },
      { id: 2, name: 'A区-02', area: 'A区', station_type: 'seat', price_per_hour: 8, status: 'using', description: '使用中机位' },
      { id: 3, name: 'B区-01', area: 'B区', station_type: 'seat', price_per_hour: 8, status: 'reserved', description: '已预约机位' },
      // 历史遗留故障：状态 fault 但没有任何报修单（用于复现卡死缺陷的页面场景）
      { id: 4, name: 'B区-02', area: 'B区', station_type: 'seat', price_per_hour: 10, status: 'fault', description: '遗留故障机位' },
    ],
    repairs: [],
  }
}

/** 重置为初始种子数据，保证可连续运行。 */
export function resetMockDB(): void {
  db = freshDB()
  requestLog.length = 0
}

/** 直接读取内部状态（测试用于自建数据/精准断言）。 */
export function mockDB(): DB {
  return db
}

const requestLog: MockRequestLog[] = []
export function mockRequests(): readonly MockRequestLog[] {
  return requestLog
}

// 固定基准时钟：2026-01-01 08:00 UTC，每次写操作递增 5 秒，断言稳定。
function nowISO(): string {
  db.clock += 5
  const d = new Date(Date.UTC(2026, 0, 1, 8, 0, db.clock))
  return d.toISOString()
}

const TOKEN_USER: Record<string, UserRow> = {
  'token-admin': { id: 101, username: 'admin', role: 'admin', nickname: '管理员' },
  'token-staff': { id: 202, username: 'clerk', role: 'staff', nickname: '店员' },
  'token-member': { id: 303, username: 'gamer', role: 'member', nickname: '会员' },
}

function openRepair(stationID: number): RepairRow | undefined {
  return db.repairs.find((r) => r.station_id === stationID && r.status === 'pending')
}

function historyOf(stationID: number): RepairRow[] {
  return db.repairs
    .filter((r) => r.station_id === stationID)
    .sort((a, b) => b.id - a.id)
    .slice(0, 20)
}

class MockHttpError extends Error {
  response: { status: number; data: { code: number; message: string } }
  constructor(status: number, code: number, message: string) {
    super(message)
    this.response = { status, data: { code, message } }
  }
}

function logAnd(status: number, code: number, role: string, method: string, url: string): void {
  requestLog.push({ method, url, status, code, role })
}

function ok(data: unknown) {
  return {
    data: { code: CODE.OK, message: 'ok', data },
    status: 200,
    statusText: 'OK',
    headers: {},
  }
}

function fail(status: number, code: number, message: string): Promise<never> {
  return Promise.reject(new MockHttpError(status, code, message))
}

/** 机位状态机（与后端 allowedStationTransition 对齐）。 */
function transitionAllowed(from: string, to: string): boolean {
  if (from === to) return true
  switch (from) {
    case 'idle':
      return ['fault', 'reserved', 'using'].includes(to)
    case 'fault':
      return to === 'idle'
    case 'reserved':
      return ['idle', 'using'].includes(to)
    case 'using':
      return to === 'idle'
  }
  return false
}

export const mockAdapter = async (config: any) => {
  const method: string = (config.method || 'get').toLowerCase()
  const url: string = config.url || ''
  const body = config.data ? JSON.parse(config.data) : {}
  const params = config.params || {}

  // 认证
  const headers = config.headers || {}
  const auth = typeof headers.get === 'function' ? headers.get('Authorization') : headers.Authorization
  const token = typeof auth === 'string' && auth.startsWith('Bearer ') ? auth.slice(7) : ''
  const user = TOKEN_USER[token]
  const role = user?.role || ''
  const done = (status: number, code: number) => logAnd(status, code, role, method.toUpperCase(), url)

  // GET /stations（分页 + area/status 筛选）
  if (method === 'get' && url === '/stations') {
    if (!user) { done(401, CODE.UNAUTHORIZED); return fail(401, CODE.UNAUTHORIZED, '未登录或登录已过期') }
    let rows = db.stations
    if (params.area) rows = rows.filter((s) => s.area === params.area)
    if (params.status) rows = rows.filter((s) => s.status === params.status)
    const page = Number(params.page || 1)
    const pageSize = Number(params.page_size || 10)
    const total = rows.length
    const list = rows.slice((page - 1) * pageSize, page * pageSize)
    done(200, CODE.OK)
    return ok({ list, total, page, page_size: pageSize })
  }

  // GET /stations/all
  if (method === 'get' && url === '/stations/all') {
    if (!user) { done(401, CODE.UNAUTHORIZED); return fail(401, CODE.UNAUTHORIZED, '未登录或登录已过期') }
    done(200, CODE.OK)
    return ok(db.stations)
  }

  // GET /stations/:id/detail
  let m = url.match(/^\/stations\/(\d+)\/detail$/)
  if (method === 'get' && m) {
    if (!user) { done(401, CODE.UNAUTHORIZED); return fail(401, CODE.UNAUTHORIZED, '未登录') }
    const st = db.stations.find((s) => s.id === Number(m![1]))
    if (!st) { done(404, CODE.NOT_FOUND); return fail(404, CODE.NOT_FOUND, '机位不存在') }
    done(200, CODE.OK)
    return ok({ station: st, open_repair: openRepair(st.id) ?? null, repair_records: historyOf(st.id) })
  }

  // GET /stations/:id/repairs
  m = url.match(/^\/stations\/(\d+)\/repairs$/)
  if (method === 'get' && m) {
    if (!user) { done(401, CODE.UNAUTHORIZED); return fail(401, CODE.UNAUTHORIZED, '未登录') }
    done(200, CODE.OK)
    return ok(historyOf(Number(m[1])))
  }

  // POST /stations/:id/repair-report
  m = url.match(/^\/stations\/(\d+)\/repair-report$/)
  if (method === 'post' && m) {
    const stationID = Number(m[1])
    if (!user) { done(401, CODE.UNAUTHORIZED); return fail(401, CODE.UNAUTHORIZED, '未登录或登录已过期') }
    if (role !== 'admin' && role !== 'staff') { done(403, CODE.FORBIDDEN); return fail(403, CODE.FORBIDDEN, '没有操作权限，当前角色不允许该操作') }
    const reason = String(body.reason ?? '').trim()
    if (!reason) { done(400, CODE.VALIDATION); return fail(400, CODE.VALIDATION, '登记报修参数校验失败：原因为必填') }
    const st = db.stations.find((s) => s.id === stationID)
    if (!st) { done(404, CODE.NOT_FOUND); return fail(404, CODE.NOT_FOUND, '机位不存在，无法登记报修') }
    if (st.status === 'using' || st.status === 'reserved') {
      done(409, CODE.CONFLICT)
      return fail(409, CODE.CONFLICT, `机位当前为「${st.status}」，仅空闲机位可以标记故障报修`)
    }
    if (st.status === 'fault' && openRepair(stationID)) {
      done(409, CODE.REPAIR_OPEN)
      return fail(409, CODE.REPAIR_OPEN, '该机位已有待处理报修记录，请勿重复登记')
    }
    const ts = nowISO()
    db.repairID += 1
    const rec: RepairRow = {
      id: db.repairID, station_id: stationID, reason, status: 'pending',
      report_user_id: user.id, report_by_name: user.username, report_role: user.role, reported_at: ts,
      handle_result: '', handle_user_id: 0, handle_by_name: '', handle_role: '', handled_at: null,
      created_at: ts, updated_at: ts,
    }
    db.repairs.push(rec)
    st.status = 'fault'
    done(200, CODE.OK)
    return ok({ repair: rec, station: st })
  }

  // POST /stations/:id/repair-close
  m = url.match(/^\/stations\/(\d+)\/repair-close$/)
  if (method === 'post' && m) {
    const stationID = Number(m[1])
    if (!user) { done(401, CODE.UNAUTHORIZED); return fail(401, CODE.UNAUTHORIZED, '未登录或登录已过期') }
    if (role !== 'admin' && role !== 'staff') { done(403, CODE.FORBIDDEN); return fail(403, CODE.FORBIDDEN, '没有操作权限，当前角色不允许该操作') }
    const result = String(body.handle_result ?? '').trim()
    if (!result) { done(400, CODE.VALIDATION); return fail(400, CODE.VALIDATION, '处理结果不能为空') }
    const st = db.stations.find((s) => s.id === stationID)
    if (!st) { done(404, CODE.NOT_FOUND); return fail(404, CODE.NOT_FOUND, '机位不存在，无法关闭报修') }
    if (st.status !== 'fault') {
      done(409, CODE.CONFLICT)
      return fail(409, CODE.CONFLICT, `机位当前为「${st.status}」不是故障状态，不能关闭报修`)
    }
    const ts = nowISO()
    db.repairID += 1
    const open = openRepair(stationID)
    let rec: RepairRow
    let legacy = false
    if (open) {
      open.status = 'closed'
      open.handle_result = result
      open.handle_user_id = user.id
      open.handle_by_name = user.username
      open.handle_role = user.role
      open.handled_at = ts
      open.updated_at = ts
      rec = open
    } else {
      // 历史遗留故障：无待处理单，补录一条已关闭记录
      legacy = true
      rec = {
        id: db.repairID, station_id: stationID, reason: LEGACY_REASON, status: 'closed',
        report_user_id: 0, report_by_name: '', report_role: '', reported_at: ts,
        handle_result: result, handle_user_id: user.id, handle_by_name: user.username,
        handle_role: user.role, handled_at: ts, created_at: ts, updated_at: ts,
      }
      db.repairs.push(rec)
    }
    st.status = 'idle'
    done(200, CODE.OK)
    return ok({ repair: rec, station: st, legacy })
  }

  // PUT /stations/:id/status（旧状态接口：故障/恢复均不得绕过报修闭环）
  m = url.match(/^\/stations\/(\d+)\/status$/)
  if (method === 'put' && m) {
    const stationID = Number(m[1])
    if (!user) { done(401, CODE.UNAUTHORIZED); return fail(401, CODE.UNAUTHORIZED, '未登录或登录已过期') }
    if (role !== 'admin' && role !== 'staff') { done(403, CODE.FORBIDDEN); return fail(403, CODE.FORBIDDEN, '没有操作权限') }
    const target = String(body.status ?? '')
    if (!['idle', 'using', 'fault', 'reserved'].includes(target)) {
      done(400, CODE.VALIDATION)
      return fail(400, CODE.VALIDATION, '机位状态参数校验失败')
    }
    const st = db.stations.find((s) => s.id === stationID)
    if (!st) { done(404, CODE.NOT_FOUND); return fail(404, CODE.NOT_FOUND, '机位不存在') }
    if (st.status === 'fault' && target === 'idle') {
      done(409, CODE.REPAIR_NONE)
      return fail(409, CODE.REPAIR_NONE, '故障机位恢复空闲必须填写处理结果并关闭报修')
    }
    if (target === 'fault') {
      done(409, CODE.REPAIR_OPEN)
      return fail(409, CODE.REPAIR_OPEN, '标记故障必须填写报修原因，请使用报修登记操作')
    }
    if (!transitionAllowed(st.status, target)) {
      done(409, CODE.CONFLICT)
      return fail(409, CODE.CONFLICT, '机位状态不允许该变更')
    }
    st.status = target
    done(200, CODE.OK)
    return ok(st)
  }

  // DELETE /stations/:id
  m = url.match(/^\/stations\/(\d+)$/)
  if (method === 'delete' && m) {
    if (!user) { done(401, CODE.UNAUTHORIZED); return fail(401, CODE.UNAUTHORIZED, '未登录') }
    if (role !== 'admin') { done(403, CODE.FORBIDDEN); return fail(403, CODE.FORBIDDEN, '没有操作权限') }
    const stationID = Number(m[1])
    db.stations = db.stations.filter((s) => s.id !== stationID)
    done(200, CODE.OK)
    return ok(null)
  }

  // GET /repairs（管理列表，仅 admin/staff）
  if (method === 'get' && url === '/repairs') {
    if (!user) { done(401, CODE.UNAUTHORIZED); return fail(401, CODE.UNAUTHORIZED, '未登录或登录已过期') }
    if (role !== 'admin' && role !== 'staff') { done(403, CODE.FORBIDDEN); return fail(403, CODE.FORBIDDEN, '没有操作权限') }
    let rows = [...db.repairs].sort((a, b) => b.id - a.id)
    if (params.status) rows = rows.filter((r) => r.status === params.status)
    if (params.station_id) rows = rows.filter((r) => r.station_id === Number(params.station_id))
    const total = rows.length
    const page = Number(params.page || 1)
    const pageSize = Number(params.page_size || 10)
    done(200, CODE.OK)
    return ok({ list: rows.slice((page - 1) * pageSize, page * pageSize), total, page, page_size: pageSize })
  }

  // GET /repairs/:id
  m = url.match(/^\/repairs\/(\d+)$/)
  if (method === 'get' && m) {
    if (!user) { done(401, CODE.UNAUTHORIZED); return fail(401, CODE.UNAUTHORIZED, '未登录') }
    const rec = db.repairs.find((r) => r.id === Number(m![1]))
    if (!rec) { done(404, CODE.NOT_FOUND); return fail(404, CODE.NOT_FOUND, '报修记录不存在') }
    done(200, CODE.OK)
    return ok(rec)
  }

  return fail(404, CODE.NOT_FOUND, `mock backend: 未实现的接口 ${method.toUpperCase()} ${url}`)
}

resetMockDB()
