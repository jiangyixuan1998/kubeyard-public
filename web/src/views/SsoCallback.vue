<template>
  <div class="sso-callback">
    <div class="box">
      <template v-if="error">
        <el-icon class="ic fail"><CircleCloseFilled /></el-icon>
        <p class="t">统一认证登录失败</p>
        <p class="d">{{ error }}</p>
        <el-button type="primary" @click="$router.push('/login')">返回登录</el-button>
      </template>
      <template v-else>
        <el-icon class="is-loading ic"><Loading /></el-icon>
        <p class="t">统一认证登录中…</p>
        <p class="d">正在完成登录，请稍候</p>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
// SSO 回调落地页：后端 /api/v1/auth/oauth/callback 校验完成后 302 到这里，
// 令牌放在 URL fragment（#access_token=..&refresh_token=..），不会进服务器访问日志
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth'
import { http } from '../api/http'

const router = useRouter()
const auth = useAuthStore()
const error = ref('')

onMounted(async () => {
  const hash = new URLSearchParams(window.location.hash.slice(1))
  if (hash.get('error')) {
    error.value = hash.get('error') || '登录失败'
    return
  }
  const access = hash.get('access_token') || ''
  const refresh = hash.get('refresh_token') || ''
  if (!access || !refresh) {
    error.value = '回调参数缺失，请重新发起登录'
    return
  }
  try {
    auth.setTokens(access, refresh)
    const res = await http.get('/auth/me')
    auth.setUser(res.data)
    await auth.loadScope()
    // 清掉地址栏里的令牌再跳转（避免留在浏览器历史/地址栏）
    history.replaceState(null, '', '/dashboard')
    router.push('/dashboard')
  } catch (e: any) {
    auth.clear()
    error.value = e?.response?.data?.message || '登录态初始化失败，请重新登录'
  }
})
</script>

<style scoped>
.sso-callback { height: 100%; display: flex; align-items: center; justify-content: center; background: var(--page-bg, #f7f5f6); }
.box { display: flex; flex-direction: column; align-items: center; gap: 10px; padding: 40px 48px; background: #fff; border-radius: 14px; box-shadow: 0 8px 30px rgba(0, 0, 0, .06); }
.ic { font-size: 42px; color: var(--el-color-primary, #C84158); }
.ic.fail { color: var(--el-color-danger, #f56c6c); }
.t { margin: 4px 0 0; font-size: 16px; font-weight: 600; color: #2c2430; }
.d { margin: 0; font-size: 13px; color: #9a94a0; max-width: 420px; text-align: center; line-height: 1.7; }
</style>
