<template>
  <el-card shadow="never">
    <div class="toolbar">
      <span>本地账号与 LDAP 自动开户用户统一在此管理；集群操作权限在「权限授权」页配置</span>
      <div style="flex: 1"></div>
      <el-input
        v-model="search" clearable style="width: 240px" placeholder="搜索用户名 / 姓名 / 邮箱"
        :prefix-icon="Search" @clear="search = ''"
      />
      <el-button type="primary" @click="openCreate"><el-icon><Plus /></el-icon>新建用户</el-button>
      <el-button :loading="exporting" @click="doExport"><el-icon><Download /></el-icon>导出 Excel</el-button>
    </div>

    <el-table :data="paged" stripe border v-loading="loading">
      <el-table-column prop="id" label="ID" width="60" />
      <el-table-column prop="username" label="用户名" min-width="120" />
      <el-table-column prop="display_name" label="姓名" min-width="120" />
      <el-table-column prop="email" label="邮箱" min-width="180" show-overflow-tooltip />
      <el-table-column label="来源" width="80">
        <template #default="{ row }">
          <el-tag size="small" :type="row.source === 'ldap' ? 'warning' : 'info'">{{ row.source }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="角色" width="120">
        <template #default="{ row }">
          <el-tag size="small" :type="row.role === 'SuperAdmin' ? 'danger' : ''">{{ row.role }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="80">
        <template #default="{ row }">
          <el-tag size="small" :type="row.status === 1 ? 'success' : 'info'">{{ row.status === 1 ? '启用' : '禁用' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="最近登录" width="170">
        <template #default="{ row }">{{ row.last_login_at ? new Date(row.last_login_at).toLocaleString('zh-CN', { hour12: false }) : '-' }}</template>
      </el-table-column>
      <el-table-column label="操作" width="250" fixed="right">
        <template #default="{ row }">
          <el-button link type="primary" size="small" @click="openPerms(row)">权限</el-button>
          <el-button link type="primary" size="small" @click="openEdit(row)">编辑</el-button>
          <el-button link type="warning" size="small" @click="toggleStatus(row)">{{ row.status === 1 ? '禁用' : '启用' }}</el-button>
          <el-button link type="danger" size="small" @click="doDelete(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>
    <el-pagination v-if="filtered.length > pageSize" style="margin-top: 12px; justify-content: flex-end"
      layout="total, prev, pager, next" :total="filtered.length" v-model:current-page="page" :page-size="pageSize" />

    <el-dialog v-model="dlg" :title="form.id ? `编辑用户：${form.username}` : '新建用户'" width="480px" destroy-on-close>
      <el-form :model="form" label-width="90px">
        <el-form-item label="用户名">
          <el-input v-model="form.username" :disabled="!!form.id" />
        </el-form-item>
        <el-form-item label="姓名"><el-input v-model="form.display_name" /></el-form-item>
        <el-form-item label="邮箱"><el-input v-model="form.email" /></el-form-item>
        <el-form-item label="平台角色">
          <el-select v-model="form.role" style="width: 100%">
            <el-option label="Viewer - 只读用户" value="Viewer" />
            <el-option label="Developer - 开发者（需授权）" value="Developer" />
            <el-option label="ClusterAdmin - 集群管理员（需授权）" value="ClusterAdmin" />
            <el-option label="SuperAdmin - 平台管理员" value="SuperAdmin" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="!form.id" label="初始密码">
          <el-input v-model="form.password" type="password" show-password placeholder="留空自动生成（创建后显示一次）" />
        </el-form-item>
        <el-form-item v-if="form.id && form.source === 'local'" label="重置密码">
          <el-input v-model="form.password" type="password" show-password placeholder="留空则不修改" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dlg = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="doSave">保存</el-button>
      </template>
    </el-dialog>

    <!-- 权限详情：该用户在哪些权限组 + 单独配置了什么授权 -->
    <UserPermsDrawer v-model="permsOpen" :user="permsUser" />
  </el-card>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Search, Download } from '@element-plus/icons-vue'
import { listUsers, createUser, updateUser, deleteUser } from '../../api'
import { errMsg } from '../../api/http'
import { downloadGet } from '../../utils/download'
import UserPermsDrawer from '../../components/UserPermsDrawer.vue'

const users = ref<any[]>([])
const loading = ref(false)
const dlg = ref(false)
const saving = ref(false)
const form = reactive<any>({})
const search = ref('')
const exporting = ref(false)

function doExport() {
  exporting.value = true
  downloadGet('/admin/users/export', 'kubeyard-users').finally(() => { exporting.value = false })
}

// 客户端模糊搜索：用户名 / 姓名 / 邮箱，不区分大小写
const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return users.value
  return users.value.filter((u) =>
    [u.username, u.display_name, u.email].some((v) => (v || '').toLowerCase().includes(q))
  )
})

// 客户端分页（用户全量拉取后本地过滤；用户多时一次渲染上千行 DOM 会卡）
const page = ref(1)
const pageSize = 50
const paged = computed(() => filtered.value.slice((page.value - 1) * pageSize, page.value * pageSize))
watch(search, () => { page.value = 1 }) // 搜索变化回第一页

async function load() {
  loading.value = true
  try { users.value = (await listUsers()).data } finally { loading.value = false }
}

function openCreate() {
  Object.assign(form, { id: 0, username: '', display_name: '', email: '', role: 'Viewer', password: '', source: 'local' })
  dlg.value = true
}

function openEdit(row: any) {
  Object.assign(form, row, { password: '' })
  dlg.value = true
}

async function doSave() {
  saving.value = true
  try {
    if (form.id) {
      const payload: any = { display_name: form.display_name, email: form.email, role: form.role }
      if (form.password) payload.password = form.password
      await updateUser(form.id, payload)
      ElMessage.success('已保存')
    } else {
      const res = await createUser(form)
      if (res.data.initial_password) {
        ElMessageBox.alert(`初始密码：${res.data.password}`, '请牢记', { confirmButtonText: '我已保存' })
      } else {
        ElMessage.success('已创建')
      }
    }
    dlg.value = false
    load()
  } catch (e) { ElMessage.error(errMsg(e)) } finally { saving.value = false }
}

async function toggleStatus(row: any) {
  try {
    await updateUser(row.id, { status: row.status === 1 ? 0 : 1, role: row.role })
    ElMessage.success('已更新')
    load()
  } catch (e) { ElMessage.error(errMsg(e)) }
}

async function doDelete(row: any) {
  try {
    await ElMessageBox.confirm(`确定删除用户 "${row.username}" 吗？其授权将一并删除。`, '删除确认', { type: 'warning' })
  } catch { return }
  try {
    await deleteUser(row.id)
    ElMessage.success('已删除')
    load()
  } catch (e) { ElMessage.error(errMsg(e)) }
}

// 权限详情抽屉：一次看清该用户的权限组归属 + 直接授权
const permsOpen = ref(false)
const permsUser = ref<any>(null)
function openPerms(row: any) {
  permsUser.value = row
  permsOpen.value = true
}

onMounted(load)
</script>

<style scoped>
.toolbar { display: flex; align-items: center; margin-bottom: 12px; color: #606266; font-size: 13px; }
</style>
