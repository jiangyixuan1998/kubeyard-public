<template>
  <el-card shadow="never" v-loading="loading">
    <el-alert type="info" :closable="false" style="margin-bottom: 12px"
      title="事件每 30 秒自动采集入库，保留 7 天；K8s 集群侧事件默认约 1 小时就过期，平台侧可回溯最近 7 天。" />
    <div class="toolbar">
      <el-select v-model="cluster" placeholder="集群" style="width: 200px" filterable @change="onClusterChange">
        <el-option v-for="c in clusters" :key="c.name" :label="clusterLabel(c)" :value="c.name">
          <span>{{ clusterLabel(c) }}</span>
        </el-option>
      </el-select>
      <el-select v-model="namespace" placeholder="全部命名空间" style="width: 220px" clearable filterable @change="onFilterChange">
        <el-option v-for="ns in namespaces" :key="ns.name" :label="ns.alias ? `${ns.alias} (${ns.name})` : ns.name" :value="ns.name" />
      </el-select>
      <!-- 模糊搜索走后端（object/message/reason LIKE，命中上限 500 条），输入防抖 300ms（清空同样走防抖） -->
      <el-input v-model="search" placeholder="搜索对象 / 原因 / 消息" style="width: 260px" clearable
        :prefix-icon="Search" @input="searchDebounced" />
      <el-radio-group v-model="type" @change="onFilterChange">
        <el-radio-button value="">全部</el-radio-button>
        <el-radio-button value="Warning">Warning</el-radio-button>
        <el-radio-button value="Normal">Normal</el-radio-button>
      </el-radio-group>
      <el-button @click="load" :icon="Refresh">刷新</el-button>
    </div>

    <el-table :data="paged" stripe border height="calc(100vh - 310px)" v-loading="loading">
      <el-table-column prop="last_time" label="时间" width="180">
        <template #default="{ row }">{{ fmtTime(row.last_time) }}</template>
      </el-table-column>
      <el-table-column label="命名空间" width="170" show-overflow-tooltip>
        <template #default="{ row }">{{ nsAliasLabel(row.namespace) }}</template>
      </el-table-column>
      <el-table-column label="类型" width="100">
        <template #default="{ row }">
          <el-tag :type="row.type === 'Warning' ? 'danger' : 'info'" size="small">{{ row.type }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="reason" label="原因" width="160" show-overflow-tooltip />
      <el-table-column prop="object" label="对象" min-width="220" show-overflow-tooltip />
      <el-table-column prop="message" label="消息" min-width="320" show-overflow-tooltip />
      <el-table-column prop="count" label="次数" width="80" />
      <el-table-column label="操作" width="100" fixed="right">
        <template #default="{ row }">
          <!-- 只读 AI 诊断：上下文 = 该对象近 24h 事件 + Pod/工作负载状态，后端按 get/list 权限裁剪 -->
          <el-button link type="primary" size="small" :icon="MagicStick" @click="diagnose(row)">AI 诊断</el-button>
        </template>
      </el-table-column>
    </el-table>
    <el-pagination v-if="filtered.length > pageSize" style="margin-top: 12px; justify-content: flex-end"
      layout="total, prev, pager, next" :total="filtered.length"
      v-model:current-page="page" :page-size="pageSize" />
    <AiDialog ref="aiDlgRef" :title="aiTitle" />
  </el-card>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { MagicStick, Refresh, Search } from '@element-plus/icons-vue'
import { listClusters, listNamespaces, listEvents, aiDiagnose } from '../api'
import AiDialog from '../components/AiDialog.vue'

const clusters = ref<any[]>([])
const namespaces = ref<any[]>([])
const cluster = ref('')
const namespace = ref('')
const type = ref('')
const search = ref('')
const events = ref<any[]>([])
const loading = ref(false)
const page = ref(1)
const pageSize = 50

// 类型为前端本地过滤；搜索/命名空间走后端（LIKE 检索，命中上限 500 条）
const filtered = computed(() => type.value ? events.value.filter((e) => e.type === type.value) : events.value)
const paged = computed(() => filtered.value.slice((page.value - 1) * pageSize, page.value * pageSize))

function fmtTime(s?: string): string {
  if (!s) return '-'
  return new Date(s).toLocaleString('zh-CN', { hour12: false })
}

// 命名空间别名：有别名显示「别名 (原名)」
function nsAliasLabel(name?: string): string {
  if (!name) return '-'
  const ns = namespaces.value.find((n) => n.name === name)
  return ns?.alias ? `${ns.alias} (${name})` : name
}

// 集群显示名：纳管时填了描述则显示「描述(名称)」，否则显示名称（与项目管理页同口径，可搜索别名）
function clusterLabel(c: any): string {
  const desc = (c.description || '').trim()
  return desc ? `${desc}(${c.name})` : c.name
}

async function load() {
  if (!cluster.value) return
  loading.value = true
  try {
    events.value = (await listEvents(cluster.value, namespace.value || undefined, search.value.trim() || undefined)).data || []
  } catch { /* http 层已提示 */ } finally { loading.value = false }
}

// 命名空间/类型切换：回第一页再拉数据（类型纯前端过滤，换页由分页器自己控制）
function onFilterChange() {
  page.value = 1
  load()
}

// 搜索输入防抖 300ms（后端 LIKE 检索，逐键请求太频繁）
let searchTimer: number | undefined
function searchDebounced() {
  if (searchTimer) window.clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => { page.value = 1; load() }, 300)
}

// 切换集群时命名空间列表必须跟随重载（各集群命名空间不同）；
// 默认选中用户有权限的第一个命名空间（避免默认全命名空间数据量大加载慢），下拉可清空查看全部
async function onClusterChange() {
  namespace.value = ''
  page.value = 1
  try { namespaces.value = (await listNamespaces(cluster.value)).data } catch { namespaces.value = [] }
  pickDefaultNamespace()
  load()
}

function pickDefaultNamespace() {
  if (namespace.value) return
  const list = namespaces.value.map((n: any) => n.name).sort((a: string, b: string) => a.localeCompare(b))
  namespace.value = list[0] || ''
}

// ---------- AI 诊断（只读：结论 / 根因 / 修复建议） ----------
const aiDlgRef = ref<InstanceType<typeof AiDialog>>()
const aiTitle = ref('AI 诊断')

function diagnose(row: any) {
  aiTitle.value = `AI 诊断 — ${row.object_type}/${row.object_name}`
  aiDlgRef.value?.start((onDelta, signal) =>
    aiDiagnose({
      cluster: cluster.value,
      namespace: row.namespace || '',
      object_type: row.object_type,
      object_name: row.object_name,
    }, onDelta, signal))
}

onMounted(async () => {
  clusters.value = (await listClusters()).data
  if (clusters.value.length) {
    cluster.value = clusters.value[0].name
    try { namespaces.value = (await listNamespaces(cluster.value)).data } catch { /* 忽略 */ }
    pickDefaultNamespace()
    load()
  }
})
</script>

<style scoped>
.toolbar { display: flex; gap: 10px; margin-bottom: 12px; }
</style>
