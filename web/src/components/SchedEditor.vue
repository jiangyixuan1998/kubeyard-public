<template>
  <div class="sched" :class="{ 'is-disabled': disabled }">
    <!-- ① 节点定向：nodeName + nodeSelector -->
    <div class="sched-card" style="--sched-accent: var(--el-color-primary)">
      <div class="sched-card-head">
        <span class="sched-no">1</span>
        <div class="sched-head-text">
          <span class="sched-card-title">节点定向</span>
          <span class="sched-sub">指定节点，或按节点标签筛选（所有条件须同时满足）</span>
        </div>
      </div>

      <div class="sched-field">
        <div class="sched-field-label">
          指定节点（nodeName）
          <el-tooltip content="直接绑定到某个节点；节点名不存在时 Pod 会一直 Pending。通常应优先用标签选择器或亲和" placement="top">
            <el-icon class="sched-help"><QuestionFilled /></el-icon>
          </el-tooltip>
        </div>
        <el-select :model-value="m.node_name" :disabled="disabled" filterable allow-create clearable
          default-first-option placeholder="留空 = 不指定节点；可下拉选择或手输节点名" style="width: 100%"
          @update:model-value="(v: string) => { m.node_name = v || '' }">
          <el-option v-for="n in nodes" :key="n.name" :label="n.name" :value="n.name" />
        </el-select>
        <div v-if="nodeUnschedulable(m.node_name)" class="sched-warn-line">
          该节点已封锁调度（unschedulable=true），除非有 Pod tolerations，指定后 Pod 会一直 Pending
        </div>
      </div>

      <div class="sched-field">
        <div class="sched-field-label">
          节点标签选择器（nodeSelector）
          <span class="sched-sub">Pod 只调度到同时满足所有标签的节点；下拉为「键 = 值」整对候选，来自集群节点实际标签</span>
        </div>
        <el-table :data="m.node_selector" size="small" empty-text="暂无选择条件" class="sched-table">
          <el-table-column label="标签（键 = 值）" min-width="360">
            <template #default="{ row }">
              <el-select :model-value="kvOf(row)" :disabled="disabled" filterable allow-create default-first-option
                placeholder="下拉选择「键 = 值」或分别输入" style="width: 100%" @update:model-value="(v: string) => setKv(row, v)">
                <el-option v-for="p in labelPairs" :key="p" :label="p" :value="p" />
              </el-select>
            </template>
          </el-table-column>
          <el-table-column width="46" align="center">
            <template #default="{ $index }">
              <el-button :disabled="disabled" link type="danger" :icon="Delete" @click="m.node_selector.splice($index, 1)" />
            </template>
          </el-table-column>
        </el-table>
        <el-button :disabled="disabled" size="small" :icon="Plus" @click="m.node_selector.push({ key: '', value: '' })">添加条件</el-button>
      </div>
    </div>

    <!-- ② 节点亲和：required + preferred -->
    <div class="sched-card" style="--sched-accent: var(--el-color-success)">
      <div class="sched-card-head">
        <span class="sched-no">2</span>
        <div class="sched-head-text">
          <span class="sched-card-title">节点亲和（nodeAffinity）</span>
          <span class="sched-sub">比 nodeSelector 更灵活：支持集合操作与软性偏好</span>
        </div>
      </div>

      <div class="sched-field">
        <div class="sched-sub2">硬性约束<span class="sched-hint">必须满足，否则 Pod 一直 Pending</span></div>
        <el-table :data="m.node_affinity_required" size="small" empty-text="暂无硬性约束" class="sched-table">
          <el-table-column label="标签（键 = 值）" min-width="200">
            <template #default="{ row }">
              <el-select :model-value="kvOf(row)" :disabled="disabled" filterable allow-create default-first-option
                placeholder="下拉选择「键 = 值」或输入" style="width: 100%"
                @update:model-value="(v: string) => setKv(row, v)">
                <el-option v-for="p in labelPairs" :key="p" :label="p" :value="p" />
              </el-select>
            </template>
          </el-table-column>
          <el-table-column label="操作符" width="120">
            <template #default="{ row }">
              <el-select :model-value="row.operator" :disabled="disabled" @update:model-value="(op: string) => onOperatorChange(row, op)">
                <el-option v-for="op in operators" :key="op" :label="op" :value="op" />
              </el-select>
            </template>
          </el-table-column>
          <el-table-column label="值" min-width="130">
            <template #default="{ row }">
              <el-select v-if="valuesOf(row.key).length" :model-value="valuesOfRow(row)" :disabled="disabled || !needVal(row.operator)"
                filterable allow-create default-first-option multiple collapse-tags
                :placeholder="needVal(row.operator) ? '下拉多选或输入' : '该操作符无需值'" style="width: 100%"
                @update:model-value="(v: string[]) => setValuesOfRow(row, v)">
                <el-option v-for="v in valuesOf(row.key)" :key="v" :label="v" :value="v" />
              </el-select>
              <el-input v-else v-model="row.values" :disabled="disabled || !needVal(row.operator)"
                :placeholder="opPlaceholder(row.operator)" />
            </template>
          </el-table-column>
          <el-table-column width="46" align="center">
            <template #default="{ $index }">
              <el-button :disabled="disabled" link type="danger" :icon="Delete" @click="m.node_affinity_required.splice($index, 1)" />
            </template>
          </el-table-column>
        </el-table>
        <el-button :disabled="disabled" size="small" :icon="Plus" @click="addRow(m.node_affinity_required)">添加硬性约束</el-button>
      </div>

      <div class="sched-field">
        <div class="sched-sub2">偏好<span class="sched-hint">软性加权排序，不会导致 Pending</span></div>
        <el-table :data="m.node_affinity_preferred" size="small" empty-text="暂无偏好" class="sched-table">
          <el-table-column label="标签（键 = 值）" min-width="190">
            <template #default="{ row }">
              <el-select :model-value="kvOf(row)" :disabled="disabled" filterable allow-create default-first-option
                placeholder="下拉选择「键 = 值」或输入" style="width: 100%"
                @update:model-value="(v: string) => setKv(row, v)">
                <el-option v-for="p in labelPairs" :key="p" :label="p" :value="p" />
              </el-select>
            </template>
          </el-table-column>
          <el-table-column label="操作符" width="115">
            <template #default="{ row }">
              <el-select :model-value="row.operator" :disabled="disabled" @update:model-value="(op: string) => onOperatorChange(row, op)">
                <el-option v-for="op in operators" :key="op" :label="op" :value="op" />
              </el-select>
            </template>
          </el-table-column>
          <el-table-column label="值" min-width="120">
            <template #default="{ row }">
              <el-select v-if="valuesOf(row.key).length" :model-value="valuesOfRow(row)" :disabled="disabled || !needVal(row.operator)"
                filterable allow-create default-first-option multiple collapse-tags
                :placeholder="needVal(row.operator) ? '下拉多选或输入' : '该操作符无需值'" style="width: 100%"
                @update:model-value="(v: string[]) => setValuesOfRow(row, v)">
                <el-option v-for="v in valuesOf(row.key)" :key="v" :label="v" :value="v" />
              </el-select>
              <el-input v-else v-model="row.values" :disabled="disabled || !needVal(row.operator)"
                :placeholder="opPlaceholder(row.operator)" />
            </template>
          </el-table-column>
          <el-table-column label="权重" width="90">
            <template #default="{ row }">
              <el-input-number v-model="row.weight" :disabled="disabled" :min="1" :max="100" controls-position="right" style="width: 100%" />
            </template>
          </el-table-column>
          <el-table-column width="46" align="center">
            <template #default="{ $index }">
              <el-button :disabled="disabled" link type="danger" :icon="Delete" @click="m.node_affinity_preferred.splice($index, 1)" />
            </template>
          </el-table-column>
        </el-table>
        <el-button :disabled="disabled" size="small" :icon="Plus" @click="addRow(m.node_affinity_preferred, true)">添加偏好</el-button>
      </div>
    </div>

    <!-- ③ Pod 反亲和：required + preferred -->
    <div class="sched-card" style="--sched-accent: var(--el-color-warning)">
      <div class="sched-card-head">
        <span class="sched-no">3</span>
        <div class="sched-head-text">
          <span class="sched-card-title">Pod 反亲和（podAntiAffinity）</span>
          <span class="sched-sub">打散本应用的多个副本，避免同节点/同区单点</span>
        </div>
      </div>

      <div class="sched-field">
        <div class="sched-sub2">硬性约束<span class="sched-hint">必须满足，否则 Pod 一直 Pending</span></div>
        <el-table :data="m.pod_anti_required" size="small" empty-text="暂无硬性约束" class="sched-table">
          <el-table-column label="拓扑键" min-width="160">
            <template #default="{ row }">
              <el-select v-model="row.topology_key" :disabled="disabled" filterable allow-create default-first-option
                placeholder="如下拉选择拓扑键" style="width: 100%">
                <el-option v-for="k in topoOptions" :key="k" :label="k" :value="k" />
              </el-select>
            </template>
          </el-table-column>
          <el-table-column label="标签（键 = 值）" min-width="180">
            <template #default="{ row }">
              <el-select :model-value="kvOf(row)" :disabled="disabled" filterable allow-create default-first-option
                placeholder="如 app=本应用标签值，或手输" style="width: 100%"
                @update:model-value="(v: string) => setKv(row, v)">
                <el-option v-for="p in labelPairs" :key="p" :label="p" :value="p" />
              </el-select>
            </template>
          </el-table-column>
          <el-table-column label="操作符" width="115">
            <template #default="{ row }">
              <el-select :model-value="row.operator" :disabled="disabled" @update:model-value="(op: string) => onOperatorChange(row, op)">
                <el-option v-for="op in operators" :key="op" :label="op" :value="op" />
              </el-select>
            </template>
          </el-table-column>
          <el-table-column label="值" min-width="110">
            <template #default="{ row }">
              <el-select v-if="valuesOf(row.key).length" :model-value="valuesOfRow(row)" :disabled="disabled || !needVal(row.operator)"
                filterable allow-create default-first-option multiple collapse-tags
                :placeholder="needVal(row.operator) ? '多选或输入' : '该操作符无需值'" style="width: 100%"
                @update:model-value="(v: string[]) => setValuesOfRow(row, v)">
                <el-option v-for="v in valuesOf(row.key)" :key="v" :label="v" :value="v" />
              </el-select>
              <el-input v-else v-model="row.values" :disabled="disabled || !needVal(row.operator)"
                :placeholder="opPlaceholder(row.operator)" />
            </template>
          </el-table-column>
          <el-table-column width="46" align="center">
            <template #default="{ $index }">
              <el-button :disabled="disabled" link type="danger" :icon="Delete" @click="m.pod_anti_required.splice($index, 1)" />
            </template>
          </el-table-column>
        </el-table>
        <el-button :disabled="disabled" size="small" :icon="Plus" @click="addAntiRow(m.pod_anti_required)">添加硬性约束</el-button>
      </div>

      <div class="sched-field">
        <div class="sched-sub2">偏好<span class="sched-hint">软性加权排序，尽量打散</span></div>
        <el-table :data="m.pod_anti_preferred" size="small" empty-text="暂无偏好" class="sched-table">
          <el-table-column label="拓扑键" min-width="150">
            <template #default="{ row }">
              <el-select v-model="row.topology_key" :disabled="disabled" filterable allow-create default-first-option
                placeholder="下拉选择或输入拓扑键" style="width: 100%">
                <el-option v-for="k in topoOptions" :key="k" :label="k" :value="k" />
              </el-select>
            </template>
          </el-table-column>
          <el-table-column label="标签（键 = 值）" min-width="170">
            <template #default="{ row }">
              <el-select :model-value="kvOf(row)" :disabled="disabled" filterable allow-create default-first-option
                placeholder="如 app=本应用标签值，或手输" style="width: 100%"
                @update:model-value="(v: string) => setKv(row, v)">
                <el-option v-for="p in labelPairs" :key="p" :label="p" :value="p" />
              </el-select>
            </template>
          </el-table-column>
          <el-table-column label="操作符" width="110">
            <template #default="{ row }">
              <el-select :model-value="row.operator" :disabled="disabled" @update:model-value="(op: string) => onOperatorChange(row, op)">
                <el-option v-for="op in operators" :key="op" :label="op" :value="op" />
              </el-select>
            </template>
          </el-table-column>
          <el-table-column label="值" min-width="105">
            <template #default="{ row }">
              <el-select v-if="valuesOf(row.key).length" :model-value="valuesOfRow(row)" :disabled="disabled || !needVal(row.operator)"
                filterable allow-create default-first-option multiple collapse-tags
                :placeholder="needVal(row.operator) ? '多选或输入' : '该操作符无需值'" style="width: 100%"
                @update:model-value="(v: string[]) => setValuesOfRow(row, v)">
                <el-option v-for="v in valuesOf(row.key)" :key="v" :label="v" :value="v" />
              </el-select>
              <el-input v-else v-model="row.values" :disabled="disabled || !needVal(row.operator)"
                :placeholder="opPlaceholder(row.operator)" />
            </template>
          </el-table-column>
          <el-table-column label="权重" width="92">
            <template #default="{ row }">
              <el-input-number v-model="row.weight" :disabled="disabled" :min="1" :max="100" controls-position="right" style="width: 100%" />
            </template>
          </el-table-column>
          <el-table-column width="46" align="center">
            <template #default="{ $index }">
              <el-button :disabled="disabled" link type="danger" :icon="Delete" @click="m.pod_anti_preferred.splice($index, 1)" />
            </template>
          </el-table-column>
        </el-table>
        <el-button :disabled="disabled" size="small" :icon="Plus" @click="addAntiRow(m.pod_anti_preferred, true)">添加偏好</el-button>
      </div>
    </div>
  </div>
</template>

<script lang="ts">
// 纯逻辑（类型 + K8s 读写回/校验）放普通 script 以便父组件按名导入；
// Vue 限制：<script setup> 内不能出现 export
export interface SchedExpr { key: string; operator: string; values: string; weight?: number; topology_key?: string }
export interface SchedModel {
  node_name: string
  node_selector: { key: string; value: string }[]
  node_affinity_required: SchedExpr[]
  node_affinity_preferred: (SchedExpr & { weight: number })[]
  pod_anti_required: SchedExpr[]
  pod_anti_preferred: (SchedExpr & { weight: number })[]
}

// 拆逗号分隔值 → 数组（去空白去空）
export function splitVals(s: string): string[] {
  return (s || '').split(',').map((v) => v.trim()).filter(Boolean)
}

// 空模型（fd 与表单编辑共用）
export function schedModelDefault(): SchedModel {
  return {
    node_name: '',
    node_selector: [],
    node_affinity_required: [],
    node_affinity_preferred: [],
    pod_anti_required: [],
    pod_anti_preferred: [],
  }
}

// UI 行模型 → 写入 podSpec 的 nodeSelector/nodeName/affinity 对象（三键均可能为 undefined）
export function schedToK8s(m: SchedModel): { nodeSelector?: Record<string, string>; nodeName?: string; affinity?: Record<string, any> } {
  const out: { nodeSelector?: Record<string, string>; nodeName?: string; affinity?: Record<string, any> } = {}
  const ns: Record<string, string> = {}
  for (const r of m.node_selector) {
    const k = r.key.trim()
    if (k) ns[k] = r.value.trim()
  }
  if (Object.keys(ns).length) out.nodeSelector = ns
  const nn = (m.node_name || '').trim()
  if (nn) out.nodeName = nn
  const nodeAff: Record<string, any> = {}
  const reqExprs = m.node_affinity_required
    .filter((r) => r.key.trim())
    .map((r) => exprToK8s(r))
  if (reqExprs.length) {
    nodeAff.requiredDuringSchedulingIgnoredDuringExecution = {
      nodeSelectorTerms: [{ matchExpressions: reqExprs }],
    }
  }
  const prefTerms = m.node_affinity_preferred
    .filter((r) => r.key.trim())
    .map((r) => ({
      // K8s 真实形状：{weight, preference: {nodeSelectorTerms: [...]}}；每行独立打分（行间 OR）
      weight: r.weight || 1,
      preference: { nodeSelectorTerms: [{ matchExpressions: [exprToK8s(r)] }] },
    }))
  if (prefTerms.length) {
    nodeAff.preferredDuringSchedulingIgnoredDuringExecution = prefTerms
  }
  const anti: Record<string, any> = {}
  const antiReq = m.pod_anti_required
    .filter((r) => r.key.trim() && (r.topology_key || '').trim())
    .map((r) => ({
      topologyKey: r.topology_key!.trim(),
      labelSelector: { matchExpressions: [exprToK8s(r)] },
    }))
  if (antiReq.length) {
    anti.requiredDuringSchedulingIgnoredDuringExecution = antiReq
  }
  const antiPref = m.pod_anti_preferred
    .filter((r) => r.key.trim() && (r.topology_key || '').trim())
    .map((r) => ({
      weight: r.weight || 1,
      podAffinityTerm: {
        topologyKey: r.topology_key!.trim(),
        labelSelector: { matchExpressions: [exprToK8s(r)] },
      },
    }))
  if (antiPref.length) {
    anti.preferredDuringSchedulingIgnoredDuringExecution = antiPref
  }
  if (Object.keys(nodeAff).length || Object.keys(anti).length) {
    out.affinity = {}
    if (Object.keys(nodeAff).length) out.affinity.nodeAffinity = nodeAff
    if (Object.keys(anti).length) out.affinity.podAntiAffinity = anti
  }
  return out
}

// 单行 → matchExpression（values 拆数组；Exists/DoesNotExist 不带 values）
export function exprToK8s(r: SchedExpr): Record<string, any> {
  const e: Record<string, any> = { key: r.key.trim(), operator: r.operator || 'In' }
  if (e.operator === 'In' || e.operator === 'NotIn' || e.operator === 'Gt' || e.operator === 'Lt') {
    e.values = splitVals(r.values)
  }
  return e
}

// K8s podSpec → UI 行模型（读回）；未建模的 affinity 结构置 unmodeled=true 由父级提示
export function schedFromK8s(ps: any): { model: SchedModel; unmodeled: boolean } {
  const model = schedModelDefault()
  if (!ps || typeof ps !== 'object') return { model, unmodeled: false }
  const ns = ps.nodeSelector
  if (ns && typeof ns === 'object' && !Array.isArray(ns)) {
    for (const [k, v] of Object.entries(ns)) {
      if (typeof v === 'string') model.node_selector.push({ key: k, value: v })
    }
  }
  if (typeof ps.nodeName === 'string') model.node_name = ps.nodeName
  const aff = ps.affinity
  let unmodeled = false
  if (aff && typeof aff === 'object') {
    for (const k of Object.keys(aff)) {
      if (k !== 'nodeAffinity' && k !== 'podAntiAffinity') unmodeled = true
    }
    const na = aff.nodeAffinity
    if (na && typeof na === 'object') {
      for (const k of Object.keys(na)) {
        if (k !== 'requiredDuringSchedulingIgnoredDuringExecution' && k !== 'preferredDuringSchedulingIgnoredDuringExecution') unmodeled = true
      }
      const req = na.requiredDuringSchedulingIgnoredDuringExecution
      if (req) {
        const terms = req.nodeSelectorTerms || []
        if (terms.length > 1) unmodeled = true
        const t = terms[0]
        if (t) {
          for (const k of Object.keys(t)) {
            if (k !== 'matchExpressions') unmodeled = true
          }
          if (!(t.matchExpressions || []).length) unmodeled = true // 空 term 保存会静默丢失
          for (const e of t.matchExpressions || []) {
            model.node_affinity_required.push(exprToRow(e))
          }
        }
      }
      // preferred 真实形状：{weight, preference: {nodeSelectorTerms: [{matchExpressions}]}}。
      // 只建模「每 term 恰好一个表达式」：多表达式是单 term 内 AND，拆成多行保存会变成
      // 多个独立打分 term（OR）静默改义 → 置 unmodeled 由警示拦截（后端守卫同规则）
      const pref = na.preferredDuringSchedulingIgnoredDuringExecution
      if (Array.isArray(pref)) {
        for (const p of pref) {
          const terms = p?.preference?.nodeSelectorTerms
          const t = Array.isArray(terms) && terms.length === 1 ? terms[0] : undefined
          if (!t || p?.weight == null) {
            unmodeled = true
            continue
          }
          if (Object.keys(t).some((k) => k !== 'matchExpressions') || (t.matchExpressions || []).length !== 1) {
            unmodeled = true
            continue
          }
          const row = exprToRow(t.matchExpressions[0]) as SchedExpr & { weight: number }
          row.weight = p.weight
          model.node_affinity_preferred.push(row)
        }
      }
    }
    const pa = aff.podAntiAffinity
    if (pa && typeof pa === 'object') {
      for (const grp of ['requiredDuringSchedulingIgnoredDuringExecution', 'preferredDuringSchedulingIgnoredDuringExecution'] as const) {
        const terms = pa[grp]
        if (!Array.isArray(terms)) continue
        for (const t of terms) {
          const preferred = grp === 'preferredDuringSchedulingIgnoredDuringExecution'
          const pt = preferred ? t?.podAffinityTerm : t
          if (!pt || !pt.topologyKey || typeof pt.topologyKey !== 'string' || !pt.labelSelector?.matchExpressions) {
            unmodeled = true
            continue
          }
          const ls = pt.labelSelector
          if (Object.keys(ls).some((k) => k !== 'matchExpressions') || Object.keys(pt).some((k) => k !== 'topologyKey' && k !== 'labelSelector')) {
            unmodeled = true
          }
          // 单 selector 内多表达式是 AND，拆成多行保存会变成多个 term（行间 OR）静默改义 → 拦截
          if (ls.matchExpressions.length !== 1) unmodeled = true
          for (const e of ls.matchExpressions) {
            const row = exprToRow(e)
            row.topology_key = pt.topologyKey
            if (preferred) {
              const r2 = row as SchedExpr & { weight: number }
              r2.weight = t.weight
              model.pod_anti_preferred.push(r2)
            } else {
              model.pod_anti_required.push(row)
            }
          }
        }
      }
    }
  }
  return { model, unmodeled }
}

// matchExpression → UI 行（values 数组拼回逗号串）
export function exprToRow(e: any): SchedExpr {
  const op = typeof e.operator === 'string' ? e.operator : 'In'
  const vals = Array.isArray(e.values) ? e.values.join(',') : ''
  return { key: e.key || '', operator: op, values: vals }
}

// 校验 UI 行模型；返回错误文案（空=通过）。与后端 applyScheduling 同规则
export function validateSched(m: SchedModel): string {
  const keyRe = /^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$/
  const valRe = /^([A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?)?$/
  const prefixSegRe = /^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/
  const checkKey = (k: string): boolean => {
    let name = k
    if (k.includes('/')) {
      const parts = k.split('/')
      if (parts.length !== 2 || !parts[0] || !parts[1]) return false
      if (parts[0].length > 253 || parts[0].split('.').some((s) => !prefixSegRe.test(s))) return false
      name = parts[1]
    }
    return !!name && name.length <= 63 && keyRe.test(name)
  }
  const needVal = (op: string) => op === 'In' || op === 'NotIn' || op === 'Gt' || op === 'Lt'
  const empty = (rows: SchedExpr[]) => !rows.some((r) => r.key.trim() || (r.values || '').trim())

  for (const r of m.node_selector) {
    const k = r.key.trim()
    if (!k && !r.value.trim()) continue // 整行空白跳过
    if (!k) return '节点标签选择器：存在未填标签键的行'
    if (!checkKey(k)) return `节点标签选择器：标签键 ${k} 不合法`
    if (r.value.length > 63 || !valRe.test(r.value)) return `节点标签选择器：${k} 的值不合法`
  }
  // 防御：历史 bug 曾让 node_name 变成对象（el-option :value 绑整个 option 对象）——
  // 校验函数应报错而非自己抛 TypeError（抛错会让保存函数中断且无提示）
  if (m.node_name != null && typeof m.node_name !== 'string') {
    return '指定节点名数据异常（非字符串），请重新选择节点'
  }
  if (m.node_name.trim() && !prefixSegRe.test(m.node_name.trim())) {
    return '指定节点名不合法（需为 DNS 名，如 node1.example.com）'
  }
  const exprChecks: [string, SchedExpr[]][] = [
    ['节点亲和硬性约束', m.node_affinity_required],
    ['节点亲和偏好', m.node_affinity_preferred],
    ['Pod 反亲和硬性约束', m.pod_anti_required],
    ['Pod 反亲和偏好', m.pod_anti_preferred],
  ]
  for (const [label, rows] of exprChecks) {
    if (empty(rows)) continue
    for (const r of rows) {
      const k = r.key.trim()
      if (!k) return `${label}：存在未填标签键的行`
      if (!checkKey(k)) return `${label}：标签键 ${k} 不合法`
      if (needVal(r.operator) && splitVals(r.values).length === 0) return `${label}：键 ${k} 的 ${r.operator} 操作符需要至少一个值`
      if ((r.topology_key !== undefined) && !(r.topology_key || '').trim()) return `${label}：拓扑键不能为空`
    }
  }
  for (const r of [...m.node_affinity_preferred, ...m.pod_anti_preferred]) {
    if ((r.weight || 0) < 1 || (r.weight || 0) > 100) return '权重需为 1-100'
  }
  return ''
}
</script>

<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import { Delete, Plus, QuestionFilled } from '@element-plus/icons-vue'

const props = defineProps<{
  modelValue: SchedModel
  /** 节点候选 [{name, unschedulable}]；为空时 nodeName 仍可手输 */
  nodes?: { name: string; unschedulable?: boolean }[]
  /** 节点标签键聚合（父级从集群节点实际 labels 计算）；为空时下拉无选项仍可手输 */
  labelKeys?: string[]
  /** 标签键 → 该键在集群节点上的实际值集合（键选中后值级联下拉） */
  labelValues?: Record<string, string[]>
  disabled?: boolean
}>()

const operators = ['In', 'NotIn', 'Exists', 'DoesNotExist', 'Gt', 'Lt']

// 本地可变副本：props 是深层 readonly 代理，v-model 直接写 props.modelValue.x 会被
// 「Set operation on key failed: target is readonly」静默拦截（保存无反应的根因）。
// 方案：watch 同步 props.modelValue → 本地 reactive 副本；副本任意变更 emit 整个模型给父级
const emit = defineEmits<{ (e: 'update:modelValue', v: SchedModel): void }>()
// 模型归一：undefined 字段补默认值。el-select clearable 清空时 emit 的是 undefined 而非 ''，
// 直接写入会让 node_name 变 undefined——validateSched 的 .trim() 崩（保存无响应）、
// JSON 深拷贝时 undefined 键被丢弃（父组件 schedForm 同样缺字段）
function normalizeModel(v: any): SchedModel {
  const d = schedModelDefault()
  const src = v || {}
  return {
    node_name: typeof src.node_name === 'string' ? src.node_name : d.node_name,
    node_selector: Array.isArray(src.node_selector) ? src.node_selector : [],
    node_affinity_required: Array.isArray(src.node_affinity_required) ? src.node_affinity_required : [],
    node_affinity_preferred: Array.isArray(src.node_affinity_preferred) ? src.node_affinity_preferred : [],
    pod_anti_required: Array.isArray(src.pod_anti_required) ? src.pod_anti_required : [],
    pod_anti_preferred: Array.isArray(src.pod_anti_preferred) ? src.pod_anti_preferred : [],
  }
}
const m = reactive(schedModelDefault())
// 回环去重：props 回流会 assign 相同内容到 m 触发 m.watch——按内容签名去重（比 syncing 布尔闸
// 可靠：布尔闸会吞掉「回流同周期内的用户首次写入」，父组件不回写时该次变更永久丢失）
let lastEmitted = ''
const snapshot = () => JSON.stringify(m)
watch(m, () => {
  const sig = snapshot()
  if (sig === lastEmitted) return
  lastEmitted = sig
  emit('update:modelValue', JSON.parse(sig))
}, { deep: true })
watch(() => props.modelValue, (v) => {
  if (v === m) return
  Object.assign(m, JSON.parse(JSON.stringify(normalizeModel(v))))
  lastEmitted = snapshot() // 回流内容即当前状态，不视为新变更
}, { immediate: true, deep: true })

// 节点名是否已封锁调度（选中已封锁节点时行内提示）
function nodeUnschedulable(name: string): boolean {
  return !!((props.nodes || []).find((n) => n.name === name) || {}).unschedulable
}

// 拓扑键候选：K8s 标准拓扑键 + 节点标签键（自定义拓扑域一般也打在节点标签上）
const topoOptions = computed(() => [
  'kubernetes.io/hostname',
  'topology.kubernetes.io/zone',
  'topology.kubernetes.io/region',
  ...(props.labelKeys || []),
])

// 「键 = 值」整对候选：来自集群节点实际标签（每对一行选项，选中即同时定 key 与 value）
const labelPairs = computed(() => {
  const out: string[] = []
  for (const k of props.labelKeys || []) {
    const vs = (props.labelValues || {})[k] || []
    if (vs.length) for (const v of vs) out.push(`${k}=${v}`)
    else out.push(`${k}=`)
  }
  return out
})

// nodeSelector 行的「键 = 值」视图代理：下拉选中 "k=v" 拆回 row.key/row.value；
// 手输时同样按第一个 = 拆分（无 = 则整串作为键、值为空）
function kvOf(row: { key: string; value: string }): string {
  return row.key ? `${row.key}=${row.value}` : ''
}
function setKv(row: { key: string; value: string }, pair: string) {
  const i = pair.indexOf('=')
  if (i >= 0) {
    row.key = pair.slice(0, i).trim()
    row.value = pair.slice(i + 1).trim()
  } else {
    row.key = pair.trim()
    row.value = ''
  }
}

// 操作符切换时：切到 Exists/DoesNotExist 清空值（该操作符无值语义），避免残留导致 K8s 拒绝
function onOperatorChange(row: SchedExpr, op: string) {
  row.operator = op
  if (!needVal(op)) row.values = ''
}

function opPlaceholder(op: string): string {
  if (op === 'Exists' || op === 'DoesNotExist') return '无需填值'
  if (op === 'Gt' || op === 'Lt') return '如 4（数值比较，填一个数）'
  return '如 ssd,hdd'
}

// 操作符是否需要值（In/NotIn/Gt/Lt 需要值；Exists/DoesNotExist 无值）
function needVal(op: string): boolean {
  return op === 'In' || op === 'NotIn' || op === 'Gt' || op === 'Lt'
}

// 键选中后值下拉级联（键为空/无候选时仅手输）
function valuesOf(key: string): string[] {
  return (props.labelValues || {})[key] || []
}

// 值列双形态适配：级联多选下拉绑数组、纯手输绑逗号串。
// 读取统一为 string[]（下拉 model-value 需数组）；写入统一回收成逗号串（行模型 values 始终为 string）
function valuesOfRow(row: SchedExpr): string[] {
  return Array.isArray(row.values) ? row.values : splitVals(row.values)
}
function setValuesOfRow(row: SchedExpr, v: string[] | string) {
  row.values = Array.isArray(v) ? v.join(',') : v
}

// 空行模板：preferred 带默认权重，反亲和带默认拓扑键
function addRow(rows: SchedExpr[], preferred = false) {
  const row: SchedExpr = { key: '', operator: 'In', values: '' }
  if (preferred) row.weight = 50
  rows.push(row)
}
function addAntiRow(rows: SchedExpr[], preferred = false) {
  const row: SchedExpr = { key: '', operator: 'In', values: '', topology_key: 'kubernetes.io/hostname' }
  if (preferred) row.weight = 50
  rows.push(row)
}
</script>

<style scoped>
/* 三张编号卡片：节点定向 / 节点亲和 / Pod 反亲和，纵向排布统一节奏 */
.sched { display: flex; flex-direction: column; gap: 12px; }
.sched.is-disabled { opacity: 0.65; }
/* 卡片：左缘 3px 语义色条（每卡不同色）+ 浅底，模块一眼可分 */
.sched-card {
  display: flex; flex-direction: column; gap: 10px;
  border: 1px solid var(--el-border-color-lighter); border-left: 3px solid var(--sched-accent, var(--el-color-primary));
  border-radius: 8px; padding: 12px 14px;
  background: var(--el-fill-color-blank);
}
.sched-card-head { display: flex; align-items: center; gap: 10px; }
.sched-no {
  display: inline-flex; align-items: center; justify-content: center; flex-shrink: 0;
  width: 24px; height: 24px; border-radius: 6px;
  background: var(--sched-accent, var(--el-color-primary)); color: #fff; font-size: 13px; font-weight: 700;
}
.sched-head-text { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
.sched-card-title { font-weight: 600; font-size: 14px; color: var(--el-text-color-primary); }
.sched-sub { font-weight: 400; font-size: 12px; color: var(--el-text-color-secondary); }
.sched-field { display: flex; flex-direction: column; gap: 6px; }
.sched-field + .sched-field { border-top: 1px dashed var(--el-border-color-lighter); padding-top: 10px; }
.sched-field-label {
  display: flex; align-items: center; gap: 6px;
  font-size: 13px; font-weight: 500; color: var(--el-text-color-regular);
}
.sched-help { color: var(--el-text-color-secondary); cursor: help; }
/* 子小节标题：色点 + 名称 + 弱化说明 */
.sched-sub2 { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--el-text-color-primary); font-weight: 500; }
.sched-sub2::before { content: ''; width: 6px; height: 6px; border-radius: 2px; background: var(--sched-accent, var(--el-color-primary-light-5)); opacity: .75; }
.sched-hint { color: var(--el-text-color-placeholder); font-weight: 400; }
.sched-table { width: 100%; margin-bottom: 6px; }
.sched-table :deep(.el-table__empty-text) { font-size: 12px; color: var(--el-text-color-placeholder); }
/* 指定已封锁节点的行内提示 */
.sched-warn-line { font-size: 12px; color: var(--el-color-warning); line-height: 1.5; }
</style>
