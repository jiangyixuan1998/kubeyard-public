import { defineStore } from 'pinia'

interface UserInfo {
  id: number
  username: string
  display_name: string
  email: string
  role: string
  source: string
  account_type: string       // personal 个人 / service 服务
  expires_at: string | null  // 账号有效期；null=永久（到期个人账号仍可登录，仅失去资源权限）
}

export const useAuthStore = defineStore('auth', {
  state: () => ({
    accessToken: localStorage.getItem('ky_access') || '',
    refreshToken: localStorage.getItem('ky_refresh') || '',
    user: JSON.parse(localStorage.getItem('ky_user') || 'null') as UserInfo | null,
    // 是否为集群/命名空间管理员（可看管辖范围内的审计日志与终端录像）
    scopeAdmin: localStorage.getItem('ky_scope_admin') === '1',
  }),
  getters: {
    isLoggedIn: (s) => !!s.accessToken,
    role: (s) => s.user?.role || '',
    username: (s) => s.user?.display_name || s.user?.username || '',
  },
  actions: {
    setTokens(access: string, refresh: string) {
      this.accessToken = access
      this.refreshToken = refresh
      localStorage.setItem('ky_access', access)
      localStorage.setItem('ky_refresh', refresh)
    },
    setUser(user: UserInfo) {
      this.user = user
      localStorage.setItem('ky_user', JSON.stringify(user))
    },
    // 拉取生效授权，计算是否管理员级（资源=* 且 动词=* 的规则，SuperAdmin 直接为 true）
    async loadScope() {
      if (this.role === 'SuperAdmin') {
        this.scopeAdmin = true
        localStorage.setItem('ky_scope_admin', '1')
        return
      }
      try {
        const { http } = await import('../api/http')
        const res = await http.get('/me/grants')
        const rows: any[] = res.data || []
        this.scopeAdmin = rows.some((r) => (r.resources === '*' || r.resources === '') && (r.verbs === '*' || r.verbs === ''))
        localStorage.setItem('ky_scope_admin', this.scopeAdmin ? '1' : '0')
      } catch {
        this.scopeAdmin = false
        localStorage.setItem('ky_scope_admin', '0')
      }
    },
    async login(username: string, password: string) {
      const { http } = await import('../api/http')
      const res = await http.post('/auth/login', { username, password })
      this.setTokens(res.data.access_token, res.data.refresh_token)
      this.setUser(res.data.user)
      this.loadScope() // 异步计算管理员范围，不阻塞登录跳转
    },
    async logout() {
      try {
        const { http } = await import('../api/http')
        await http.post('/auth/logout')
      } catch { /* 忽略 */ }
      this.clear()
    },
    clear() {
      this.accessToken = ''
      this.refreshToken = ''
      this.user = null
      this.scopeAdmin = false
      localStorage.removeItem('ky_access')
      localStorage.removeItem('ky_refresh')
      localStorage.removeItem('ky_user')
      localStorage.removeItem('ky_scope_admin')
    },
  },
})
