<template>
  <el-card shadow="never">
    <el-tabs v-model="tab">
      <!-- ======================== 权限预设 ======================== -->
      <el-tab-pane label="权限预设" name="perm">
        <el-alert type="info" :closable="false" style="margin-bottom: 14px" title="免发版维护权限预制"
          description="默认预设是全部资源的兜底，资源覆盖只替换对应资源；动作目录决定各资源的细粒度动作及其可修改字段。保存后新授权实时生效；已有授权用下方「检查并对齐」先看差异，再确认应用。" />

        <div class="section-title">角色预设</div>
        <div class="preset-section-hint">以下为默认预设，未被资源覆盖的资源按此生效</div>
        <div class="preset-grid">
          <div v-for="k in presetKinds" :key="k" class="preset-card">
            <div class="preset-name">{{ presetNames[k] }}</div>
            <div class="preset-field">
              <div class="field-label">动词</div>
              <el-select v-model="editPresets[k].verbs" multiple filterable collapse-tags collapse-tags-tooltip style="width: 100%">
                <el-option v-for="v in verbOptions" :key="v" :value="v" :label="verbAliases[v] || v" />
              </el-select>
            </div>
            <div class="preset-field">
              <div class="field-label">细粒度动作（含 update 时生效）</div>
              <el-select v-model="editPresets[k].actions" multiple filterable allow-create default-first-option
                collapse-tags collapse-tags-tooltip style="width: 100%">
                <el-option value="*" label="*（完整更新）" />
                <el-option v-for="a in allActionOptions" :key="a.value" :value="a.value" :label="a.label" />
              </el-select>
            </div>
          </div>
        </div>

        <div class="preset-section-hint" style="margin-top: 14px">以下资源有单独覆盖时替换上方默认预设</div>
        <el-table :data="presetRows" border size="small" v-loading="permLoading" row-key="key">
          <el-table-column type="expand">
            <template #default="{ row }">
              <div class="resource-preset-editor">
                <div v-for="k in presetKinds" :key="k" class="preset-card">
                  <div class="preset-name">
                    {{ presetNames[k] }}
                    <el-switch :model-value="!!resourceOverride(row.key, k)"
                      @change="(v: any) => setResourceOverride(row.key, k, !!v)" />
                  </div>
                  <div v-if="resourceOverride(row.key, k)" class="preset-field">
                    <div class="field-label">动词</div>
                    <el-select v-model="resourceOverride(row.key, k)!.verbs" multiple filterable collapse-tags
                      collapse-tags-tooltip style="width: 100%">
                      <el-option v-for="v in resourceVerbOptions(row.key, k)" :key="v" :value="v"
                        :label="v === '*' ? '*（全部动词）' : (verbAliases[v] || v)" />
                    </el-select>
                  </div>
                  <div v-if="resourceOverride(row.key, k) && hasUpdateVerb(resourceOverride(row.key, k)!.verbs)" class="preset-field">
                    <div class="field-label">细粒度动作</div>
                    <el-select v-model="resourceOverride(row.key, k)!.actions" multiple filterable allow-create
                      default-first-option collapse-tags collapse-tags-tooltip style="width: 100%">
                      <el-option value="*" label="*（完整更新）" />
                      <el-option v-for="a in resourceActionOptions(row.key)" :key="a.value" :value="a.value"
                        :label="a.label" />
                    </el-select>
                  </div>
                  <div v-else class="dim small">使用默认预设</div>
                </div>
              </div>
            </template>
          </el-table-column>
          <el-table-column label="资源" width="260">
            <template #default="{ row }">{{ resourceAliases[row.key] || row.key }}</template>
          </el-table-column>
          <el-table-column v-for="k in presetKinds" :key="k" :label="presetNames[k]" min-width="190">
            <template #default="{ row }">
              <div class="preset-summary">
                <el-tag size="small" :type="resourceOverride(row.key, k) ? 'warning' : 'info'">
                  {{ resourceOverride(row.key, k) ? '覆盖' : '默认' }}
                </el-tag>
                <span class="small">{{ presetSummaryText(row.key, k) }}</span>
              </div>
            </template>
          </el-table-column>
        </el-table>

        <!-- 动作目录 -->
        <div class="section-title" style="margin-top: 18px">动作目录（资源 → 细粒度动作 → 可修改字段）</div>
        <div class="catalog-bar">
          <el-select v-model="activeRes" filterable allow-create default-first-option style="width: 280px"
            placeholder="选择或输入资源名（如 ingresses）">
            <el-option v-for="r in resourceList" :key="r" :value="r" :label="resourceAliases[r] || r" />
          </el-select>
          <el-button :icon="Plus" @click="addAction">新增动作</el-button>
          <el-button :icon="Delete" plain type="danger" @click="removeResource">清空该资源目录</el-button>
          <span class="catalog-hint">字段路径含 <code>*</code> 通配任意数组下标（如 spec.containers.*.image）；纯动作令牌（如 restart）字段留空</span>
        </div>
        <el-table :data="actionRows" border size="small" v-loading="permLoading">
          <el-table-column label="动作键" width="150">
            <template #default="{ row }">
              <el-input v-model="row.key" placeholder="如 image / probes" />
            </template>
          </el-table-column>
          <el-table-column label="显示名" width="200">
            <template #default="{ row }"><el-input v-model="row.label" placeholder="授权页/我的权限里展示" /></template>
          </el-table-column>
          <el-table-column label="可修改字段（逗号分隔的路径，留空 = 纯动作令牌）">
            <template #default="{ row }"><el-input v-model="row.fields" placeholder="spec.template.spec.containers.*.image" /></template>
          </el-table-column>
          <el-table-column width="70" align="center">
            <template #default="{ $index }">
              <el-button link type="danger" size="small" :icon="Delete" @click="actionRows.splice($index, 1)" />
            </template>
          </el-table-column>
        </el-table>

        <div class="btn-bar">
          <el-button type="primary" :loading="savingPerm" @click="savePermPresets">保存</el-button>
          <el-button @click="resetPerm" plain>恢复默认</el-button>
        </div>

        <!-- 已有授权一次性检查与对齐 -->
        <div class="section-title" style="margin-top: 18px">已有授权对齐</div>
        <div class="catalog-bar">
          <el-button type="warning" plain :loading="syncing" @click="openSync">检查并对齐已有授权</el-button>
          <span class="catalog-hint">先生成差异，确认后才会写入；CR 与动词为 * 的授权不参与预设对齐。</span>
        </div>

        <el-dialog v-model="syncDlg" title="已有授权差异确认" width="76%" top="6vh">
          <el-alert type="warning" :closable="false" style="margin-bottom: 10px" :title="syncAlertTitle" />
          <el-table :data="syncItems" border size="small" max-height="52vh">
            <el-table-column label="变更类型" width="125">
              <template #default="{ row }">
                <el-tag size="small" :type="String(row.sync_type || '').startsWith('align') ? 'primary' : 'danger'">
                  {{ syncTypeLabel(row) }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="归属方式" width="90">
              <template #default="{ row }">
                <el-tag size="small" :type="row.kind === 'user' ? 'info' : 'warning'">{{ row.kind === 'user' ? '用户' : '权限组' }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="owner" label="归属" width="130" show-overflow-tooltip />
            <el-table-column label="命名空间" width="150" show-overflow-tooltip>
              <template #default="{ row }">{{ row.namespace || '—' }}</template>
            </el-table-column>
            <el-table-column prop="resource" label="资源" min-width="140" show-overflow-tooltip />
            <el-table-column label="动词变更" min-width="200">
              <template #default="{ row }">
                <template v-if="row.new_verb && row.new_verb !== row.verb">
                  <span class="diff-inline"><code>{{ row.verb }}</code> → <code>{{ row.new_verb }}</code></span>
                  <div class="diff-tags">
                    <el-tag v-for="v in diffTokens(row.verb, row.new_verb).added" :key="'va'+v" size="small" type="success">+{{ verbAliases[v] || v }}</el-tag>
                    <el-tag v-for="v in diffTokens(row.verb, row.new_verb).removed" :key="'vr'+v" size="small" type="danger">-{{ verbAliases[v] || v }}</el-tag>
                  </div>
                </template>
                <template v-else-if="row.new_verb != null">
                  <div class="diff-tags"><el-tag size="small" type="info">动词无变更</el-tag></div>
                  <div class="small" style="margin-top:4px"><code>{{ row.verb }}</code></div>
                </template>
                <template v-else><code>{{ row.verb }}</code></template>
              </template>
            </el-table-column>
            <el-table-column label="动作变更" min-width="300">
              <template #default="{ row }">
                <template v-if="String(row.sync_type || '').startsWith('align')">
                  <template v-if="row.new_actions != null && row.new_actions !== row.actions">
                    <span class="diff-inline"><code>{{ row.actions }}</code> → <code>{{ row.new_actions }}</code></span>
                    <div class="diff-tags">
                      <el-tag v-for="a in diffTokens(row.actions, row.new_actions).added" :key="'aa'+a" size="small" type="success">+{{ actionLabelOf(a) }}（{{ a }}）</el-tag>
                      <el-tag v-for="a in diffTokens(row.actions, row.new_actions).removed" :key="'ar'+a" size="small" type="danger">-{{ actionLabelOf(a) }}（{{ a }}）</el-tag>
                    </div>
                  </template>
                  <template v-else-if="row.new_actions != null">
                    <div class="diff-tags"><el-tag size="small" type="info">动作无变更</el-tag></div>
                    <div class="small" style="margin-top:4px"><code>{{ row.actions }}</code></div>
                  </template>
                  <template v-else><code>{{ row.actions || '（无动作）' }}</code></template>
                </template>
                <template v-else>
                  <template v-if="(row.stale || []).length">
                    <span class="small" style="margin-bottom:4px;display:block">移除失效动作：</span>
                    <div class="diff-tags">
                      <el-tag v-for="a in row.stale" :key="'sa'+a" size="small" type="danger">-{{ actionLabelOf(a) }}（{{ a }}）</el-tag>
                    </div>
                  </template>
                  <template v-else>
                    <div class="diff-tags"><el-tag size="small" type="info">无失效动作</el-tag></div>
                  </template>
                </template>
              </template>
            </el-table-column>
          </el-table>
          <template #footer>
            <el-button @click="syncDlg = false">取消</el-button>
            <el-button type="danger" :loading="syncing" @click="doSync(true)">确认应用 {{ syncItems.length }} 项</el-button>
          </template>
        </el-dialog>
      </el-tab-pane>

      <!-- ======================== AI 模型 ======================== -->
      <el-tab-pane label="AI 模型" name="ai" lazy>
        <el-alert type="info" :closable="false" style="margin-bottom: 14px" title="AI 助手模型接入（严格只读）"
          description="支持 OpenAI 兼容协议（DeepSeek / 通义 / vLLM / Ollama 及各类网关，内网自建与公有云均可）、Anthropic Claude、Azure OpenAI 三种接入方式。AI 只做诊断分析与 YAML 草稿生成，永远不直接写集群——生成内容经人工确认后走既有部署通道（权限校验与审计照常）。API Key 仅保存在服务端，不回显前端；留空保存即保留原值。" />

        <el-form label-width="110px" style="max-width: 640px" v-loading="aiLoading">
          <el-form-item label="启用 AI 助手">
            <el-switch v-model="aiForm.enabled" />
          </el-form-item>
          <el-form-item label="接入协议">
            <el-radio-group v-model="aiForm.provider">
              <el-radio value="openai">OpenAI 兼容</el-radio>
              <el-radio value="anthropic">Anthropic Claude</el-radio>
              <el-radio value="azure">Azure OpenAI</el-radio>
            </el-radio-group>
          </el-form-item>
          <el-form-item label="服务地址">
            <el-input v-model="aiForm.base_url" :placeholder="aiUrlPlaceholder" />
          </el-form-item>
          <el-form-item label="模型名称">
            <el-input v-model="aiForm.model" :placeholder="aiModelPlaceholder" />
            <div class="ai-tip">Azure 填部署名（deployment name），模型版本由部署配置决定</div>
          </el-form-item>
          <el-form-item label="API Key">
            <el-input v-model="aiForm.api_key" type="password" show-password autocomplete="new-password"
              :placeholder="aiForm.has_key ? '已配置，留空保持不变' : 'sk-...'" />
          </el-form-item>
          <el-form-item label="单次回复上限">
            <el-input-number v-model="aiForm.max_tokens" :min="0" :max="16384" :step="512" />
            <div class="ai-tip">0 = 默认 4096；思考型模型（R1/Qwen3 类）的思考过程也计入，过小会导致只有思考没有回答</div>
          </el-form-item>
          <el-form-item label="每人每日配额">
            <el-input-number v-model="aiForm.daily_quota" :min="1" :max="10000" :step="10" />
            <div class="ai-tip">全体登录用户的每日 AI 调用次数上限（连通测试不计入）</div>
          </el-form-item>
          <el-form-item>
            <el-button type="primary" :loading="aiSaving" @click="saveAI">保存</el-button>
            <el-button :loading="aiTesting" :disabled="!aiForm.base_url || !aiForm.model" @click="doTestAI">连通测试</el-button>
          </el-form-item>
        </el-form>
      </el-tab-pane>

      <!-- ======================== 键值对 ======================== -->
      <el-tab-pane label="键值对" name="kv">
        <!-- 续期门户：到期提醒的申请入口（空 = 前端不显示入口，文案回落为「联系管理员」） -->
        <div class="section-title">续期门户</div>
        <div class="preset-section-hint">
          配置权限/账号到期提醒里的「申请续期」跳转地址（如企业统一权限门户）。
          留空 = 前端只提示「联系平台管理员」，不显示跳转按钮。保存后对全站生效
        </div>
        <div class="kv-row">
          <el-input v-model="renewalUrl" placeholder="https://portal.example.com" style="max-width: 480px" />
          <el-button type="primary" :loading="renewalSaving" @click="saveRenewal">保存</el-button>
        </div>
        <el-divider style="margin: 18px 0" />
        <div class="section-title">自定义键值对</div>
        <div v-for="(row, i) in rows" :key="i" class="kv-row">
          <el-input v-model="row.key" placeholder="键" style="width: 240px" />
          <el-input v-model="row.value" placeholder="值" />
          <el-button link type="danger" :icon="Delete" @click="rows.splice(i, 1)" />
        </div>
        <el-button style="margin-top: 10px" @click="rows.push({ key: '', value: '' })">新增</el-button>
        <div style="margin-top: 16px">
          <el-button type="primary" :loading="saving" @click="doSave">保存</el-button>
        </div>
      </el-tab-pane>
    </el-tabs>
  </el-card>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Plus } from '@element-plus/icons-vue'
import { getSettings, putSettings, putPermPresets, resetPermPresets, syncPermPresets, getAIConfig, putAIConfig, testAI } from '../../api'
import { errMsg } from '../../api/http'
import {
  verbOptions, verbAliases, resourceAliases, permPresetState, verbOptionsOf, trimPreset,
  resourcePresetVerbOptions,
  actionLabelOf, loadPermPresets, builtinActionCatalogOf, builtinRolePresetsOf,
  builtinResourceRolePresetsOf, rolePresetOf,
} from '../../utils/grant'
import type { ActionMeta, RolePreset, ResourceRolePresets, RolePresetKind } from '../../utils/grant'

const tab = ref('perm')

// ---------- 权限预设 ----------
const permLoading = ref(false)
const savingPerm = ref(false)
const presetKinds = ['viewer', 'dev', 'admin'] as const
const presetNames: Record<string, string> = { viewer: '只读', dev: '开发', admin: '管理员' }

// 编辑态：资源 → 动作键 → {label, fields}；表格行（fields 为逗号串）在切换资源时与 editActions 互写
const editActions = ref<Record<string, Record<string, ActionMeta>>>({})
const editPresets = reactive<Record<string, RolePreset>>({ viewer: { verbs: [], actions: [] }, dev: { verbs: [], actions: [] }, admin: { verbs: [], actions: [] } })
const editResourcePresets = ref<ResourceRolePresets>({})
const activeRes = ref('deployments')
const actionRows = ref<{ key: string; label: string; fields: string }[]>([])

const resourceList = computed(() => Object.keys(editActions.value).sort())
// 角色预设可选动作 = 生效目录所有动作的并集
const allActionOptions = computed(() => {
  const seen = new Map<string, string>()
  for (const m of Object.values(editActions.value)) {
    for (const [v, meta] of Object.entries(m)) seen.set(v, meta.label || v)
  }
  return [...seen].map(([value, label]) => ({ value, label }))
})
const presetRows = computed(() => {
  const keys = new Set([...Object.keys(editActions.value), ...Object.keys(editResourcePresets.value)])
  return [...keys].sort().map((key) => ({ key }))
})

function resourceActionOptions(res: string): Array<{ value: string; label: string }> {
  return Object.entries(editActions.value[res] || {}).map(([value, meta]) => ({ value, label: meta.label || value }))
}

function hasUpdateVerb(verbs: string[]): boolean {
  return verbs.includes('update') || verbs.includes('*')
}

function diffTokens(oldStr: string, newStr: string): { added: string[]; removed: string[] } {
  const oldSet = new Set((oldStr || '').split(',').map((s) => s.trim()).filter(Boolean))
  const newSet = new Set((newStr || '').split(',').map((s) => s.trim()).filter(Boolean))
  const added = [...newSet].filter((t) => !oldSet.has(t))
  const removed = [...oldSet].filter((t) => !newSet.has(t))
  return { added, removed }
}

function presetSummaryText(res: string, kind: RolePresetKind): string {
  const preset = resourceOverride(res, kind) || applicableResourcePreset(res, editPresets[kind])
  const render = (tokens: string[]) => tokens.map((t) => (t === '*' ? '*' : verbAliases[t] || actionLabelOf(t))).join(' / ')
  return `${render(preset.verbs)}；${render(preset.actions)}`
}

function resourceOverride(res: string, kind: RolePresetKind): RolePreset | undefined {
  return editResourcePresets.value[res]?.[kind] || undefined
}

function resourceVerbOptions(res: string, kind: RolePresetKind): string[] {
  return resourcePresetVerbOptions(res, resourceOverride(res, kind))
}

function applicableResourcePreset(res: string, preset: RolePreset): RolePreset {
  const verbSet = new Set(verbOptionsOf([res]))
  const actionSet = new Set(Object.keys(editActions.value[res] || {}))
  const verbs = preset.verbs.includes('*') ? ['*'] : preset.verbs.filter((v) => verbSet.has(v))
  const actions = preset.actions.includes('*')
    ? ['*']
    : preset.actions.filter((a) => a.includes('.') || a === 'update' || actionSet.has(a))
  return {
    verbs: verbs.length ? verbs : preset.verbs,
    actions,
  }
}

function setResourceOverride(res: string, kind: RolePresetKind, enabled: boolean) {
  if (!res) return
  editResourcePresets.value[res] = editResourcePresets.value[res] || {}
  if (enabled) {
    const builtin = builtinResourceRolePresetsOf()[res]?.[kind]
    const base = builtin || rolePresetOf(kind)
    const scoped = trimPreset(base, [res])
    editResourcePresets.value[res][kind] = { verbs: [...scoped.verbs], actions: [...scoped.actions] }
  } else if (editResourcePresets.value[res][kind]) {
    editResourcePresets.value[res][kind] = null
  }
}

// 表格行 ↔ 目录互写（行内编辑改的是 actionRows 的副本对象，切换/保存时回写）
function rowsToCatalog(res: string) {
  const m: Record<string, ActionMeta> = {}
  for (const r of actionRows.value) {
    if (!r.key.trim()) continue
    const fields = r.fields.split(',').map((s) => s.trim()).filter(Boolean)
    m[r.key.trim()] = { label: (r.label || r.key).trim(), fields }
  }
  // 空表且该资源原本就不存在（新建未编辑就切走）：不产生条目，避免误提交为「清空」
  if (Object.keys(m).length === 0 && !editActions.value[res] && !builtinActionCatalogOf()[res]) return
  editActions.value[res] = m
}
function catalogToRows(res: string) {
  const m = editActions.value[res] || {}
  actionRows.value = Object.entries(m).map(([key, meta]) => ({ key, label: meta.label, fields: (meta.fields || []).join(',') }))
}
// 加载期间重设 activeRes 时不回写（否则旧表格行会覆盖刚加载的目录）
let loadingPerm = false
watch(activeRes, (nv, ov) => {
  if (loadingPerm) { catalogToRows(nv); return }
  if (ov) rowsToCatalog(ov)
  catalogToRows(nv)
}, { flush: 'sync' })

async function loadPerm() {
  permLoading.value = true
  loadingPerm = true
  try {
    await loadPermPresets(true) // 拉最新生效配置（内置 ⊕ 自定义），失败回落内置
    const state = permPresetState()
    const actions = state.loaded ? state.actions : builtinActionCatalogOf()
    editActions.value = JSON.parse(JSON.stringify(actions))
    for (const k of presetKinds) {
      const p = (state.loaded && state.presets[k]) || builtinRolePresetsOf()[k]
      editPresets[k] = { verbs: [...p.verbs], actions: [...p.actions] }
    }
    const loadedPresets = JSON.parse(JSON.stringify(state.resourcePresets || {})) as ResourceRolePresets
    for (const [res, roles] of Object.entries(loadedPresets)) {
      for (const [kind, preset] of Object.entries(roles || {})) {
        if (preset) loadedPresets[res]![kind as RolePresetKind] = applicableResourcePreset(res, preset)
      }
    }
    editResourcePresets.value = loadedPresets
    if (!resourceList.value.includes(activeRes.value)) activeRes.value = resourceList.value[0] || 'deployments'
    catalogToRows(activeRes.value)
  } finally { permLoading.value = false; loadingPerm = false }
}

function addAction() {
  if (!activeRes.value) { ElMessage.warning('先选择或输入资源名'); return }
  actionRows.value.push({ key: `action${actionRows.value.length + 1}`, label: '', fields: '' })
}

function removeResource() {
  ElMessageBox.confirm(`清空资源「${activeRes.value}」的动作目录？该资源将无细粒度动作可授权（保存后生效）。`, '清空确认', { type: 'warning' })
    .then(() => {
      editActions.value[activeRes.value] = {} // 空表 = 清空该资源目录（后端合并语义）
      catalogToRows(activeRes.value)
    })
    .catch(() => {})
}

// 保存：仅提交与内置有差异的资源与预设（未动的保持内置，后续版本更新内置默认可自然生效）
async function savePermPresets() {
  rowsToCatalog(activeRes.value) // 表格行回写目录
  for (const [res, m] of Object.entries(editActions.value)) {
    for (const [key, meta] of Object.entries(m)) {
      if (/[, ]/.test(key)) { ElMessage.error(`资源 ${res} 存在非法动作键: "${key}"（禁止逗号/空格）`); return }
      for (const f of meta.fields || []) { // 后端纯动作令牌 fields 为 null，兜底空数组
        if (/[ ,]/.test(f)) { ElMessage.error(`动作 ${res}/${key} 存在非法字段路径（禁止逗号/空格）: ${f}`); return }
      }
    }
  }
  for (const k of presetKinds) {
    if (!editPresets[k].verbs.length) {
      ElMessage.error(`角色预设「${presetNames[k]}」的动词/动作不能为空`); return
    }
  }
  for (const [res, roles] of Object.entries(editResourcePresets.value)) {
    for (const [kind, preset] of Object.entries(roles || {})) {
      if (preset && !preset.verbs.length) {
        ElMessage.error(`资源级预设 ${resourceAliases[res] || res}/${presetNames[kind]} 的动词/动作不能为空`)
        return
      }
      if (preset) {
        const normalized = applicableResourcePreset(res, preset)
        if (JSON.stringify(normalized.verbs) !== JSON.stringify(preset.verbs) ||
            JSON.stringify(normalized.actions) !== JSON.stringify(preset.actions)) {
          ElMessage.error(`资源级预设 ${resourceAliases[res] || res}/${presetNames[kind]} 包含该资源不适用的动词/动作`)
          return
        }
      }
    }
  }
  const builtinActions = builtinActionCatalogOf()
  const diffActions: Record<string, Record<string, ActionMeta>> = {}
  for (const [res, m] of Object.entries(editActions.value)) {
    if (JSON.stringify(m) !== JSON.stringify(builtinActions[res] || null)) diffActions[res] = m
  }
  const builtinPresets = builtinRolePresetsOf()
  const diffPresets: Record<string, RolePreset> = {}
  for (const k of presetKinds) {
    if (JSON.stringify(editPresets[k]) !== JSON.stringify(builtinPresets[k])) diffPresets[k] = editPresets[k]
  }
  savingPerm.value = true
  try {
    await putPermPresets({
      actions: diffActions,
      presets: diffPresets,
      resource_preset_mode: 'scoped' as const,
      resource_presets: editResourcePresets.value,
    })
    await loadPermPresets(true)
    ElMessage.success('权限预设矩阵已保存并实时生效；存量授权可用「检查并对齐」处理')
  } catch (e) { ElMessage.error(errMsg(e)) } finally { savingPerm.value = false }
}

async function resetPerm() {
  try {
    await ElMessageBox.confirm('恢复全部内置默认（动作目录 + 角色预设）？当前自定义配置将被清除。', '恢复默认', { type: 'warning' })
  } catch { return }
  try {
    await resetPermPresets()
    await loadPermPresets(true)
    ElMessage.success('已恢复内置默认')
    loadPerm()
  } catch (e) { ElMessage.error(errMsg(e)) }
}

// ---------- 存量授权一键差异对齐 ----------
const syncing = ref(false)
const syncDlg = ref(false)
const syncItems = ref<any[]>([])
const alignCount = computed(() => syncItems.value.filter((row) => String(row.sync_type || '').startsWith('align')).length)
const staleCount = computed(() => syncItems.value.length - alignCount.value)
const syncAlertTitle = computed(() =>
  `检测到 ${alignCount.value} 条预设对齐和 ${staleCount.value} 条失效动作清理。确认后一次应用；集群、命名空间、资源范围不变。`)

function syncTypeLabel(row: any): string {
  const type = String(row.sync_type || '')
  if (type.startsWith('align:')) return `${presetNames[type.slice(6)] || type.slice(6)}对齐`
  return '失效清理'
}

async function doSync(apply: boolean) {
  syncing.value = true
  try {
    const res = await syncPermPresets(apply, undefined, undefined, undefined, 'all')
    if (apply) {
      ElMessage.success(`已对齐 ${res.data.aligned || 0} 条、清理 ${res.data.cleaned || 0} 条授权（详见审计日志）`)
      syncDlg.value = false
      syncItems.value = []
    } else {
      syncItems.value = res.data.items || []
      if (!syncItems.value.length) {
        ElMessage.info('当前存量授权已与预设矩阵一致，也没有引用已删除动作的失效令牌')
      } else {
        syncDlg.value = true
      }
    }
  } catch (e) { ElMessage.error(errMsg(e)) } finally { syncing.value = false }
}
function openSync() {
  doSync(false)
}

// ---------- AI 模型 ----------
const aiLoading = ref(false)
const aiSaving = ref(false)
const aiTesting = ref(false)
const aiForm = reactive({
  provider: 'openai', base_url: '', model: '', api_key: '',
  has_key: false, max_tokens: 2048, daily_quota: 30, enabled: false,
})
// 各协议的地址/模型占位提示（内网自建与公有云都给示例）
const aiUrlPlaceholder = computed(() => ({
  openai: 'https://api.deepseek.com 或 http://内网模型地址:8000',
  anthropic: 'https://api.anthropic.com',
  azure: 'https://资源名.openai.azure.com',
}[aiForm.provider] || ''))
const aiModelPlaceholder = computed(() => ({
  openai: '如 deepseek-chat / qwen2.5-32b-instruct',
  anthropic: '如 claude-sonnet-5',
  azure: '部署名，如 gpt-4o-deploy',
}[aiForm.provider] || ''))

async function loadAI() {
  aiLoading.value = true
  try {
    const d = (await getAIConfig()).data || {}
    Object.assign(aiForm, d, { api_key: '' }) // Key 不回显，输入框留空
  } catch { /* http 层已提示 */ } finally { aiLoading.value = false }
}

async function saveAI() {
  aiSaving.value = true
  try {
    await putAIConfig(aiForm)
    aiForm.has_key = aiForm.has_key || !!aiForm.api_key
    aiForm.api_key = ''
    ElMessage.success('AI 模型配置已保存')
  } catch (e) { ElMessage.error(errMsg(e)) } finally { aiSaving.value = false }
}

async function doTestAI() {
  aiTesting.value = true
  try {
    const res = await testAI()
    ElMessage.success(res.data?.message || '连通正常')
  } catch (e) { ElMessage.error(errMsg(e)) } finally { aiTesting.value = false }
}

// ---------- 键值对 ----------
const rows = ref<{ key: string; value: string }[]>([])
const saving = ref(false)
// 续期门户地址（renewal.portal_url）：到期提醒的申请入口
const renewalUrl = ref('')
const renewalSaving = ref(false)
async function loadRenewal() {
  try {
    const m = (await getSettings()).data || {}
    renewalUrl.value = String(m['renewal.portal_url'] || '')
  } catch { /* 忽略 */ }
}
async function saveRenewal() {
  renewalSaving.value = true
  try {
    await putSettings({ 'renewal.portal_url': renewalUrl.value.trim() })
    ElMessage.success('续期门户已保存')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally { renewalSaving.value = false }
}

async function load() {
  try {
    const m = (await getSettings()).data || {}
    rows.value = Object.entries(m).map(([key, value]) => ({ key, value: String(value) }))
  } catch { /* http 层已提示 */ }
}

async function doSave() {
  const m: Record<string, string> = {}
  for (const r of rows.value) {
    if (r.key.trim()) m[r.key.trim()] = r.value
  }
  saving.value = true
  try {
    await putSettings(m)
    ElMessage.success('已保存')
  } catch (e) { ElMessage.error(errMsg(e)) } finally { saving.value = false }
}

onMounted(() => {
  loadPerm()
  load()
  loadAI()
})
</script>

<style scoped>
.section-title { font-size: 14px; font-weight: 600; margin-bottom: 10px; color: #1f2d3d; }
.preset-section-hint { font-size: 12px; color: #909399; margin-bottom: 8px; }
.diff-inline { display: block; margin-bottom: 4px; }
.diff-tags { display: flex; flex-wrap: wrap; gap: 4px; }
.preset-grid { display: flex; gap: 16px; }
.preset-card { flex: 1; border: 1px solid var(--el-border-color-lighter); border-radius: 8px; padding: 12px; }
.preset-name { font-size: 13px; font-weight: 600; margin-bottom: 10px; }
.preset-field { margin-bottom: 8px; }
.field-label { font-size: 12px; color: #909399; margin-bottom: 4px; }
.catalog-bar { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; flex-wrap: wrap; }
.matrix-bar { margin-bottom: 8px; }
.catalog-hint { font-size: 12px; color: #909399; }
.resource-preset-editor { display: flex; gap: 12px; padding: 4px 8px 8px; }
.resource-preset-editor.disabled { opacity: .62; }
.resource-preset-editor .preset-card { min-width: 260px; }
.preset-summary { display: flex; align-items: flex-start; gap: 6px; }
.small { font-size: 12px; color: #606266; line-height: 18px; word-break: break-all; }
.btn-bar { margin-top: 16px; display: flex; gap: 10px; }
.kv-row { display: flex; gap: 8px; margin-bottom: 8px; }
.ai-tip { font-size: 12px; color: #909399; line-height: 1.5; margin-top: 2px; }
</style>
