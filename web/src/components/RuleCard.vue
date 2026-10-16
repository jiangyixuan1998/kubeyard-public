<template>
  <div class="rule-card" :class="{ 'rule-expired': expired }">
    <div class="rule-head">
      <span class="rule-tag">规则 {{ index + 1 }}</span>
      <!-- 有到期时间才显示分色标签（红过期/黄将到期/绿有时限）；永久不渲染，避免每条规则都挂红标 -->
      <ExpiryTag v-if="rule.expiresAt" :expires-at="rule.expiresAt" :show-permanent="false" />
      <el-tag v-else-if="isAdmin" type="success" size="small">管理员权限</el-tag>
      <el-tag v-else-if="isViewer" type="info" size="small">只读</el-tag>
      <el-tag v-else-if="isDev" type="warning" size="small">开发权限</el-tag>
      <div style="flex: 1"></div>
      <el-button-group size="small">
        <el-button size="small" @click="$emit('preset', 'viewer')">只读</el-button>
        <el-button size="small" @click="$emit('preset', 'dev')">开发</el-button>
        <el-button size="small" @click="$emit('preset', 'admin')">管理员</el-button>
      </el-button-group>
      <el-button size="small" type="danger" plain @click="$emit('remove')">删除</el-button>
    </div>

    <div class="rule-grid">
      <div class="rule-field">
        <div class="rule-label">集群</div>
        <el-select :model-value="rule.clusterIds" multiple filterable collapse-tags collapse-tags-tooltip
          placeholder="选择集群（可多选）" style="width: 100%"
          @update:model-value="update('clusterIds', $event)"
          @visible-change="(v: boolean) => v && rule.clusterIds?.forEach(loadNSBy)">
          <el-option v-for="c in clusters" :key="c.id" :label="clusterLabel(c)" :value="c.id" />
        </el-select>
      </div>

      <div class="rule-field">
        <div class="rule-label">命名空间</div>
        <el-select :model-value="rule.nsList" multiple filterable allow-create default-first-option
          placeholder="选择或输入，* 为全部" collapse-tags collapse-tags-tooltip style="width: 100%"
          @update:model-value="(v: any) => { update('nsList', v); onListChange(rule, 'nsList') }"
          @visible-change="(v: boolean) => v && rule.clusterIds?.forEach(loadNSBy)">
          <el-option value="*" label="*（全部）" />
          <el-option v-for="ns in nsOptionsOf(rule)" :key="ns.name"
            :label="ns.alias ? `${ns.alias} (${ns.name})` : ns.name" :value="ns.name" />
        </el-select>
      </div>

      <div class="rule-field">
        <div class="rule-label">资源</div>
        <el-select :model-value="rule.resList" multiple filterable allow-create default-first-option
          placeholder="选择资源，* 为全部" collapse-tags collapse-tags-tooltip
          popper-class="rule-res-popper" placement="bottom-start" style="width: 100%"
          @update:model-value="(v: any) => { update('resList', v); onResListChange(rule) }">
          <el-option value="*" label="*（全部）" />
          <el-option v-for="r in resourceOptions" :key="r" :value="r" :label="resourceAliases[r] || r" />
          <el-option v-for="c in crResOptions" :key="`cr:${c.value}`" :value="c.value" :label="`${c.label} (${c.value})`">
            <!-- 面板限宽后长名称会省略号截断，title 悬停可看全名 -->
            <span :title="`${c.label} (${c.value})`">{{ c.label }} ({{ c.value }})</span>
          </el-option>
        </el-select>
        <div v-if="crNeedsNS" class="rule-hint">已选集群但未选命名空间，仅显示 cluster-scoped CR；先选「命名空间」（或 *）即可看到所有 CR。</div>
        <div v-if="hasCRField(rule)" class="rule-hint">CR 名称已填写「group/plural」全限定名；可修改字段支持输入原始字段路径（如 <code>spec.image</code>），不填默认 *（完整更新）。</div>
      </div>

      <div class="rule-field">
        <div class="rule-label">动作</div>
        <el-select :model-value="rule.verbList" multiple filterable allow-create default-first-option
          placeholder="选择动作，* 为全部" collapse-tags collapse-tags-tooltip style="width: 100%"
          @update:model-value="(v: any) => { update('verbList', v); onVerbChange(rule) }">
          <el-option value="*" label="*（全部）" />
          <el-option v-for="v in verbsOf" :key="v" :value="v" :label="verbAliases[v] || v" />
        </el-select>
      </div>

      <div class="rule-field">
        <div class="rule-label">可修改字段 <span class="hint">（含 update 时生效）</span></div>
        <el-select v-if="hasUpdate(rule)" :model-value="rule.actionList"
          multiple filterable allow-create default-first-option
          placeholder="选择细粒度权限" collapse-tags collapse-tags-tooltip style="width: 100%"
          @update:model-value="(v: any) => update('actionList', v)">
          <el-option value="*" label="*（完整更新）" />
          <el-option v-for="a in actionOptionsOf(rule)" :key="a.value" :value="a.value"
            :disabled="isActionUnmatched(rule, a.value)"
            :label="isActionUnmatched(rule, a.value) && a.value !== '*' ? `${a.label}（未匹配当前资源/动作）` : a.label" />
        </el-select>
        <div v-else class="hint-box">
          <span>动作不含 update，无需配置</span>
        </div>
        <div v-if="hasCRField(rule)" class="rule-hint">CR 资源只匹配 create / update / delete 三类简单动作（前端只显示这几项）；自定义字段路径在 YAML diff 中校验。</div>
      </div>

      <div v-if="withExpiry" class="rule-field">
        <div class="rule-label">到期时间</div>
        <el-date-picker :model-value="rule.expiresAt" type="datetime" placeholder="永久" clearable
          format="YYYY-MM-DD HH:mm" :shortcuts="expiryShortcuts" style="width: 100%"
          @update:model-value="(v: any) => update('expiresAt', v)" />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'
import { listNamespaces } from '../api'
import { useCRs } from '../composables/useCRs'
import {
  verbAliases, resourceOptions, resourceAliases, actionOptionsOf,
  verbOptionsOf, onResListChange, onListChange,
  actionCatalog, rolePresetForResources, hasUpdate, onVerbChange, expiryShortcuts, isExpired,
} from '../utils/grant'
import ExpiryTag from './ExpiryTag.vue'

const props = defineProps<{
  rule: any
  index: number
  clusters: any[]
  nsCache: Record<number, any[]>
  withExpiry?: boolean
}>()

const emit = defineEmits<{
  (e: 'update:rule', patch: Record<string, any>): void
  (e: 'preset', kind: 'viewer' | 'dev' | 'admin'): void
  (e: 'remove'): void
}>()

const expired = computed(() => isExpired(props.rule))
// 预设标签识别兼容 CR 形态（CR 只读 = get+list；CR 开发 = get/list/update/delete）
const isViewer = computed(() => isPresetOf(props.rule, ['get', 'list', 'logs']) || isPresetOf(props.rule, ['get', 'list']))
const isDev = computed(() =>
  isPresetOf(props.rule, rolePresetForResources('dev', props.rule.resList || []).verbs)
  || isPresetOf(props.rule, ['get', 'list', 'update']))
const isAdmin = computed(() => props.rule.verbList?.includes('*'))

// 动作选项随所选资源联动：pods 全量；非 pods 内置去掉 exec/logs/download；CR 仅增删改
const verbsOf = computed(() => verbOptionsOf(props.rule.resList))

// 集群显示名：纳管时填了描述则显示「描述(名称)」，否则显示名称（与项目管理页同口径，可搜索别名）
function clusterLabel(c: any): string {
  const desc = (c.description || '').trim()
  return desc ? `${desc}(${c.name})` : c.name
}

// 预设识别只看动词集（预设不改动命名空间/资源，任意资源范围下勾预设都应显示标签）
function isPresetOf(rule: any, verbs: string[]): boolean {
  const v = rule.verbList
  return Array.isArray(v) && v.length === verbs.length && verbs.every((x) => v.includes(x))
}

function update(key: string, value: any) {
  emit('update:rule', { [key]: value })
}

// 当前规则下，所选资源能匹配的 action 集合（与生效动作目录求并集）；
// 用于在「可修改字段」下拉中标灰不匹配的项（不匹配但允许选——后端 ActionAllowed 兜底）
const actionPresetsByRes = (resList: string[]): Set<string> => {
  const set = new Set<string>(['*']) // * 总是有效
  const catalog = actionCatalog()
  const all = resList.includes('*') || resList.length === 0
  const reses = all ? Object.keys(catalog) : resList
  for (const r of reses) {
    for (const key of Object.keys(catalog[r] || {})) set.add(key)
  }
  return set
}

// 该 action 对当前规则是否「不匹配」——用于「可修改字段」下拉标灰提示
// CR 规则限定 create/update/delete；内置资源检查是否在 presets 中
function isActionUnmatched(rule: any, actionValue: string): boolean {
  if (!actionValue) return false
  if (actionValue.includes('.')) return false // 自定义字段路径
  if (actionValue === '*') return false
  const resList: string[] = rule.resList || []
  const isCRRule = resList.some((r) => typeof r === 'string' && r.includes('/') && !r.startsWith('*/'))
  if (isCRRule) return !['create', 'update', 'delete'].includes(actionValue)
  // 内置资源：未在任何所选资源的 preset 中 → 不匹配
  return !actionPresetsByRes(resList).has(actionValue)
}

// 多集群时命名空间选项 = 所选各集群命名空间的并集（同名合并）
function nsOptionsOf(rule: any): any[] {
  const seen = new Set<string>()
  const out: any[] = []
  for (const cid of rule.clusterIds || []) {
    for (const ns of props.nsCache[cid] || []) {
      if (!seen.has(ns.name)) {
        seen.add(ns.name)
        out.push(ns)
      }
    }
  }
  return out
}

// 按集群加载 namespace 列表（带缓存）
async function loadNSBy(cid: number) {
  if (!cid || props.nsCache[cid]) return
  const name = props.clusters.find((c: any) => c.id === cid)?.name
  if (!name) return
  try {
    const res = await listNamespaces(name, { _silent: 1 })
    props.nsCache[cid] = (res.data || []).map((n: any) => ({ name: n.name, alias: n.alias || '' })).sort((a: any, b: any) => a.name.localeCompare(b.name))
  } catch {
    props.nsCache[cid] = []
  }
}

// ---------- 自定义资源（按所选集群 × namespace 过滤）----------
// 策略：未选集群 → 空；选了集群但未选 namespace → 只显示 cluster-scoped CR（提示用户先选 ns 看 namespaced CR）；
// 选了 namespace（* 或具体多个取并集）→ 全部 CR 可选（cluster-scoped 不受 ns 影响，namespaced CR 跟着 namespace 上下文）
const { load: loadCRs, get: getCRs } = useCRs()

// 监听 clusterIds 变化：缓存里没有的集群主动拉一次（父组件可能未预加载）
watch(() => props.rule.clusterIds, (ids) => {
  for (const cid of ids || []) {
    const cn = props.clusters.find((c: any) => c.id === cid)?.name
    if (cn) loadCRs(cn) // 异步、无 await：避免阻塞下拉打开
  }
}, { immediate: true })

const crResOptions = computed(() => {
  const cids: number[] = props.rule.clusterIds || []
  if (!cids.length) return [] // 未选集群：什么都不给
  const nsList: string[] = props.rule.nsList || []
  const hasNs = nsList.length > 0
  const seen = new Map<string, string>() // value（group/plural） → label（Kind）
  for (const cid of cids) {
    const cn = props.clusters.find((c: any) => c.id === cid)?.name
    if (!cn) continue
    for (const cr of getCRs(cn) || []) {
      // 未选 namespace 时只显 cluster-scoped CR（提示选 ns 拿 namespaced CR）
      if (!hasNs && cr.namespaced) continue
      const v = `${cr.group}/${cr.plural}`
      if (!seen.has(v)) seen.set(v, cr.kind)
    }
  }
  return [...seen].map(([value, label]) => ({ value, label })).sort((a, b) => a.label.localeCompare(b.label))
})

// 当前规则选 cluster 但未选 namespace → 提示用户先选 ns
const crNeedsNS = computed(() => {
  const cids: number[] = props.rule.clusterIds || []
  const nsList: string[] = props.rule.nsList || []
  if (!cids.length) return false
  if (nsList.length > 0) return false
  // 有集群但无 ns：可能存在 namespaced CR 被过滤掉
  for (const cid of cids) {
    const cn = props.clusters.find((c: any) => c.id === cid)?.name
    if (!cn) continue
    if ((getCRs(cn) || []).some(c => c.namespaced)) return true
  }
  return false
})

// resList 含 CR 全限定名（group/plural 形式）时显示字段路径提示
function hasCRField(rule: any): boolean {
  return (rule.resList || []).some((r: string) => typeof r === 'string' && r.includes('/') && !r.startsWith('*/'))
}
</script>

<style scoped>
.rule-card {
  border: 1px solid var(--el-border-color-light);
  border-radius: 6px;
  padding: 10px 14px;
  margin-bottom: 10px;
  background: var(--el-bg-color);
  transition: border-color .2s;
}
.rule-card:hover { border-color: var(--el-border-color); }
.rule-expired { opacity: .62; }
.rule-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
}
.rule-tag {
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}
.rule-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
  gap: 10px 16px;
}
.rule-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-bottom: 4px;
}
.hint { font-weight: normal; }
.rule-hint {
  margin-top: 4px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  line-height: 1.5;
}
.rule-hint code { font-family: ui-monospace, Consolas, monospace; }
.hint-box {
  height: 32px;
  display: flex;
  align-items: center;
  color: var(--el-text-color-placeholder);
  font-size: 12px;
}
</style>

<style>
/* 资源下拉弹出层：必须写在全局（非 scoped）——popper 由 Element Plus 渲染并
   teleport 到 body 末尾，不携带本组件的 data-v 属性，scoped 样式永远命中不了。
   宽高都要限制：CR 选项标签很长（如 Certificate (cert-manager.io/certificates)）、
   数量也多，面板被撑高/撑宽后 popper 会因放不下而翻转/偏移到侧面；
   限宽 + 省略号截断 + 限高（内部滚动）后面板保持紧凑，始终正对输入框展开 */
.rule-res-popper { min-width: 360px; max-width: 560px; }
.rule-res-popper .el-select-dropdown__item {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.rule-res-popper .el-select-dropdown__wrap { max-height: 280px; }
</style>
