import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '../stores/auth'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', name: 'login', component: () => import('../views/Login.vue') },
    // SSO 统一认证回调落地页（后端 302 带令牌 fragment 到这里），不进 Layout
    { path: '/sso-callback', name: 'sso-callback', component: () => import('../views/SsoCallback.vue') },
    {
      path: '/',
      component: () => import('../views/Layout.vue'),
      redirect: '/dashboard',
      children: [
        { path: 'dashboard', name: 'dashboard', component: () => import('../views/Dashboard.vue'), meta: { title: '总览' } },
        { path: 'resources', name: 'resources', component: () => import('../views/Resources.vue'), meta: { title: '项目管理' } },
        { path: 'events', name: 'events', component: () => import('../views/Events.vue'), meta: { title: '事件中心' } },
        { path: 'schedule', name: 'schedule', component: () => import('../views/ScheduleTasks.vue'), meta: { title: '定时任务' } },
        { path: 'admin/clusters', name: 'admin-clusters', component: () => import('../views/admin/Clusters.vue'), meta: { title: '集群纳管', admin: true } },
        { path: 'admin/users', name: 'admin-users', component: () => import('../views/admin/Users.vue'), meta: { title: '用户管理', admin: true } },
        { path: 'admin/grants', name: 'admin-grants', component: () => import('../views/admin/Grants.vue'), meta: { title: '权限授权', admin: true } },
        { path: 'admin/groups', name: 'admin-groups', component: () => import('../views/admin/Groups.vue'), meta: { title: '权限组', admin: true } },
        { path: 'admin/ldap', name: 'admin-ldap', component: () => import('../views/admin/LdapConfig.vue'), meta: { title: 'LDAP 配置', admin: true } },
        { path: 'admin/oauth', name: 'admin-oauth', component: () => import('../views/admin/OAuthConfig.vue'), meta: { title: 'SSO 认证', admin: true } },
        { path: 'admin/tokens', name: 'admin-tokens', component: () => import('../views/admin/Tokens.vue'), meta: { title: '集成令牌', admin: true } },
        { path: 'admin/drafts', name: 'admin-drafts', component: () => import('../views/admin/Drafts.vue'), meta: { title: '暂存变更', admin: true } },
        { path: 'admin/audit', name: 'admin-audit', component: () => import('../views/admin/Audit.vue'), meta: { title: '审计日志', admin: true, scopeOk: true } },
        { path: 'admin/recordings', name: 'admin-recordings', component: () => import('../views/admin/Recordings.vue'), meta: { title: '终端录像', admin: true, scopeOk: true } },
        { path: 'admin/settings', name: 'admin-settings', component: () => import('../views/admin/Settings.vue'), meta: { title: '系统设置', admin: true } },
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: '/dashboard' },
  ],
})

router.beforeEach((to) => {
  // 认证中心把 code 重定向到了前端页面（回调地址只注册到域名根时会出现）：
  // 转发给后端回调接口完成换票——state cookie 的 Path 限定在回调路径上，导航过去会自动携带
  const q = to.query as Record<string, any>
  if (q.code && q.state) {
    window.location.replace(
      `/api/v1/auth/oauth/callback?code=${encodeURIComponent(String(q.code))}&state=${encodeURIComponent(String(q.state))}`,
    )
    return false
  }
  const auth = useAuthStore()
  // /sso-callback 免登录（它自己负责把 fragment 里的令牌落成登录态）
  if (!auth.isLoggedIn && to.path !== '/login' && to.path !== '/sso-callback') {
    return '/login'
  }
  if (auth.isLoggedIn && to.path === '/login') {
    return '/dashboard'
  }
  // 管理页默认仅 SuperAdmin；带 scopeOk 标记的页面（审计/录像）对集群/命名空间管理员开放
  if (to.meta.admin && !(auth.role === 'SuperAdmin' || (to.meta.scopeOk && auth.scopeAdmin))) {
    return '/dashboard'
  }
})

export default router
