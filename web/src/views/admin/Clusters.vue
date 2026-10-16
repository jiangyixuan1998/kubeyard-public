<template>
  <el-card shadow="never">
    <div class="toolbar">
      <span>导入有读写权限的 kubeconfig 纳管集群（全程加密存储，测试通过才会入库）</span>
      <div style="flex: 1"></div>
      <el-button type="primary" @click="openWizard"><el-icon><Plus /></el-icon>导入集群</el-button>
    </div>

    <el-table :data="clusters" stripe border v-loading="loading">
      <el-table-column prop="name" label="集群名" min-width="120" />
      <el-table-column label="状态" width="95">
        <template #default="{ row }">
          <el-tag :type="statusColor(row.status)" size="small">{{ row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="description" label="描述" min-width="150" show-overflow-tooltip />
      <el-table-column prop="version" label="版本" width="110" show-overflow-tooltip />
      <el-table-column prop="node_count" label="节点" width="60" />
      <el-table-column label="监控" width="80" align="center">
        <template #default="{ row }">
          <el-tooltip :content="row.grafana_url ? 'Pod 监控看板已配置' : '未配置 Pod 监控看板地址'">
            <el-tag v-if="row.grafana_url" size="small" type="success">已配</el-tag>
            <el-tag v-else size="small" type="info">未配</el-tag>
          </el-tooltip>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="440" fixed="right">
        <template #default="{ row }">
          <el-button link type="primary" size="small" @click="doTest(row)">重新检测</el-button>
          <el-button link type="primary" size="small" @click="editDesc(row)">编辑描述</el-button>
          <el-button link type="primary" size="small" @click="editGrafana(row)">监控地址</el-button>
          <el-button link type="primary" size="small" @click="openAliases(row)">命名空间别名</el-button>
          <el-button link type="warning" size="small" @click="doRotate(row)">轮换凭据</el-button>
          <el-button link type="danger" size="small" @click="doDelete(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 导入向导 -->
    <el-dialog v-model="wizard" :title="`导入集群（步骤 ${step}/3）`" width="680px" destroy-on-close @closed="resetWizard">
      <el-steps :active="step - 1" finish-status="success" simple style="margin-bottom: 20px">
        <el-step title="基本信息" />
        <el-step title="选择 Context" />
        <el-step title="连接测试" />
      </el-steps>

      <!-- 步骤 1 -->
      <el-form v-if="step === 1" label-width="100px">
        <el-form-item label="集群名称">
          <el-input v-model="form.name" placeholder="小写字母/数字/中划线，如 prod-ent3" />
        </el-form-item>
        <el-form-item label="描述">
          <el-input v-model="form.description" placeholder="可选" />
        </el-form-item>
        <el-form-item label="kubeconfig">
          <div style="width: 100%">
            <input type="file" accept=".yaml,.yml,.conf,text/*" @change="onFile" />
            <el-input v-model="form.kubeconfig" type="textarea" :rows="10" placeholder="或直接粘贴 kubeconfig 内容"
              style="margin-top: 8px; font-family: Consolas, monospace; font-size: 12px" />
          </div>
        </el-form-item>
      </el-form>

      <!-- 步骤 2 -->
      <el-form v-if="step === 2" label-width="100px">
        <el-form-item label="Context">
          <el-select v-model="form.context_name" style="width: 100%">
            <el-option v-for="ct in contexts" :key="ct" :label="ct" :value="ct" />
          </el-select>
          <div class="tip">该 kubeconfig 包含 {{ contexts.length }} 个 context，请选择要纳管的集群</div>
        </el-form-item>
      </el-form>

      <!-- 步骤 3 -->
      <div v-if="step === 3">
        <template v-if="testResult">
          <el-result v-if="testResult.all_ok" icon="success" title="测试全部通过" :sub-title="`版本 ${testResult.version}`" />
          <el-result v-else icon="error" title="测试未通过" sub-title="请根据以下步骤结果排查后重试" />
          <el-steps direction="vertical" :active="testResult.steps.filter((s: any) => s.ok).length">
            <el-step v-for="s in testResult.steps" :key="s.name" :title="s.name" :status="s.ok ? 'success' : 'error'"
              :description="s.message" />
          </el-steps>
        </template>
      </div>

      <template #footer>
        <el-button v-if="step > 1" @click="step--">上一步</el-button>
        <el-button v-if="step === 1" type="primary" :loading="testing" :disabled="!form.kubeconfig" @click="parseContexts">解析 kubeconfig</el-button>
        <el-button v-if="step === 2" type="primary" @click="runTest">开始测试</el-button>
        <el-button v-if="step === 3" :disabled="!testResult?.all_ok" :loading="creating" type="primary" @click="doCreate">确认纳管</el-button>
        <el-button v-if="step === 3 && !testResult?.all_ok" @click="step = 1">重新填写</el-button>
      </template>
    </el-dialog>

    <!-- 监控地址配置 -->
    <el-dialog v-model="grafanaDlg" :title="`Pod 监控地址 - ${grafanaCluster}`" width="680px" destroy-on-close>
      <el-alert type="info" :closable="false" style="margin-bottom: 10px"
        title="配置 Pod 监控看板的地址前缀，项目管理页 Pod 列表的「监控」按钮会自动拼接 var-namespace 与 var-pod 打开对应面板。"
        description="粘贴完整看板地址，截取到 var-namespace 之前（以 & 结尾）即可；例如 https://grafana.xxx.com/d/xxx/kubeyard-pod-monitor?orgId=1&from=now-1h&to=now&var-datasource=xxx&var-env_id=xxx&" />
      <el-input v-model="grafanaValue" type="textarea" :rows="4" placeholder="https://grafana.xxx.com/d/xxx/kubeyard-pod-monitor?orgId=1&...&var-env_id=xxx&"
        style="font-family: Consolas, monospace; font-size: 12px" />
      <div class="tip">留空保存 = 清除配置；配置后 Pod 操作栏会出现「监控」按钮（需查看权限 get/list 即可，不额外校验）。</div>
      <template #footer>
        <el-button @click="grafanaDlg = false">取消</el-button>
        <el-button type="primary" :loading="grafanaSaving" @click="saveGrafana">保存</el-button>
      </template>
    </el-dialog>

    <!-- 命名空间别名 -->
    <el-dialog v-model="aliasDlg" :title="`命名空间别名 - ${aliasCluster}`" width="640px" destroy-on-close>
      <el-alert type="info" :closable="false" style="margin-bottom: 10px"
        title="为命名空间配置显示别名（如 test → 测试团队），普通用户在各页面的命名空间下拉中将显示「别名 (原名)」。" />
      <el-input v-model="aliasFilter" placeholder="过滤命名空间" size="small" clearable style="margin-bottom: 8px" />
      <el-table :data="aliasRowsFiltered" size="small" border height="380" v-loading="aliasLoading">
        <el-table-column prop="name" label="命名空间" min-width="180" show-overflow-tooltip />
        <el-table-column label="显示别名" min-width="200">
          <template #default="{ row }">
            <el-input v-model="row.alias" size="small" placeholder="留空则显示原名" maxlength="64" />
          </template>
        </el-table-column>
      </el-table>
      <template #footer>
        <el-button @click="aliasDlg = false">取消</el-button>
        <el-button type="primary" :loading="aliasSaving" @click="saveAliases">保存</el-button>
      </template>
    </el-dialog>
  </el-card>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  adminListClusters, createCluster, deleteCluster, updateCluster,
  adminTestCluster, kubeconfigContexts, kubeconfigTest,
  listNamespaces, getNSAliases, putNSAliases,
} from '../../api'
import { errMsg } from '../../api/http'
import { statusColor } from '../../utils/format'

const clusters = ref<any[]>([])
const loading = ref(false)

const wizard = ref(false)
const step = ref(1)
const testing = ref(false)
const creating = ref(false)
const contexts = ref<string[]>([])
const testResult = ref<any>(null)
const rotateMode = ref(false)
const form = reactive({ name: '', description: '', kubeconfig: '', context_name: '' })

async function load() {
  loading.value = true
  try { clusters.value = (await adminListClusters()).data } finally { loading.value = false }
}

function openWizard() {
  resetWizard()
  wizard.value = true
}

function resetWizard() {
  step.value = 1
  contexts.value = []
  testResult.value = null
  rotateMode.value = false
  Object.assign(form, { name: '', description: '', kubeconfig: '', context_name: '' })
}

function onFile(ev: Event) {
  const file = (ev.target as HTMLInputElement).files?.[0]
  if (!file) return
  const reader = new FileReader()
  reader.onload = () => { form.kubeconfig = String(reader.result || '') }
  reader.readAsText(file)
}

async function parseContexts() {
  if (!form.kubeconfig.trim()) return
  testing.value = true
  try {
    const res = await kubeconfigContexts(form.kubeconfig)
    contexts.value = res.data.contexts || []
    form.context_name = res.data.current_context || contexts.value[0] || ''
    step.value = 2
  } catch (e) { ElMessage.error(errMsg(e)) } finally { testing.value = false }
}

async function runTest() {
  testing.value = true
  testResult.value = null
  try {
    const res = await kubeconfigTest(form.kubeconfig, form.context_name)
    testResult.value = res.data
    step.value = 3
  } catch (e) { ElMessage.error(errMsg(e)) } finally { testing.value = false }
}

async function doCreate() {
  creating.value = true
  try {
    if (rotateMode.value) {
      await updateCluster(form.name, {
        description: form.description,
        kubeconfig: form.kubeconfig,
        context_name: form.context_name,
      })
      ElMessage.success(`集群 ${form.name} 凭据已轮换`)
    } else {
      await createCluster({
        name: form.name, description: form.description,
        kubeconfig: form.kubeconfig, context_name: form.context_name,
      })
      ElMessage.success(`集群 ${form.name} 已纳管`)
    }
    wizard.value = false
    load()
  } catch (e) { ElMessage.error(errMsg(e)) } finally { creating.value = false }
}

async function doTest(row: any) {
  try {
    const res = await adminTestCluster(row.name)
    if (res.data.all_ok) ElMessage.success(`${row.name} 连接正常（${res.data.version}）`)
    else ElMessage.error(`${row.name} 测试未通过，请查看控制台`)
    console.log(res.data)
    load()
  } catch (e) { ElMessage.error(errMsg(e)) }
}

async function editDesc(row: any) {
  let value: string
  try {
    const { value: v } = await ElMessageBox.prompt('集群描述（项目管理等页面的集群下拉会显示「描述(名称)」）',
      `编辑描述 - ${row.name}`, {
        inputValue: row.description || '',
        inputPlaceholder: '如：企业ent（留空清除描述）',
        confirmButtonText: '保存',
        cancelButtonText: '取消',
      })
    value = v.trim()
  } catch { return }
  try {
    await updateCluster(row.name, { description: value })
    row.description = value
    ElMessage.success('描述已更新')
  } catch (e) { ElMessage.error(errMsg(e)) }
}

async function doRotate(row: any) {
  await ElMessageBox.alert(
    '轮换凭据：提交一份新的 kubeconfig（同集群），平台会先测试再重新加密存储',
    '轮换凭据', { confirmButtonText: '知道了' },
  )
  resetWizard()
  form.name = row.name
  form.description = row.description
  rotateMode.value = true
  wizard.value = true
}

async function doDelete(row: any) {
  try {
    await ElMessageBox.confirm(
      `确定删除集群 "${row.name}" 吗？相关凭据与用户授权将一并清除。`,
      '删除确认', { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
    )
  } catch { return }
  try {
    await deleteCluster(row.name)
    ElMessage.success('已删除')
    load()
  } catch (e) { ElMessage.error(errMsg(e)) }
}

// ---------- 监控地址 ----------
const grafanaDlg = ref(false)
const grafanaCluster = ref('')
const grafanaValue = ref('')
const grafanaSaving = ref(false)

function editGrafana(row: any) {
  grafanaCluster.value = row.name
  grafanaValue.value = row.grafana_url || ''
  grafanaDlg.value = true
}

async function saveGrafana() {
  grafanaSaving.value = true
  try {
    await updateCluster(grafanaCluster.value, { grafana_url: grafanaValue.value.trim() })
    ElMessage.success('监控地址已保存')
    grafanaDlg.value = false
    load()
  } catch (e) { ElMessage.error(errMsg(e)) } finally { grafanaSaving.value = false }
}

// ---------- 命名空间别名 ----------
const aliasDlg = ref(false)
const aliasCluster = ref('')
const aliasRows = ref<any[]>([])
const aliasFilter = ref('')
const aliasLoading = ref(false)
const aliasSaving = ref(false)

const aliasRowsFiltered = computed(() => {
  const kw = aliasFilter.value.trim().toLowerCase()
  if (!kw) return aliasRows.value
  return aliasRows.value.filter((r) => r.name.toLowerCase().includes(kw) || (r.alias || '').toLowerCase().includes(kw))
})

async function openAliases(row: any) {
  aliasCluster.value = row.name
  aliasFilter.value = ''
  aliasDlg.value = true
  aliasLoading.value = true
  try {
    const [nsRes, aliasRes] = await Promise.all([listNamespaces(row.name), getNSAliases(row.name)])
    const aliases = aliasRes.data || {}
    aliasRows.value = ((nsRes.data || []) as any[]).map((ns) => ({ name: ns.name, alias: aliases[ns.name] || '' }))
  } catch (e) {
    ElMessage.error(errMsg(e))
    aliasRows.value = []
  } finally { aliasLoading.value = false }
}

async function saveAliases() {
  aliasSaving.value = true
  try {
    const body: Record<string, string> = {}
    for (const r of aliasRows.value) {
      if (r.alias.trim()) body[r.name] = r.alias.trim()
    }
    await putNSAliases(aliasCluster.value, body)
    ElMessage.success('别名已保存')
    aliasDlg.value = false
  } catch (e) { ElMessage.error(errMsg(e)) } finally { aliasSaving.value = false }
}

onMounted(load)
</script>

<style scoped>
.toolbar { display: flex; align-items: center; margin-bottom: 12px; color: #606266; font-size: 13px; }
.tip { color: #909399; font-size: 12px; margin-top: 4px; }
</style>
