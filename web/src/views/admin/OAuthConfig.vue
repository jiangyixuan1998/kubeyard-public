<template>
  <el-row :gutter="16">
    <!-- 单条配置表单 -->
    <el-col :span="15">
      <el-card shadow="never" v-loading="loading">
        <template #header>
          <div class="card-header">
            <span>SSO 统一认证（OAuth 2.0 授权码）</span>
            <el-tag v-if="form.id" :type="form.enabled ? 'success' : 'info'" size="small">
              {{ form.enabled ? '已启用' : '未启用' }}
            </el-tag>
          </div>
        </template>
        <el-form :model="form" label-width="130px">
          <el-row>
            <el-col :span="12">
              <el-form-item label="显示名称"><el-input v-model="form.name" placeholder="如 企业统一认证" /></el-form-item>
            </el-col>
            <el-col :span="12">
              <el-form-item label="启用">
                <el-switch v-model="form.enabled" />
                <span class="tip" style="margin-left: 8px">启用后登录页出现 SSO 按钮</span>
              </el-form-item>
            </el-col>
          </el-row>
          <el-row>
            <el-col :span="12">
              <el-form-item label="Client ID"><el-input v-model="form.client_id" placeholder="应用集成申请时分配" /></el-form-item>
            </el-col>
            <el-col :span="12">
              <el-form-item label="Client Secret">
                <el-input v-model="clientSecret" type="password" show-password :placeholder="secretPlaceholder" />
              </el-form-item>
            </el-col>
          </el-row>
          <el-form-item label="Authorize 地址">
            <el-input v-model="form.authorize_url" placeholder="https://sso.example.com/openapi/oauth/authorize" />
          </el-form-item>
          <el-form-item label="Token 地址">
            <el-input v-model="form.token_url" placeholder="https://sso.example.com/openapi/oauth/token" />
          </el-form-item>
          <el-form-item label="UserInfo 地址">
            <el-input v-model="form.userinfo_url" placeholder="https://sso.example.com/openapi/oauth/userinfo" />
          </el-form-item>
          <el-form-item label="回调地址">
            <el-input v-model="form.redirect_uri" placeholder="https://<平台域名>/api/v1/auth/oauth/callback" />
            <div class="tip">需与认证中心注册的回调地址完全一致（应用集成申请时填写）</div>
          </el-form-item>
          <el-row>
            <el-col :span="12">
              <el-form-item label="账号字段">
                <el-input v-model="form.username_field" placeholder="yumADAccount" />
                <div class="tip">userinfo 返回中取用户名的字段，取不到时按内置顺序回退</div>
              </el-form-item>
            </el-col>
            <el-col :span="12">
              <el-form-item label="默认角色">
                <el-select v-model="form.default_role">
                  <el-option label="Viewer（只读）" value="Viewer" />
                  <el-option label="Developer（读写，需授权）" value="Developer" />
                  <el-option label="ClusterAdmin（读写+管理，需授权）" value="ClusterAdmin" />
                </el-select>
                <div class="tip">SSO 首次登录自动开户的平台角色，集群权限仍由授权决定</div>
              </el-form-item>
            </el-col>
          </el-row>
          <el-form-item label="自动开户">
            <el-switch v-model="form.auto_create" />
            <span class="tip" style="margin-left: 8px">关：SSO 登录仅允许已存在的账号（AD 账号与 LDAP 同源，老用户自动匹配）</span>
          </el-form-item>
          <el-form-item label="仅 SSO 登录">
            <el-switch v-model="form.only_sso" :disabled="!form.enabled" />
            <div class="tip">阶段二切换开关：开启后密码登录关闭（本地超管保留密码兜底）；需先启用 SSO</div>
          </el-form-item>
        </el-form>
        <div class="actions">
          <el-button type="primary" :loading="saving" @click="doSave">保存</el-button>
          <el-button @click="load">重置</el-button>
        </div>
      </el-card>
    </el-col>

    <!-- 说明 -->
    <el-col :span="9">
      <el-card shadow="never">
        <template #header>对接说明</template>
        <div class="doc">
          <p><b>流程</b>：登录页点击 SSO → 认证中心登录授权 → 回调平台 → 平台校验 code 换取用户信息 → 按 AD 账号匹配/开户 → 签发平台登录态。</p>
          <p><b>与 LDAP 共存</b>：SSO 与本地/LDAP 密码登录并行；AD 账号与 LDAP 同源，已开户用户直接复用原有权限，不产生重复账号。</p>
          <p><b>切换到仅 SSO</b>：确认全员可正常 SSO 登录后，打开「仅 SSO 登录」开关完成切换；本地超管（admin）保留密码登录兜底。</p>
          <p><b>上线前需向认证中心申请</b>：应用集成申请（获得 Client ID / Secret），注册回调地址，确认 userinfo 账号字段；生产环境接口地址待中心提供后更新即可。</p>
        </div>
      </el-card>
    </el-col>
  </el-row>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { getOAuthConfig, putOAuthConfig } from '../../api'
import { errMsg } from '../../api/http'

const loading = ref(false)
const saving = ref(false)
const clientSecret = ref('')

const secretPlaceholder = computed(() =>
  form.has_secret ? '已保存，留空则不修改' : '应用集成申请时分配的密钥',
)

const emptyForm = () => ({
  id: 0, name: '企业统一认证', enabled: false,
  client_id: '', authorize_url: '', token_url: '', userinfo_url: '',
  redirect_uri: '', username_field: 'yumADAccount', default_role: 'Viewer',
  auto_create: true, only_sso: false, has_secret: false,
})
const form = reactive<any>(emptyForm())

async function load() {
  loading.value = true
  try {
    const res = await getOAuthConfig()
    Object.assign(form, emptyForm(), res.data)
    clientSecret.value = ''
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

async function doSave() {
  if (form.enabled && (!form.client_id || !form.redirect_uri)) {
    ElMessage.warning('启用前需填写 Client ID 与回调地址')
    return
  }
  saving.value = true
  try {
    const res = await putOAuthConfig({ ...form, client_secret: clientSecret.value })
    Object.assign(form, emptyForm(), res.data)
    clientSecret.value = ''
    ElMessage.success('已保存，立即生效')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.tip { color: #909399; font-size: 12px; line-height: 1.7; }
.actions { display: flex; gap: 8px; }
.doc p { margin: 0 0 12px; color: #606266; font-size: 13px; line-height: 1.8; }
.doc b { color: #303133; }
</style>
