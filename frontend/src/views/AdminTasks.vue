<template>
  <a-layout style="min-height: 100vh">
    <admin-sider value="tasks" />
    <a-layout>
      <a-layout-header :style="{ background: '#fff', padding: 0 }">
        <a-page-header :title="$t('adminTasks.title')" :sub-title="$t('adminTasks.subtitle')" />
      </a-layout-header>
      <a-layout-content :style="{ margin: '24px 16px 0' }">
        <div :style="{ padding: '24px', background: '#fff' }">
          <a-alert
            type="info"
            show-icon
            :message="$t('adminTasks.retention')"
            style="margin-bottom: 16px"
          />
          <a-descriptions
            v-if="storage"
            :title="$t('adminTasks.storage')"
            size="small"
            bordered
            :column="3"
            style="margin-bottom: 16px"
          >
            <a-descriptions-item
              v-for="key in storageFields"
              :key="key"
              :label="$t(`adminTasks.storageLabels.${key}`)"
              >{{ storage[key] ?? '—' }}</a-descriptions-item
            >
            <a-descriptions-item :label="$t('adminTasks.pendingDevices')">{{
              pendingDevices
            }}</a-descriptions-item>
          </a-descriptions>
          <a-space style="margin-bottom: 16px">
            <a-button :loading="loading" @click="refresh">{{ $t('adminTasks.refresh') }}</a-button>
            <span>{{ $t('adminTasks.autoRefresh') }}</span>
          </a-space>
          <a-alert
            v-if="error"
            type="error"
            show-icon
            :message="error"
            style="margin-bottom: 16px"
          />
          <a-table
            :columns="columns"
            :data-source="tasks"
            row-key="name"
            :pagination="false"
            :scroll="{ x: 1500 }"
          >
            <template #bodyCell="{ column, record }">
              <template v-if="column.key === 'name'">
                <strong>{{ taskName(record.name) }}</strong>
                <div>
                  {{
                    record.manualOnly
                      ? $t('adminTasks.manualOnly')
                      : $t('adminTasks.every', { seconds: record.intervalSeconds })
                  }}
                </div>
              </template>
              <template v-else-if="column.key === 'state'">
                <a-tag
                  :color="
                    record.running
                      ? 'processing'
                      : record.lastError
                        ? 'error'
                        : record.runs
                          ? 'success'
                          : 'default'
                  "
                >
                  {{
                    $t(
                      `adminTasks.${record.running ? 'running' : record.lastError ? 'failed' : record.runs ? 'ready' : 'pending'}`
                    )
                  }}
                </a-tag>
              </template>
              <template v-else-if="column.key === 'action'">
                <a-popconfirm
                  :title="
                    $t(
                      record.name === 'clean-inactive-users'
                        ? 'adminTasks.confirmInactive'
                        : 'adminTasks.confirmRun'
                    )
                  "
                  @confirm="run(record.name)"
                >
                  <a-button
                    size="small"
                    :disabled="record.running || submitting === record.name"
                    :loading="submitting === record.name"
                  >
                    {{ $t('adminTasks.run') }}
                  </a-button>
                </a-popconfirm>
              </template>
              <template v-else-if="column.key === 'runs'"
                >{{ record.runs }} / {{ record.failures }}</template
              >
              <template v-else-if="column.key === 'rows'"
                >{{ record.lastRowsAffected }} / {{ record.totalRowsAffected }}</template
              >
              <template v-else-if="column.key === 'duration'"
                >{{ record.lastDurationMs }} ms</template
              >
              <template v-else-if="column.key === 'error'">{{ record.lastError || '—' }}</template>
              <template v-else>{{ formatTime(record[column.key]) }}</template>
            </template>
            <template #expandedRowRender="{ record }">
              <a-descriptions :title="$t('adminTasks.details')" size="small" bordered :column="2">
                <a-descriptions-item
                  v-for="(value, key) in record.details"
                  :key="key"
                  :label="detailName(key)"
                  >{{ value }}</a-descriptions-item
                >
              </a-descriptions>
            </template>
          </a-table>
        </div>
      </a-layout-content>
      <footer-view />
    </a-layout>
  </a-layout>
</template>

<script setup>
import axios from 'axios'
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { message } from 'ant-design-vue'
import { useI18n } from 'vue-i18n'

const { t, te } = useI18n()
const tasks = ref([])
const storage = ref(null)
const pendingDevices = ref(0)
const storageFields = [
  'queueDepth',
  'batchesCommitted',
  'jobsCommitted',
  'jobsFailed',
  'lastBatchSize'
]
const loading = ref(false)
const submitting = ref('')
const error = ref('')
let refreshTimer
let disposed = false
const columns = computed(() => [
  { key: 'name', title: t('adminTasks.name'), width: 190 },
  { key: 'state', title: t('adminTasks.state'), width: 100 },
  { key: 'lastStartedAt', title: t('adminTasks.lastStartedAt'), width: 170 },
  { key: 'lastSuccessAt', title: t('adminTasks.lastSuccessAt'), width: 170 },
  { key: 'nextRunAt', title: t('adminTasks.nextRunAt'), width: 170 },
  { key: 'duration', title: t('adminTasks.duration'), width: 110 },
  { key: 'rows', title: t('adminTasks.rows'), width: 130 },
  { key: 'runs', title: t('adminTasks.runs'), width: 130 },
  { key: 'error', title: t('adminTasks.error'), width: 220 },
  { key: 'action', title: t('adminTasks.action'), width: 120, fixed: 'right' }
])
const formatTime = (value) => (value ? new Date(value).toLocaleString() : '—')
const taskName = (name) => (te(`adminTasks.names.${name}`) ? t(`adminTasks.names.${name}`) : name)
const detailName = (name) =>
  te(`adminTasks.detailLabels.${name}`) ? t(`adminTasks.detailLabels.${name}`) : name

const refresh = async () => {
  if (loading.value || disposed) return
  loading.value = true
  try {
    const response = await axios.post('/api/admin/backgroundTasks')
    if (response.data.status !== 0) throw new Error(t('security.requestFailed'))
    tasks.value = response.data.data.tasks
    storage.value = response.data.data.storage
    pendingDevices.value = response.data.data.pendingDevices ?? 0
    error.value = ''
  } catch {
    error.value = t('security.requestFailed')
  } finally {
    loading.value = false
  }
}

const run = async (name) => {
  submitting.value = name
  try {
    const response = await axios.post('/api/admin/runBackgroundTask', { name })
    if (response.data.status !== 0) return
    message.success(t('adminTasks.queued'))
    await refresh()
  } catch {
    error.value = t('security.requestFailed')
  } finally {
    submitting.value = ''
  }
}

onMounted(() => {
  refresh()
  refreshTimer = setInterval(() => {
    if (!document.hidden) refresh()
  }, 5000)
})
onUnmounted(() => {
  disposed = true
  clearInterval(refreshTimer)
})
</script>
