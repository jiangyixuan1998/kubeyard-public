<template>
  <el-row :gutter="16">
    <!-- 配置列表 -->
    <el-col :span="9">
      <el-card shadow="never">
        <template #header>
          <div class="card-header">
            <span>LDAP 服务配置</span>
            <el-button type="primary" size="small" @click="openCreate">新增</el-button>
          </div>
        </template>
        <el-table :data="configs" v-loading="loading" highlight-current-row @current-change="onPick">
          <el-table-column prop="name" label="名称" min-width="100" />
          <el-table-column prop="host" label="地址" min-width="140" show-overflow-tooltip />
          <el-table-column label="启用" width="70">
            <template #default="{ row }"><el-switch v-model="row.enabled" @change="toggle(row)" /></template>
          </el-table-column>
          <el-table-column label="操作" width="120">
            <template #default="{ row }">
              <el-button link type="primary" size="small" @click="openEdit(row)">编辑</el-button>
              <el-button link type="danger" size="small" @click="doDelete(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-card>
    </el-col>

    <!-- 编辑表单 -->
    <el-col :span="15">
      <el-card shadow="never">
        <template #header>{{ form.id ? `编辑：${form.name}` : '新增 LDAP 配置' }}</template>
        <el-form :model="form" label-width="130px" v-loading="saving">
          <el-row>
            <el-col :span="12">
              <el-form-item label="名称"><el-input v-model="form.name" placeholder="如 公司 AD" /></el-form-item>
            </el-col>
            <el-col :span="12">
              <el-form-item label="启用">
                <el-switch v-model="form.enabled" />
                <span class="tip" style="margin-left: 8px">启用后立即生效，无需重启</span>
              </el-form-item>
            </el-col>
          </el-row>
          <el-row>
            <el-col :span="12">
              <el-form-item label="主机">
                <el-input v-model="form.host" placeholder="ldap.example.com" />
              </el-form-item>
            </el-col>
            <el-col :span="6">
              <el-form-item label="端口"><el-input-number v-model="form.port" :min="1" :max="65535" /></el-form-item>
            </el-col>
            <el-col :span="6">
              <el-form-item label="优先级"><el-input-number v-model="form.priority" :min="0" /></el-form-item>
            </el-col>
          </el-row>
          <el-form-item label="加密方式">
            <el-radio-group v-model="form.security">
              <el-radio value="none">不加密</el-radio>
              <el-radio value="starttls">StartTLS</el-radio>
              <el-radio value="ldaps">LDAPS</el-radio>
            </el-radio-group>
            <el-checkbox v-model="form.skip_tls_verify" style="margin-left: 16px">跳过证书校验</el-checkbox>
          </el-form-item>
          <el-form-item label="Bind DN">
            <el-input v-model="form.bind_dn" placeholder="cn=svc-kubeyard,ou=Service,dc=example,dc=com" />
          </el-form-item>
          <el-form-item label="Bind 密码">
            <el-input v-model="bindPassword" type="password" show-password :placeholder="bindPwdPlaceholder" />
          </el-form-item>
          <el-form-item label="用户搜索 Base">
            <el-input v-model="form.user_base_dn" placeholder="ou=Staff,dc=example,dc=com" />
          </el-form-item>
          <el-form-item label="用户过滤器">
            <el-input v-model="form.user_filter" placeholder="(sAMAccountName=%s)" />
            <div class="tip">%s 会被替换为登录用户名，并做注入转义</div>
          </el-form-item>
          <el-form-item label="搜索范围">
            <el-radio-group v-model="form.search_scope">
              <el-radio value="sub">sub（子树）</el-radio>
              <el-radio value="one">one（单层）</el-radio>
            </el-radio-group>
          </el-form-item>
          <el-row>
            <el-col :span="8">
              <el-form-item label="用户名属性"><el-input v-model="form.attr_username" /></el-form-item>
            </el-col>
            <el-col :span="8">
              <el-form-item label="姓名属性"><el-input v-model="form.attr_display_name" /></el-form-item>
            </el-col>
            <el-col :span="8">
              <el-form-item label="邮箱属性"><el-input v-model="form.attr_email" /></el-form-item>
            </el-col>
          </el-row>
          <el-form-item label="默认角色">
            <el-select v-model="form.default_role">
              <el-option label="Viewer（只读）" value="Viewer" />
              <el-option label="Developer（读写，需授权）" value="Developer" />
              <el-option label="ClusterAdmin（读写+管理，需授权）" value="ClusterAdmin" />
            </el-select>
            <div class="tip">LDAP 用户首次登录自动开户时使用的平台角色，集群操作权限仍由授权决定</div>
          </el-form-item>
        </el-form>
        <div class="actions">
          <el-button type="primary" :loading="saving" @click="doSave">保存</el-button>
          <el-button @click="openTest">测试连接</el-button>
          <el-button @click="reset">重置</el-button>
        </div>
      </el-card>
    </el-col>
  </el-row>

  <!-- 测试面板 -->
  <el-dialog v-model="testDlg" title="LDAP 连接测试" width="560px">
    <el-form label-width="110px">
      <el-form-item label="测试账号">
        <el-input v-model="testForm.test_username" placeholder="用于验证搜索的真实用户名（可选）" style="width: 260px" />
      </el-form-item>
      <el-form-item label="测试密码">
        <el-input v-model="testForm.test_user_password" type="password" show-password
          placeholder="验证该账号可登录（可选）" style="width: 260px" />
      </el-form-item>
    </el-form>
    <template v-if="testSteps.length">
      <el-steps direction="vertical" :active="testSteps.filter((s: any) => s.ok).length" style="margin-top: 8px">
        <el-step v-for="s in testSteps" :key="s.name" :title="s.name" :status="s.ok ? 'success' : 'error'"
          :description="s.message" />
      </el-steps>
    </template>
    <template #footer>
      <el-button @click="testDlg = false">关闭</el-button>
      <el-button type="primary" :loading="testing" @click="doTest">开始测试</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { listLDAP, createLDAP, updateLDAP, deleteLDAP, testLDAP } from '../../api'
import { errMsg } from '../../api/http'

const configs = ref<any[]>([])
const loading = ref(false)
const saving = ref(false)
const bindPassword = ref('')

const bindPwdPlaceholder = computed(() =>
  form.id ? (form.has_password ? '已加密存储，留空则不修改' : '未设置') : 'Bind 账号密码',
)

const emptyForm = () => ({
  id: 0, name: '', enabled: false, priority: 0,
  host: '', port: 389, security: 'none', skip_tls_verify: false,
  bind_dn: '', user_base_dn: '', user_filter: '(sAMAccountName=%s)',
  search_scope: 'sub', attr_username: 'sAMAccountName', attr_display_name: 'displayName',
  attr_email: 'mail', default_role: 'Viewer', has_password: false,
})
const form = reactive<any>(emptyForm())

const testDlg = ref(false)
const testing = ref(false)
const testSteps = ref<any[]>([])
const testForm = reactive({ test_username: '', test_user_password: '' })

async function load() {
  loading.value = true
  try { configs.value = (await listLDAP()).data } finally { loading.value = false }
}

function openCreate() {
  Object.assign(form, emptyForm())
  bindPassword.value = ''
}

function openEdit(row: any) {
  Object.assign(form, emptyForm(), row)
  bindPassword.value = ''
}

function onPick(row: any) {
  if (row) openEdit(row)
}

function reset() {
  if (form.id) {
    const row = configs.value.find((c) => c.id === form.id)
    if (row) { openEdit(row); return }
  }
  openCreate()
}

async function doSave() {
  if (!form.name || !form.host || !form.port) {
    ElMessage.warning('请填写名称 / 主机 / 端口')
    return
  }
  saving.value = true
  try {
    const payload = { ...form, bind_password: bindPassword.value }
    if (form.id) await updateLDAP(form.id, payload)
    else await createLDAP(payload)
    ElMessage.success('已保存')
    load()
  } catch (e) { ElMessage.error(errMsg(e)) } finally { saving.value = false }
}

async function toggle(row: any) {
  try {
    await updateLDAP(row.id, { ...row })
    ElMessage.success(row.enabled ? '已启用' : '已停用')
  } catch (e) {
    ElMessage.error(errMsg(e))
    load()
  }
}

async function doDelete(row: any) {
  try {
    await ElMessageBox.confirm(`确定删除 LDAP 配置 "${row.name}" 吗？`, '删除确认', { type: 'warning' })
  } catch { return }
  try {
    await deleteLDAP(row.id)
    ElMessage.success('已删除')
    if (form.id === row.id) openCreate()
    load()
  } catch (e) { ElMessage.error(errMsg(e)) }
}

function openTest() {
  testDlg.value = true
  testSteps.value = []
}

async function doTest() {
  testing.value = true
  testSteps.value = []
  try {
    const res = await testLDAP({
      id: form.id || 0, name: form.name, host: form.host, port: form.port,
      security: form.security, skip_tls_verify: form.skip_tls_verify,
      bind_dn: form.bind_dn, bind_password: bindPassword.value,
      user_base_dn: form.user_base_dn, user_filter: form.user_filter,
      search_scope: form.search_scope, attr_username: form.attr_username,
      attr_display_name: form.attr_display_name, attr_email: form.attr_email,
      ...testForm,
    })
    testSteps.value = res.data.steps || []
  } catch (e) { ElMessage.error(errMsg(e)) } finally { testing.value = false }
}

onMounted(load)
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.tip { color: #909399; font-size: 12px; }
.actions { display: flex; gap: 8px; }
</style>
