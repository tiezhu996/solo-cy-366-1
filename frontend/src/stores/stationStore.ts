import { defineStore } from 'pinia'
import { ref } from 'vue'
import { listAllStations, updateStationStatus, type Station } from '@/api/station'
import { reportRepair, closeRepair } from '@/api/repair'

export const useStationStore = defineStore('station', () => {
  const stations = ref<Station[]>([])

  async function loadAll() {
    stations.value = await listAllStations()
  }

  async function changeStatus(id: number, status: string) {
    const updated = await updateStationStatus(id, status)
    const idx = stations.value.findIndex((s) => s.id === id)
    if (idx >= 0) {
      stations.value[idx] = updated
    }
    return updated
  }

  // 标记故障并登记报修（故障原因必填）
  async function reportFault(id: number, reason: string) {
    const { station } = await reportRepair(id, reason)
    const idx = stations.value.findIndex((s) => s.id === id)
    if (idx >= 0) stations.value[idx] = station
    return station
  }

  // 恢复空闲并关闭报修（处理结果必填）
  async function recoverIdle(id: number, handleResult: string) {
    const { station } = await closeRepair(id, handleResult)
    const idx = stations.value.findIndex((s) => s.id === id)
    if (idx >= 0) stations.value[idx] = station
    return station
  }

  return { stations, loadAll, changeStatus, reportFault, recoverIdle }
})
