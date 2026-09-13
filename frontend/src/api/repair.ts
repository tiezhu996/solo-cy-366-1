import { get, post } from '@/utils/request'

// 与后端 model.RepairRecord 对应：机位报修闭环记录
export interface RepairRecord {
  id: number
  station_id: number
  reason: string
  status: string // pending 待处理 / closed 已关闭
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

// 机位详情：机位信息 + 当前待处理报修 + 历史报修（后端 dto.StationDetailResp）
export interface StationDetail {
  station: import('./station').Station
  open_repair: RepairRecord | null
  repair_records: RepairRecord[]
}

// 标记故障并登记报修（管理员/店员）
export function reportRepair(stationId: number, reason: string) {
  return post<{ repair: RepairRecord; station: import('./station').Station }>(
    `/stations/${stationId}/repair-report`,
    { reason },
  )
}

// 恢复空闲并关闭报修（管理员/店员）。
// legacy=true 表示该机位故障但无待处理报修单（历史遗留），后端已补录一条已关闭记录。
export function closeRepair(stationId: number, handleResult: string) {
  return post<{ repair: RepairRecord; station: import('./station').Station; legacy?: boolean }>(
    `/stations/${stationId}/repair-close`,
    { handle_result: handleResult },
  )
}

// 机位详情（含原因/结果/操作角色/时间）
export function getStationDetail(stationId: number) {
  return get<StationDetail>(`/stations/${stationId}/detail`)
}

// 机位报修历史
export function listStationRepairs(stationId: number) {
  return get<RepairRecord[]>(`/stations/${stationId}/repairs`)
}

// 报修记录分页列表（管理员/店员）
export function listRepairs(params: { page: number; page_size: number; station_id?: number; status?: string }) {
  return get<{ list: RepairRecord[]; total: number; page: number; page_size: number }>('/repairs', params)
}
