<template>
  <el-card shadow="never">
    <el-alert v-if="!isSuper" type="info" :closable="false" style="margin-bottom: 12px"
      title="当前为管理员范围视图：仅显示您管辖的集群（集群管理员）或授权命名空间（命名空间管理员）内的操作记录。" />
    <div class="toolbar">
      <el-input v-model="filter.user" placeholder="姓名/账号/邮箱（模糊）" style="width: 190px" clearable @change="reload" />
      <el-input v-model="filter.q" placeholder="关键词：动作/对象/详情…模糊" style="width: 220px" clearable @change="reload" />
      <el-select v-model="filter.cluster" placeholder="集群" style="width: 160px" clearable filterable @change="reload">
        <el-option v-for="c in clusters" :key="c.name" :label="clusterLabel(c)" :value="c.name" />
      </el-select>
      <el-select v-model="filter.status" placeholder="结果" style="width: 110px" clearable @change="reload">
        <el-option label="成功" value="success" />
        <el-option label="失败" value="failed" />
        <el-option label="拒绝" value="denied" />
      </el-select>
      <el-select v-model="filter.action" placeholder="动作" style="width: 180px" clearable filterable @change="reload">
        <el-option-group v-for="g in actionGroups" :key="g.label" :label="g.label">
          <el-option v-for="a in g.items" :key="a.value" :label="`${a.label}（${a.value}）`" :value="a.value" />
        </el-option-group>
      </el-select>
      <el-button @click="reload" :icon="Search">查询</el-button>
    </div>

    <el-table :data="logs" stripe border v-loading="loading" height="calc(100vh - 285px)">
      <el-table-column label="时间" width="96">
        <template #default="{ row }">
          <div class="t-date">{{ fmtDate(row.created_at) }}</div>
          <div class="t-time">{{ fmtClock(row.created_at) }}</div>
        </template>
      </el-table-column>
      <el-table-column label="用户" width="130">
        <template #default="{ row }">
          <!-- 姓名主行 + 账号弱化副行；用户被删时回退仅账号（LEFT JOIN display_name 为空） -->
          <div class="u-name">{{ row.display_name || row.username }}</div>
          <div class="u-account" :title="row.email ? `${row.username} · ${row.email}` : row.username">{{ row.username }}</div>
        </template>
      </el-table-column>
      <el-table-column label="动作" width="120">
        <template #default="{ row }">
          <span :title="row.action">{{ actionLabel(row.action) }}</span>
        </template>
      </el-table-column>
      <el-table-column label="位置" width="185">
        <template #default="{ row }">
          <template v-if="row.cluster || row.namespace">
            <div class="loc-cluster">{{ row.cluster || '-' }}</div>
            <div v-if="row.namespace" class="loc-ns">{{ row.namespace }}</div>
          </template>
          <span v-else class="dim2">平台</span>
        </template>
      </el-table-column>
      <el-table-column label="对象" min-width="200" show-overflow-tooltip>
        <template #default="{ row }">
          {{ [row.resource_type, row.resource_name].filter(Boolean).join('/') || '-' }}
        </template>
      </el-table-column>
      <el-table-column label="详情" min-width="220">
        <template #default="{ row }">
          <el-link v-if="row.detail" type="primary" :underline="false" class="detail-link" @click="showDetail(row)">
            {{ row.detail.includes('\n') ? '查看详情' : row.detail }}
          </el-link>
          <span v-else class="dim2">-</span>
        </template>
      </el-table-column>
      <el-table-column label="结果" width="76" align="center">
        <template #default="{ row }">
          <el-tag size="small" :type="statusType(row.status)" effect="light">{{ statusLabel(row.status) }}</el-tag>
        </template>
      </el-table-column>
    </el-table>

    <el-pagination style="margin-top: 12px; justify-content: flex-end" layout="total, prev, pager, next, sizes"
      :total="total" v-model:current-page="page" v-model:page-size="size" :page-sizes="[20, 50, 100]"
      @current-change="load" @size-change="load" />

    <!-- 详情弹窗 -->
    <el-dialog v-model="detailDlg" title="审计详情" width="680px">
      <template v-if="detailRow">
        <div class="detail-meta">
          <div>时间：{{ new Date(detailRow.created_at).toLocaleString('zh-CN', { hour12: false }) }}</div>
          <div>用户：{{ detailRow.display_name || detailRow.username }}（{{ detailRow.username }}{{ detailRow.email ? ' · ' + detailRow.email : '' }}）　动作：{{ actionLabel(detailRow.action) }}（{{ detailRow.action }}）　结果：
            <el-tag size="small" :type="statusType(detailRow.status)">{{ statusLabel(detailRow.status) }}</el-tag>
          </div>
          <div v-if="detailRow.cluster || detailRow.namespace">
            位置：{{ [detailRow.cluster, detailRow.namespace, detailRow.resource_type, detailRow.resource_name].filter(Boolean).join(' / ') }}
          </div>
        </div>
        <pre class="detail-pre">{{ detailRow.detail }}</pre>
      </template>
    </el-dialog>
  </el-card>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { Search } from '@element-plus/icons-vue'
import { listClusters, listAudits } from '../../api'
import { useAuthStore } from '../../stores/auth'

const isSuper = useAuthStore().role === 'SuperAdmin'

const clusters = ref<any[]>([])
const logs = ref<any[]>([])
const loading = ref(false)
const total = ref(0)
const page = ref(1)
const size = ref(20)
const filter = reactive({ user: '', q: '', cluster: '', action: '', status: '' })
const detailDlg = ref(false)
const detailRow = ref<any>(null)

function showDetail(row: any) {
  detailRow.value = row
  detailDlg.value = true
}

// 动作中文标签 + 分组（筛选项与表格共用；value 仍为后端 action 代码，
// 与后端 audit.Log 的调用点保持同步——漏一项就搜不到对应的审计记录）
const actionGroups = [
  { label: '会话', items: [
    { value: 'login', label: '登录' },
    { value: 'login.sso', label: 'SSO 登录' },
    { value: 'logout', label: '登出' },
  ] },
  { label: '集群资源操作', items: [
    { value: 'exec', label: '终端' },
    { value: 'logs', label: '日志' },
    { value: 'describe', label: 'Describe' },
    { value: 'download', label: '下载文件' },
    { value: 'upload', label: '上传文件' },
    { value: 'restart', label: '滚动重启' },
    { value: 'scale', label: '调整副本数' },
    { value: 'stop', label: '停止' },
    { value: 'start', label: '启动' },
    { value: 'apply', label: 'YAML 应用' },
    { value: 'apply_dry_run', label: 'YAML 预检' },
    { value: 'form_edit', label: '表单编辑' },
    { value: 'resource_get', label: '查看资源' },
    { value: 'resource_update', label: '更新资源' },
    { value: 'resource_delete', label: '删除资源' },
    { value: 'summary', label: '节点概要' },
    { value: 'node_cordon', label: '节点封锁/恢复调度' },
    { value: 'node_drain', label: '节点排空' },
    { value: 'image_history', label: '镜像历史' },
    { value: 'draft_stage', label: 'AI 草稿暂存' },
    { value: 'draft_discard', label: 'AI 草稿丢弃' },
  ] },
  { label: 'AI 功能', items: [
    { value: 'ai.config', label: 'AI 模型配置' },
    { value: 'ai.diagnose', label: 'AI 诊断' },
    { value: 'ai.logs', label: 'AI 日志分析' },
    { value: 'ai.recording', label: 'AI 录像摘要' },
  ] },
  { label: '定时任务', items: [
    { value: 'schedule.create', label: '创建定时任务' },
    { value: 'schedule.update', label: '更新定时任务' },
    { value: 'schedule.delete', label: '删除定时任务' },
    { value: 'schedule.restart', label: '定时任务执行（重启）' },
    { value: 'schedule.scale', label: '定时任务执行（扩缩容）' },
  ] },
  { label: '集群纳管', items: [
    { value: 'cluster_create', label: '纳管集群' },
    { value: 'cluster_update', label: '更新集群' },
    { value: 'cluster_delete', label: '移除集群' },
  ] },
  { label: '用户与权限', items: [
    { value: 'user_create', label: '新建用户' },
    { value: 'user_update', label: '更新用户' },
    { value: 'user_delete', label: '删除用户' },
    { value: 'grants_update', label: '授权变更' },
    { value: 'group_create', label: '新建权限组' },
    { value: 'group_update', label: '更新权限组' },
    { value: 'group_delete', label: '删除权限组' },
    { value: 'perm_preset_update', label: '更新权限预设' },
    { value: 'perm_preset_reset', label: '重置权限预设' },
    { value: 'perm_preset_sync', label: '同步权限预设' },
  ] },
  { label: '令牌与集成', items: [
    { value: 'token_create', label: '创建令牌' },
    { value: 'token_revoke', label: '吊销令牌' },
    { value: 'integ_grant', label: '集成下发授权' },
    { value: 'integ_revoke', label: '集成回收授权' },
    { value: 'integ_group_join', label: '集成入组' },
    { value: 'integ_group_leave', label: '集成移出组' },
    { value: 'integ_user_provision', label: '集成开通用户' },
    { value: 'integ_users_export', label: '集成导出用户' },
    { value: 'users_export', label: '导出用户' },
    { value: 'groups_export', label: '导出权限组' },
  ] },
  { label: 'LDAP / SSO', items: [
    { value: 'ldap_create', label: '新增 LDAP' },
    { value: 'ldap_update', label: '更新 LDAP' },
    { value: 'ldap_delete', label: '删除 LDAP' },
    { value: 'ldap_test', label: '测试 LDAP' },
    { value: 'oauth_config', label: 'SSO 配置' },
  ] },
]
const actionMap: Record<string, string> = Object.fromEntries(
  actionGroups.flatMap((g) => g.items.map((i) => [i.value, i.label])))
function actionLabel(action: string): string {
  return actionMap[action] || action
}

const statusLabels: Record<string, string> = { success: '成功', failed: '失败', denied: '拒绝' }
function statusLabel(s: string): string {
  return statusLabels[s] || s
}
function statusType(s: string): string {
  return s === 'success' ? 'success' : s === 'denied' ? 'warning' : 'danger'
}

function fmtDate(ts?: string): string {
  if (!ts) return '-'
  const d = new Date(ts)
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}
function fmtClock(ts?: string): string {
  if (!ts) return '-'
  const d = new Date(ts)
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}:${String(d.getSeconds()).padStart(2, '0')}`
}

async function load() {
  loading.value = true
  try {
    const params: any = { page: page.value, page_size: size.value }
    for (const k of Object.keys(filter) as (keyof typeof filter)[]) {
      if (filter[k]) params[k] = filter[k]
    }
    const res = await listAudits(params)
    logs.value = res.data.items || []
    total.value = res.data.total || 0
  } catch { /* http 层已提示 */ } finally { loading.value = false }
}

// 筛选条件变化/点查询：回第一页再拉（否则在第 N 页上改筛选，新结果集没有第 N 页会看到空表）
function reload() {
  page.value = 1
  load()
}

// 集群显示名：纳管时填了描述则显示「描述(名称)」，否则显示名称（与项目管理页同口径，可搜索别名）
function clusterLabel(c: any): string {
  const desc = (c.description || '').trim()
  return desc ? `${desc}(${c.name})` : c.name
}

onMounted(async () => {
  try { clusters.value = (await listClusters()).data } catch { /* 忽略 */ }
  load()
})
</script>

<style scoped>
.toolbar { display: flex; gap: 10px; margin-bottom: 12px; }
/* 时间两行：日期弱化 + 时间突出 */
.t-date { font-size: 11px; color: var(--el-text-color-placeholder); line-height: 1.4; }
.t-time { font-size: 13px; color: var(--el-text-color-primary); line-height: 1.4; font-variant-numeric: tabular-nums; }
/* 位置两行：集群 + 命名空间弱化 */
.loc-cluster { font-size: 13px; color: var(--el-text-color-primary); line-height: 1.4; }
.loc-ns { font-size: 12px; color: var(--el-text-color-secondary); line-height: 1.4; }
.dim2 { color: var(--el-text-color-placeholder); }
.detail-link {
  display: inline-block; max-width: 100%;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  vertical-align: bottom;
}
.detail-meta { color: #606266; font-size: 13px; line-height: 1.9; margin-bottom: 10px; }
/* 用户列两行：姓名主行 + 账号弱化副行 */
.u-name { font-size: 13px; color: var(--el-text-color-primary); line-height: 1.4; }
.u-account { font-size: 12px; color: var(--el-text-color-secondary); line-height: 1.4; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.detail-pre {
  background: #f5f7fa; border-radius: 4px; padding: 12px; font-size: 12px; line-height: 1.7;
  white-space: pre-wrap; word-break: break-all; max-height: 55vh; overflow: auto; margin: 0;
}
</style>
