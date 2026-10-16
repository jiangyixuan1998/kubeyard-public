<template>
  <el-dialog v-model="visible" title="创建定时任务" width="580px" destroy-on-close @closed="emit('closed')">
    <el-form label-width="92px">
      <el-form-item label="目标">
        <span class="mono">{{ targetDesc }}</span>
      </el-form-item>
      <el-form-item label="任务名" required>
        <el-input v-model="form.name" placeholder="如：订单服务月底扩容" style="width: 340px" />
      </el-form-item>
      <el-form-item label="类型">
        <el-radio-group v-model="form.type">
          <el-radio value="restart">定时重启</el-radio>
          <el-radio value="scale" :disabled="resource === 'daemonsets'">定时扩缩容</el-radio>
        </el-radio-group>
      </el-form-item>
      <el-form-item v-if="form.type === 'scale'" label="目标副本数" required>
        <el-input-number v-model="form.replicas" :min="0" :max="9999" />
        <span class="dim" style="margin-left: 10px">0 = 缩容到停止</span>
      </el-form-item>
      <el-form-item label="触发方式">
        <el-radio-group v-model="form.trigger">
          <el-radio value="once">单次</el-radio>
          <el-radio value="cron">周期（cron）</el-radio>
        </el-radio-group>
      </el-form-item>
      <el-form-item v-if="form.trigger === 'once'" label="执行时间" required>
        <el-date-picker v-model="form.runAt" type="datetime" placeholder="选择执行时间"
          :disabled-date="(d: Date) => d.getTime() < new Date().setHours(0, 0, 0, 0)" style="width: 220px" />
      </el-form-item>
      <template v-else>
        <el-form-item label="常用模板">
          <el-select v-model="template" placeholder="选择模板填充表达式" clearable style="width: 220px" @change="onTemplate">
            <el-option v-for="t in templates" :key="t.expr" :value="t.expr" :label="t.label" />
          </el-select>
        </el-form-item>
        <el-form-item label="cron 表达式" required>
          <el-input v-model="form.cron" placeholder="分 时 日 月 周，如 0 4 * * 6" style="width: 220px" />
        </el-form-item>
        <el-form-item label="下次执行">
          <span v-if="previewErr" class="err">{{ previewErr }}</span>
          <span v-else-if="preview.length" class="dim">
            {{ preview.map((t) => new Date(t).toLocaleString()).join('　→　') }}
          </span>
          <span v-else class="dim">输入表达式后自动预览</span>
        </el-form-item>
        <el-alert type="info" :closable="false"
          title="月底周期扩缩容：建两条任务即可——扩容用 0 0 25 * *（每月 25 号 0 点），缩容用 0 0 1 * *（每月 1 号 0 点）" />
      </template>
    </el-form>
    <template #footer>
      <el-button @click="visible = false">取消</el-button>
      <el-button type="primary" :loading="saving" @click="submit">创建任务</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { createScheduleTask, previewCron } from '../api'
import { errMsg } from '../api/http'

const emit = defineEmits<{ created: []; closed: [] }>()

const visible = ref(false)
const saving = ref(false)
const cluster = ref('')
const namespace = ref('')
const resource = ref('')
const resName = ref('')

const form = reactive({
  name: '', type: 'restart', trigger: 'once',
  runAt: null as Date | null, cron: '0 4 * * 6', replicas: 1,
})
const template = ref('')

const templates = [
  { label: '每天 02:00', expr: '0 2 * * *' },
  { label: '每周一 09:00', expr: '0 9 * * 1' },
  { label: '每周六 04:00', expr: '0 4 * * 6' },
  { label: '每月 1 号 00:00（缩容回平时）', expr: '0 0 1 * *' },
  { label: '每月 25 号 00:00（月底扩容）', expr: '0 0 25 * *' },
]

const resourceAlias: Record<string, string> = {
  deployments: 'Deployment', statefulsets: 'StatefulSet', daemonsets: 'DaemonSet',
}
const targetDesc = computed(() =>
  `${cluster.value} / ${namespace.value} / ${resourceAlias[resource.value] || resource.value} / ${resName.value}`)

// open 由资源页调用；daemonsets 只有重启（无 replicas）
function open(cl: string, ns: string, res: string, name: string) {
  cluster.value = cl
  namespace.value = ns
  resource.value = res
  resName.value = name
  form.name = `${name}-restart`
  form.type = 'restart'
  form.trigger = 'once'
  form.runAt = null
  form.cron = '0 4 * * 6'
  form.replicas = 1
  template.value = ''
  visible.value = true
}
defineExpose({ open })

function onTemplate(expr: string) {
  if (expr) form.cron = expr
}

// cron 预览：防抖 400ms，输入合法时拉未来 5 次
let timer: number | null = null
watch(() => [form.trigger, form.cron] as const, () => {
  if (timer) clearTimeout(timer)
  preview.value = []
  previewErr.value = ''
  if (form.trigger !== 'cron') return
  timer = window.setTimeout(async () => {
    try {
      preview.value = (await previewCron(form.cron.trim())).data || []
      previewErr.value = ''
    } catch (e: any) {
      preview.value = []
      previewErr.value = e?.response?.data?.message || '表达式无效'
    }
  }, 400)
})
const preview = ref<string[]>([])
const previewErr = ref('')

async function submit() {
  if (!form.name.trim()) { ElMessage.warning('请填写任务名'); return }
  if (form.trigger === 'once' && !form.runAt) { ElMessage.warning('请选择执行时间'); return }
  // 今天可选后允许选到已过的时刻，提交前兜底校验（后端同口径）
  if (form.trigger === 'once' && form.runAt && form.runAt.getTime() <= Date.now()) {
    ElMessage.warning('执行时间需晚于当前时间')
    return
  }
  if (form.trigger === 'cron' && previewErr.value) { ElMessage.warning('cron 表达式无效'); return }
  if (form.type === 'scale' && (form.replicas === null || form.replicas === undefined)) { ElMessage.warning('请填写目标副本数'); return }
  saving.value = true
  try {
    await createScheduleTask(cluster.value, {
      name: form.name.trim(), type: form.type,
      namespace: namespace.value, resource: resource.value, res_name: resName.value,
      trigger: form.trigger,
      run_at: form.trigger === 'once' ? form.runAt?.toISOString() : null,
      cron: form.trigger === 'cron' ? form.cron.trim() : '',
      replicas: form.type === 'scale' ? form.replicas : null,
    })
    ElMessage.success('定时任务已创建，可在「定时任务」页查看与管理')
    visible.value = false
    emit('created')
  } catch (e) { ElMessage.error(errMsg(e)) } finally { saving.value = false }
}
</script>

<style scoped>
.mono { font-family: ui-monospace, Consolas, monospace; font-size: 12.5px; }
.dim { color: #909399; font-size: 12px; }
.err { color: #f56c6c; font-size: 12px; }
</style>
