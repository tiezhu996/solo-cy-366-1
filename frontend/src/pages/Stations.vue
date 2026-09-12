<template>
  <div class="stations-page">
    <van-search v-model="area" placeholder="按区域搜索，如 A区" @search="load" />
    <van-dropdown-menu>
      <van-dropdown-item v-model="status" :options="statusOptions" @change="load" />
    </van-dropdown-menu>
    <div v-if="isStaffOrAdmin" class="repair-entry">
      <van-button size="small" plain type="primary" icon="orders-o" @click="router.push('/stations/repairs')">报修记录</van-button>
    </div>

    <van-cell-group inset title="机位列表">
      <van-cell v-for="st in list" :key="st.id" :title="`${st.name}`" :label="`${st.area} · ${STATION_TYPE_TEXT[st.station_type]} · ¥${st.price_per_hour}/小时`" is-link @click="openDetail(st)">
        <template #value>
          <StatusBadge kind="station" :status="st.status" />
        </template>
      </van-cell>
    </van-cell-group>
    <van-pagination v-model="page" :total-items="total" :items-per-page="pageSize" @change="load" />

    <!-- 机位详情（含报修闭环：原因/结果/操作角色/时间） -->
    <van-popup v-model:show="showDetail" position="bottom" round :style="{ maxHeight: '80%' }">
      <div class="detail-wrap">
        <div class="detail-title">
          <span>{{ detail?.station.name || selected?.name }}</span>
          <StatusBadge v-if="detail" kind="station" :status="detail.station.status" />
        </div>
        <van-cell-group inset v-if="detail">
          <van-cell title="区域" :value="detail.station.area" />
          <van-cell title="类型" :value="STATION_TYPE_TEXT[detail.station.station_type] || detail.station.station_type" />
          <van-cell title="时价" :value="`¥${detail.station.price_per_hour}/小时`" />
          <van-cell v-if="detail.station.description" title="备注" :label="detail.station.description" />
        </van-cell-group>

        <!-- 当前待处理报修 -->
        <div v-if="detail?.open_repair" class="repair-block">
          <div class="block-head">
            <span class="block-title">当前报修</span>
            <StatusBadge kind="repair" :status="detail.open_repair.status" />
          </div>
          <van-cell-group inset>
            <van-cell title="故障原因" :label="detail.open_repair.reason" />
            <van-cell title="登记人" :value="`${ROLE_TEXT[detail.open_repair.report_role] || detail.open_repair.report_role} · ${detail.open_repair.report_by_name}`" />
            <van-cell title="登记时间" :value="formatTime(detail.open_repair.reported_at)" />
          </van-cell-group>
        </div>
        <van-empty v-else-if="detail && detail.station.status === STATION_STATUS.FAULT" description="故障机位缺少待处理报修记录，请联系管理员" image="error" />

        <!-- 报修历史（最近记录） -->
        <div v-if="detail && detail.repair_records.length" class="repair-block">
          <div class="block-head"><span class="block-title">报修记录</span></div>
          <van-cell-group inset>
            <van-cell
              v-for="r in detail.repair_records"
              :key="r.id"
              :title="`#${r.id} ${r.reason}`"
              :label="repairLabel(r)"
            >
              <template #value><StatusBadge kind="repair" :status="r.status" /></template>
            </van-cell>
          </van-cell-group>
        </div>

        <!-- 管理操作：原有状态操作保持可用，故障/恢复走报修闭环 -->
        <div v-if="isStaffOrAdmin && detail" class="detail-actions">
          <van-button
            v-if="detail.station.status === STATION_STATUS.IDLE"
            type="danger" size="small" plain
            @click="openReport"
          >标记故障并登记报修</van-button>
          <van-button
            v-if="detail.station.status === STATION_STATUS.FAULT && detail.open_repair"
            type="primary" size="small"
            @click="openClose"
          >恢复空闲（关闭报修）</van-button>
          <van-button
            v-if="detail.station.status === STATION_STATUS.RESERVED"
            type="primary" size="small" plain
            @click="markIdle"
          >标记为空闲</van-button>
          <van-button type="default" size="small" plain @click="openEdit">编辑机位</van-button>
          <van-button type="danger" size="small" @click="removeStation">删除机位</van-button>
        </div>
      </div>
    </van-popup>

    <!-- 标记故障：填写报修原因 -->
    <van-popup v-model:show="showReport" position="bottom" round>
      <div class="form-wrap">
        <h3>标记故障 · 登记报修</h3>
        <van-field
          v-model="reason"
          type="textarea"
          rows="3"
          maxlength="500"
          show-word-limit
          label="故障原因"
          placeholder="请填写故障现象/原因（必填）"
        />
        <div class="form-btns">
          <van-button block plain @click="showReport = false">取消</van-button>
          <van-button block type="danger" :loading="submitting" @click="submitReport">确认登记</van-button>
        </div>
      </div>
    </van-popup>

    <!-- 恢复空闲：填写处理结果 -->
    <van-popup v-model:show="showClose" position="bottom" round>
      <div class="form-wrap">
        <h3>恢复空闲 · 关闭报修</h3>
        <van-field
          v-model="handleResult"
          type="textarea"
          rows="3"
          maxlength="500"
          show-word-limit
          label="处理结果"
          placeholder="请填写维修/处理结果（必填）"
        />
        <div class="form-btns">
          <van-button block plain @click="showClose = false">取消</van-button>
          <van-button block type="primary" :loading="submitting" @click="submitClose">确认恢复</van-button>
        </div>
      </div>
    </van-popup>

    <van-dialog v-model:show="showForm" :title="formTitle" show-cancel-button @confirm="submitForm">
      <van-form>
        <van-cell-group inset>
          <van-field v-model="form.name" label="名称" placeholder="如 A区-01" />
          <van-field v-model="form.area" label="区域" placeholder="A区/B区/包厢区" />
          <van-field v-model="form.station_type" label="类型" placeholder="seat 或 box" />
          <van-field v-model="form.price_per_hour" type="number" label="时价(元)" />
        </van-cell-group>
      </van-form>
    </van-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref, computed } from 'vue'
import { useRouter } from 'vue-router'
import { showSuccessToast, showConfirmDialog, showFailToast } from 'vant'
import StatusBadge from '@/components/StatusBadge.vue'
import { listStations, createStation, updateStation, deleteStation, updateStationStatus, type Station } from '@/api/station'
import { getStationDetail, reportRepair, closeRepair, type StationDetail, type RepairRecord } from '@/api/repair'
import { STATION_STATUS, STATION_TYPE_TEXT, USER_ROLE_TEXT as ROLE_TEXT } from '@/constants'
import { formatTime } from '@/utils/format'
import { useAuth } from '@/hooks/useAuth'

const { isStaffOrAdmin } = useAuth()
const router = useRouter()
const list = ref<Station[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 10
const area = ref('')
const status = ref('')
const statusOptions = [
  { text: '全部状态', value: '' },
  { text: '空闲', value: 'idle' },
  { text: '使用中', value: 'using' },
  { text: '故障', value: 'fault' },
  { text: '已预约', value: 'reserved' },
]

const selected = ref<Station | null>(null)
const detail = ref<StationDetail | null>(null)
const showDetail = ref(false)
const showForm = ref(false)
const editingId = ref<number | null>(null)
const form = ref({ name: '', area: '', station_type: 'seat', price_per_hour: 8 })
const formTitle = computed(() => (editingId.value ? '编辑机位' : '新增机位'))

// 报修闭环表单
const showReport = ref(false)
const showClose = ref(false)
const reason = ref('')
const handleResult = ref('')
const submitting = ref(false)

async function load() {
  try {
    const data = await listStations({ page: page.value, page_size: pageSize, area: area.value || undefined, status: status.value || undefined })
    list.value = data.list
    total.value = data.total
  } catch { /* toast 已处理 */ }
}

async function openDetail(st: Station) {
  selected.value = st
  detail.value = null
  showDetail.value = true
  try {
    detail.value = await getStationDetail(st.id)
  } catch { /* toast 已处理 */ }
}

// 报修记录副文本：登记角色/时间 + 处理结果/角色/时间
function repairLabel(r: RepairRecord): string {
  const reportRole = ROLE_TEXT[r.report_role] || r.report_role
  let label = `登记：${reportRole} ${r.report_by_name} · ${formatTime(r.reported_at)}`
  if (r.status === 'closed') {
    const handleRole = ROLE_TEXT[r.handle_role] || r.handle_role
    label += `\n结果：${r.handle_result}\n处理：${handleRole} ${r.handle_by_name} · ${formatTime(r.handled_at)}`
  }
  return label
}

function openReport() {
  reason.value = ''
  showReport.value = true
}

function openClose() {
  handleResult.value = ''
  showClose.value = true
}

async function submitReport() {
  if (!selected.value) return
  if (!reason.value.trim()) {
    showFailToast('请填写故障原因')
    return
  }
  submitting.value = true
  try {
    await reportRepair(selected.value.id, reason.value.trim())
    showSuccessToast('报修登记成功，机位已标记故障')
    showReport.value = false
    await refreshDetail()
  } catch { /* toast 已处理 */ } finally {
    submitting.value = false
  }
}

async function submitClose() {
  if (!selected.value) return
  if (!handleResult.value.trim()) {
    showFailToast('请填写处理结果')
    return
  }
  submitting.value = true
  try {
    await closeRepair(selected.value.id, handleResult.value.trim())
    showSuccessToast('报修已关闭，机位恢复空闲')
    showClose.value = false
    await refreshDetail()
  } catch { /* toast 已处理 */ } finally {
    submitting.value = false
  }
}

async function markIdle() {
  if (!selected.value) return
  try {
    await updateStationStatus(selected.value.id, STATION_STATUS.IDLE)
    showSuccessToast('状态已更新')
    await refreshDetail()
  } catch { /* toast 已处理 */ }
}

async function refreshDetail() {
  if (selected.value) {
    detail.value = await getStationDetail(selected.value.id)
    selected.value = detail.value.station
  }
  await load()
}

function openEdit() {
  const st = detail.value?.station
  if (!st) return
  editingId.value = st.id
  form.value = { name: st.name, area: st.area, station_type: st.station_type, price_per_hour: st.price_per_hour }
  showDetail.value = false
  showForm.value = true
}

async function removeStation() {
  const st = detail.value?.station
  if (!st) return
  try {
    await showConfirmDialog({ title: '删除机位', message: `确定删除机位「${st.name}」吗？` })
    await deleteStation(st.id)
    showSuccessToast('删除成功')
    showDetail.value = false
    load()
  } catch { /* 取消 */ }
}

async function submitForm() {
  const payload = { ...form.value }
  if (editingId.value) {
    await updateStation(editingId.value, payload)
    showSuccessToast('更新成功')
  } else {
    await createStation(payload)
    showSuccessToast('创建成功')
  }
  load()
}

onMounted(load)
</script>

<style scoped>
.repair-entry {
  padding: 4px 16px 8px;
}
/* 报修记录的原因/结果/角色/时间按行展示 */
:deep(.van-cell__label) {
  white-space: pre-line;
}
.detail-wrap {
  padding: 16px 0 24px;
}
.detail-title {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 24px 12px;
  font-size: 17px;
  font-weight: 600;
}
.repair-block {
  margin-top: 12px;
}
.block-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 24px 6px;
}
.block-title {
  font-size: 14px;
  font-weight: 600;
  color: #323233;
}
.detail-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  padding: 16px 24px 0;
}
.form-wrap {
  padding: 16px 0 24px;
}
.form-wrap h3 {
  margin: 0 16px 12px;
  font-size: 16px;
}
.form-btns {
  display: flex;
  gap: 12px;
  padding: 16px;
}
</style>
