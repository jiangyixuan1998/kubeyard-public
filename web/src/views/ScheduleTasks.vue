<template>
  <div class="page">
    <div class="toolbar">
      <span class="dim">定时重启 / 定时扩缩容任务。在「项目管理」的 Deployment / StatefulSet / DaemonSet 行操作里点「定时」即可创建。</span>
      <el-button :loading="loading" @click="load">刷新</el-button>
    </div>

    <el-table :data="tasks" v-loading="loading" size="default">
      <el-table-column label="任务名" min-width="150">
        <template #default="{ row }">
          <span class="name">{{ row.name }}</span>
        </template>
      </el-table-column>
      <el-table-column label="类型" width="110">
        <template #default="{ row }">
          <el-tag size="small" :type="row.type === 'restart' ? 'warning' : 'primary'">
            {{ row.type === 'restart' ? '定时重启' : '定时扩缩容' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="目标" min-width="230">
        <template #default="{ row }">
          <span class="mono">{{ row.cluster }} / {{ row.namespace }} / {{ resAlias(row.resource) }} / {{ row.res_name }}</span>
        </template>
      </el-table-column>
      <el-table-column label="触发" min-width="150">
        <template #default="{ row }">
          <template v-if="row.trigger === 'once'">单次 {{ fmt(row.run_at) }}</template>
          <template v-else><span class="mono">{{ row.cron }}</span></template>
        </template>
      </el-table-column>
      <el-table-column label="参数" width="90">
        <template #default="{ row }">{{ row.type === 'scale' ? row.replicas + ' 副本' : '—' }}</template>
      </el-table-column>
      <el-table-column label="状态" width="80">
        <template #default="{ row }">
          <el-tag size="small" :type="row.enabled ? 'success' : 'info'">{{ row.enabled ? '启用' : '停用' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="下次执行" width="160">
        <template #default="{ row }">{{ row.enabled ? fmt(row.next_run_at) : '—' }}</template>
      </el-table-column>
      <el-table-column label="上次执行" width="160">
        <template #default="{ row }">
          <span v-if="row.last_run_at">{{ fmt(row.last_run_at) }}</span>
          <span v-else class="dim">未执行</span>
        </template>
      </el-table-column>
      <el-table-column label="上次结果" min-width="140">
        <template #default="{ row }">
          <template v-if="!row.last_status"><span class="dim">—</span></template>
          <el-tooltip v-else-if="row.last_error" :content="row.last_error" placement="top">
            <el-tag size="small" :type="statusType(row.last_status)">{{ statusLabel(row.last_status) }}</el-tag>
          </el-tooltip>
          <el-tag v-else size="small" :type="statusType(row.last_status)">{{ statusLabel(row.last_status) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="创建人" prop="creator_name" width="100" />
      <el-table-column label="操作" width="230" fixed="right">
        <template #default="{ row }">
          <el-button size="small" link type="primary" @click="openEdit(row)">编辑</el-button>
          <el-button size="small" link :type="row.enabled ? 'warning' : 'success'" @click="toggle(row)">
            {{ row.enabled ? '停用' : '启用' }}
          </el-button>
          <el-button size="small" link type="primary" :loading="row._running" @click="runNow(row)">立即执行</el-button>
          <el-popconfirm title="删除该定时任务？" @confirm="del(row)">
            <template #reference><el-button size="small" link type="danger">删除</el-button></template>
          </el-popconfirm>
        </template>
      </el-table-column>
      <template #empty>
        <el-empty description="暂无定时任务 — 到「项目管理」找到目标资源，在行操作里点「定时」创建" :image-size="70" />
      </template>
    </el-table>

    <!-- 轻量编辑：改名 / 调 cron 或单次时间 / 调副本数 -->
    <el-dialog v-model="editVisible" title="编辑定时任务" width="480px" destroy-on-close>
      <el-form label-width="100px">
        <el-form-item label="任务名">
          <el-input v-model="editForm.name" style="width: 300px" />
        </el-form-item>
        <el-form-item v-if="editing?.trigger === 'cron'" label="cron 表达式">
          <el-input v-model="editForm.cron" style="width: 200px" placeholder="分 时 日 月 周" />
        </el-form-item>
        <el-form-item v-else label="执行时间">
          <el-date-picker v-model="editForm.runAt" type="datetime" style="width: 220px"
            :disabled-date="(d: Date) => d.getTime() < new Date().setHours(0, 0, 0, 0)" />
        </el-form-item>
        <el-form-item v-if="editing?.type === 'scale'" label="目标副本数">
          <el-input-number v-model="editForm.replicas" :min="0" :max="9999" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="saveEdit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { deleteScheduleTask, listScheduleTasks, runScheduleTask, updateScheduleTask } from '../api'
import { errMsg } from '../api/http'

const tasks = ref<any[]>([])
const loading = ref(false)
const saving = ref(false)

async function load() {
  loading.value = true
  try {
    tasks.value = (await listScheduleTasks()).data || []
    for (const t of tasks.value) t._running = false
  } catch (e: any) {
    ElMessage.error(errMsg(e))
  } finally { loading.value = false }
}

let timer: number | null = null
onMounted(() => {
  load()
  // 编辑对话框打开期间跳过轮询：定时刷新会替换表格数据触发整页重渲染，
  // 正在输入的输入框光标会被重置（跳到末尾），关掉对话框后恢复
  timer = window.setInterval(() => { if (!editVisible.value) load() }, 30_000)
})
onUnmounted(() => { if (timer) clearInterval(timer) })

function resAlias(r: string) {
  return ({ deployments: 'Deployment', statefulsets: 'StatefulSet', daemonsets: 'DaemonSet' } as Record<string, string>)[r] || r
}
function fmt(t?: string | null) {
  if (!t) return '—'
  return new Date(t).toLocaleString()
}
function statusType(s: string) {
  return s === 'success' ? 'success' : s === 'running' ? 'primary' : 'danger'
}
function statusLabel(s: string) {
  return ({ success: '成功', failed: '失败', running: '执行中', missed: '已跳过' } as Record<string, string>)[s] || s
}

async function toggle(row: any) {
  try {
    await updateScheduleTask(row.id, { enabled: !row.enabled })
    ElMessage.success(row.enabled ? '已停用' : '已启用')
    load()
  } catch (e: any) { ElMessage.error(errMsg(e)) }
}

async function runNow(row: any) {
  row._running = true
  try {
    const r = await runScheduleTask(row.id)
    const t = r.data
    if (t.last_status === 'success') ElMessage.success('执行成功')
    else ElMessage.error(`执行失败：${t.last_error || '未知错误'}`)
    load()
  } catch (e: any) { ElMessage.error(errMsg(e)) } finally { row._running = false }
}

async function del(row: any) {
  try {
    await deleteScheduleTask(row.id)
    ElMessage.success('已删除')
    load()
  } catch (e: any) { ElMessage.error(errMsg(e)) }
}

// ---- 编辑 ----
const editVisible = ref(false)
const editing = ref<any>(null)
const editForm = reactive({ name: '', cron: '', runAt: null as Date | null, replicas: 1 })

function openEdit(row: any) {
  editing.value = row
  editForm.name = row.name
  editForm.cron = row.cron
  editForm.runAt = row.run_at ? new Date(row.run_at) : null
  editForm.replicas = row.replicas ?? 1
  editVisible.value = true
}

async function saveEdit() {
  const t = editing.value
  const payload: any = { name: editForm.name.trim() }
  if (t.trigger === 'cron') payload.cron = editForm.cron.trim()
  else if (editForm.runAt) payload.run_at = editForm.runAt.toISOString()
  if (t.type === 'scale') payload.replicas = editForm.replicas
  saving.value = true
  try {
    await updateScheduleTask(t.id, payload)
    ElMessage.success('已保存')
    editVisible.value = false
    load()
  } catch (e: any) { ElMessage.error(errMsg(e)) } finally { saving.value = false }
}
</script>

<style scoped>
.page { background: var(--el-bg-color); border-radius: 10px; padding: 14px; }
.toolbar { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 12px; }
.dim { color: #909399; font-size: 12.5px; }
.mono { font-family: ui-monospace, Consolas, monospace; font-size: 12.5px; }
.name { font-weight: 500; }
</style>
