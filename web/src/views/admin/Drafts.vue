<template>
  <el-card shadow="never">
    <el-alert type="info" :closable="false" style="margin-bottom: 12px"
      title="资源编辑弹窗里点「暂存修改」的变更集中在这里（只存平台、未写入集群，不影响线上）。应用会真实写入集群：Pod 模板类模块将触发滚动重启；ConfigMap/Secret 数据应用后需重启应用才生效。" />
    <div class="toolbar">
      <el-select v-model="filter.cluster" placeholder="集群" style="width: 170px" clearable filterable @change="load">
        <el-option v-for="c in clusters" :key="c.name" :label="clusterLabel(c)" :value="c.name" />
      </el-select>
      <el-input v-model="filter.ns" placeholder="命名空间" style="width: 150px" clearable @input="loadDebounced" />
      <el-input v-model="filter.res" placeholder="资源（如 deployments）" style="width: 180px" clearable @input="loadDebounced" />
      <el-input v-model="filter.name" placeholder="资源名称" style="width: 170px" clearable @input="loadDebounced" />
      <el-button :icon="Search" @click="load">查询</el-button>
      <div style="flex: 1"></div>
      <el-button :icon="Refresh" circle :loading="loading" title="刷新" @click="load" />
    </div>

    <el-table :data="paged" stripe border v-loading="loading">
      <el-table-column label="集群" width="130" show-overflow-tooltip>
        <template #default="{ row }">{{ row.cluster || `#${row.id}` }}</template>
      </el-table-column>
      <el-table-column label="目标" min-width="240" show-overflow-tooltip>
        <template #default="{ row }">
          <span class="mono">{{ row.namespace ? row.namespace + '/' : '' }}{{ row.name }}</span>
          <span class="dim">（{{ resLabel(row.resource) }}）</span>
        </template>
      </el-table-column>
      <el-table-column label="模块" width="130">
        <template #default="{ row }">
          <el-tag size="small">{{ row.section_label || row.section }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="暂存人" width="110" prop="created_by" />
      <el-table-column label="暂存时间" width="160">
        <template #default="{ row }">{{ fmtTime(row.updated_at) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="200" fixed="right">
        <template #default="{ row }">
          <el-button link type="primary" size="small" @click="openView(row)">查看内容</el-button>
          <el-button link type="success" size="small" :loading="applyingId === row.id" @click="applyRow(row)">应用</el-button>
          <el-button link type="danger" size="small" @click="discardRow(row)">放弃</el-button>
        </template>
      </el-table-column>
      <template #empty>暂无暂存变更</template>
    </el-table>
    <el-pagination v-if="filtered.length > pageSize" style="margin-top: 12px; justify-content: flex-end"
      layout="total, prev, pager, next" :total="filtered.length" v-model:current-page="page" :page-size="pageSize" />

    <!-- 查看内容 -->
    <el-dialog v-model="viewDlg" :title="`暂存内容 - ${viewRow?.name}（${viewRow?.section_label || viewRow?.section}）`" width="70%" top="4vh" destroy-on-close>
      <div v-if="viewRow" class="view-meta dim">
        {{ viewRow.cluster }} / {{ viewRow.namespace || '集群级' }} · {{ resLabel(viewRow.resource) }} · {{ viewRow.name }}
        · 暂存人 {{ viewRow.created_by }} · {{ fmtTime(viewRow.updated_at) }}
      </div>
      <pre class="payload-pre mono">{{ viewText }}</pre>
      <template #footer>
        <el-button @click="viewDlg = false">关闭</el-button>
        <el-button type="primary" :loading="applyingId === viewRow?.id" @click="applyRow(viewRow); viewDlg = false">应用该暂存</el-button>
      </template>
    </el-dialog>
  </el-card>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh, Search } from '@element-plus/icons-vue'
import { adminListDrafts, draftDelete, listClusters, resourceFormEdit } from '../../api'
import { errMsg } from '../../api/http'

const clusters = ref<any[]>([])
const items = ref<any[]>([])
const loading = ref(false)
const applyingId = ref<number | null>(null)
const filter = reactive({ cluster: '', ns: '', res: '', name: '' })
const page = ref(1)
const pageSize = 50

const filtered = computed(() => items.value.filter((d) =>
  (!filter.cluster || d.cluster === filter.cluster)
  && (!filter.ns || (d.namespace || '').includes(filter.ns.trim()))
  && (!filter.res || d.resource.includes(filter.res.trim()))
  && (!filter.name || d.name.includes(filter.name.trim()))))
const paged = computed(() => filtered.value.slice((page.value - 1) * pageSize, page.value * pageSize))

const resAlias: Record<string, string> = {
  deployments: '应用', statefulsets: '有状态应用', daemonsets: '守护进程', jobs: '任务', cronjobs: '定时任务',
  services: '服务', ingresses: '路由', configmaps: '配置', secrets: '密钥', persistentvolumeclaims: '持久卷',
}
function resLabel(r: string): string {
  return resAlias[r] || r
}
function fmtTime(s?: string): string {
  if (!s) return '-'
  const d = new Date(s)
  return Number.isNaN(d.getTime()) ? s : d.toLocaleString('zh-CN', { hour12: false })
}

// 集群显示名：纳管时填了描述则显示「描述(名称)」，否则显示名称（与项目管理页同口径，可搜索别名）
function clusterLabel(c: any): string {
  const desc = (c.description || '').trim()
  return desc ? `${desc}(${c.name})` : c.name
}

async function load() {
  loading.value = true
  try {
    const [drafts, cls] = await Promise.all([adminListDrafts(), listClusters()])
    items.value = drafts.data || []
    clusters.value = cls.data || []
    page.value = 1
  } catch (e) { ElMessage.error(errMsg(e)) } finally { loading.value = false }
}
let timer: number | null = null
function loadDebounced() {
  if (timer) clearTimeout(timer)
  timer = window.setTimeout(load, 300)
}

// ---------- 查看 ----------
const viewDlg = ref(false)
const viewRow = ref<any>(null)
const viewText = computed(() => JSON.stringify(viewRow.value?.payload ?? {}, null, 2))
function openView(row: any) {
  viewRow.value = row
  viewDlg.value = true
}

// ---------- 应用 / 放弃 ----------
// Pod 模板类模块确认滚动重启；CM/Secret 数据提示需重启应用（与 Resources.vue 的 restartSections 同步维护）
const restartSections = ['image', 'command', 'ports', 'resources', 'env', 'probes', 'storage']
async function applyRow(row: any) {
  const isPodTpl = restartSections.includes(row.section)
  const isCfg = (row.resource === 'configmaps' || row.resource === 'secrets') && row.section === 'data'
  const hint = isPodTpl
    ? '应用将修改应用配置并触发滚动重启（Pod 逐个重建，滚动策略下业务不中断）。'
    : isCfg ? '应用后已运行的 Pod 不会自动加载新配置，需重启应用才生效。' : ''
  try {
    if (hint) {
      await ElMessageBox.confirm(hint, `应用暂存 - ${row.name}`, { type: 'warning', confirmButtonText: '应用', cancelButtonText: '取消' })
    } else {
      await ElMessageBox.confirm(`确定把「${row.section_label || row.section}」的暂存内容写入集群？`, '应用暂存',
        { type: 'info', confirmButtonText: '应用', cancelButtonText: '取消' })
    }
  } catch { return }
  applyingId.value = row.id
  try {
    await resourceFormEdit(row.cluster, {
      group: row.group || '', version: row.version || '', resource: row.resource,
      namespace: row.namespace || '', name: row.name,
    }, row.section, row.payload)
    await draftDelete(row.cluster, { resource: row.resource, namespace: row.namespace || '', name: row.name, section: row.section })
    ElMessage.success('已应用暂存')
    await load()
  } catch (e) { ElMessage.error(errMsg(e)) } finally { applyingId.value = null }
}

async function discardRow(row: any) {
  try {
    await ElMessageBox.confirm(`确定放弃「${row.section_label || row.section}」的暂存修改？放弃后不可恢复。`, '放弃暂存',
      { type: 'warning', confirmButtonText: '放弃', cancelButtonText: '取消' })
  } catch { return }
  try {
    await draftDelete(row.cluster, { resource: row.resource, namespace: row.namespace || '', name: row.name, section: row.section })
    ElMessage.success('已放弃暂存')
    await load()
  } catch (e) { ElMessage.error(errMsg(e)) }
}

onMounted(load)
</script>

<style scoped>
.toolbar { display: flex; align-items: center; gap: 8px; margin-bottom: 12px; flex-wrap: wrap; }
.view-meta { margin-bottom: 8px; font-size: 13px; }
.payload-pre {
  background: var(--el-fill-color-light); border-radius: 6px; padding: 12px;
  max-height: 55vh; overflow: auto; font-size: 12px; line-height: 1.6; white-space: pre-wrap; word-break: break-all;
}
</style>
