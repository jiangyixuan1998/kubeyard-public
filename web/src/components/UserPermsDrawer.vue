<template>
  <el-drawer :model-value="modelValue" size="880px" @update:model-value="$emit('update:modelValue', $event)" @open="load">
    <template #header>
      <div class="drawer-head">
        <span>权限详情：{{ user?.username }}</span>
        <el-tag v-if="user" size="small" :type="user.role === 'SuperAdmin' ? 'danger' : 'info'">{{ user.role }}</el-tag>
        <span v-if="user?.display_name" class="dim">{{ user.display_name }}</span>
      </div>
    </template>
    <div v-loading="loading" class="perms-body">
      <!-- 目标用户是平台管理员：后端返回单条 * 规则，直接整段提示 -->
      <el-alert v-if="isSuper" type="success" :closable="false"
        title="平台管理员：拥有全部集群、全部资源的全部权限" />
      <template v-else-if="clusters.length">
        <div v-for="g in clusters" :key="g.cluster" class="perm-cluster">
          <div class="perm-cluster-title"><el-icon><Cpu /></el-icon>{{ g.label }}</div>
          <el-table :data="g.rows" size="small" border :row-class-name="rowClass">
            <el-table-column label="命名空间" width="110">
              <template #default="{ row }">
                <el-tag v-if="row.namespace === '*' || !row.namespace" size="small">全部</el-tag>
                <template v-else>
                  <el-tag v-for="n in splitList(row.namespace)" :key="n" size="small" type="info" class="tag-gap">{{ n }}</el-tag>
                </template>
              </template>
            </el-table-column>
            <el-table-column label="资源" min-width="150">
              <template #default="{ row }">
                <el-tag v-if="row.resources === '*' || !row.resources" size="small">全部</el-tag>
                <template v-else>
                  <el-tag v-for="r in splitList(row.resources)" :key="r" size="small" type="info" class="tag-gap">{{ resLabel(r) }}</el-tag>
                </template>
              </template>
            </el-table-column>
            <el-table-column label="动作" min-width="150">
              <template #default="{ row }">
                <el-tag v-if="row.verbs === '*' || !row.verbs" size="small" type="success">全部</el-tag>
                <template v-else>
                  <el-tag v-for="v in splitList(row.verbs)" :key="v" size="small" type="success" class="tag-gap">{{ verbLabel(v) }}</el-tag>
                </template>
              </template>
            </el-table-column>
            <el-table-column label="可修改字段" width="110">
              <template #default="{ row }">
                <span v-if="!hasUpdateVerb(row.verbs)" class="dim">—</span>
                <el-tag v-else-if="row.actions === '*' || !row.actions" size="small" type="warning">完整更新</el-tag>
                <template v-else>
                  <el-tag v-for="a in splitList(row.actions)" :key="a" size="small" type="warning" class="tag-gap">{{ actionLabel(a) }}</el-tag>
                </template>
              </template>
            </el-table-column>
            <el-table-column label="来源" width="130" show-overflow-tooltip>
              <template #default="{ row }">
                <el-tag size="small" :type="row.source?.startsWith('权限组') ? 'warning' : 'info'">{{ row.source || '直接授权' }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="授权 / 入组" width="150">
              <template #default="{ row }">
                <span v-if="!validDate(row.created_at)" class="dim">—</span>
                <span v-else>{{ fmtDate(row.created_at) }}</span>
              </template>
            </el-table-column>
            <el-table-column label="到期" width="170">
              <template #default="{ row }">
                <!-- 分色标签（红过期/黄将到期/绿有时限/红永久）+ 日期 -->
                <ExpiryTag :expires-at="row.expires_at" />
                <div v-if="row.expires_at" class="dim sub">{{ fmtDate(row.expires_at) }}</div>
              </template>
            </el-table-column>
          </el-table>
        </div>
        <div class="perms-tip">
          权限 = 集群 × 命名空间 × 资源 × 动作；「可修改字段」仅在动作含 update 时生效。
          「来源」区分直接授权与权限组；组权限的到期时间即该用户的成员关系时限。
        </div>
      </template>
      <el-empty v-else-if="!loading" description="该用户暂无任何授权（直接授权与权限组均无）" />
    </div>
  </el-drawer>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { getUserPerms } from '../api'
import { verbAliases, resourceAliases, actionLabelOf } from '../utils/grant'
import ExpiryTag from './ExpiryTag.vue'

const props = defineProps<{ modelValue: boolean; user: any }>()
defineEmits<{ (e: 'update:modelValue', v: boolean): void }>()

const isSuper = computed(() => props.user?.role === 'SuperAdmin')
const loading = ref(false)
const rows = ref<any[]>([])

// 按集群分组展示（与「我的权限」抽屉同一形态；标题用显示名「描述(名称)」，与集群下拉同口径）
const clusters = computed(() => {
  const map = new Map<string, any[]>()
  for (const r of rows.value) {
    const key = r.cluster || '未知集群'
    if (!map.has(key)) map.set(key, [])
    map.get(key)!.push(r)
  }
  return [...map.entries()].map(([cluster, rs]) => ({ cluster, label: rs[0]?.cluster_label || cluster, rows: rs }))
})

// 已过期行整体置灰（row-class-name 回调）
function rowClass({ row }: any): string {
  return row.expires_at && expired(row.expires_at) ? 'perm-expired-row' : ''
}

function splitList(v?: string): string[] {
  if (!v || v === '*') return []
  return v.split(',').map((s) => s.trim()).filter(Boolean)
}
function resLabel(r: string): string { return resourceAliases[r] || r }
function verbLabel(v: string): string { return verbAliases[v] || v }

// 可修改字段令牌的友好名（优先取系统设置维护的动作目录；字段路径原样显示）
function actionLabel(a: string): string { return a.includes('.') ? a : actionLabelOf(a) }

function hasUpdateVerb(verbs?: string): boolean {
  if (!verbs) return false
  return verbs === '*' || verbs.split(',').map((s) => s.trim()).includes('update')
}

// 存量数据的零值时间（GORM 零值 0001-01-01）当无值处理
function validDate(t?: string): boolean {
  return !!t && !t.startsWith('0001-')
}
function expired(t: string): boolean { return new Date(t).getTime() < Date.now() }
function fmtDate(t: string): string {
  return new Date(t).toLocaleString('zh-CN', { hour12: false })
}

async function load() {
  if (!props.user || isSuper.value) return // 超管固定提示，无需请求
  loading.value = true
  rows.value = []
  try { rows.value = (await getUserPerms(props.user.id)).data || [] } catch { rows.value = [] } finally { loading.value = false }
}
</script>

<style scoped>
.drawer-head { display: flex; align-items: center; gap: 10px; font-size: 16px; font-weight: 600; }
.dim { color: var(--el-text-color-placeholder); }
.perms-body { padding: 0 4px; }
.perm-cluster { margin-bottom: 18px; }
.perm-cluster-title {
  display: flex; align-items: center; gap: 6px;
  font-size: 14px; font-weight: 600; color: #1f2d3d; margin-bottom: 8px;
}
.tag-gap { margin: 2px 4px 2px 0; }
.sub { font-size: 12px; margin-top: 2px; }
.perms-tip {
  margin-top: 6px; font-size: 12px; color: var(--el-text-color-secondary); line-height: 1.6;
}
</style>

<style>
/* 已过期授权行整体置灰（el-table 行样式须全局，行元素不带 scoped data-v） */
.perm-expired-row { opacity: .55; }
</style>
