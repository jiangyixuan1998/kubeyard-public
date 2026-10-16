<template>
  <el-card shadow="never">
    <el-alert type="info" :closable="false" style="margin-bottom: 12px"
      :title="isSuper
        ? '用户打开容器终端时会自动录制完整会话（asciinema 格式），可在此回放与检索。'
        : '用户打开容器终端时会自动录制完整会话（asciinema 格式），可在此回放与检索。当前为管理员范围视图：仅显示您管辖的集群/命名空间内的录像。'" />
    <div class="toolbar">
      <el-input v-model="filter.user" placeholder="用户名" style="width: 140px" clearable @change="reload" />
      <el-select v-model="filter.cluster" placeholder="集群" style="width: 150px" clearable filterable @change="reload">
        <el-option v-for="c in clusters" :key="c.name" :label="clusterLabel(c)" :value="c.name" />
      </el-select>
      <el-input v-model="filter.namespace" placeholder="命名空间" style="width: 150px" clearable @change="reload" />
      <el-input v-model="filter.pod" placeholder="Pod 名称" style="width: 170px" clearable @change="reload" />
      <el-select v-model="filter.risk" placeholder="风险等级" style="width: 130px" clearable @change="reload">
        <el-option label="高危" value="high" />
        <el-option label="中风险" value="medium" />
        <el-option label="低风险" value="low" />
        <el-option label="空会话" value="none" />
        <el-option label="内容丢失" value="lost" />
        <el-option label="未评估" value="none2" />
      </el-select>
      <el-button @click="reload" :icon="Search">查询</el-button>
      <el-button type="warning" plain :disabled="!selected.length || batchEvaling" :loading="batchEvaling" @click="batchReEval">
        批量重评{{ selected.length ? `（${selected.length}）` : '' }}
      </el-button>
      <span class="dim" style="font-size: 12px">历史录像补评请分批勾选（单批 ≤50，逐条串行评估）</span>
    </div>

    <el-table :data="items" stripe border v-loading="loading" @selection-change="onSelChange">
      <el-table-column type="selection" width="42" />
      <el-table-column label="开始时间" width="170">
        <template #default="{ row }">{{ new Date(row.started_at).toLocaleString('zh-CN', { hour12: false }) }}</template>
      </el-table-column>
      <el-table-column prop="username" label="用户" width="110" />
      <el-table-column prop="cluster" label="集群" width="110" />
      <el-table-column prop="namespace" label="命名空间" width="130" show-overflow-tooltip />
      <el-table-column prop="pod" label="Pod" min-width="200" show-overflow-tooltip />
      <el-table-column prop="container" label="容器" min-width="130" show-overflow-tooltip />
      <el-table-column label="AI 风险" width="130">
        <template #default="{ row }">
          <el-tooltip v-if="row.risk_level" :content="row.risk_why || '无评级理由'" placement="top">
            <el-tag size="small" :type="riskTagType(row.risk_level)" effect="light">{{ riskLabel(row.risk_level) }}</el-tag>
          </el-tooltip>
          <span v-else class="dim">未评估</span>
        </template>
      </el-table-column>
      <el-table-column label="时长" width="100">
        <template #default="{ row }">{{ fmtDuration(row.duration) }}</template>
      </el-table-column>
      <el-table-column label="大小" width="90">
        <template #default="{ row }">{{ fmtSize(row.size_bytes) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="270" fixed="right">
        <template #default="{ row }">
          <el-button link type="primary" size="small" @click="openPlay(row)">回放</el-button>
          <el-button link type="success" size="small" @click="openRaw(row)">原始记录</el-button>
          <el-button link type="warning" size="small" @click="openAiSummary(row)">AI 摘要</el-button>
          <el-button link type="info" size="small" :loading="reevalId === row.id" @click="reEval(row)">重评</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-pagination style="margin-top: 12px; justify-content: flex-end" layout="total, prev, pager, next, sizes"
      :total="total" v-model:current-page="page" v-model:page-size="size" :page-sizes="[20, 50, 100]"
      @current-change="load" @size-change="load" />

    <!-- 回放 -->
    <el-dialog v-model="playDlg" :title="`终端回放 - ${playRow?.pod}`" width="78%" top="4vh" destroy-on-close @closed="disposePlayer">
      <div v-if="playRow" class="play-meta">
        {{ playRow.username }} · {{ playRow.cluster }} / {{ playRow.namespace }} · 容器 {{ playRow.container }}
        · {{ new Date(playRow.started_at).toLocaleString('zh-CN', { hour12: false }) }} · 时长 {{ fmtDuration(playRow.duration) }}
      </div>
      <div v-loading="castLoading" class="player-box" ref="playerBox"></div>
      <el-alert v-if="castErr" type="error" :title="castErr" :closable="false" />
    </el-dialog>

    <!-- 原始记录 -->
    <el-dialog v-model="rawDlg" :title="`原始记录 - ${rawRow?.pod}`" width="78%" top="4vh" destroy-on-close>
      <div v-loading="castLoading" class="raw-box">
        <pre>{{ rawText }}</pre>
      </div>
      <el-alert v-if="castErr" type="error" :title="castErr" :closable="false" />
    </el-dialog>

    <!-- AI 审计摘要 -->
    <AiDialog ref="aiDlgRef" :title="`AI 审计摘要 - ${aiRow?.username} @ ${aiRow?.pod}`" />
  </el-card>
</template>

<script setup lang="ts">
import { nextTick, onMounted, reactive, ref } from 'vue'
import { Search } from '@element-plus/icons-vue'
import { create as createAsciinemaPlayer } from 'asciinema-player'
import 'asciinema-player/dist/bundle/asciinema-player.css'
import AiDialog from '../../components/AiDialog.vue'
import { aiRecordingSummary, listClusters, listExecRecordings, getRecordingCast, reEvalRecordingRisk, reEvalRecordingRiskBatch } from '../../api'
import { errMsg } from '../../api/http'
import { ElMessage } from 'element-plus'
import { useAuthStore } from '../../stores/auth'

const isSuper = useAuthStore().role === 'SuperAdmin'

const clusters = ref<any[]>([])
const items = ref<any[]>([])
const loading = ref(false)
const total = ref(0)
const page = ref(1)
const size = ref(20)
const filter = reactive({ user: '', cluster: '', namespace: '', pod: '', risk: '' })

// 批量重评
const selected = ref<any[]>([])
const batchEvaling = ref(false)
function onSelChange(rows: any[]) {
  selected.value = rows
}
async function batchReEval() {
  if (!selected.value.length) return
  const ids = selected.value.map((r) => r.id)
  batchEvaling.value = true
  try {
    const res = await reEvalRecordingRiskBatch(ids)
    const r = res.data
    ElMessage.success(`批量重评完成：成功 ${r.ok}，失败 ${r.failed}${r.skipped ? `，跳过 ${r.skipped}（无权限）` : ''}`)
    load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally { batchEvaling.value = false }
}

const playDlg = ref(false)
const playRow = ref<any>(null)
const playerBox = ref<HTMLElement | null>(null)
let playerInstance: any = null
const rawDlg = ref(false)
const rawRow = ref<any>(null)
const rawText = ref('')
const castLoading = ref(false)
const castErr = ref('')

function fmtDuration(sec?: number): string {
  if (!sec || sec < 0) return '-'
  if (sec < 60) return `${sec} 秒`
  return `${Math.floor(sec / 60)} 分 ${sec % 60} 秒`
}

function fmtSize(b?: number): string {
  if (!b) return '-'
  if (b < 1024) return `${b} B`
  if (b < 1024 * 1024) return `${(b / 1024).toFixed(1)} KB`
  return `${(b / 1024 / 1024).toFixed(1)} MB`
}

// 风险等级显示
function riskLabel(l: string): string {
  return l === 'high' ? '高危' : l === 'medium' ? '中风险' : l === 'low' ? '低风险'
    : l === 'none' ? '空会话' : l === 'lost' ? '内容丢失' : l
}
function riskTagType(l: string): string {
  return l === 'high' ? 'danger' : l === 'medium' ? 'warning' : l === 'low' ? 'success' : 'info'
}

// 手动重新评估单条录像风险（会话结束时已自动评估；此为失败兜底/模型更新后重评）
const reevalId = ref<number | null>(null)
async function reEval(row: any) {
  reevalId.value = row.id
  try {
    const res = await reEvalRecordingRisk(row.id)
    row.risk_level = res.data.risk_level
    row.risk_why = res.data.risk_why
    if (res.data.risk_level === 'none') {
      ElMessage.info('该会话没有可识别的输入命令（空会话），无需评估')
    } else if (res.data.risk_level === 'lost') {
      ElMessage.info('该录像内容已丢失（服务重启时中断），无法评估')
    } else {
      ElMessage.success(`评估完成：${riskLabel(res.data.risk_level)}`)
    }
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally { reevalId.value = null }
}

// 筛选条件变化/点查询：回第一页再拉（否则在第 N 页上改筛选，新结果集没有第 N 页会看到空表）
function reload() {
  page.value = 1
  load()
}

async function load() {
  loading.value = true
  try {
    const params: any = { page: page.value, page_size: size.value }
    for (const k of Object.keys(filter) as (keyof typeof filter)[]) {
      if (filter[k]) params[k] = filter[k]
    }
    const res = await listExecRecordings(params)
    items.value = res.data.items || []
    total.value = res.data.total || 0
  } catch { /* http 层已提示 */ } finally { loading.value = false }
}

async function fetchCast(id: number): Promise<string | null> {
  castLoading.value = true
  castErr.value = ''
  try {
    const res = await getRecordingCast(id)
    return String(res.data || '')
  } catch (e) {
    castErr.value = errMsg(e)
    return null
  } finally { castLoading.value = false }
}

async function openPlay(row: any) {
  playRow.value = row
  playDlg.value = true
  await nextTick()
  const text = await fetchCast(row.id)
  if (!text || !playerBox.value) return
  playerBox.value.innerHTML = ''
  try {
    playerInstance = createAsciinemaPlayer({ data: text }, playerBox.value, {
      autoPlay: false,
      controls: { bottom: true } as any,
      theme: 'asciinema',
    })
    await fitPlayer()
    window.addEventListener('resize', fitPlayer)
  } catch (e: any) {
    castErr.value = '回放初始化失败: ' + (e?.message || e)
  }
}

// 录像行数多时播放器高度会超出 1080P 屏幕；播放器随容器宽度自适应字号，
// 按窗口可用高度等比收窄宽度即可让整体缩到屏幕内
async function fitPlayer() {
  const box = playerBox.value
  const el = box?.firstElementChild as HTMLElement | null
  if (!box || !el) return
  // 预算：4vh 弹窗顶 + 对话框标题栏 + meta 行 + 内边距 ≈ 210px
  const avail = Math.max(300, window.innerHeight - 210)
  el.style.width = ''
  el.style.margin = '0 auto' // 收窄后居中，避免靠左露出大片空白
  for (let i = 0; i < 5; i++) {
    await new Promise((r) => setTimeout(r, 120)) // 等终端渲染 / ResizeObserver 重排
    const h = el.offsetHeight
    if (!h || h <= avail) break
    const w = el.offsetWidth
    el.style.width = `${Math.floor((w * avail) / h)}px`
  }
}

function disposePlayer() {
  window.removeEventListener('resize', fitPlayer)
  try { playerInstance?.dispose?.() } catch { /* 忽略 */ }
  playerInstance = null
}

// 仅输出内容的文本（去掉 ANSI 控制序列，便于检索）；兼容 asciicast v2 数组与对象两种事件写法
function castToText(cast: string): string {
  const out: string[] = []
  for (const line of cast.split('\n')) {
    const t = line.trim()
    if (!t) continue
    try {
      const ev = JSON.parse(t)
      if (Array.isArray(ev) && ev[1] === 'o' && typeof ev[2] === 'string') out.push(ev[2])
      else if (ev?.event === 'o' && typeof ev.data === 'string') out.push(ev.data)
    } catch { /* 头部或坏行跳过 */ }
  }
  return out.join('')
    // eslint-disable-next-line no-control-regex
    .replace(/\x1b\[[0-9;?=><!]*[a-zA-Z]/g, '')
    // eslint-disable-next-line no-control-regex
    .replace(/\x1b\][^\x07]*(\x07|\x1b\\)/g, '')
    .replace(/\r\n/g, '\n')
}

async function openRaw(row: any) {
  rawRow.value = row
  rawDlg.value = true
  rawText.value = ''
  const text = await fetchCast(row.id)
  if (!text) return
  const header = text.split('\n')[0]
  let meta = ''
  try {
    const h = JSON.parse(header)
    const w = h.width ?? h.cols
    const hh = h.height ?? h.rows
    meta = `会话终端 ${w}×${hh}，录制于 ${h.timestamp ? new Date(h.timestamp * 1000).toLocaleString('zh-CN', { hour12: false }) : '-'}\n\n`
  } catch { /* 无头部 */ }
  rawText.value = meta + castToText(text)
}

// 集群显示名：纳管时填了描述则显示「描述(名称)」，否则显示名称（与项目管理页同口径，可搜索别名）
function clusterLabel(c: any): string {
  const desc = (c.description || '').trim()
  return desc ? `${desc}(${c.name})` : c.name
}

// AI 审计摘要：从录像提取命令序列生成摘要（权限与回放同口径，后端校验）
const aiDlgRef = ref()
const aiRow = ref<any>(null)
function openAiSummary(row: any) {
  aiRow.value = row
  aiDlgRef.value?.start((onDelta: (t: string) => void, signal: AbortSignal) => aiRecordingSummary(row.id, onDelta, signal))
}

onMounted(async () => {
  try { clusters.value = (await listClusters()).data } catch { /* 忽略 */ }
  load()
})
</script>

<style scoped>
.toolbar { display: flex; gap: 10px; margin-bottom: 12px; align-items: center; }
.player-box { min-height: 420px; margin-top: 8px; background: #0c0c0c; border-radius: 4px; }
.play-meta { color: #909399; font-size: 12px; margin-bottom: 8px; }
.raw-box { max-height: 65vh; overflow: auto; background: #0c0c0c; border-radius: 4px; }
.raw-box pre { color: #d4d4d4; font-size: 12px; line-height: 1.5; padding: 12px; white-space: pre-wrap; word-break: break-all; margin: 0; }
</style>

<style>
/* asciinema-player 控制条增强：原生进度条是灰色系（前景灰 vs 20% 灰底）且仅 3px 高，
   黑底终端上几乎看不出播放进度——已播部分改主题主色并加高、加光晕 */
.ap-player .ap-control-bar .ap-progressbar .ap-bar .ap-gutter {
  height: 4px;
  top: 14px;
}
.ap-player .ap-control-bar .ap-progressbar .ap-bar .ap-gutter-empty {
  background-color: rgba(255, 255, 255, .28);
}
.ap-player .ap-control-bar .ap-progressbar .ap-bar .ap-gutter-full {
  background-color: var(--el-color-primary, #BE0915);
  box-shadow: 0 0 8px 0 var(--el-color-primary, #BE0915);
}
/* 悬停时进度条再加粗一点，方便点击/拖拽定位 */
.ap-player .ap-control-bar .ap-progressbar .ap-bar:hover .ap-gutter {
  height: 6px;
  top: 13px;
}
/* 控制条常驻但半透明（原生是 opacity:0 悬停才出现，太隐蔽）；
   需 !important 压过库内联优先级，悬停恢复不透明 */
.ap-player .ap-control-bar { opacity: .75 !important; }
.ap-player:hover .ap-control-bar { opacity: 1 !important; }
.dim { color: var(--el-text-color-placeholder); }
</style>
