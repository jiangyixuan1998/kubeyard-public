<template>
  <div class="login-page">
    <!-- 左侧品牌区：清新浅红 + 光斑 + 特性 + 数据速览 -->
    <div class="login-left">
      <div class="glow g1"></div>
      <div class="glow g2"></div>
      <div class="glow g3"></div>
      <svg class="deco" viewBox="0 0 260 170" fill="none" aria-hidden="true">
        <g stroke="#C84158" stroke-opacity="0.15" stroke-width="1.2">
          <path d="M30 125 L100 62 L172 96 M100 62 L150 22 M172 96 L232 34 M172 96 L196 148" />
        </g>
        <g fill="#fff" stroke="#D97A8A" stroke-opacity="0.5">
          <circle cx="30" cy="125" r="7" />
          <circle cx="100" cy="62" r="9" />
          <circle cx="150" cy="22" r="6" />
          <circle cx="232" cy="34" r="7" />
          <circle cx="196" cy="148" r="6" />
        </g>
        <circle cx="172" cy="96" r="12" fill="#C84158" fill-opacity="0.14" stroke="#C84158" stroke-opacity="0.4" />
        <circle cx="172" cy="96" r="4.5" fill="#C84158" />
      </svg>
      <div class="left-content">
        <div class="brand">
          <span class="logo"><LogoMark :size="34" /></span>
          <div class="brand-text">
            <span class="brand-name">KubeYard</span>
            <span class="brand-sub">Kubernetes 管理平台</span>
          </div>
        </div>
        <ul class="features">
          <li><span class="fi"><el-icon><Connection /></el-icon></span>多集群统一纳管</li>
          <li><span class="fi"><el-icon><Key /></el-icon></span>字段级权限与权限组</li>
          <li><span class="fi"><el-icon><EditPen /></el-icon></span>白屏向导式部署</li>
          <li><span class="fi"><el-icon><VideoCamera /></el-icon></span>终端录像与全量审计</li>
        </ul>
        <div class="stats">
          <div class="stat"><span class="num">5<i>分钟</i></span><span class="lab">完成一个集群纳管</span></div>
          <div class="stat"><span class="num">字段级</span><span class="lab">权限控制粒度</span></div>
          <div class="stat"><span class="num">100<i>%</i></span><span class="lab">操作可审计回放</span></div>
        </div>
        <p class="copy">KubeYard 独立产品 · <a class="support-link" href="javascript:void(0)" @click.prevent="supportOpen = true">遇到问题？联系技术支持</a></p>
      </div>
    </div>
    <!-- 右侧登录表单 -->
    <div class="login-right">
      <div class="login-card">
        <h2>欢迎回来</h2>
        <p class="sub">请登录以继续管理你的集群</p>
        <template v-if="sso.enabled && sso.only_sso && !showPwd">
          <div class="sso-only-box">
            <p class="sso-only-tip">平台已启用统一认证，请使用 {{ sso.name || '企业统一认证' }} 登录</p>
            <el-button type="primary" size="large" style="width: 100%" @click="goSso">使用统一认证登录</el-button>
            <a class="pwd-entry" @click="showPwd = true">管理员密码登录 →</a>
          </div>
        </template>
        <template v-else>
          <el-form :model="form" @submit.prevent="doLogin" size="large">
            <el-form-item>
              <el-input v-model="form.username" placeholder="用户名" :prefix-icon="User" autofocus />
            </el-form-item>
            <el-form-item>
              <el-input v-model="form.password" type="password" placeholder="密码" :prefix-icon="Lock" show-password
                @keyup.enter="doLogin" />
            </el-form-item>
            <el-button style="width: 100%" :loading="loading" @click="doLogin">本地账号登录</el-button>
          </el-form>
          <div v-if="sso.enabled" class="sso-sep"><span>或</span></div>
          <el-button v-if="sso.enabled" type="primary" size="large" style="width: 100%" @click="goSso">
            {{ sso.name || '企业统一认证' }} 登录
          </el-button>
        </template>
        <p class="login-tip">支持本地账号与 NSSO 账号 · 忘记密码请联系平台管理员</p>
      </div>
      <p class="ver">KubeYard · 企业级 Kubernetes 管理平台</p>
    </div>
    <!-- 技术支持群二维码 -->
    <el-dialog v-model="supportOpen" title="技术支持" width="360px" align-center>
      <div class="support-qr">
        <img src="/img/support-qr.png" alt="技术支持群二维码" />
        <p>扫码加入技术支持群，获取帮助与反馈问题</p>
      </div>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { User, Lock } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { useAuthStore } from '../stores/auth'
import { errMsg } from '../api/http'
import { oauthStatus } from '../api'
import LogoMark from '../components/LogoMark.vue'

const router = useRouter()
const auth = useAuthStore()
const loading = ref(false)
const form = reactive({ username: '', password: '' })
// SSO 统一认证状态：enabled 显示按钮；only_sso 隐藏密码表单（超管可点「管理员密码登录」兜底）
const sso = reactive({ enabled: false, only_sso: false, name: '' })
const showPwd = ref(false)
// 技术支持群二维码弹窗（原跳飞书外链，改为本平台内扫码，避免外链失效/跳走）
const supportOpen = ref(false)

onMounted(async () => {
  try {
    const res = await oauthStatus()
    sso.enabled = !!res.data?.enabled
    sso.only_sso = !!res.data?.only_sso
    sso.name = res.data?.name || ''
  } catch { /* 状态接口失败按未启用处理，不影响密码登录 */ }
})

function goSso() {
  window.location.href = '/api/v1/auth/oauth/start'
}

async function doLogin() {
  if (!form.username || !form.password) {
    ElMessage.warning('请输入用户名和密码')
    return
  }
  loading.value = true
  try {
    await auth.login(form.username, form.password)
    router.push('/dashboard')
  } catch (e: any) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.login-page { height: 100%; display: flex; }

/* ---------- 左侧品牌区（清新浅红玫瑰色调） ---------- */
.login-left {
  flex: 1.15; position: relative; overflow: hidden;
  display: flex; align-items: center;
  background: linear-gradient(165deg, #fffdfd 0%, #fdf4f6 48%, #fae9ed 100%);
}
.login-left::before {
  content: ''; position: absolute; inset: 0;
  background-image: radial-gradient(rgba(200, 65, 88, .09) 1px, transparent 1px);
  background-size: 26px 26px;
}
.glow { position: absolute; border-radius: 50%; filter: blur(85px); pointer-events: none; }
.g1 { width: 420px; height: 420px; right: -90px; top: -110px; background: rgba(200, 65, 88, .14); animation: float1 9s ease-in-out infinite alternate; }
.g2 { width: 380px; height: 380px; left: -80px; bottom: -130px; background: rgba(255, 255, 255, .9); animation: float2 11s ease-in-out infinite alternate; }
.g3 { width: 300px; height: 300px; left: 42%; top: 55%; background: rgba(255, 214, 170, .18); animation: float1 13s ease-in-out infinite alternate-reverse; }
@keyframes float1 { from { transform: translate(0, 0); } to { transform: translate(26px, 20px); } }
@keyframes float2 { from { transform: translate(0, 0); } to { transform: translate(-22px, -16px); } }
.deco { position: absolute; right: 26px; bottom: 22px; width: 250px; pointer-events: none; }

.left-content {
  position: relative; display: flex; flex-direction: column; justify-content: center;
  padding: 0 72px; max-width: 520px; min-height: 100%;
}
.brand { display: flex; align-items: center; gap: 16px; margin-bottom: 42px; }
.brand .logo {
  display: inline-flex; align-items: center; justify-content: center;
  width: 60px; height: 60px; border-radius: 16px; background: #fff;
  box-shadow: 0 10px 26px rgba(200, 65, 88, .18); flex: none;
}
.brand-text { display: flex; flex-direction: column; gap: 5px; }
.brand-name { font-size: 28px; font-weight: 600; letter-spacing: 1px; color: #7a2839; line-height: 1; }
.brand-sub { font-size: 14px; color: #a66b77; letter-spacing: .5px; }
.features { list-style: none; margin: 0; padding: 0; }
.features li {
  display: flex; align-items: center; gap: 14px;
  margin: 16px 0; color: #8a5560; font-size: 15px;
}
.fi {
  display: inline-flex; align-items: center; justify-content: center;
  width: 36px; height: 36px; border-radius: 10px; flex: none;
  background: rgba(255, 255, 255, .8);
  box-shadow: 0 4px 12px rgba(200, 65, 88, .10);
  color: #C84158;
}
.stats {
  display: flex; gap: 40px; margin-top: 38px; padding-top: 26px;
  border-top: 1px dashed rgba(200, 65, 88, .22);
}
.stat { display: flex; flex-direction: column; gap: 7px; }
.num { font-size: 24px; font-weight: 700; color: #C84158; line-height: 1; letter-spacing: .5px; }
.num i { font-style: normal; font-size: 13px; font-weight: 500; margin-left: 2px; }
.lab { font-size: 12px; color: #a66b77; }
.copy { position: absolute; left: 72px; bottom: 32px; color: rgba(122, 40, 57, .45); font-size: 12px; }
.support-link { color: rgba(200, 65, 88, .75); text-decoration: none; }
.support-link:hover { color: #C84158; text-decoration: underline; }

/* ---------- 右侧表单区 ---------- */
.login-right { flex: 1; position: relative; display: flex; align-items: center; justify-content: center; background: #fff; }
.login-card { width: 360px; padding: 0 24px; }
.login-card h2 { margin: 0 0 6px; font-size: 24px; color: #2c2430; }
.login-card .sub { margin: 0 0 30px; color: #9a94a0; font-size: 14px; }
.login-tip { text-align: center; color: #b3adb9; font-size: 12px; margin-top: 18px; }
.sso-sep { display: flex; align-items: center; margin: 16px 0 12px; color: #c9c4ce; font-size: 12px; }
.sso-sep::before, .sso-sep::after { content: ''; flex: 1; height: 1px; background: #ece9ee; }
.sso-only-box { margin-bottom: 8px; }
.sso-only-tip { margin: 0 0 18px; color: #6b6472; font-size: 13.5px; }
.pwd-entry { display: block; text-align: center; margin-top: 14px; color: #b3adb9; font-size: 12px; cursor: pointer; }
.pwd-entry:hover { color: #C84158; }
.support-qr { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 4px 0 0; }
.support-qr img { width: 260px; height: 260px; object-fit: contain; border-radius: 8px; }
.support-qr p { margin: 0; font-size: 13px; color: #909399; text-align: center; }
.ver { position: absolute; bottom: 26px; left: 0; right: 0; text-align: center; color: #c9c4ce; font-size: 12px; letter-spacing: .5px; }

@media (max-width: 860px) {
  .login-left { display: none; }
  .login-right { background: var(--page-bg, #f7f5f6); }
}
</style>
