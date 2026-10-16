// 权限五元组规则的共享常量与工具（权限授权 / 权限组两页共用，保证行为一致）
import { reactive } from 'vue'
import { getPermPresets } from '../api'

export const verbOptions = ['get', 'list', 'watch', 'create', 'update', 'delete', 'exec', 'logs', 'download', 'upload']

// 动作下拉的中文别名（value 仍为英文动词，与后端五元组一致）
export const verbAliases: Record<string, string> = {
  get: 'get（查看详情）',
  list: 'list（列表）',
  watch: 'watch（监听）',
  create: 'create（创建）',
  update: 'update（更新）',
  delete: 'delete（删除）',
  exec: 'exec（终端）',
  logs: 'logs（日志）',
  download: 'download（下载文件）',
  upload: 'upload（上传文件）',
}

// 资源下拉的友好别名（与后端 resDisplayAliases 同一套，改一处记得同步另一处；value 仍为资源名）
export const resourceAliases: Record<string, string> = {
  deployments: '应用 (deployments)',
  pods: '运行实例 (pods)',
  services: '服务 (services)',
  statefulsets: '有状态应用 (statefulsets)',
  daemonsets: '守护进程集 (daemonsets)',
  jobs: '任务 (jobs)',
  cronjobs: '定时任务 (cronjobs)',
  horizontalpodautoscalers: '自动扩缩容 (HPA)',
  poddisruptionbudgets: '中断预算 (PDB)',
  ingresses: '路由 (ingresses)',
  endpoints: '端点 (endpoints)',
  networkpolicies: '网络策略 (networkpolicies)',
  endpointslices: '端点切片 (endpointslices)',
  ingressclasses: 'Ingress 类 (ingressclasses)',
  configmaps: '配置 (configmaps)',
  secrets: '密钥 (secrets)',
  persistentvolumeclaims: '存储声明 (persistentvolumeclaims)',
  resourcequotas: '资源配额 (resourcequotas)',
  limitranges: '限制范围 (limitranges)',
  serviceaccounts: '服务账号 (serviceaccounts)',
  roles: '角色 (roles)',
  rolebindings: '角色绑定 (rolebindings)',
  clusterroles: '集群角色 (clusterroles)',
  clusterrolebindings: '集群角色绑定 (clusterrolebindings)',
  storageclasses: '存储类 (storageclasses)',
  nodes: '节点 (nodes)',
  namespaces: '命名空间 (namespaces)',
}

export const resourceOptions = [
  'pods', 'deployments', 'statefulsets', 'daemonsets', 'jobs', 'cronjobs',
  'horizontalpodautoscalers', 'poddisruptionbudgets',
  'services', 'ingresses', 'endpoints', 'endpointslices', 'networkpolicies', 'ingressclasses',
  'configmaps', 'secrets', 'resourcequotas', 'limitranges', 'serviceaccounts',
  'roles', 'rolebindings', 'clusterroles', 'clusterrolebindings',
  'persistentvolumeclaims', 'storageclasses', 'nodes', 'namespaces',
]

// 动作（动词）随资源类型联动：
// pods 全量；其他内置资源去掉 exec/logs/download/upload（Pod 专属动词）；
// CR = 只读（get/list）+ 增删改（create/update/delete）——不懂 K8s 的管理员只需理解「能看 / 能改」
const podOnlyVerbs = ['exec', 'logs', 'download', 'upload']
const crVerbOptions = ['get', 'list', 'create', 'update', 'delete']
// 可修改字段下拉中 CR 适用的令牌（字段路径含 . 另算）
const crActionKeys = ['create', 'update', 'delete']

export function verbOptionsOf(resList?: string[]): string[] {
  const list = resList || []
  if (!list.length || list.includes('*')) return verbOptions
  const hasCR = list.some((r) => typeof r === 'string' && r.includes('/') && !r.startsWith('*/'))
  const hasPods = list.includes('pods')
  const hasBuiltIn = list.some((r) => r !== 'pods' && !(typeof r === 'string' && r.includes('/')))
  if (!hasCR && !hasPods && !hasBuiltIn) return verbOptions // 未知形态兜底全量
  const allowed = new Set<string>()
  if (hasPods || hasBuiltIn) {
    for (const v of verbOptions) {
      if (podOnlyVerbs.includes(v) && !hasPods) continue
      allowed.add(v)
    }
  }
  if (hasCR) crVerbOptions.forEach((v) => allowed.add(v))
  return verbOptions.filter((v) => allowed.has(v))
}

// 资源级预设的动词候选；显式保留 *，避免 admin 预设在下拉中变成未知值
export function resourcePresetVerbOptions(res: string, preset?: RolePreset): string[] {
  const options = verbOptionsOf([res])
  return preset?.verbs.includes('*') ? ['*', ...options] : options
}

// 资源切换联动：收敛已选动词与可修改字段——切到 CR 时动词只留只读+增删改、
// 可修改字段只留 CR 三类与字段路径（含 .）；切到非 pods 资源时去掉 Pod 专属动词
export function onResListChange(rule: any) {
  onListChange(rule, 'resList')
  const allowed = verbOptionsOf(rule.resList)
  const vl = rule.verbList
  if (Array.isArray(vl) && vl.length && !vl.includes('*')) {
    rule.verbList = vl.filter((v: string) => allowed.includes(v))
  }
  const resList: string[] = rule.resList || []
  const hasCR = resList.some((r) => typeof r === 'string' && r.includes('/') && !r.startsWith('*/'))
  const al = rule.actionList
  if (hasCR && Array.isArray(al) && al.length && !al.includes('*')) {
    rule.actionList = al.filter((a: string) => a.includes('.') || crActionKeys.includes(a))
  }
}

// —— 可配置权限预设（系统设置 → 权限预设 维护；未加载/失败时回落内置默认）——
export interface ActionMeta { label: string; fields: string[] }
export interface RolePreset { verbs: string[]; actions: string[] }
export type RolePresetKind = 'viewer' | 'dev' | 'admin'
export type ResourceRolePresets = Record<string, Partial<Record<RolePresetKind, RolePreset | null>>>

// 内置动作目录（与后端 middleware.builtinActions 一致；fields 为空 = 纯动作令牌如 restart）
const builtinActionCatalog: Record<string, Record<string, ActionMeta>> = {
  deployments: {
    image: { label: '更新镜像', fields: ['spec.template.spec.containers.*.image', 'spec.template.spec.initContainers.*.image'] },
    command: { label: '入口/命令 (Entrypoint/CMD)', fields: ['spec.template.spec.containers.*.command', 'spec.template.spec.containers.*.args', 'spec.template.spec.initContainers.*.command', 'spec.template.spec.initContainers.*.args'] },
    ports: { label: '更新端口', fields: ['spec.template.spec.containers.*.ports', 'spec.template.spec.initContainers.*.ports'] },
    resources: { label: '资源配置（limits/requests）', fields: ['spec.template.spec.containers.*.resources', 'spec.template.spec.initContainers.*.resources'] },
    env: { label: '环境变量增删改', fields: ['spec.template.spec.containers.*.env', 'spec.template.spec.initContainers.*.env'] },
    probes: { label: '健康检查（存活/就绪/启动探针）', fields: ['spec.template.spec.containers.*.livenessProbe', 'spec.template.spec.containers.*.readinessProbe', 'spec.template.spec.containers.*.startupProbe', 'spec.template.spec.initContainers.*.livenessProbe', 'spec.template.spec.initContainers.*.readinessProbe', 'spec.template.spec.initContainers.*.startupProbe'] },
    storage: { label: '存储挂载（卷/持久化）', fields: ['spec.template.spec.containers.*.volumeMounts', 'spec.template.spec.initContainers.*.volumeMounts', 'spec.template.spec.volumes'] },
    scheduling: { label: '调度策略（节点选择/亲和）', fields: ['spec.template.spec.nodeSelector', 'spec.template.spec.nodeName', 'spec.template.spec.affinity'] },
    replicas: { label: '副本数', fields: ['spec.replicas'] },
    restart: { label: '重启', fields: [] },
    stop: { label: '停止', fields: [] },
    start: { label: '启动', fields: [] },
  },
  statefulsets: {
    image: { label: '更新镜像', fields: ['spec.template.spec.containers.*.image', 'spec.template.spec.initContainers.*.image'] },
    command: { label: '入口/命令 (Entrypoint/CMD)', fields: ['spec.template.spec.containers.*.command', 'spec.template.spec.containers.*.args', 'spec.template.spec.initContainers.*.command', 'spec.template.spec.initContainers.*.args'] },
    ports: { label: '更新端口', fields: ['spec.template.spec.containers.*.ports', 'spec.template.spec.initContainers.*.ports'] },
    resources: { label: '资源配置（limits/requests）', fields: ['spec.template.spec.containers.*.resources', 'spec.template.spec.initContainers.*.resources'] },
    env: { label: '环境变量增删改', fields: ['spec.template.spec.containers.*.env', 'spec.template.spec.initContainers.*.env'] },
    probes: { label: '健康检查（存活/就绪/启动探针）', fields: ['spec.template.spec.containers.*.livenessProbe', 'spec.template.spec.containers.*.readinessProbe', 'spec.template.spec.containers.*.startupProbe', 'spec.template.spec.initContainers.*.livenessProbe', 'spec.template.spec.initContainers.*.readinessProbe', 'spec.template.spec.initContainers.*.startupProbe'] },
    storage: { label: '存储挂载（卷/持久化）', fields: ['spec.template.spec.containers.*.volumeMounts', 'spec.template.spec.initContainers.*.volumeMounts', 'spec.template.spec.volumes'] },
    scheduling: { label: '调度策略（节点选择/亲和）', fields: ['spec.template.spec.nodeSelector', 'spec.template.spec.nodeName', 'spec.template.spec.affinity'] },
    replicas: { label: '副本数', fields: ['spec.replicas'] },
    restart: { label: '重启', fields: [] },
    stop: { label: '停止', fields: [] },
    start: { label: '启动', fields: [] },
  },
  daemonsets: {
    image: { label: '更新镜像', fields: ['spec.template.spec.containers.*.image', 'spec.template.spec.initContainers.*.image'] },
    command: { label: '入口/命令 (Entrypoint/CMD)', fields: ['spec.template.spec.containers.*.command', 'spec.template.spec.containers.*.args', 'spec.template.spec.initContainers.*.command', 'spec.template.spec.initContainers.*.args'] },
    ports: { label: '更新端口', fields: ['spec.template.spec.containers.*.ports', 'spec.template.spec.initContainers.*.ports'] },
    resources: { label: '资源配置（limits/requests）', fields: ['spec.template.spec.containers.*.resources', 'spec.template.spec.initContainers.*.resources'] },
    env: { label: '环境变量增删改', fields: ['spec.template.spec.containers.*.env', 'spec.template.spec.initContainers.*.env'] },
    probes: { label: '健康检查（存活/就绪/启动探针）', fields: ['spec.template.spec.containers.*.livenessProbe', 'spec.template.spec.containers.*.readinessProbe', 'spec.template.spec.containers.*.startupProbe', 'spec.template.spec.initContainers.*.livenessProbe', 'spec.template.spec.initContainers.*.readinessProbe', 'spec.template.spec.initContainers.*.startupProbe'] },
    storage: { label: '存储挂载（卷/持久化）', fields: ['spec.template.spec.containers.*.volumeMounts', 'spec.template.spec.initContainers.*.volumeMounts', 'spec.template.spec.volumes'] },
    scheduling: { label: '调度策略（节点选择/亲和）', fields: ['spec.template.spec.nodeSelector', 'spec.template.spec.nodeName', 'spec.template.spec.affinity'] },
    restart: { label: '重启', fields: [] },
  },
  jobs: {
    image: { label: '更新镜像', fields: ['spec.template.spec.containers.*.image', 'spec.template.spec.initContainers.*.image'] },
    command: { label: '入口/命令 (Entrypoint/CMD)', fields: ['spec.template.spec.containers.*.command', 'spec.template.spec.containers.*.args', 'spec.template.spec.initContainers.*.command', 'spec.template.spec.initContainers.*.args'] },
    ports: { label: '更新端口', fields: ['spec.template.spec.containers.*.ports', 'spec.template.spec.initContainers.*.ports'] },
    resources: { label: '资源配置（limits/requests）', fields: ['spec.template.spec.containers.*.resources', 'spec.template.spec.initContainers.*.resources'] },
    env: { label: '环境变量增删改', fields: ['spec.template.spec.containers.*.env', 'spec.template.spec.initContainers.*.env'] },
    probes: { label: '健康检查（存活/就绪/启动探针）', fields: ['spec.template.spec.containers.*.livenessProbe', 'spec.template.spec.containers.*.readinessProbe', 'spec.template.spec.containers.*.startupProbe', 'spec.template.spec.initContainers.*.livenessProbe', 'spec.template.spec.initContainers.*.readinessProbe', 'spec.template.spec.initContainers.*.startupProbe'] },
    storage: { label: '存储挂载（卷/持久化）', fields: ['spec.template.spec.containers.*.volumeMounts', 'spec.template.spec.initContainers.*.volumeMounts', 'spec.template.spec.volumes'] },
    scheduling: { label: '调度策略（节点选择/亲和）', fields: ['spec.template.spec.nodeSelector', 'spec.template.spec.nodeName', 'spec.template.spec.affinity'] },
  },
  cronjobs: {
    image: { label: '更新镜像', fields: ['spec.jobTemplate.spec.template.spec.containers.*.image', 'spec.jobTemplate.spec.template.spec.initContainers.*.image'] },
    command: { label: '入口/命令 (Entrypoint/CMD)', fields: ['spec.jobTemplate.spec.template.spec.containers.*.command', 'spec.jobTemplate.spec.template.spec.containers.*.args', 'spec.jobTemplate.spec.template.spec.initContainers.*.command', 'spec.jobTemplate.spec.template.spec.initContainers.*.args'] },
    ports: { label: '更新端口', fields: ['spec.jobTemplate.spec.template.spec.containers.*.ports', 'spec.jobTemplate.spec.template.spec.initContainers.*.ports'] },
    resources: { label: '资源配置（limits/requests）', fields: ['spec.jobTemplate.spec.template.spec.containers.*.resources', 'spec.jobTemplate.spec.template.spec.initContainers.*.resources'] },
    env: { label: '环境变量增删改', fields: ['spec.jobTemplate.spec.template.spec.containers.*.env', 'spec.jobTemplate.spec.template.spec.initContainers.*.env'] },
    probes: { label: '健康检查（存活/就绪/启动探针）', fields: ['spec.jobTemplate.spec.template.spec.containers.*.livenessProbe', 'spec.jobTemplate.spec.template.spec.containers.*.readinessProbe', 'spec.jobTemplate.spec.template.spec.containers.*.startupProbe', 'spec.jobTemplate.spec.template.spec.initContainers.*.livenessProbe', 'spec.jobTemplate.spec.template.spec.initContainers.*.readinessProbe', 'spec.jobTemplate.spec.template.spec.initContainers.*.startupProbe'] },
    storage: { label: '存储挂载（卷/持久化）', fields: ['spec.jobTemplate.spec.template.spec.containers.*.volumeMounts', 'spec.jobTemplate.spec.template.spec.initContainers.*.volumeMounts', 'spec.jobTemplate.spec.template.spec.volumes'] },
    scheduling: { label: '调度策略（节点选择/亲和）', fields: ['spec.jobTemplate.spec.template.spec.nodeSelector', 'spec.jobTemplate.spec.template.spec.nodeName', 'spec.jobTemplate.spec.template.spec.affinity'] },
  },
  pods: {
    image: { label: '更新镜像', fields: ['spec.containers.*.image', 'spec.initContainers.*.image'] },
    command: { label: '入口/命令 (Entrypoint/CMD)', fields: ['spec.containers.*.command', 'spec.containers.*.args', 'spec.initContainers.*.command', 'spec.initContainers.*.args'] },
    ports: { label: '更新端口', fields: ['spec.containers.*.ports', 'spec.initContainers.*.ports'] },
    resources: { label: '资源配置（limits/requests）', fields: ['spec.containers.*.resources', 'spec.initContainers.*.resources'] },
    env: { label: '环境变量增删改', fields: ['spec.containers.*.env', 'spec.initContainers.*.env'] },
  },
  configmaps: { data: { label: '配置数据', fields: ['data'] } },
  secrets: { data: { label: '数据', fields: ['data', 'stringData'] } },
  services: {
    ports: { label: '更新端口', fields: ['spec.ports'] },
    selector: { label: '选择器', fields: ['spec.selector'] },
  },
  ingresses: {
    routes: { label: '路由规则（host/路径/后端服务）', fields: ['spec.rules'] },
  },
  persistentvolumeclaims: { storage: { label: '容量', fields: ['spec.resources.requests.storage'] } },
}

// 内置角色快捷预设（与后端 middleware.builtinPresets 一致）。
// dev 动作池覆盖全部内置资源的细粒度动作，应用时按所选资源裁剪（trimPreset）
const builtinRolePresets: Record<string, RolePreset> = {
  viewer: { verbs: ['get', 'list', 'logs'], actions: ['*'] },
  dev: { verbs: ['get', 'list', 'watch', 'update', 'delete', 'exec', 'logs', 'download', 'upload'], actions: ['image', 'command', 'ports', 'resources', 'env', 'probes', 'storage', 'scheduling', 'data', 'routes', 'selector', 'replicas', 'restart', 'stop', 'start'] },
  admin: { verbs: ['*'], actions: ['*'] },
}

// 推荐的资源级 dev 预设；仅在后端 resource_preset_mode=scoped 时生效。
const builtinResourceRolePresets: ResourceRolePresets = {
  pods: { dev: { verbs: ['get', 'list', 'watch', 'update', 'delete', 'exec', 'logs', 'download', 'upload'], actions: ['image', 'command', 'ports', 'resources', 'env'] } },
  deployments: { dev: { verbs: ['get', 'list', 'watch', 'update'], actions: ['image', 'command', 'ports', 'resources', 'env', 'probes', 'storage', 'scheduling', 'replicas', 'restart', 'stop', 'start'] } },
  statefulsets: { dev: { verbs: ['get', 'list', 'watch', 'update'], actions: ['image', 'command', 'ports', 'resources', 'env', 'probes', 'storage', 'scheduling', 'replicas', 'restart', 'stop', 'start'] } },
  daemonsets: { dev: { verbs: ['get', 'list', 'watch', 'update'], actions: ['image', 'command', 'ports', 'resources', 'env', 'probes', 'storage', 'scheduling', 'restart'] } },
  jobs: { dev: { verbs: ['get', 'list', 'watch', 'update'], actions: ['image', 'command', 'ports', 'resources', 'env', 'probes', 'storage', 'scheduling'] } },
  cronjobs: { dev: { verbs: ['get', 'list', 'watch', 'update'], actions: ['image', 'command', 'ports', 'resources', 'env', 'probes', 'storage', 'scheduling'] } },
}

// —— 预设按资源裁剪（与后端 middleware.TrimPresetForResources 同口径）——
// Pod 专属动词仅范围含 pods 时保留；动作与所选资源目录键求交（* / 字段路径保留）
const podOnlyVerbs2 = ['exec', 'logs', 'download', 'upload']

function trimVerbs(verbs: string[], resList: string[]): string[] {
  if (verbs.includes('*')) return [...verbs]
  const hasPods = resList.includes('pods')
  const out = verbs.filter((v) => !podOnlyVerbs2.includes(v) || hasPods)
  return out.length ? out : [...verbs]
}

function trimActions(actions: string[], resList: string[], hasUpdateVerb: boolean): string[] {
  if (actions.includes('*')) return [...actions]
  const catalog = actionCatalog()
  const all = resList.includes('*') || resList.length === 0
  const valid = new Set<string>()
  let hasKnown = false
  for (const r of all ? Object.keys(catalog) : resList) {
    const keys = Object.keys(catalog[r] || {})
    if (keys.length) { hasKnown = true; keys.forEach((k) => valid.add(k)) }
  }
  const out = actions.filter((a) => a.includes('.') || a === 'update' || valid.has(a))
  if (out.length) return out
  // 资源在目录中但动作全不匹配：不退回原始列表（会触发后端校验失败），回退到 update 令牌
  if (hasKnown) return hasUpdateVerb ? ['update'] : []
  // 所选资源均不在动作目录（endpoints/storageclasses/nodes/namespaces 等）：
  // 与 CR 同一约定——动词含 update → update 令牌 = 完整更新（否则 update 动词是死的）
  if (hasUpdateVerb) return ['update']
  return [...actions]
}

// 预设按规则所选资源裁剪（admin 预设 * 语义不裁；范围含 * 或空 = 不限，原样返回）
export function trimPreset(p: RolePreset, resList: string[]): RolePreset {
  const list = (resList || []).filter(Boolean)
  if (!list.length || list.includes('*')) return { verbs: [...p.verbs], actions: [...p.actions] }
  return { verbs: trimVerbs(p.verbs, list), actions: trimActions(p.actions, list, p.verbs.includes('update')) }
}

// 模块级缓存：授权页 / 权限组页 / 设置页共用一份；GET 返回后端合并后的完整生效配置
const presetState = reactive<{
  loaded: boolean
  actions: Record<string, Record<string, ActionMeta>>
  presets: Record<string, RolePreset>
  resourcePresetMode: 'legacy' | 'scoped'
  resourcePresets: ResourceRolePresets
}>({
  loaded: false, actions: {}, presets: {}, resourcePresetMode: 'legacy', resourcePresets: {},
})

export async function loadPermPresets(force = false) {
  if (presetState.loaded && !force) return
  try {
    const d = (await getPermPresets()).data || {}
    // 归一化：后端纯动作令牌（restart 等）的 nil Fields 序列化为 null，统一转空数组，
    // 否则设置页校验迭代 null 会崩、diff 比较也会把未改资源误判为已自定义
    const actions: Record<string, Record<string, ActionMeta>> = {}
    for (const [res, m] of Object.entries(d.actions || {})) {
      actions[res] = Object.fromEntries(
        Object.entries(m || {}).map(([k, meta]) => [k, { label: meta.label || k, fields: meta.fields || [] }]),
      )
    }
    presetState.actions = actions
    presetState.presets = d.presets || {}
    presetState.resourcePresetMode = d.resource_preset_mode === 'scoped' ? 'scoped' : 'legacy'
    presetState.resourcePresets = d.resource_presets || {}
    presetState.loaded = true
  } catch { /* 失败回落内置默认 */ }
}

// 生效动作目录 / 角色预设（未加载时 = 内置默认）
export function actionCatalog(): Record<string, Record<string, ActionMeta>> {
  return presetState.loaded ? presetState.actions : builtinActionCatalog
}
export function rolePresetOf(kind: 'viewer' | 'dev' | 'admin'): RolePreset {
  return (presetState.loaded && presetState.presets[kind]) || builtinRolePresets[kind]
}
function intersectTokens(first: string[], rest: string[][]): string[] {
  if (!rest.length) return [...first]
  return first.filter((token) => rest.every((list) => list.includes(token)))
}

// scoped 模式下的资源级预设；多资源取交集，避免 Pod 的 delete/exec 扩散到 Deployment。
export function rolePresetForResources(kind: RolePresetKind, resList: string[]): RolePreset {
  const global = rolePresetOf(kind)
  const list = (resList || []).filter(Boolean)
  if (!presetState.loaded || presetState.resourcePresetMode !== 'scoped' || !list.length || list.includes('*')) return global
  if (list.some((r) => r.includes('/'))) return global
  const selected = list.map((res) => {
    const override = presetState.resourcePresets[res]?.[kind]
    return override || global
  })
  return {
    verbs: intersectTokens(selected[0].verbs, selected.slice(1).map((p) => p.verbs)),
    actions: intersectTokens(selected[0].actions, selected.slice(1).map((p) => p.actions)),
  }
}
// 供设置页编辑用：当前已加载的原始数据（含 loaded 标记）与内置默认（保存时差异比对，
// 仅提交被修改的资源/预设，未动的保持内置——后续版本更新内置默认可自然生效）
export function permPresetState() { return presetState }
export function builtinActionCatalogOf() { return builtinActionCatalog }
export function builtinRolePresetsOf() { return builtinRolePresets }
export function builtinResourceRolePresetsOf() { return builtinResourceRolePresets }

// 细粒度动作：动作选项来自所选各资源目录的并集（保存时跨资源共用，
// 后端按「当前操作的单个资源」查动作目录，非该资源的动作自然不命中）。
// 资源为 *（全部）时取所有资源目录的并集。
// CR 规则（resList 含 group/plural）只返回 create/update/delete 三类简单动作（CR 不与内置资源一起选）。
export function actionOptionsOf(rule: any): Array<{ value: string; label: string }> {
  const resList: string[] = rule.resList || []
  // CR 规则判定：resList 含 group/plural 全限定名
  const hasCR = resList.some((r) => typeof r === 'string' && r.includes('/') && !r.startsWith('*/'))
  if (hasCR) {
    // 仅返回 CR 适用的「简单动作」
    return [
      { value: 'create', label: '创建' },
      { value: 'update', label: '更新' },
      { value: 'delete', label: '删除' },
    ]
  }
  const catalog = actionCatalog()
  const seen = new Map<string, string>()
  const all = resList.includes('*') || resList.length === 0
  for (const res of all ? Object.keys(catalog) : resList) {
    for (const [value, meta] of Object.entries(catalog[res] || {})) {
      if (!seen.has(value)) seen.set(value, meta.label || value)
    }
  }
  return [...seen].map(([value, label]) => ({ value, label }))
}

// 动作令牌的友好名（动态目录优先，未加载回落内置；CR 令牌静态兜底）
const staticActionAlias: Record<string, string> = {
  create: '创建', update: '完整更新', delete: '删除',
}
export function actionLabelOf(token: string): string {
  const catalog = actionCatalog()
  for (const m of Object.values(catalog)) {
    if (m[token]) return m[token].label || token
  }
  return staticActionAlias[token] || token
}

// verb 是否包含 update（* 也算）
export function hasUpdate(rule: any) {
  const v = rule.verbList || ['*']
  return v.includes('*') || v.includes('update')
}

// 「*」与其他选项互斥：选了 * 就只保留 *（命名空间/资源共用）
export function onListChange(rule: any, key: 'nsList' | 'resList') {
  const list = rule[key]
  if (list?.includes('*') && list.length > 1) rule[key] = ['*']
}

// 「*」与其他动作互斥：选了 * 就不再保留具体动作
export function onVerbChange(rule: any) {
  if (rule.verbList?.includes('*') && rule.verbList.length > 1) rule.verbList = ['*']
}

// 预设：只读 / 开发 / 管理员 —— 勾选内容来自系统设置「权限预设」（未配置回落内置默认），
// 并按所选资源裁剪（Pod 专属动词仅含 pods 的规则保留；动作仅保留所选资源目录支持的项，
// admin 的 * 语义不裁）；不动已选的命名空间/资源/集群。
// CR 规则的预设 CR 感知：只读 = get+list（无日志），开发 = 只读+增删改（字段为完整更新）
export function applyPreset(rule: any, kind: 'viewer' | 'dev' | 'admin') {
  const resList: string[] = rule.resList || []
  const hasCR = resList.some((r) => typeof r === 'string' && r.includes('/') && !r.startsWith('*/'))
  if (hasCR) {
    if (kind === 'viewer') {
      rule.verbList = ['get', 'list']
      rule.actionList = ['*']
    } else if (kind === 'dev') {
      rule.verbList = ['get', 'list', 'create', 'update', 'delete']
      rule.actionList = ['*'] // CR 无字段预设，开发预设 = 完整更新
    } else {
      rule.verbList = ['*']
      rule.actionList = ['*']
    }
    return
  }
  const trimmed = trimPreset(rolePresetForResources(kind, resList), resList)
  rule.verbList = trimmed.verbs
  rule.actionList = trimmed.actions
}

// 行唯一 key（模板 v-for 用，不参与保存与对比）
let rowSeq = 0

// 新规则的默认形态（首个集群 / 全部命名空间 / deployments 只读）
export function newRuleRow(firstClusterId?: number) {
  return {
    _id: ++rowSeq,
    clusterIds: firstClusterId ? [firstClusterId] : [],
    nsList: ['*'],
    resList: ['deployments'],
    verbList: ['get', 'list', 'logs'],
    actionList: ['*'],
    expiresAt: null,
  }
}
// 到期时间快捷档（工单四档中的三个时限，清空=永久）
export const expiryShortcuts = [
  { text: '7 天', value: () => addDays(7) },
  { text: '30 天', value: () => addDays(30) },
  { text: '90 天', value: () => addDays(90) },
]
function addDays(n: number): Date {
  const d = new Date()
  d.setDate(d.getDate() + n)
  return d
}

// 到期时间已过（仅供界面标识）
export function isExpired(rule: any): boolean {
  return !!rule.expiresAt && rule.expiresAt.getTime() < Date.now()
}

// —— 时限状态机（ExpiryTag 组件与计数处共用）——
// expired 已过期 / soon 7 天内到期 / limited 有时限（≥7 天）/ permanent 永久
export type ExpiryState = 'expired' | 'soon' | 'limited' | 'permanent'
const EXPIRY_SOON_MS = 7 * 86400000

export function expiryStateOf(expiresAt?: Date | string | null): ExpiryState {
  if (!expiresAt) return 'permanent'
  const t = expiresAt instanceof Date ? expiresAt.getTime() : new Date(expiresAt).getTime()
  if (Number.isNaN(t)) return 'permanent'
  if (t < Date.now()) return 'expired'
  if (t - Date.now() < EXPIRY_SOON_MS) return 'soon'
  return 'limited'
}

// 剩余天数（向上取整；已过期返回 0）
export function daysLeft(expiresAt?: Date | string | null): number {
  if (!expiresAt) return 0
  const t = expiresAt instanceof Date ? expiresAt.getTime() : new Date(expiresAt).getTime()
  return Math.max(0, Math.ceil((t - Date.now()) / 86400000))
}

function splitList(v?: string): string[] {
  if (!v || v === '*') return ['*']
  return v.split(',').map((s) => s.trim()).filter(Boolean)
}

// 后端规则 → 前端展示行：同形规则（ns/资源/动作/字段[+时限]全同）按集群合并为一行
export function mergeBackendRules(list: any[], withExpiry: boolean): any[] {
  const mergeMap = new Map<string, any>()
  for (const g of list) {
    const nsList = splitList(g.namespace)
    const resList = splitList(g.resource)
    const verbList = splitList(g.verb)
    const actionList = splitList(g.actions)
    const key = JSON.stringify(withExpiry
      ? [nsList, resList, verbList, actionList, g.expires_at || '']
      : [nsList, resList, verbList, actionList])
    const row = mergeMap.get(key)
    if (row) {
      row.clusterIds.push(g.cluster_id)
    } else {
      mergeMap.set(key, {
        _id: ++rowSeq,
        clusterIds: [g.cluster_id], nsList, resList, verbList, actionList,
        expiresAt: g.expires_at ? new Date(g.expires_at) : null,
      })
    }
  }
  return [...mergeMap.values()]
}

// 前端展示行 → 后端规则：多集群行展开为每集群一条；extra 附加字段（如 expires_at）。
// actions 支持多资源规则：后端按「当前操作的单个资源」查 ActionPresets 匹配，非该资源的动作自然不命中
export function expandRows(rows: any[], extra?: (row: any) => Record<string, any>): any[] {
  return rows.flatMap((r) => r.clusterIds.map((cid: number) => ({
    cluster_id: cid,
    namespace: r.nsList?.includes('*') || !r.nsList?.length ? '*' : r.nsList.join(','),
    resource: r.resList?.includes('*') || !r.resList?.length ? '*' : r.resList.join(','),
    verb: !r.verbList?.length || r.verbList.includes('*') ? '*' : r.verbList.join(','),
    actions: !hasUpdate(r) || r.actionList?.includes('*') || !r.actionList?.length
      ? '*'
      : r.actionList.join(','),
    ...(extra ? extra(r) : {}),
  })))
}

// 行校验：返回错误文案（null = 通过）
export function validateRow(rule: any): string | null {
  if (!rule.clusterIds?.length) return '未选择集群'
  if (!rule.nsList?.length || !rule.resList?.length || !rule.verbList?.length) return '命名空间 / 资源 / 动作不能为空'
  // 勾了 update（或 *）必须显式选择细粒度动作或 *（完整更新）：
  // 空动作保存会默认存成 *（最宽松），管理员容易无意下发完整更新权限
  if (hasUpdate(rule) && !rule.actionList?.length) return '已选 update（或 *）动词，需勾选可修改字段动作或 *（完整更新）'
  return null
}
