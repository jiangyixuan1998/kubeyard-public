<template>
  <el-drawer :model-value="modelValue" title="我的权限" size="880px"
    @update:model-value="$emit('update:modelValue', $event)" @open="load">
    <div v-loading="loading" class="perms-body">
      <!-- 平台管理员：后端返回单条 * 规则，直接整段提示 -->
      <el-alert v-if="isSuper" type="success" :closable="false"
        title="平台管理员：拥有全部集群、全部资源的全部权限" />
      <template v-else-if="groups.length">
        <div v-for="g in groups" :key="g.cluster" class="perm-cluster">
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
            <el-table-column label="来源" width="120" show-overflow-tooltip>
              <template #default="{ row }">
                <el-tag size="small" :type="row.source?.startsWith('权限组') ? 'warning' : 'info'">{{ row.source || '直接授权' }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="授权时间" width="150">
              <template #default="{ row }">
                <span v-if="!validDate(row.created_at)" class="dim">—</span>
                <span v-else>{{ fmtDate(row.created_at) }}</span>
              </template>
            </el-table-column>
            <el-table-column label="到期" width="170">
              <template #default="{ row }">
                <!-- 分色标签（红过期/黄将到期/绿有时限/红永久）+ 日期 + 周期 -->
                <ExpiryTag :expires-at="row.expires_at" />
                <template v-if="row.expires_at">
                  <div v-if="expired(row.expires_at)" class="dim sub">{{ fmtDate(row.expires_at) }} 止</div>
                  <div v-else class="dim sub">{{ fmtDate(row.expires_at) }}</div>
                  <div v-if="validDate(row.created_at)" class="dim sub">周期 {{ period(row) }}</div>
                </template>
              </template>
            </el-table-column>
          </el-table>
        </div>
        <div class="perms-tip">
          权限 = 集群 × 命名空间 × 资源 × 动作；「可修改字段」仅在动作含 update 时生效。
          已过期条目仅失去效力、授权关系保留——联系管理员改期即可续授，无需重新配置。
        </div>
        <!-- 存在到期/将到期条目：续期申请入口（可配置门户地址） -->
        <div v-if="hasExpiring" class="perms-renew">
          <span>你有即将到期或已到期的权限/账号{{ renewalPortalUrl ? '，请前往统一权限门户申请续期' : '，请联系平台管理员申请续期' }}。</span>
          <el-button v-if="renewalPortalUrl" size="small" type="warning" plain tag="a" :href="renewalPortalUrl" target="_blank" rel="noopener">前往统一权限门户申请</el-button>
        </div>
      </template>
      <el-empty v-else-if="!loading" description="暂无任何授权" />
    </div>
  </el-drawer>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { myGrants } from '../api'
import { useAuthStore } from '../stores/auth'
import { verbAliases, resourceAliases, actionLabelOf, expiryStateOf } from '../utils/grant'
import ExpiryTag from './ExpiryTag.vue'

defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void }>()

const auth = useAuthStore()
const isSuper = computed(() => auth.role === 'SuperAdmin')
const loading = ref(false)
const rows = ref<any[]>([])

// 按集群分组展示（标题用显示名「描述(名称)」，与集群下拉同口径；无描述时与名称相同）
const groups = computed(() => {
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

// 续期门户地址（系统设置 renewal.portal_url）：配置了才显示跳转按钮
const renewalPortalUrl = ref('')
onMounted(async () => {
  try {
    const { getSettings } = await import('../api')
    const m = (await getSettings()).data || {}
    const u = String(m['renewal.portal_url'] || '').trim()
    if (u) renewalPortalUrl.value = u
  } catch { /* 读取失败不显示入口 */ }
})

// 是否存在到期/将到期（<7 天）条目 → 显示续期申请入口（有效期排除零值占位）
const hasExpiring = computed(() => rows.value.some((r) => {
  if (!r.expires_at || !validDate(r.expires_at)) return false
  return expiryStateOf(r.expires_at) === 'expired' || expiryStateOf(r.expires_at) === 'soon'
}))

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
// 周期 = 授权时间 → 到期时间；无起点（存量组权限）显示 —
function period(row: any): string {
  if (!validDate(row.created_at) || !row.expires_at) return '—'
  const days = Math.ceil((new Date(row.expires_at).getTime() - new Date(row.created_at).getTime()) / 86400000)
  return days >= 1 ? `${days} 天` : '不足 1 天'
}

async function load() {
  if (isSuper.value) return // 超管固定提示，无需请求
  loading.value = true
  try { rows.value = (await myGrants()).data || [] } catch { rows.value = [] } finally { loading.value = false }
}
</script>

<style scoped>
.perms-body { padding: 0 4px; }
.perm-cluster { margin-bottom: 18px; }
.perm-cluster-title {
  display: flex; align-items: center; gap: 6px;
  font-size: 14px; font-weight: 600; color: #1f2d3d; margin-bottom: 8px;
}
.tag-gap { margin: 2px 4px 2px 0; }
.dim { color: var(--el-text-color-placeholder); }
.sub { font-size: 12px; margin-top: 2px; }
.perms-tip {
  margin-top: 6px; font-size: 12px; color: var(--el-text-color-secondary); line-height: 1.6;
}
/* 到期续期入口条：文案 + 申请按钮同行（窄屏换行） */
.perms-renew {
  display: flex; align-items: center; gap: 12px; flex-wrap: wrap;
  margin-top: 10px; padding: 10px 12px;
  border: 1px solid var(--el-color-warning-light-5); border-radius: 6px;
  background: var(--el-color-warning-light-9);
  font-size: 12px; color: var(--el-text-color-regular);
}
</style>

<style>
/* 已过期授权行整体置灰（el-table 行样式须全局，行元素不带 scoped data-v） */
.perm-expired-row { opacity: .55; }
</style>
