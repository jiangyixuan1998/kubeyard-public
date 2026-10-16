<template>
  <div v-loading="loading">
    <!-- 顶部工具条 -->
    <div class="top-bar">
      <span class="sub">指标数据生成于 {{ genTime }}（后台每 5 分钟预热，按你的授权过滤）；Warning 事件来自 30 秒采集（近 24 小时）</span>
      <div style="display: flex; gap: 8px">
        <!-- 一键巡检：上下文=本页同源数据（权限同口径），AI 给出概览点评与关注项 -->
        <el-button size="small" type="primary" plain :icon="MagicStick" @click="runInspect">AI 巡检</el-button>
        <el-button size="small" :loading="loading" :icon="Refresh" @click="load(true)">立即刷新</el-button>
      </div>
    </div>

    <!-- 顶部统计 -->
    <el-row :gutter="16" class="stat-row">
      <el-col :span="3"><el-card shadow="never"><el-statistic title="集群" :value="data.total_clusters" /></el-card></el-col>
      <el-col :span="3"><el-card shadow="never"><el-statistic title="健康" :value="data.healthy_count" /></el-card></el-col>
      <el-col :span="3"><el-card shadow="never"><el-statistic title="降级" :value="data.degraded_count" /></el-card></el-col>
      <el-col :span="3"><el-card shadow="never"><el-statistic title="离线" :value="data.offline_count" /></el-card></el-col>
      <el-col :span="3"><el-card shadow="never"><el-statistic title="节点" :value="data.total_nodes" /></el-card></el-col>
      <el-col :span="3"><el-card shadow="never"><el-statistic title="Pod 总数" :value="data.total_pods" /></el-card></el-col>
      <el-col :span="3"><el-card shadow="never"><el-statistic title="异常 Pod" :value="data.total_abnormal" :value-style="data.total_abnormal ? { color: '#f56c6c' } : undefined" /></el-card></el-col>
      <el-col :span="3"><el-card shadow="never"><el-statistic title="未就绪应用" :value="data.total_not_ready_deploys" :value-style="data.total_not_ready_deploys ? { color: '#e6a23c' } : undefined" /></el-card></el-col>
    </el-row>

    <!-- 集群卡片（等高：行内最高的卡片撑齐，异常节点名单截断 + 悬浮看全量） -->
    <el-row :gutter="16" class="stat-row">
      <el-col v-for="c in data.clusters" :key="c.name" :xs="24" :sm="12" :lg="8" class="cl-col">
        <el-card shadow="never" class="cl-card">
          <div class="cl-head">
            <!-- 显示名与集群下拉同口径：纳管时填了描述显示「描述(名称)」，否则名称 -->
            <span class="cl-name">{{ c.description ? `${c.description}(${c.name})` : c.name }}</span>
            <el-tag :type="statusColor(c.status)" size="small">{{ c.status }}</el-tag>
            <span class="cl-ver">{{ c.version }}</span>
            <!-- 权限不全：标注下面的计数 / 清单只是可见范围（监控仪表为全集群口径） -->
            <el-tag v-if="c.scope_all === false" size="small" type="info" effect="plain" class="scope-tag">清单为可见范围</el-tag>
          </div>
          <div class="cl-nums">
            <div>节点 <b>{{ c.node_ready }}</b>/<b>{{ c.node_count }}</b></div>
            <div>命名空间 <b>{{ c.ns_count }}</b></div>
            <div>
              Pod <b>{{ c.pod_count }}</b><span v-if="c.pod_abnormal_total" class="bad-num">（{{ c.pod_abnormal_total }} 异常）</span>
            </div>
            <div>
              应用 <b>{{ c.deploy_count }}</b><span v-if="c.deploy_not_ready_total" class="warn-num">（{{ c.deploy_not_ready_total }} 未就绪）</span>
            </div>
          </div>
          <!-- 资源用度：汽车仪表盘样式，分档变色（<60 蓝 / 60-80 橙 / >80 红） -->
          <div class="cl-gauges">
            <div class="gauge">
              <el-progress type="dashboard" :percentage="pct(c.cpu_req_milli, c.cpu_alloc_milli)"
                :width="104" :stroke-width="9" :color="barColor">
                <template #default="{ percentage }">
                  <div class="g-pct">{{ percentage }}<span class="g-unit">%</span></div>
                  <div class="g-label">CPU</div>
                </template>
              </el-progress>
              <div class="g-val">{{ cores(c.cpu_req_milli) }} / {{ cores(c.cpu_alloc_milli) }}</div>
            </div>
            <div class="gauge">
              <el-progress type="dashboard" :percentage="pct(c.mem_req_bytes, c.mem_alloc_bytes)"
                :width="104" :stroke-width="9" :color="barColor">
                <template #default="{ percentage }">
                  <div class="g-pct">{{ percentage }}<span class="g-unit">%</span></div>
                  <div class="g-label">内存</div>
                </template>
              </el-progress>
              <div class="g-val">{{ gi(c.mem_req_bytes) }} / {{ gi(c.mem_alloc_bytes) }}</div>
            </div>
          </div>
          <!-- 权限不全时单独展示可见范围用量，与全集群仪表盘区分口径 -->
          <div v-if="c.scope_all === false" class="cl-myscope">
            我的可见命名空间：{{ cores(c.my_cpu_req_milli) }} / {{ gi(c.my_mem_req_bytes) }}
            <span class="dim">（占集群已申请 CPU {{ pct(c.my_cpu_req_milli, c.cpu_req_milli) }}% · 内存 {{ pct(c.my_mem_req_bytes, c.mem_req_bytes) }}%）</span>
          </div>
          <div v-if="usageHint(c)" class="cl-hint">{{ usageHint(c) }}</div>
          <div v-if="c.node_not_ready?.length" class="cl-bad-nodes">
            <span>NotReady：</span>
            <el-tooltip :content="c.node_not_ready.join('、')" placement="top" :disabled="c.node_not_ready.length <= 2">
              <span class="bad-nodes-inner">
                <el-tag v-for="n in c.node_not_ready.slice(0, 2)" :key="n" type="danger" size="small" class="node-tag">{{ n }}</el-tag>
                <span v-if="c.node_not_ready.length > 2" class="dim">等 {{ c.node_not_ready.length }} 个</span>
              </span>
            </el-tooltip>
          </div>
        </el-card>
      </el-col>
    </el-row>

    <!-- 异常 Pod + Warning 事件 -->
    <el-row :gutter="16" class="stat-row">
      <el-col :xs="24" :lg="14">
        <el-card shadow="never">
          <template #header><span>异常 Pod（可见范围内）</span></template>
          <el-table v-if="abnormalRows.length" :data="abnormalRows" size="small" max-height="360" row-class-name="clickable" @row-click="goPod">
            <el-table-column prop="cluster" label="集群" width="100" show-overflow-tooltip />
            <el-table-column prop="namespace" label="命名空间" min-width="110" show-overflow-tooltip />
            <el-table-column prop="name" label="Pod" min-width="180" show-overflow-tooltip />
            <el-table-column label="原因" width="170">
              <template #default="{ row }">
                <el-tag size="small" :type="reasonColor(row.reason)">{{ row.reason }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="ready" label="就绪" width="70" />
            <el-table-column prop="restarts" label="重启" width="70" />
            <el-table-column label="启动时间" width="150">
              <template #default="{ row }">{{ fmtTime(row.start_time) }}</template>
            </el-table-column>
          </el-table>
          <el-empty v-else description="无异常 Pod" :image-size="60" />
        </el-card>
      </el-col>
      <el-col :xs="24" :lg="10">
        <el-card shadow="never">
          <template #header>
            <span>Warning 事件（近 24 小时{{ data.warnings?.more ? '，超 300 条仅统计前 300' : '' }}）</span>
            <el-tag v-if="data.warnings?.total" type="danger" size="small" style="margin-left: 8px">{{ data.warnings.total }}{{ data.warnings.more ? '+' : '' }}</el-tag>
          </template>
          <el-table v-if="data.warnings?.items?.length" :data="data.warnings.items" size="small" max-height="360">
            <el-table-column label="时间" width="140">
              <template #default="{ row }">{{ fmtTime(row.last_time) }}</template>
            </el-table-column>
            <el-table-column prop="cluster" label="集群" width="90" show-overflow-tooltip />
            <el-table-column label="对象" min-width="150" show-overflow-tooltip>
              <template #default="{ row }">{{ row.namespace ? row.namespace + ' / ' : '' }}{{ row.object }}</template>
            </el-table-column>
            <el-table-column prop="reason" label="原因" width="130" show-overflow-tooltip />
            <el-table-column prop="message" label="内容" min-width="200" show-overflow-tooltip />
          </el-table>
          <el-empty v-else description="近 24 小时无 Warning 事件" :image-size="60" />
        </el-card>
      </el-col>
    </el-row>

    <!-- 未就绪应用 + 命名空间 Top N -->
    <el-row :gutter="16">
      <el-col :xs="24" :lg="14">
        <el-card shadow="never">
          <template #header><span>未就绪应用（可见范围内）</span></template>
          <el-table v-if="deployRows.length" :data="deployRows" size="small" max-height="320" row-class-name="clickable" @row-click="goDeploy">
            <el-table-column prop="cluster" label="集群" width="100" show-overflow-tooltip />
            <el-table-column prop="namespace" label="命名空间" min-width="120" show-overflow-tooltip />
            <el-table-column prop="name" label="应用" min-width="200" show-overflow-tooltip />
            <el-table-column label="就绪/期望" width="110">
              <template #default="{ row }">
                <span class="warn-num">{{ row.ready }}</span> / {{ row.desired }}
              </template>
            </el-table-column>
          </el-table>
          <el-empty v-else description="所有应用均已就绪" :image-size="60" />
        </el-card>
      </el-col>
      <el-col :xs="24" :lg="10">
        <el-card shadow="never">
          <template #header><span>命名空间 Top {{ topNsLimit }}（按 Pod 数）</span></template>
          <el-table v-if="nsRows.length" :data="nsRows" size="small" max-height="320" row-class-name="clickable" @row-click="goNs">
            <el-table-column prop="cluster" label="集群" width="100" show-overflow-tooltip />
            <el-table-column prop="namespace" label="命名空间" min-width="140" show-overflow-tooltip />
            <el-table-column prop="pod_count" label="Pod 数" width="90" sortable />
            <el-table-column label="CPU 请求" width="100">
              <template #default="{ row }">{{ cores(row.cpu_request_milli) }}</template>
            </el-table-column>
            <el-table-column label="内存请求" width="100">
              <template #default="{ row }">{{ gi(row.mem_request_bytes) }}</template>
            </el-table-column>
          </el-table>
          <el-empty v-else description="无可见命名空间数据" :image-size="60" />
        </el-card>
      </el-col>
    </el-row>
    <AiDialog ref="inspectDlgRef" title="AI 巡检报告" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { MagicStick, Refresh } from '@element-plus/icons-vue'
import { dashboard, aiInspect } from '../api'
import AiDialog from '../components/AiDialog.vue'
import { statusColor } from '../utils/format'

const router = useRouter()
const loading = ref(false)
const data = ref<any>({ clusters: [], warnings: { items: [] } })
let timer: number | null = null
const topNsLimit = 10

async function load(refresh = false) {
  loading.value = true
  try {
    data.value = (await dashboard(refresh)).data
  } catch { /* http 层已提示 */ } finally { loading.value = false }
}

onMounted(() => {
  load()
  timer = window.setInterval(load, 30000)
})
onUnmounted(() => { if (timer) clearInterval(timer) })

// ---------- 展示聚合 ----------
const genTime = computed(() => data.value.generated_at ? new Date(data.value.generated_at).toLocaleString() : '—')
// 异常 Pod / 未就绪应用 / 命名空间 Top：拍平各集群列表，供下方表格
const abnormalRows = computed(() =>
  (data.value.clusters || []).flatMap((c: any) => (c.pod_abnormal || []).map((p: any) => ({ cluster: c.name, ...p }))))
const deployRows = computed(() =>
  (data.value.clusters || []).flatMap((c: any) => (c.deploys_not_ready || []).map((d: any) => ({ cluster: c.name, ...d }))))
const nsRows = computed(() =>
  (data.value.clusters || []).flatMap((c: any) => (c.top_namespaces || []).map((n: any) => ({ cluster: c.name, ...n }))))

// 用量卡片下方的状态提示：采集失败 > 未设置 requests，都正常则不显示
function usageHint(c: any): string {
  if (c.collect_errors?.includes('pods')) return 'Pod 指标采集失败，用量与异常暂不可用'
  if (c.collect_errors?.includes('nodes')) return '节点指标采集失败，可分配量暂不可用'
  if (c.collect_errors?.length) return `部分指标采集失败：${c.collect_errors.join('、')}`
  if (c.pod_count > 0 && !c.cpu_req_milli && !c.mem_req_bytes) {
    return '工作负载未设置资源请求（requests），用量无法统计'
  }
  return ''
}

// ---------- 格式化 ----------
function cores(milli: number): string {
  if (!milli) return '0'
  return (milli / 1000).toFixed(1).replace(/\.0$/, '') + ' 核'
}
function gi(bytes: number): string {
  if (!bytes) return '0'
  return (bytes / 1024 ** 3).toFixed(1).replace(/\.0$/, '') + ' Gi'
}
function pct(v: number, total: number): number {
  return total > 0 ? Math.min(100, Math.round((v / total) * 100)) : 0
}
const barColor = (p: number) => (p >= 80 ? '#f56c6c' : p >= 60 ? '#e6a23c' : '#409eff')
function reasonColor(reason: string): 'danger' | 'warning' | 'info' {
  if (/Crash|BackOff|Err|Failed|Error|Killed|Evicted/i.test(reason)) return 'danger'
  if (reason === 'Pending') return 'warning'
  return 'info'
}
function fmtTime(t: string): string {
  return t ? new Date(t).toLocaleString() : '—'
}

// ---------- AI 巡检（只读：上下文与总览页同源，权限同口径） ----------
const inspectDlgRef = ref<InstanceType<typeof AiDialog>>()
function runInspect() {
  inspectDlgRef.value?.start((onDelta, signal) => aiInspect(onDelta, signal))
}

// ---------- 跳转（资源页深链：cluster/ns/resource/search） ----------
// 异常 Pod：搜索词带上异常原因（后端 matchSearch 对 pods 支持 waiting 原因 / status.reason /
// phase），落地即只剩该原因的 Pod；原因词与 Pod 名等兜底搜索是 OR 关系，直观可改
function goPod(row: any) {
  router.push({ path: '/resources', query: { cluster: row.cluster, ns: row.namespace, resource: 'pods', search: row.reason } })
}
function goDeploy(row: any) {
  // search=name 定位应用（关闭抽屉后列表也只剩它），detail=name 直达详情抽屉——
  // 默认落「运行实例」tab，直接看是哪些实例未就绪，少一次点击
  router.push({ path: '/resources', query: { cluster: row.cluster, ns: row.namespace, resource: 'deployments', search: row.name, detail: row.name } })
}
function goNs(row: any) {
  router.push({ path: '/resources', query: { cluster: row.cluster, ns: row.namespace, resource: 'pods' } })
}
</script>

<style scoped>
.top-bar { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
.sub { color: #909399; font-size: 12px; }
.stat-row { margin-bottom: 16px; }
/* 行内卡片等高：el-col 纵向 flex + 卡片 flex:1，避免异常节点多时参差不齐 */
.cl-col { display: flex; flex-direction: column; }
.cl-card { flex: 1; margin-bottom: 16px; }
.cl-head { display: flex; align-items: center; gap: 8px; }
.cl-name { font-weight: 600; font-size: 14px; }
.cl-ver { color: #909399; font-size: 12px; margin-left: auto; }
.scope-tag { flex: none; }
.cl-myscope { margin-top: 8px; font-size: 12.5px; color: #606266; }
.cl-nums { display: flex; flex-wrap: wrap; gap: 6px 18px; margin: 10px 0 4px; font-size: 13px; color: #606266; }
.bad-num { color: #f56c6c; font-size: 12px; }
.warn-num { color: #e6a23c; font-size: 12px; }
.cl-gauges { display: flex; justify-content: space-around; margin-top: 4px; }
.gauge { text-align: center; }
.g-pct { font-size: 20px; font-weight: 600; line-height: 1.2; }
.g-unit { font-size: 12px; font-weight: normal; margin-left: 1px; }
.g-label { font-size: 12px; color: #909399; margin-top: 2px; }
.g-val { font-size: 12px; color: #909399; margin-top: 2px; white-space: nowrap; }
.cl-hint { margin-top: 8px; font-size: 12px; color: #e6a23c; }
.cl-bad-nodes { margin-top: 8px; font-size: 12px; color: #f56c6c; display: flex; align-items: center; flex-wrap: nowrap; overflow: hidden; }
.bad-nodes-inner { display: inline-flex; align-items: center; gap: 4px; min-width: 0; }
.node-tag { max-width: 130px; }
.dim { color: #909399; font-size: 12px; }
:deep(.clickable) { cursor: pointer; }
</style>
