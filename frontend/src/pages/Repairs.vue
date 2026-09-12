<template>
  <div class="repairs-page">
    <van-dropdown-menu>
      <van-dropdown-item v-model="status" :options="statusOptions" @change="load" />
    </van-dropdown-menu>
    <van-cell-group inset title="报修记录">
      <van-cell
        v-for="r in list"
        :key="r.id"
        :title="`#${r.id} 机位 ${r.station_id} · ${r.reason}`"
        :label="repairLabel(r)"
      >
        <template #value><StatusBadge kind="repair" :status="r.status" /></template>
      </van-cell>
    </van-cell-group>
    <EmptyState v-if="!list.length" description="暂无报修记录" />
    <van-pagination v-model="page" :total-items="total" :items-per-page="pageSize" @change="load" />
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import EmptyState from '@/components/EmptyState.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { listRepairs, type RepairRecord } from '@/api/repair'
import { USER_ROLE_TEXT } from '@/constants'
import { formatTime } from '@/utils/format'

const list = ref<RepairRecord[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 10
const status = ref('')
const statusOptions = [
  { text: '全部', value: '' },
  { text: '待处理', value: 'pending' },
  { text: '已关闭', value: 'closed' },
]

async function load() {
  try {
    const data = await listRepairs({ page: page.value, page_size: pageSize, status: status.value || undefined })
    list.value = data.list
    total.value = data.total
  } catch { /* toast 已处理 */ }
}

function repairLabel(r: RepairRecord): string {
  const reportRole = USER_ROLE_TEXT[r.report_role] || r.report_role
  let label = `登记：${reportRole} ${r.report_by_name} · ${formatTime(r.reported_at)}`
  if (r.status === 'closed') {
    const handleRole = USER_ROLE_TEXT[r.handle_role] || r.handle_role
    label += `\n结果：${r.handle_result}\n处理：${handleRole} ${r.handle_by_name} · ${formatTime(r.handled_at)}`
  }
  return label
}

onMounted(load)
</script>

<style scoped>
:deep(.van-cell__label) {
  white-space: pre-line;
}
</style>
