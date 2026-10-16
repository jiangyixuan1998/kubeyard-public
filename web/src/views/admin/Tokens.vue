<template>
  <el-card shadow="never">
    <div class="toolbar">
      <span>创建服务端 API 令牌，供 Jenkins / 工单平台调用授权接口（工单审批通过后自动给用户开通集群权限）</span>
      <div style="flex: 1"></div>
      <el-button type="primary" @click="openCreate"><el-icon><Plus /></el-icon>新建令牌</el-button>
    </div>

    <el-table :data="tokens" stripe border v-loading="loading">
      <el-table-column prop="name" label="用途" min-width="140" />
      <el-table-column prop="token_prefix" label="令牌前缀" min-width="140">
        <template #default="{ row }"><span class="mono">{{ row.token_prefix }}…</span></template>
      </el-table-column>
      <el-table-column label="最近使用" min-width="160">
        <template #default="{ row }">{{ fmtTime(row.last_used_at) }}</template>
      </el-table-column>
      <el-table-column label="创建时间" min-width="160">
        <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
      </el-table-column>
      <el-table-column label="状态" width="90">
        <template #default="{ row }">
          <el-tag :type="row.revoked_at ? 'danger' : 'success'" size="small">{{ row.revoked_at ? '已吊销' : '有效' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="100" fixed="right">
        <template #default="{ row }">
          <el-button v-if="!row.revoked_at" link type="danger" size="small" @click="doRevoke(row)">吊销</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 新建：令牌只显示一次 -->
    <el-dialog v-model="createDlg" title="新建 API 令牌" width="560px">
      <el-form v-if="!newToken" label-width="90px">
        <el-form-item label="用途名称" required>
          <el-input v-model="newName" placeholder="如 jenkins / ticket-platform" maxlength="64" @keyup.enter="doCreate" />
        </el-form-item>
      </el-form>
      <template v-else>
        <el-alert type="warning" :closable="false" style="margin-bottom: 12px"
          title="令牌明文仅此一次显示，请立即复制保存（刷新后无法再查看）" />
        <div class="token-box mono">{{ newToken }}</div>
        <div style="text-align: center; margin-top: 10px">
          <el-button size="small" @click="copyToken">{{ copied ? '已复制' : '复制令牌' }}</el-button>
        </div>
      </template>
      <template #footer>
        <el-button @click="createDlg = false">关闭</el-button>
        <el-button v-if="!newToken" type="primary" :loading="creating" :disabled="!newName.trim()" @click="doCreate">生成</el-button>
      </template>
    </el-dialog>

    <el-divider content-position="left">调用说明（供 Jenkins / 工单平台 / 安全部门开发参考）</el-divider>
    <div class="api-doc">
      <!-- 通用约定 -->
      <ul class="doc-notes">
        <li v-for="n in docNotes" :key="n">{{ n }}</li>
      </ul>

      <!-- 分组接口清单 -->
      <div v-for="g in docGroups" :key="g.title" class="doc-group">
        <div class="group-title">{{ g.title }}</div>
        <div v-for="api in g.apis" :key="api.path + api.method" class="api-item">
          <div class="api-head">
            <el-tag :type="api.method === 'GET' ? 'success' : 'primary'" size="small" class="mono method-tag">{{ api.method }}</el-tag>
            <span class="api-path mono">{{ api.path }}</span>
            <span class="api-title">{{ api.title }}</span>
            <div style="flex: 1"></div>
            <el-button link type="primary" size="small" @click="copyCmd(api)">
              {{ copiedCmd === api.method + api.path ? '已复制' : '复制' }}
            </el-button>
          </div>
          <div v-if="api.desc" class="api-desc">
            <div v-for="(d, i) in ([] as string[]).concat(api.desc)" :key="i">{{ d }}</div>
          </div>
          <pre class="api-code mono">{{ api.cmd }}</pre>
        </div>
      </div>
    </div>
  </el-card>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { listApiTokens, createApiToken, revokeApiToken } from '../../api'
import { errMsg } from '../../api/http'

const tokens = ref<any[]>([])
const loading = ref(false)
const createDlg = ref(false)
const creating = ref(false)
const newName = ref('')
const newToken = ref('')
const copied = ref(false)

function fmtTime(v?: string): string {
  if (!v) return '-'
  const d = new Date(v)
  return isNaN(d.getTime()) ? '-' : d.toLocaleString('zh-CN', { hour12: false })
}

async function load() {
  loading.value = true
  try { tokens.value = (await listApiTokens()).data } finally { loading.value = false }
}

function openCreate() {
  newName.value = ''
  newToken.value = ''
  copied.value = false
  createDlg.value = true
}

async function doCreate() {
  creating.value = true
  try {
    const res = await createApiToken(newName.value.trim())
    newToken.value = res.data.plain_token
    load()
  } catch (e) { ElMessage.error(errMsg(e)) } finally { creating.value = false }
}

async function copyToken() {
  try {
    await navigator.clipboard.writeText(newToken.value)
    copied.value = true
  } catch { /* 剪贴板不可用时手动复制 */ }
}

async function doRevoke(row: any) {
  try {
    await ElMessageBox.confirm(
      `吊销后使用该令牌的所有调用立即失效。确定吊销 "${row.name}" 吗？`,
      '吊销确认', { type: 'warning', confirmButtonText: '吊销', cancelButtonText: '取消' },
    )
  } catch { return }
  try {
    await revokeApiToken(row.id)
    ElMessage.success('已吊销')
    load()
  } catch (e) { ElMessage.error(errMsg(e)) }
}

// ---------- 调用说明（数据驱动渲染，改这里即可同步文档）----------
const docNotes = [
  '鉴权：所有请求带请求头 X-API-Token: kyb-xxxx（令牌在本页生成，明文仅创建时显示一次）',
  '有效期两种写法：duration_days 四档 7 / 30 / 90 / 0（永久）；或 expires_at 绝对到期时间（优先），格式 "2026-09-30 00:00:00" 或 "2026-09-30"',
  '集群与权限组均支持多选（数组 clusters / groups；旧单选参数 cluster / group 仍兼容，可混用，自动去重）',
]

const HDR = '-H "X-API-Token: kyb-xxxx" -H "Content-Type: application/json"'
const BASE = 'http://<平台地址>:8000/api/v1/integ'

const docGroups: { title: string; apis: any[] }[] = [
  {
    title: '授权管理（直接授权）',
    apis: [
      {
        method: 'POST', path: `${BASE}/grants`, title: '授予授权',
        desc: '给用户写五元组直接授权；clusters 多集群一次写入，同形态重复提交 = 续期重置时限',
        cmd: `curl -X POST ${BASE}/grants \\
  ${HDR} \\
  -d '{"username":"ycj3603","clusters":["ent3","ent4"],"namespace":"ai-recommend","resource":"pods,deployments","verb":"get,list,logs,exec,download","duration_days":30}'`,
      },
      {
        method: 'POST', path: `${BASE}/grants/revoke`, title: '撤销授权',
        desc: '工单到期 / 回收；clusters 多集群一次撤销，namespace 可选过滤',
        cmd: `curl -X POST ${BASE}/grants/revoke \\
  ${HDR} \\
  -d '{"username":"ycj3603","clusters":["ent3","ent4"]}'`,
      },
      {
        method: 'GET', path: `${BASE}/grants`, title: '查询用户授权',
        desc: '查询用户当前授权（含已过期记录，expired=true）',
        cmd: `curl "${BASE}/grants?username=ycj3603&cluster=ent3" -H "X-API-Token: kyb-xxxx"`,
      },
    ],
  },
  {
    title: '权限组',
    apis: [
      {
        method: 'POST', path: `${BASE}/group-members`, title: '加入权限组',
        desc: '推荐：组规则是模板，时限绑成员关系，到期自动失效；重复加入 = 续期重置时限；groups 多选',
        cmd: `curl -X POST ${BASE}/group-members \\
  ${HDR} \\
  -d '{"username":"ycj3603","groups":["消费者应用运维只读","AI平台开发"],"duration_days":30}'`,
      },
      {
        method: 'POST', path: `${BASE}/group-members/remove`, title: '移出权限组',
        cmd: `curl -X POST ${BASE}/group-members/remove \\
  ${HDR} \\
  -d '{"username":"ycj3603","groups":["消费者应用运维只读","AI平台开发"]}'`,
      },
      {
        method: 'GET', path: `${BASE}/groups`, title: '权限组清单',
        desc: '申请单「角色」下拉用；响应含 clusters（组覆盖的集群列表）',
        cmd: `curl "${BASE}/groups" -H "X-API-Token: kyb-xxxx"`,
      },
      {
        method: 'GET', path: `${BASE}/groups/:id`, title: '权限组权限详情',
        desc: '申请单勾选组后展示权限明细；:id 支持数字 ID 或组名，集群已转可读名称，verb_display / resource_display 为可直接渲染的中文别名，update 会展开具体动作（如更新镜像、重启、副本数）',
        cmd: `curl "${BASE}/groups/3" -H "X-API-Token: kyb-xxxx"`,
      },
      {
        method: 'GET', path: `${BASE}/clusters`, title: '集群清单',
        desc: '申请单「集群」下拉用',
        cmd: `curl "${BASE}/clusters" -H "X-API-Token: kyb-xxxx"`,
      },
    ],
  },
  {
    title: '账号开通与查询',
    apis: [
      {
        method: 'POST', path: `${BASE}/users`, title: '开通 / 续期账号',
        desc: [
          '推荐主入口：一次调用完成开户 + 入组 + 时限',
          'account_type：personal 个人（NSSO 登录）/ service 服务（返回 plain_password 明文密码，仅此一次）',
          'groups = 权限组名数组（必填之一，管理页维护）；clusters 可选：校验每个所选组都配置了所有所选集群的规则',
        ],
        cmd: `curl -X POST ${BASE}/users \\
  ${HDR} \\
  -d '{"username":"ycj3603","display_name":"张三","team":"消费者应用运维","account_type":"personal","groups":["消费者应用运维只读","AI平台开发"],"clusters":["ent3","ent4"],"expires_at":"2026-09-30 00:00:00"}'`,
      },
      {
        method: 'GET', path: `${BASE}/users/:username`, title: '账号状态查询',
        desc: '续期 / 回收前参考：类型 / 有效期 / 所属权限组 / 有效授权数',
        cmd: `curl "${BASE}/users/ycj3603" -H "X-API-Token: kyb-xxxx"`,
      },
      {
        method: 'GET', path: `${BASE}/users`, title: '全量用户权限清单',
        desc: [
          '安全部门盘点用：一次返回全量用户 + 每人权限明细（直接授权 + 权限组并集），含禁用账号与已过期条目并各自标记',
          '?username= 可过滤单个账号',
        ],
        cmd: `curl "${BASE}/users" -H "X-API-Token: kyb-xxxx"`,
      },
    ],
  },
]

const copiedCmd = ref('')
async function copyCmd(api: any) {
  try {
    await navigator.clipboard.writeText(api.cmd)
    copiedCmd.value = api.method + api.path
    setTimeout(() => { if (copiedCmd.value === api.method + api.path) copiedCmd.value = '' }, 1500)
  } catch { /* 剪贴板不可用时手动复制 */ }
}

onMounted(load)
</script>

<style scoped>
.toolbar { display: flex; align-items: center; margin-bottom: 12px; color: #606266; font-size: 13px; }
.mono { font-family: ui-monospace, Consolas, monospace; }
.token-box {
  background: #f5f7fa; border: 1px solid #dcdfe6; border-radius: 4px;
  padding: 12px; word-break: break-all; font-size: 13px;
}

/* ---------- 调用说明 ---------- */
.doc-notes {
  margin: 0; padding: 10px 14px 10px 30px;
  background: var(--el-fill-color-light); border-radius: 6px;
  font-size: 12px; color: var(--el-text-color-regular); line-height: 1.9;
}
.doc-group { margin-top: 16px; }
.group-title {
  font-size: 13px; font-weight: 600; color: var(--el-text-color-primary);
  margin-bottom: 8px; padding-left: 8px;
  border-left: 3px solid var(--el-color-primary);
}
.api-item {
  border: 1px solid var(--el-border-color-lighter); border-radius: 6px;
  margin-bottom: 8px; overflow: hidden;
}
.api-head {
  display: flex; align-items: center; gap: 10px;
  padding: 7px 12px; background: var(--el-fill-color-light);
}
.method-tag { width: 46px; text-align: center; }
.api-path { font-size: 13px; font-weight: 600; color: var(--el-text-color-primary); }
.api-title { font-size: 12px; color: var(--el-text-color-secondary); }
.api-desc {
  padding: 8px 12px 0; font-size: 12px;
  color: var(--el-text-color-secondary); line-height: 1.7;
}
.api-code {
  margin: 8px 12px 10px; padding: 10px 12px;
  background: var(--el-fill-color-darker, #f6f8fa); border-radius: 4px;
  font-size: 12px; line-height: 1.6; color: var(--el-text-color-primary);
  white-space: pre-wrap; word-break: break-all;
}
</style>
