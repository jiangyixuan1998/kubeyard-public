<template>
  <el-card shadow="never">
    <div class="grants-layout">
      <!-- 左：用户列表（搜索 + 点选） -->
      <div class="user-panel">
        <el-input v-model="userSearch" placeholder="搜索用户名 / 姓名" clearable :prefix-icon="Search" />
        <div class="user-list" v-loading="usersLoading">
          <div v-for="u in filteredUsers" :key="u.id" class="user-item"
            :class="{ active: u.id === userId }" @click="selectUser(u.id)">
            <el-avatar :size="30" class="user-avatar"
              :style="{ background: u.id === userId ? 'var(--el-color-primary)' : 'var(--el-fill-color-dark)' }">
              {{ (u.username || '?').slice(0, 1).toUpperCase() }}
            </el-avatar>
            <div class="user-meta">
              <div class="user-name">
                {{ u.username }}
                <el-tag v-if="u.role === 'SuperAdmin'" size="small" type="danger">超管</el-tag>
                <el-tag v-else-if="u.status !== 1" size="small" type="info">禁用</el-tag>
              </div>
              <div class="user-sub">{{ u.display_name || u.role }}</div>
            </div>
          </div>
          <el-empty v-if="!filteredUsers.length" description="无匹配用户" :image-size="48" />
        </div>
      </div>

      <!-- 右：授权编辑 -->
      <div class="grant-panel" v-loading="loading">
        <template v-if="!userId">
          <div class="grant-placeholder">
            <el-empty description="从左侧选择用户，配置其集群权限" />
          </div>
        </template>

        <template v-else-if="currentUser?.role === 'SuperAdmin'">
          <div class="grant-placeholder">
            <el-result icon="warning" title="超级管理员无需配置授权" sub-title="该账号拥有平台全部权限" />
          </div>
        </template>

        <template v-else>
          <!-- 用户信息头 -->
          <div class="grant-head">
            <el-avatar :size="40" class="grant-avatar">{{ initial }}</el-avatar>
            <div class="grant-info">
              <div class="grant-title">
                {{ currentUser?.username }}
                <el-tag size="small" :type="currentUser?.status !== 1 ? 'info' : ''">
                  {{ currentUser?.status !== 1 ? '已禁用' : currentUser?.role }}
                </el-tag>
              </div>
              <div class="grant-sub">{{ currentUser?.display_name || '未设置姓名' }}<template v-if="currentUser?.email"> · {{ currentUser.email }}</template></div>
            </div>
            <div v-if="userGroups.length" class="grant-groups">
              <span class="groups-label">权限组</span>
              <el-tooltip v-for="g in userGroups" :key="g.id" :content="g.description || g.name" placement="top">
                <el-tag type="warning" size="small">{{ g.name }}</el-tag>
              </el-tooltip>
            </div>
          </div>
          <div v-if="userGroups.length" class="dim union-hint">最终权限 = 权限组 ∪ 直接授权（组成员在「权限组」页面管理）</div>

          <el-alert type="info" :closable="false" class="five-alert"
            title="五元组：集群 × 命名空间 × 资源 × 动作（均可多选），update 可细化到字段；保存后该用户现有会话将强制重新登录。" />

          <!-- 规则列表 -->
          <div class="rules-head">
            <span class="rules-count">直接授权 <b>{{ rows.length }}</b> 条<template v-if="expiredCount">，其中 {{ expiredCount }} 条已过期</template><template v-if="expiringCount">，{{ expiringCount }} 条即将到期</template></span>
            <el-popover placement="bottom-start" width="380" trigger="click">
              <template #reference>
                <el-button link type="primary" size="small">预设说明</el-button>
              </template>
              <div class="preset-doc">
                <p><b>只读</b>：查看 + 日志</p>
                <p><b>开发</b>：常规增删改 + 终端 + 日志下载</p>
                <p><b>管理员</b>：全部资源全部动作——命名空间选「*」即<b>集群管理员</b>（该集群全权），选具体命名空间即<b>命名空间管理员</b>（仅管理这些命名空间内资源）。</p>
                <p>预设按所选资源自动裁剪：Pod 专属动词（终端/日志/文件）仅含 pods 的规则保留，细粒度动作仅保留所选资源支持的项。</p>
                <p>动作含 update 且规则为单一资源时可细化「可修改字段」；启动/停止为平台功能（停止缩容为 0，启动恢复原副本数）；自定义字段路径（含 . ）可直接输入放行。</p>
              </div>
            </el-popover>
          </div>

          <el-empty v-if="!rows.length" description="暂无直接授权，点击下方「添加规则」新增" :image-size="70" />
          <RuleCard v-for="(row, i) in rows" :key="row._id" :rule="row" :index="i" :clusters="clusters"
            :ns-cache="nsCache" with-expiry
            @update:rule="patchRow(row, $event)" @preset="applyPresetRow(row, $event)" @remove="removeRow(i)" />

          <!-- 底部操作条（规则多时吸底） -->
          <div class="action-bar">
            <span class="dim">多集群的同形规则保存时按集群展开，重开自动合并展示</span>
            <div style="flex: 1"></div>
            <el-button @click="addRow"><el-icon><Plus /></el-icon>添加规则</el-button>
            <el-button type="primary" :loading="saving" @click="doSave">保存授权</el-button>
          </div>
        </template>
      </div>
    </div>
  </el-card>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Search } from '@element-plus/icons-vue'
import { listUsers, listClusters, listNamespaces, getUserGrants, putUserGrants, getUserGroups } from '../../api'
import { errMsg } from '../../api/http'
import RuleCard from '../../components/RuleCard.vue'
import { newRuleRow, mergeBackendRules, expandRows, applyPreset, validateRow, isExpired, expiryStateOf, loadPermPresets } from '../../utils/grant'

const users = ref<any[]>([])
const clusters = ref<any[]>([])
const userId = ref<number | ''>('')
const rows = ref<any[]>([])
const userGroups = ref<any[]>([])
const usersLoading = ref(false)
const loading = ref(false)
const saving = ref(false)
const nsCache = ref<Record<number, any[]>>({})
const userSearch = ref('')

const currentUser = computed(() => users.value.find((u) => u.id === userId.value))
const initial = computed(() => (currentUser.value?.username || '?').slice(0, 1).toUpperCase())
const expiredCount = computed(() => rows.value.filter(isExpired).length)
// 7 天内到期的规则数（ExpiryTag 同一状态机）
const expiringCount = computed(() => rows.value.filter((r) => expiryStateOf(r.expiresAt) === 'soon').length)

// 左侧列表：按用户名 / 显示名过滤
const filteredUsers = computed(() => {
  const kw = userSearch.value.trim().toLowerCase()
  if (!kw) return users.value
  return users.value.filter((u: any) =>
    (u.username || '').toLowerCase().includes(kw) || (u.display_name || '').toLowerCase().includes(kw))
})

// 规则是否有未保存修改：RuleCard 的 patch / 新增行都置位，加载与保存成功后复位。
// 切换用户前据此确认，避免静默丢弃（与 Groups 页同口径，外部评审核实）
let grantsDirty = false

async function selectUser(id: number) {
  if (userId.value === id) return
  if (grantsDirty) {
    try {
      await ElMessageBox.confirm(`「${currentUser.value?.username || '当前用户'}」的授权修改尚未保存，切换后将丢失。确认离开？`, '未保存的修改', {
        type: 'warning', confirmButtonText: '放弃修改并切换', cancelButtonText: '取消',
      })
    } catch { return }
  }
  userId.value = id
  loadGrants()
}

// 局部更新（RuleCard 报 patch，父级落到行对象上）
function patchRow(row: any, patch: Record<string, any>) {
  Object.assign(row, patch)
  grantsDirty = true
}

async function load() {
  usersLoading.value = true
  try {
    users.value = (await listUsers()).data
    clusters.value = (await listClusters()).data
  } finally { usersLoading.value = false }
}

// 请求序号防竞态（外部评审核实：快速切换用户时前一个较晚返回的 getUserGrants
// 会把 A 的规则渲染到 B 名下，保存即写错授权对象）
let grantsSeq = 0
async function loadGrants() {
  if (!userId.value) { rows.value = []; userGroups.value = []; return }
  const seq = ++grantsSeq
  const uid = userId.value
  loading.value = true
  try {
    const list = (await getUserGrants(uid)).data || []
    const groups = (await getUserGroups(uid)).data || []
    if (seq !== grantsSeq || uid !== userId.value) return // 已切换到其他用户：丢弃过期结果
    userGroups.value = groups
    // 同形规则（ns/资源/动作/时限/字段全同）按集群合并为一行展示，编辑时展开
    rows.value = mergeBackendRules(list, true)
    grantsDirty = false // 服务端状态加载完成：未保存标记复位
    rows.value.forEach((r) => r.clusterIds.forEach(loadNSBy))
  } catch { if (seq === grantsSeq && uid === userId.value) rows.value = [] } finally {
    if (seq === grantsSeq) loading.value = false
  }
}

// 按集群加载 namespace 列表（带缓存）
async function loadNSBy(cid: number) {
  if (!cid || nsCache.value[cid]) return
  const name = clusters.value.find((c: any) => c.id === cid)?.name
  if (!name) return
  try {
    const res = await listNamespaces(name, { _silent: 1 })
    nsCache.value[cid] = (res.data || []).map((n: any) => ({ name: n.name, alias: n.alias || '' })).sort((a: any, b: any) => a.name.localeCompare(b.name))
  } catch {
    nsCache.value[cid] = []
  }
}

function addRow() {
  rows.value.push(newRuleRow(clusters.value[0]?.id))
  grantsDirty = true
  if (clusters.value[0]) loadNSBy(clusters.value[0].id)
}

// 预设/删行同样计入未保存修改
function applyPresetRow(row: any, kind: any) {
  applyPreset(row, kind)
  grantsDirty = true
}
function removeRow(i: number) {
  rows.value.splice(i, 1)
  grantsDirty = true
}

async function doSave() {
  // 行内校验：多集群必须选、空值拦截
  for (let i = 0; i < rows.value.length; i++) {
    const err = validateRow(rows.value[i])
    if (err) {
      ElMessage.warning(`第 ${i + 1} 条规则${err}`)
      return
    }
  }
  saving.value = true
  try {
    // 多集群行展开为多条规则（每集群一条）
    const grants = expandRows(rows.value, (r) => ({
      expires_at: r.expiresAt instanceof Date ? r.expiresAt.toISOString() : undefined, // 空=永久
    }))
    await putUserGrants(userId.value as number, grants)
    ElMessage.success(`授权已保存（${grants.length} 条规则），用户需重新登录生效`)
    loadGrants()
  } catch (e) { ElMessage.error(errMsg(e)) } finally { saving.value = false }
}

onMounted(() => {
  loadPermPresets() // 动作目录 / 角色预设（系统设置可配置；失败回落内置）
  load()
})
</script>

<style scoped>
.grants-layout { display: flex; min-height: calc(100vh - 180px); }
.user-panel {
  width: 264px; flex: none;
  border-right: 1px solid var(--el-border-color-lighter);
  padding-right: 16px; margin-right: 20px;
}
.user-list { height: calc(100vh - 230px); overflow-y: auto; margin-top: 10px; }
.user-item {
  display: flex; align-items: center; gap: 10px;
  padding: 8px 10px; border-radius: 8px; cursor: pointer;
  transition: background .15s;
}
.user-item:hover { background: var(--el-fill-color-light); }
.user-item.active { background: var(--el-color-primary-light-9); }
.user-meta { min-width: 0; flex: 1; }
.user-name {
  font-size: 13px; color: var(--el-text-color-primary);
  display: flex; align-items: center; gap: 6px;
}
.user-sub {
  font-size: 12px; color: var(--el-text-color-secondary);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.grant-panel { flex: 1; min-width: 0; }
.grant-placeholder {
  min-height: calc(100vh - 240px);
  display: flex; align-items: center; justify-content: center;
}
.grant-head { display: flex; align-items: center; gap: 12px; }
.grant-avatar { flex: none; }
.grant-info { min-width: 0; }
.grant-title {
  font-size: 15px; font-weight: 600; color: var(--el-text-color-primary);
  display: flex; align-items: center; gap: 8px;
}
.grant-sub {
  font-size: 12px; color: var(--el-text-color-secondary);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.grant-groups {
  margin-left: auto; display: flex; align-items: center; gap: 6px;
  flex-wrap: wrap; justify-content: flex-end; max-width: 46%;
}
.groups-label { font-size: 12px; color: var(--el-text-color-secondary); }
.union-hint { margin: 6px 0 0 52px; }
.five-alert { margin: 12px 0; }
.rules-head {
  display: flex; align-items: center; justify-content: space-between;
  margin-bottom: 8px;
}
.rules-count { font-size: 13px; color: var(--el-text-color-regular); }
.preset-doc p { margin: 0 0 8px; font-size: 13px; line-height: 1.6; }
.preset-doc p:last-child { margin-bottom: 0; }
.dim { color: #909399; font-size: 12px; }
.action-bar {
  position: sticky; bottom: 0; z-index: 1;
  display: flex; align-items: center; gap: 10px;
  margin: 16px -20px -20px; padding: 12px 20px;
  background: var(--el-bg-color);
  border-top: 1px solid var(--el-border-color-lighter);
}
</style>
