import { aiStream, http } from './http'

// ---------- 集群 ----------
export const listClusters = () => http.get('/clusters')
export const adminListClusters = () => http.get('/admin/clusters')
export const createCluster = (data: any) => http.post('/admin/clusters', data)
export const updateCluster = (name: string, data: any) => http.put(`/admin/clusters/${name}`, data)
export const deleteCluster = (name: string) => http.delete(`/admin/clusters/${name}`)
export const adminTestCluster = (name: string) => http.post(`/admin/clusters/${name}/test`)
export const kubeconfigContexts = (kubeconfig: string) => http.post('/admin/kubeconfig/contexts', { kubeconfig })
export const kubeconfigTest = (kubeconfig: string, context_name: string) => http.post('/admin/kubeconfig/test', { kubeconfig, context_name })

// ---------- 资源 ----------
export const listNamespaces = (cluster: string, params?: any) => http.get(`/clusters/${cluster}/namespaces`, { params })
export const listResources = (cluster: string, params: any) => http.get(`/clusters/${cluster}/resources`, { params })
// 自定义资源发现：按集群返回 CRD 列表（group/plural/kind/versions/namespaced...）
export interface CRInfo {
  group: string
  version: string
  versions: string[]
  kind: string
  plural: string
  singular: string
  shortNames: string[]
  namespaced: boolean
}
export const listCRs = (cluster: string) => http.get<{ items: CRInfo[] }>(`/clusters/${cluster}/crs`).then(r => r.data?.items || [])
export const getResource = (cluster: string, params: any) => http.get(`/clusters/${cluster}/resource`, { params })
// 镜像历史（只读；Deployment/StatefulSet/DaemonSet，数据来自 K8s 原生版本记录）
export const getImageHistory = (cluster: string, params: any) => http.get(`/clusters/${cluster}/image-history`, { params })
export const updateResource = (cluster: string, params: any, body: any) => http.put(`/clusters/${cluster}/resource`, body, { params })
export const resourceFormEdit = (cluster: string, params: any, section: string, payload: any) =>
  http.put(`/clusters/${cluster}/resedit`, { ...params, section, payload })
// 资源暂存草稿（表单编辑「暂存」：只存平台不写集群；应用时把 payload 原样走 resourceFormEdit 后删草稿）
export const draftStage = (cluster: string, data: any) => http.put(`/clusters/${cluster}/resdraft`, data)
export const draftList = (cluster: string, params: any) => http.get(`/clusters/${cluster}/resdrafts`, { params })
export const draftDelete = (cluster: string, data: any) => http.delete(`/clusters/${cluster}/resdraft`, { data })
export const adminListDrafts = () => http.get('/admin/resource-drafts')
export const deleteResource = (cluster: string, params: any) => http.delete(`/clusters/${cluster}/resource`, { params })
export const applyYAML = (cluster: string, data: any) => http.post(`/clusters/${cluster}/apply`, data)
export const scaleResource = (cluster: string, data: any) => http.post(`/clusters/${cluster}/scale`, data)
export const restartResource = (cluster: string, data: any) => http.post(`/clusters/${cluster}/restart`, data)
export const stopResource = (cluster: string, data: any) => http.post(`/clusters/${cluster}/stop`, data)
export const startResource = (cluster: string, data: any) => http.post(`/clusters/${cluster}/start`, data)
// 事件中心：search 为后端模糊检索（对象/原因/消息 LIKE，命中上限 500 条）
export const listEvents = (cluster: string, namespace?: string, search?: string) =>
  http.get(`/clusters/${cluster}/events`, { params: { ...(namespace ? { namespace } : {}), ...(search ? { search } : {}) } })
// Pod 内文件浏览 + 下载（需 pods 的 download 权限；多文件时 path 重复传参，后端 tar 打包）
export const listPodFiles = (cluster: string, namespace: string, pod: string, path: string, container?: string) =>
  http.get(`/clusters/${cluster}/pods/${namespace}/${pod}/files`, { params: { path, container } })
// 下载与上传同口径放宽到 10 分钟：后端全量收完再返回，上限 200MB，60s 全局超时对大文件不够
export const downloadPodFile = (cluster: string, namespace: string, pod: string, paths: string[], container?: string) =>
  http.get(`/clusters/${cluster}/pods/${namespace}/${pod}/download`, { params: { path: paths, container }, paramsSerializer: { indexes: null }, responseType: 'blob', timeout: 600000 })

// 上传文件到容器当前目录（需 pods 的 upload 权限，与 download 分开授权；逐个文件提交）
// 超时放宽到 10 分钟：上限 200MB 走 exec stdin 串行写入，60s 全局超时对大文件/慢链路不够
export const uploadPodFile = (cluster: string, namespace: string, pod: string, path: string, file: File, container?: string) => {
  const form = new FormData()
  form.append('file', file)
  return http.post(`/clusters/${cluster}/pods/${namespace}/${pod}/upload`, form, { params: { path, container }, timeout: 600000 })
}
// Pod describe（kubectl describe 风格纯文本，需 pods 的 get 权限）
export const describePod = (cluster: string, namespace: string, pod: string) =>
  http.get(`/clusters/${cluster}/pods/${namespace}/${pod}/describe`)
// 节点概要：角色 + CPU/内存/异构资源（容量/可分配/已分配 requests，需 nodes 的 get 权限）
export const nodeSummary = (cluster: string, name: string) => http.get(`/clusters/${cluster}/nodes/${name}/summary`)
// 调度候选数据（SchedEditor 下拉：节点名/标签键/值）：权限=任一工作负载的 update+scheduling 动作，
// 无需 nodes 的 get/list；无权限返回空集（前端静默降级为手输）
export const schedOptions = (cluster: string) => http.get(`/clusters/${cluster}/sched-options`)
// 节点运维：封锁/恢复调度（cordon，nodes 的 update）；排空拆两步——drain-plan 只读分类预览
// （二次确认依据），evict 单 Pod 驱逐（前端逐个调用，PDB 拦截重试在服务端，放宽到 2 分钟）
export const nodeCordon = (cluster: string, name: string, unschedulable: boolean) =>
  http.post(`/clusters/${cluster}/nodes/${name}/cordon`, { unschedulable })
export const nodeDrainPlan = (cluster: string, name: string, data: { include_daemonsets: boolean; force_bare_pods: boolean }) =>
  http.post(`/clusters/${cluster}/nodes/${name}/drain-plan`, data)
export const nodeEvict = (cluster: string, node: string, data: { namespace: string; name: string; uid: string; grace_seconds?: number }) =>
  http.post(`/clusters/${cluster}/nodes/${node}/evict`, data, { timeout: 120000 })
export const dashboard = (refresh = false) => http.get('/dashboard', { params: refresh ? { refresh: 1 } : {} })
export const getNSAliases = (cluster: string) => http.get(`/admin/clusters/${cluster}/ns-aliases`)
export const putNSAliases = (cluster: string, aliases: Record<string, string>) => http.put(`/admin/clusters/${cluster}/ns-aliases`, aliases)

// ---------- 定时任务（定时重启 / 定时扩缩容）----------
export const listScheduleTasks = () => http.get('/schedule-tasks')
export const createScheduleTask = (cluster: string, data: any) => http.post(`/clusters/${cluster}/schedule-tasks`, data)
export const updateScheduleTask = (id: number, data: any) => http.put(`/schedule-tasks/${id}`, data)
export const deleteScheduleTask = (id: number) => http.delete(`/schedule-tasks/${id}`)
export const runScheduleTask = (id: number) => http.post(`/schedule-tasks/${id}/run`)
export const previewCron = (cron: string) => http.get('/schedule-tasks/preview', { params: { cron } })

// ---------- 用户 / 授权 / 权限组 ----------
export const listUsers = () => http.get('/admin/users')
export const myGrants = () => http.get('/me/grants')
export const createUser = (data: any) => http.post('/admin/users', data)
export const updateUser = (id: number, data: any) => http.put(`/admin/users/${id}`, data)
export const deleteUser = (id: number) => http.delete(`/admin/users/${id}`)
export const getUserGrants = (id: number) => http.get(`/admin/users/${id}/grants`)
export const putUserGrants = (id: number, grants: any[]) => http.put(`/admin/users/${id}/grants`, { grants })
export const getUserGroups = (id: number) => http.get(`/admin/users/${id}/groups`)
// 指定用户的权限明细（直接授权 + 权限组规则并集，含已到期），用户管理页「权限」抽屉用
export const getUserPerms = (id: number) => http.get(`/admin/users/${id}/perms`)

// ---------- 站内消息（权限变更通知 + 到期预警）----------
export interface NotificationItem {
  id: number
  type: 'grant_added' | 'grant_removed' | 'grant_renewed' | 'grant_expiring' | 'account_expiring'
  title: string
  content: string
  read_at: string | null
  created_at: string
}
// _silent：铃铛轮询静默失败（60s 一次，弹全局错误会骚扰）
export const listNotifications = () =>
  http.get<{ items: NotificationItem[]; unread: number }>('/me/notifications', { params: { limit: 50, _silent: true } })
export const readNotifications = (data: { ids?: number[]; all?: boolean }) => http.post('/me/notifications/read', data)

// ---------- API 令牌（Jenkins / 工单平台集成）----------
export const listApiTokens = () => http.get('/admin/tokens')
export const createApiToken = (name: string) => http.post('/admin/tokens', { name })
export const revokeApiToken = (id: number) => http.delete(`/admin/tokens/${id}`)
export const listGroups = () => http.get('/admin/groups')
export const createGroup = (data: any) => http.post('/admin/groups', data)
export const getGroup = (id: number) => http.get(`/admin/groups/${id}`)
export const updateGroup = (id: number, data: any) => http.put(`/admin/groups/${id}`, data)
export const deleteGroup = (id: number) => http.delete(`/admin/groups/${id}`)

// ---------- LDAP ----------
export const listLDAP = () => http.get('/admin/ldap')
export const createLDAP = (data: any) => http.post('/admin/ldap', data)
export const updateLDAP = (id: number, data: any) => http.put(`/admin/ldap/${id}`, data)
export const deleteLDAP = (id: number) => http.delete(`/admin/ldap/${id}`)
export const testLDAP = (data: any) => http.post('/admin/ldap/test', data)

// ---------- SSO 统一认证（OAuth 2.0 授权码）----------
// 登录页公开状态（是否展示 SSO 按钮 / 是否仅 SSO）；配置管理走 admin 两条
export const oauthStatus = () => http.get('/auth/oauth/status')
export const getOAuthConfig = () => http.get('/admin/oauth')
export const putOAuthConfig = (data: any) => http.put('/admin/oauth', data)

// ---------- 审计 / 设置 ----------
export const listAudits = (params: any) => http.get('/admin/audits', { params })
export const getSettings = () => http.get('/admin/settings')
export const putSettings = (data: any) => http.put('/admin/settings', data)

// ---------- AI 助手（严格只读） ----------
// 流式接口走 fetch + SSE（axios 浏览器端不支持流式读），URL 用全路径。
// 可选 signal：对话框关闭/停止时中断，后端随请求 ctx 级联取消上游模型调用
export const aiDiagnose = (data: { cluster: string; namespace: string; object_type: string; object_name: string }, onDelta: (t: string) => void, signal?: AbortSignal) =>
  aiStream('/api/v1/ai/diagnose', data, onDelta, signal)
export const aiYamlExplain = (yaml: string, onDelta: (t: string) => void, signal?: AbortSignal) =>
  aiStream('/api/v1/ai/yaml-explain', { yaml }, onDelta, signal)
export const aiYamlGenerate = (prompt: string, onDelta: (t: string) => void, signal?: AbortSignal) =>
  aiStream('/api/v1/ai/yaml-generate', { prompt }, onDelta, signal)
export const aiYamlReview = (yaml: string, onDelta: (t: string) => void, signal?: AbortSignal) =>
  aiStream('/api/v1/ai/yaml-review', { yaml }, onDelta, signal)
// 排查助手多轮对话：messages 为 {role:'user'|'assistant', content}[]，AI 回复增量走 onDelta
export const aiChat = (messages: { role: string; content: string }[], onDelta: (t: string) => void, signal?: AbortSignal) =>
  aiStream('/api/v1/ai/chat', { messages }, onDelta, signal)
// 总览一键巡检（上下文=总览页同源数据，权限同口径）
export const aiInspect = (onDelta: (t: string) => void, signal?: AbortSignal) =>
  aiStream('/api/v1/ai/inspect', {}, onDelta, signal)
// 日志页 AI 分析（上下文=尾部日志+Pod状态+24h事件，权限与日志查看同口径）
export const aiLogs = (data: { cluster: string; namespace: string; pod: string; container?: string; previous?: boolean }, onDelta: (t: string) => void, signal?: AbortSignal) =>
  aiStream('/api/v1/ai/logs', data, onDelta, signal)
// YAML 自然语言修改（产出草稿，diff 确认后进编辑器走既有保存通道）
export const aiYamlEdit = (yaml: string, prompt: string, onDelta: (t: string) => void, signal?: AbortSignal) =>
  aiStream('/api/v1/ai/yaml-edit', { yaml, prompt }, onDelta, signal)
// 终端录像 AI 审计摘要（权限与录像回放同口径）
// 终端会话风险重评（管理员手动触发；会话结束时会自动评估，此为兜底/重评）
export const reEvalRecordingRisk = (id: number) =>
  http.post<{ risk_level: string; risk_why: string }>(`/admin/exec-recordings/${id}/risk-reeval`)
// 批量重评（历史录像补评/模型更新后刷新；单批≤50，串行评估防打爆网关）
export const reEvalRecordingRiskBatch = (ids: number[]) =>
  http.post<{ ok: number; failed: number; skipped: number; results: { id: number; risk_level?: string; risk_why?: string; error?: string }[] }>(
    '/admin/exec-recordings/risk-reeval-batch', { ids }, { timeout: 600000 })
export const aiRecordingSummary = (id: number, onDelta: (t: string) => void, signal?: AbortSignal) =>
  aiStream('/api/v1/ai/recording-summary', { id }, onDelta, signal)

// AI 模型接入配置（SuperAdmin；api_key 只写不读，has_key 标记是否已配置）
export const getAIConfig = () => http.get('/admin/ai')
export const putAIConfig = (data: any) => http.put('/admin/ai', data)
export const testAI = () => http.post('/admin/ai/test')

// ---------- 权限预设（系统设置 → 权限预设）----------
export const getPermPresets = () => http.get('/admin/perm-presets')
export const putPermPresets = (data: any) => http.put('/admin/perm-presets', data)
export const resetPermPresets = () => http.delete('/admin/perm-presets')
// mode=all 为设置页一键检查；旧 resource+action / preset 参数保留给既有调用方，行为不变
export const syncPermPresets = (apply: boolean, resource?: string, action?: string, preset?: string, mode?: 'all') =>
  http.post('/admin/perm-presets/sync', { apply, resource, action, preset, mode })

// ---------- 终端录像 ----------
export const listExecRecordings = (params: any) => http.get('/admin/exec-recordings', { params })
export const getRecordingCast = (id: number) =>
  http.get(`/admin/exec-recordings/${id}/cast`, { responseType: 'text', transformResponse: [(d: any) => d] })
