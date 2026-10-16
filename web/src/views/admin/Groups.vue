<template>
  <el-card shadow="never">
    <div class="groups-layout">
      <!-- 左：权限组列表 -->
      <div class="group-panel">
        <div class="panel-head">
          <el-input v-model="groupSearch" placeholder="搜索组名 / 描述" clearable :prefix-icon="Search" />
          <div style="display: flex; gap: 8px; margin-top: 10px">
            <el-button type="primary" style="flex: 1" @click="openCreate">
              <el-icon><Plus /></el-icon>新建权限组
            </el-button>
            <el-button :loading="exporting" @click="doExport"><el-icon><Download /></el-icon>导出</el-button>
          </div>
        </div>
        <div class="group-list" v-loading="loading">
          <div v-for="g in filteredGroups" :key="g.id" class="group-item"
            :class="{ active: g.id === editId }" @click="openEdit(g)">
            <div class="group-body">
              <div class="group-name">
                {{ g.name }}
                <el-tag v-if="g.id === editId && dirty" size="small" type="warning">未保存</el-tag>
              </div>
              <div class="group-sub">{{ g.description || '无描述' }}</div>
              <div class="group-stats">
                <span>{{ g.rule_count }} 条规则</span>
                <span>{{ g.user_count }} 名成员</span>
              </div>
            </div>
            <el-button link type="danger" size="small" class="group-del" @click.stop="doDelete(g)">
              <el-icon><Delete /></el-icon>
            </el-button>
          </div>
          <el-empty v-if="!filteredGroups.length" description="无匹配权限组" :image-size="48" />
        </div>
      </div>

      <!-- 右：组详情编辑 -->
      <div class="detail-panel" v-loading="detailLoading">
        <template v-if="!editId && !creating">
          <div class="detail-placeholder">
            <el-empty description="从左侧选择权限组，或点击「新建权限组」" />
          </div>
        </template>

        <template v-else>
          <!-- 组信息头 -->
          <div class="detail-head">
            <div class="detail-title">
              <el-input v-if="creating" v-model="form.name" placeholder="组名，如：测试团队-开发" style="width: 280px" maxlength="64" />
              <template v-else>
                <span class="detail-name">{{ form.name }}</span>
                <el-button link type="primary" size="small" @click="startRename" v-if="!renaming">
                  <el-icon><EditPen /></el-icon>重命名
                </el-button>
              </template>
            </div>
            <div v-if="renaming" class="rename-row">
              <el-input v-model="form.name" placeholder="组名" style="width: 280px" maxlength="64" />
              <el-button size="small" @click="cancelRename">取消</el-button>
              <el-button size="small" type="primary" @click="renameOk">确定</el-button>
            </div>
            <el-input v-model="form.description" placeholder="描述（可选）" maxlength="255" style="width: 480px" />
          </div>

          <div class="dim union-hint">用户加入组即获得组内全部规则（与个人直接授权取并集）；成员到期自动失去该组权限，规则长期复用。</div>

          <!-- 成员管理 -->
          <el-divider content-position="left">成员（{{ form.user_ids.length }} 人）</el-divider>
          <el-transfer v-model="form.user_ids" :data="userTransferData" filterable
            :titles="['待选用户', '组成员']" :button-texts="['移出', '加入']"
            placeholder="搜索用户名 / 显示名" />
          <!-- 成员时限：绑定在「用户入组」上（工单四档快捷），空=永久 -->
          <template v-if="form.user_ids.length">
            <div class="members-head">
              <span class="members-label">成员时限（新加入默认 30 天，清空 = 永久）：</span>
              <el-button size="small" link type="primary" @click="setAllMembersExpiry(7)">全部 7 天</el-button>
              <el-button size="small" link type="primary" @click="setAllMembersExpiry(30)">全部 30 天</el-button>
              <el-button size="small" link type="primary" @click="setAllMembersExpiry(90)">全部 90 天</el-button>
              <el-button size="small" link type="primary" @click="setAllMembersExpiry(null)">全部清空（永久）</el-button>
            </div>
            <el-table :data="memberRows" size="small" border max-height="240">
              <el-table-column label="成员" min-width="200">
                <template #default="{ row }">{{ memberLabel(row.id) }}</template>
              </el-table-column>
              <el-table-column label="到期时间" width="260">
                <template #default="{ row }">
                  <el-date-picker v-model="row.expiresAt" type="datetime" placeholder="永久" clearable
                    format="YYYY-MM-DD HH:mm" :shortcuts="expiryShortcuts" style="width: 210px" />
                </template>
              </el-table-column>
              <el-table-column width="110">
                <template #default="{ row }">
                  <!-- 分色口径：已过期红 / 剩<7天黄「即将到期」/ 有时限绿「剩 N 天」/ 永久红（永久授权需审视） -->
                  <ExpiryTag :expires-at="row.expiresAt" />
                </template>
              </el-table-column>
            </el-table>
          </template>

          <!-- 授权规则 -->
          <el-divider content-position="left">授权规则（{{ rules.length }} 条）</el-divider>
          <el-empty v-if="!rules.length" description="暂无规则，点击下方「添加规则」新增" :image-size="60" />
          <RuleCard v-for="(row, i) in rules" :key="row._id" :rule="row" :index="i" :clusters="clusters"
            :ns-cache="nsCache"
            @update:rule="Object.assign(row, $event)" @preset="applyPreset(row, $event)" @remove="rules.splice(i, 1)" />

          <el-popover placement="top-start" width="380" trigger="click">
            <template #reference>
              <el-button link type="primary" size="small" style="margin: 4px 0 0 4px">预设说明</el-button>
            </template>
            <div class="preset-doc">
              <p><b>只读</b>：查看 + 日志</p>
              <p><b>开发</b>：常规增删改 + 终端 + 日志下载</p>
              <p><b>管理员</b>：全部资源全部动作——命名空间选「*」即<b>集群管理员</b>（该集群全权），选具体命名空间即<b>命名空间管理员</b>（仅管理这些命名空间内资源）。</p>
              <p>预设按所选资源自动裁剪：Pod 专属动词（终端/日志/文件）仅含 pods 的规则保留，细粒度动作仅保留所选资源支持的项。</p>
            </div>
          </el-popover>

          <!-- 底部操作条（吸底） -->
          <div class="action-bar">
            <el-button @click="addRule"><el-icon><Plus /></el-icon>添加规则</el-button>
            <div style="flex: 1"></div>
            <el-button v-if="!creating" @click="openEdit(currentGroupRow)">重置</el-button>
            <el-button type="primary" :loading="saving" @click="doSave">
              {{ creating ? '创建权限组' : '保存修改' }}
            </el-button>
          </div>
        </template>
      </div>
    </div>
  </el-card>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Search, Delete, EditPen, Download } from '@element-plus/icons-vue'
import { listUsers, listClusters, listNamespaces, listGroups, getGroup, createGroup, updateGroup, deleteGroup } from '../../api'
import { errMsg } from '../../api/http'
import { downloadGet } from '../../utils/download'
import RuleCard from '../../components/RuleCard.vue'
import ExpiryTag from '../../components/ExpiryTag.vue'
import { newRuleRow, mergeBackendRules, expandRows, applyPreset, validateRow, expiryShortcuts, loadPermPresets } from '../../utils/grant'

const groups = ref<any[]>([])
const users = ref<any[]>([])
const clusters = ref<any[]>([])
const loading = ref(false)
const detailLoading = ref(false)
const saving = ref(false)
const exporting = ref(false)

function doExport() {
  exporting.value = true
  downloadGet('/admin/groups/export', 'kubeyard-groups').finally(() => { exporting.value = false })
}
const editId = ref<number | null>(null)
const creating = ref(false)
const renaming = ref(false)
const dirty = ref(false)
const groupSearch = ref('')
const nsCache = ref<Record<number, any[]>>({})

const form = reactive({ name: '', description: '', user_ids: [] as number[] })
const rules = ref<any[]>([])

// 成员时限：绑定「用户入组」（id → 到期时间）；随穿梭框增删成员自动同步。
// 新加入成员默认 30 天（与工单平台时限习惯一致），需要长期的清空或改期即可
const memberRows = ref<{ id: number; expiresAt: Date | null }[]>([])
watch(() => form.user_ids, (ids) => {
  const old = new Map(memberRows.value.map((r) => [r.id, r]))
  memberRows.value = ids.map((id) => old.get(id) || { id, expiresAt: addDays(30) })
}, { deep: true })
function memberLabel(id: number): string {
  const u = users.value.find((x: any) => x.id === id)
  return u ? `${u.username}（${u.display_name || u.role}）` : `#${id}`
}

// 批量设置成员时限（天）；null = 清空为永久
function setAllMembersExpiry(days: number | null) {
  for (const r of memberRows.value) {
    r.expiresAt = days ? addDays(days) : null
  }
}
function addDays(n: number): Date {
  const d = new Date()
  d.setDate(d.getDate() + n)
  return d
}

// 成员穿梭框数据（排除已禁用账号；status 为数值 1=启用 0=禁用，外部评审核实此前按字符串比较失效）
const userTransferData = computed(() => users.value
  .filter((u: any) => u.status === 1)
  .map((u: any) => ({ key: u.id, label: `${u.username}（${u.display_name || u.role}）` })))

// 左侧列表：按组名 / 描述过滤
const filteredGroups = computed(() => {
  const kw = groupSearch.value.trim().toLowerCase()
  if (!kw) return groups.value
  return groups.value.filter((g: any) =>
    (g.name || '').toLowerCase().includes(kw) || (g.description || '').toLowerCase().includes(kw))
})

const currentGroupRow = computed(() => groups.value.find((g: any) => g.id === editId.value))

// 任何编辑痕迹都标记未保存；openEdit/openCreate 程序化填充数据时抑制（否则加载即「未保存」）
let suppressDirty = false
watch([() => form.name, () => form.description, () => [...form.user_ids], () => form.user_ids.map((id) => memberRows.value.find((r) => r.id === id)?.expiresAt?.getTime() || 0), rules], () => {
  if (!suppressDirty) dirty.value = true
}, { deep: true })

async function load() {
  loading.value = true
  try {
    groups.value = (await listGroups()).data || []
  } catch { /* http 层已提示 */ } finally { loading.value = false }
}

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

function addRule() {
  rules.value.push(newRuleRow(clusters.value[0]?.id))
  if (clusters.value[0]) loadNSBy(clusters.value[0].id)
}

// 切换前确认：有未保存修改时提示（外部评审核实：此前直接丢弃无任何提示）
async function confirmDiscard(): Promise<boolean> {
  if (!dirty.value) return true
  try {
    await ElMessageBox.confirm('当前权限组有未保存的修改，切换后将丢失。确认离开？', '未保存的修改', {
      type: 'warning', confirmButtonText: '放弃修改并切换', cancelButtonText: '取消',
    })
    return true
  } catch { return false }
}

async function openCreate() {
  if (!(await confirmDiscard())) return
  editSeq++ // 使在途的组详情响应失效，避免覆盖新建表单
  suppressDirty = true
  editId.value = null
  creating.value = true
  renaming.value = false
  dirty.value = false
  form.name = ''
  form.description = ''
  form.user_ids = []
  rules.value = []
  memberRows.value = []
  nextTick(() => { suppressDirty = false })
}

// 详情加载的请求序号：快速连点两个组时，慢返回的旧请求不得覆盖新选中组的数据
let editSeq = 0

async function openEdit(row: any) {
  if (!row?.id) return
  if (row.id !== editId.value && !(await confirmDiscard())) return
  const my = ++editSeq
  const reqId = row.id
  suppressDirty = true
  editId.value = row.id
  creating.value = false
  renaming.value = false
  dirty.value = false
  form.name = row.name || ''
  form.description = row.description || ''
  form.user_ids = []
  rules.value = []
  memberRows.value = []
  detailLoading.value = true
  try {
    const res = await getGroup(reqId)
    if (my !== editSeq) return // 期间已切换到其他组：旧响应丢弃
    form.user_ids = res.data.user_ids || []
    memberRows.value = (res.data.users || []).map((u: any) => ({
      id: u.id,
      expiresAt: u.expires_at ? new Date(u.expires_at) : null, // 库里存什么就显示什么（NULL=永久）
    }))
    // 同形规则（ns/资源/动作/字段全同）按集群合并为一行展示
    rules.value = mergeBackendRules(res.data.rules || [], false)
    rules.value.forEach((r: any) => r.clusterIds.forEach(loadNSBy))
  } catch (e) {
    if (my === editSeq) ElMessage.error(errMsg(e))
  } finally {
    if (my === editSeq) detailLoading.value = false
  }
  // watch 回调按微任务批处理，且上面的数据填充已全部完成（await getGroup 之后），
  // 双重 nextTick 确保抑制跨过 watch 触发点后再解除
  nextTick(() => nextTick(() => { suppressDirty = false }))
}

// 重命名：记录进入编辑时的名称，取消时还原
let renameBackup = ''
function startRename() {
  renameBackup = form.name
  renaming.value = true
}
function cancelRename() {
  form.name = renameBackup
  renaming.value = false
}

function renameOk() {
  if (!form.name.trim()) {
    ElMessage.warning('组名不能为空')
    return
  }
  renaming.value = false
}

async function doSave() {
  if (!form.name.trim()) {
    ElMessage.warning('请填写组名')
    return
  }
  for (let i = 0; i < rules.value.length; i++) {
    const err = validateRow(rules.value[i])
    if (err) {
      ElMessage.warning(`第 ${i + 1} 条规则${err}`)
      return
    }
  }
  saving.value = true
  try {
    const body = {
      name: form.name.trim(),
      description: form.description.trim(),
      users: memberRows.value.map((r) => ({
        id: r.id,
        expires_at: r.expiresAt instanceof Date ? r.expiresAt.toISOString() : undefined, // 空=永久
      })),
      user_ids: form.user_ids,
      // 多集群规则展开为多条（每集群一条）
      rules: expandRows(rules.value),
    }
    if (editId.value) {
      await updateGroup(editId.value, body)
      ElMessage.success('权限组已更新，成员需重新登录生效')
    } else {
      await createGroup(body)
      ElMessage.success('权限组已创建')
    }
    creating.value = false
    dirty.value = false
    await load()
    if (editId.value) {
      const fresh = groups.value.find((g: any) => g.id === editId.value)
      if (fresh) openEdit(fresh)
    }
  } catch (e) { ElMessage.error(errMsg(e)) } finally { saving.value = false }
}

async function doDelete(row: any) {
  try {
    await ElMessageBox.prompt(
      `删除后组成员将失去该组授予的权限（个人直接授权不受影响）。请输入组名 "${row.name}" 确认：`,
      '删除权限组', {
        type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消',
        inputValidator: (v: string) => v.trim() === row.name || '输入的组名不匹配',
      },
    )
  } catch { return }
  try {
    await deleteGroup(row.id)
    ElMessage.success('已删除')
    if (editId.value === row.id) {
      editId.value = null
      creating.value = false
      editSeq++ // 使该组在途的详情响应失效
    }
    load()
  } catch (e) { ElMessage.error(errMsg(e)) }
}

onMounted(async () => {
  loadPermPresets() // 动作目录 / 角色预设（系统设置可配置；失败回落内置）
  try {
    users.value = (await listUsers()).data || []
    clusters.value = (await listClusters()).data || []
  } catch { /* http 层已提示 */ }
  load()
})
</script>

<style scoped>
.groups-layout { display: flex; min-height: calc(100vh - 180px); }
.group-panel {
  width: 288px; flex: none;
  border-right: 1px solid var(--el-border-color-lighter);
  padding-right: 16px; margin-right: 20px;
}
.panel-head { display: flex; flex-direction: column; }
.group-list { height: calc(100vh - 300px); overflow-y: auto; margin-top: 10px; }
.group-item {
  display: flex; align-items: center; gap: 6px;
  padding: 10px; border-radius: 8px; cursor: pointer;
  transition: background .15s;
}
.group-item:hover { background: var(--el-fill-color-light); }
.group-item.active { background: var(--el-color-primary-light-9); }
.group-body { min-width: 0; flex: 1; }
.group-name {
  font-size: 13px; font-weight: 600; color: var(--el-text-color-primary);
  display: flex; align-items: center; gap: 6px;
}
.group-sub {
  font-size: 12px; color: var(--el-text-color-secondary); margin-top: 2px;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.group-stats { font-size: 12px; color: var(--el-text-color-placeholder); margin-top: 4px; display: flex; gap: 10px; }
.group-del { flex: none; opacity: 0; transition: opacity .15s; }
.group-item:hover .group-del { opacity: 1; }
.detail-panel { flex: 1; min-width: 0; }
.detail-placeholder {
  min-height: calc(100vh - 240px);
  display: flex; align-items: center; justify-content: center;
}
.detail-head { display: flex; flex-direction: column; gap: 10px; }
.detail-title { display: flex; align-items: center; gap: 10px; }
.detail-name { font-size: 15px; font-weight: 600; color: var(--el-text-color-primary); }
.rename-row { display: flex; align-items: center; gap: 8px; }
.union-hint { margin: 8px 0 0 0; }
.members-head { display: flex; align-items: center; gap: 10px; margin: 12px 0 8px; }
.members-label { font-size: 13px; color: var(--el-text-color-regular); }
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
