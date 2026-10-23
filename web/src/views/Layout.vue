<template>
  <el-container class="layout">
    <el-aside width="220px" class="aside">
      <div class="brand">
        <span class="logo"><LogoMark :size="26" /></span>
        <span>KubeYard</span>
      </div>
      <el-menu :default-active="$route.path" router class="menu">
        <el-menu-item index="/dashboard"><el-icon><Odometer /></el-icon>集群总览</el-menu-item>
        <el-menu-item index="/resources"><el-icon><Grid /></el-icon>项目管理</el-menu-item>
        <el-menu-item index="/events"><el-icon><Bell /></el-icon>事件中心</el-menu-item>
        <el-menu-item index="/schedule"><el-icon><Timer /></el-icon>定时任务</el-menu-item>
        <template v-if="auth.role === 'SuperAdmin' || auth.scopeAdmin">
          <div class="menu-group">平台管理</div>
        </template>
        <template v-if="auth.role === 'SuperAdmin'">
          <el-menu-item index="/admin/clusters"><el-icon><Connection /></el-icon>集群纳管</el-menu-item>
          <el-menu-item index="/admin/users"><el-icon><UserFilled /></el-icon>用户管理</el-menu-item>
          <el-menu-item index="/admin/grants"><el-icon><Key /></el-icon>权限授权</el-menu-item>
          <el-menu-item index="/admin/groups"><el-icon><FolderOpened /></el-icon>权限组</el-menu-item>
          <el-menu-item index="/admin/ldap"><el-icon><Lock /></el-icon>LDAP 配置</el-menu-item>
          <el-menu-item index="/admin/oauth"><el-icon><Link /></el-icon>SSO 认证</el-menu-item>
          <el-menu-item index="/admin/tokens"><el-icon><Promotion /></el-icon>集成令牌</el-menu-item>
          <el-menu-item index="/admin/drafts"><el-icon><Notebook /></el-icon>暂存变更</el-menu-item>
        </template>
        <!-- 审计/录像：SuperAdmin 与集群/命名空间管理员（仅管辖范围）可见 -->
        <template v-if="auth.role === 'SuperAdmin' || auth.scopeAdmin">
          <el-menu-item index="/admin/audit"><el-icon><Document /></el-icon>审计日志</el-menu-item>
          <el-menu-item index="/admin/recordings"><el-icon><VideoCamera /></el-icon>终端录像</el-menu-item>
        </template>
        <template v-if="auth.role === 'SuperAdmin'">
          <el-menu-item index="/admin/settings"><el-icon><Setting /></el-icon>系统设置</el-menu-item>
        </template>
      </el-menu>
    </el-aside>
    <el-container>
      <el-header class="header">
        <div class="header-title">{{ pageTitle }}</div>
        <div class="header-right">
          <el-tooltip content="AI 排查助手（只读）：分析问题、解读报错、给排查思路" placement="bottom">
            <button class="support-btn" type="button" @click="aiOpen = true">
              <el-icon><MagicStick /></el-icon>AI 助手
            </button>
          </el-tooltip>
          <NoticeBell />
          <el-tooltip content="查看我在各集群/命名空间的授权（含到期时间）" placement="bottom">
            <button class="support-btn" type="button" @click="permsOpen = true">
              <el-icon><Key /></el-icon>我的权限
            </button>
          </el-tooltip>
          <el-tooltip content="扫码加入技术支持群" placement="bottom">
            <button class="support-btn" type="button" @click="supportOpen = true">
              <el-icon><Service /></el-icon>技术支持
            </button>
          </el-tooltip>
          <el-dropdown @command="onCommand">
          <span class="user-chip">
            <el-avatar :size="28" style="background: var(--header-avatar-bg, var(--el-color-primary))">{{ initial }}</el-avatar>
            <span class="user-name">{{ auth.username }}</span>
            <el-tag size="small" :type="auth.role === 'SuperAdmin' ? 'danger' : 'info'">{{ auth.role }}</el-tag>
          </span>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item v-for="t in themes" :key="t.value" :command="'theme:' + t.value">
                <span class="theme-dot" :style="{ background: t.dot }"></span>
                {{ t.label }}
                <el-icon v-if="theme.name === t.value" class="theme-check"><Check /></el-icon>
              </el-dropdown-item>
              <el-dropdown-item command="logout">退出登录</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
        </div>
      </el-header>
      <el-main class="main">
        <!-- 账号权限到期提示：到期不阻断登录，但所有集群资源不再显示，给用户一个明确的原因 -->
        <el-alert
          v-if="accountExpired"
          type="danger"
          :closable="false"
          show-icon
          style="margin-bottom: 12px"
          title="你的账号权限已到期，集群资源暂不可见"
        >
          <template #default>
            <div class="expiry-action">
              <span>如需继续使用，如需继续使用，请联系平台管理员申请权限续期；到期账号仍可登录平台。</span>
              <el-button v-if="renewalPortalUrl" size="small" type="danger" plain tag="a" :href="renewalPortalUrl" target="_blank" rel="noopener">前往统一权限门户申请</el-button>
            </div>
          </template>
        </el-alert>
        <!-- 将到期（<7 天）预警：黄横幅提醒续期，权限此时仍可用 -->
        <el-alert
          v-else-if="accountExpiringSoon"
          type="warning"
          :closable="false"
          show-icon
          style="margin-bottom: 12px"
          :title="`你的账号权限将于 ${accountExpiringDate} 到期，请及时续期`"
        >
          <template #default>
            <div class="expiry-action">
              <span>到期后集群资源将不再显示；请联系平台管理员申请权限续期。</span>
              <el-button v-if="renewalPortalUrl" size="small" type="warning" plain tag="a" :href="renewalPortalUrl" target="_blank" rel="noopener">前往统一权限门户申请</el-button>
            </div>
          </template>
        </el-alert>
        <router-view />
      </el-main>
    </el-container>
    <MyPermsDrawer v-model="permsOpen" />
    <AiChatDrawer ref="aiChatRef" />
    <AiAssistBridge @open="aiOpen = true" />
    <!-- 技术支持群二维码 -->
    <el-dialog v-model="supportOpen" title="技术支持" width="360px" align-center>
      <div class="support-qr">
        <img src="/img/support-qr.png" alt="技术支持群二维码" />
        <p>扫码加入技术支持群，获取帮助与反馈问题</p>
      </div>
    </el-dialog>
  </el-container>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth'
import { useThemeStore, THEMES } from '../stores/theme'
import type { ThemeName } from '../stores/theme'
import { MagicStick } from '@element-plus/icons-vue'
import LogoMark from '../components/LogoMark.vue'
import MyPermsDrawer from '../components/MyPermsDrawer.vue'
import NoticeBell from '../components/NoticeBell.vue'
import AiChatDrawer from '../components/AiChatDrawer.vue'
import AiAssistBridge from '../components/AiAssistBridge.vue'

const auth = useAuthStore()
const router = useRouter()
const theme = useThemeStore()
const themes = THEMES
const initial = computed(() => (auth.username || '?').slice(0, 1).toUpperCase())
const pageTitle = computed(() => router.currentRoute.value.name === 'resources'
  ? '项目管理'
  : String(router.currentRoute.value.meta.title || ''))

// 账号权限到期（null=永久不提示）；路由切换时会重算，会话内跨过到期点也能看到横幅
const accountExpired = computed(() => {
  const u = auth.user
  if (!u?.expires_at) return false
  return new Date(u.expires_at).getTime() < Date.now()
})

// 将到期（未过期且剩 <7 天）→ 黄色预警横幅；到期红横幅优先
const accountExpiringSoon = computed(() => {
  const u = auth.user
  if (!u?.expires_at) return false
  const t = new Date(u.expires_at).getTime()
  return t >= Date.now() && t - Date.now() < 7 * 86400000
})
const accountExpiringDate = computed(() =>
  auth.user?.expires_at ? new Date(auth.user.expires_at).toLocaleString('zh-CN', { hour12: false }) : '')

// 续期门户地址（系统设置 renewal.portal_url，可空）：配置了才显示申请跳转按钮
const renewalPortalUrl = ref('')
onMounted(async () => {
  try {
    const { getSettings } = await import('../api')
    const m = (await getSettings()).data || {}
    const u = String(m['renewal.portal_url'] || '').trim()
    if (u) renewalPortalUrl.value = u
  } catch { /* 未登录或读取失败：不显示入口 */ }
})

// 刷新页面后重算管理员范围（localStorage 缓存兜底，保证路由守卫即时可用）
onMounted(() => { auth.loadScope() })

// 「我的权限」抽屉：顶栏独立按钮入口（比藏头像下拉里更显眼，登录用户随时自查授权与到期）
const permsOpen = ref(false)
// AI 排查助手：顶栏按钮 + 任意页面的事件总线打开（Dashboard/资源页等可跳转过来）
const aiOpen = ref(false)
const aiChatRef = ref<InstanceType<typeof AiChatDrawer>>()
function openAiChat() {
  aiOpen.value = false
  aiChatRef.value?.open()
}
watch(aiOpen, (v) => { if (v) openAiChat() })
// 技术支持群二维码弹窗（原跳飞书链接，改为本平台内扫码，避免外链失效）
const supportOpen = ref(false)

async function onCommand(cmd: string) {
  if (cmd.startsWith('theme:')) {
    theme.apply(cmd.slice(6) as ThemeName)
    return
  }
  if (cmd === 'logout') {
    await auth.logout()
    router.push('/login')
  }
}
</script>

<style scoped>
.layout { height: 100%; }
.aside { background: var(--sidebar-bg); border-right: 1px solid var(--sidebar-border); }
.brand {
  height: 56px; display: flex; align-items: center; justify-content: center; gap: 8px;
  color: var(--sidebar-brand-color); font-size: 18px; font-weight: 600; letter-spacing: 1px;
  background: var(--sidebar-brand-bg);
  border-bottom: 1px solid var(--sidebar-border);
}
.brand .logo { display: inline-flex; align-items: center; flex: none; }
.menu {
  border-right: none;
  --el-menu-bg-color: var(--sidebar-bg);
  --el-menu-text-color: var(--sidebar-text);
  --el-menu-active-color: var(--sidebar-active);
  --el-menu-hover-bg-color: var(--sidebar-hover-bg);
  --el-menu-item-height: 44px;
}
.menu :deep(.el-menu-item) { margin: 3px 10px; border-radius: 8px; }
.menu :deep(.el-menu-item:hover) { background: var(--sidebar-hover-bg); border-radius: 8px; }
.menu :deep(.el-menu-item.is-active) { background: var(--sidebar-active-bg); font-weight: 600; border-radius: 8px; }
.menu-group {
  color: var(--sidebar-group); font-size: 12px; padding: 14px 20px 6px; letter-spacing: 2px;
}
.header {
  height: 56px; display: flex; align-items: center; justify-content: space-between;
  background: var(--header-bg, #fff); border-bottom: 1px solid var(--header-border);
  box-shadow: 0 2px 10px rgba(60, 30, 40, .05);
}
.header-title { font-size: 16px; font-weight: 600; color: var(--header-title, #1f2d3d); }
.header-right { display: flex; align-items: center; gap: 14px; }
.support-btn {
  display: inline-flex; align-items: center; gap: 5px;
  border: none; background: transparent; cursor: pointer;
  font-size: 13px; color: var(--header-text, #606266); padding: 6px 10px; border-radius: 6px;
  transition: color .2s, background .2s;
}
.support-btn:hover { color: var(--header-btn-hover-color, var(--el-color-primary)); background: var(--header-btn-hover-bg); }
.user-chip { display: flex; align-items: center; gap: 8px; cursor: pointer; }
.user-name { font-size: 14px; color: var(--header-title, #1f2d3d); }
.main { padding: 16px; overflow: auto; }
.theme-dot {
  display: inline-block; width: 12px; height: 12px; border-radius: 3px;
  margin-right: 8px; border: 1px solid rgba(0, 0, 0, .08);
}
.theme-check { margin-left: 6px; color: var(--el-color-primary); }
.support-qr { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 4px 0 0; }
.support-qr img { width: 260px; height: 260px; object-fit: contain; border-radius: 8px; }
.support-qr p { margin: 0; font-size: 13px; color: #909399; text-align: center; }
/* 到期横幅：文案与申请按钮同行（窄屏换行） */
.expiry-action { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
@media (max-width: 1100px) {
  .header-right { gap: 5px; }
  .support-btn { padding: 6px; }
}
@media (max-width: 760px) {

  .user-name, .header-right :deep(.el-tag) { display: none; }
}
</style>
