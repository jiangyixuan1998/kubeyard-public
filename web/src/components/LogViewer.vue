<template>
  <div class="log-viewer">
    <div class="log-toolbar">
      <el-switch v-model="follow" active-text="跟随" @change="reconnect" />
      <el-input-number v-model="tailLines" :min="100" :max="10000" :step="100" size="small" @change="reconnect" />
      <el-checkbox v-model="previous" @change="reconnect">上一容器</el-checkbox>
      <el-button size="small" @click="download">
        <el-icon><Download /></el-icon> 下载
      </el-button>
      <el-button size="small" @click="autoScroll = !autoScroll" :type="autoScroll ? 'primary' : ''">自动滚动</el-button>
      <el-button size="small" type="primary" plain @click="aiAnalyze">
        <el-icon><MagicStick /></el-icon> AI 分析
      </el-button>
    </div>
    <div ref="boxRef" class="log-box" @scroll="onScroll">{{ content }}</div>
    <AiDialog ref="aiDlgRef" title="AI 日志分析" />
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Download, MagicStick } from '@element-plus/icons-vue'
import { WsChannel } from '../utils/ws'
import { aiLogs } from '../api'
import AiDialog from './AiDialog.vue'

const props = defineProps<{
  cluster: string
  namespace: string
  pod: string
  container?: string
}>()

const content = ref('')
const follow = ref(true)
const autoScroll = ref(true)
const tailLines = ref(1000)
const previous = ref(false)
const boxRef = ref<HTMLElement>()
let channel: WsChannel | null = null
let bytes = 0

function reconnect() {
  channel?.close()
  content.value = ''
  bytes = 0
  connect()
}

function connect() {
  channel = new WsChannel()
  channel.onBinary = (data) => {
    content.value += data
    bytes += data.length
    // 上限 5MB，超出截断头部
    if (bytes > 5 * 1024 * 1024) {
      content.value = content.value.slice(-2 * 1024 * 1024)
      bytes = content.value.length
    }
    if (autoScroll.value) requestAnimationFrame(scrollBottom)
  }
  channel.onControl = (msg) => {
    if (msg.type === 'error') content.value += `\n[错误] ${msg.message}\n`
    if (msg.type === 'end' && follow.value) content.value += '\n[流已结束]\n'
  }
  const q = new URLSearchParams({
    cluster: props.cluster,
    namespace: props.namespace,
    pod: props.pod,
    follow: String(follow.value),
    tail_lines: String(tailLines.value),
    previous: String(previous.value),
  })
  if (props.container) q.set('container', props.container)
  channel.connect(`/api/v1/ws/logs?${q.toString()}`)
}

function scrollBottom() {
  if (boxRef.value) boxRef.value.scrollTop = boxRef.value.scrollHeight
}

function onScroll() {
  const el = boxRef.value
  if (!el) return
  // 手动上滚查看历史时暂停自动滚动
  if (el.scrollHeight - el.scrollTop - el.clientHeight > 40) autoScroll.value = false
}

function download() {
  const blob = new Blob([content.value], { type: 'text/plain' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = `${props.pod}-${props.container || 'default'}.log`
  a.click()
  URL.revokeObjectURL(a.href)
}

// 容器切换（多容器 Pod 在对话框标题栏选择）：断开旧流按新容器重新拉取
watch(() => props.container, () => reconnect())

// AI 日志分析：上下文取当前参数（含「上一容器」开关），权限与日志查看同口径
const aiDlgRef = ref()
function aiAnalyze() {
  aiDlgRef.value?.start((onDelta, signal) =>
    aiLogs(
      { cluster: props.cluster, namespace: props.namespace, pod: props.pod, container: props.container, previous: previous.value },
      onDelta,
      signal,
    ),
  )
}

onMounted(connect)
onBeforeUnmount(() => channel?.close())
</script>

<style scoped>
.log-viewer { display: flex; flex-direction: column; height: 100%; }
.log-toolbar { display: flex; align-items: center; gap: 12px; padding: 8px; border-bottom: 1px solid #dcdfe6; }
.log-box {
  flex: 1; overflow: auto; background: #1e1e2e; color: #d4d4d4;
  font-family: 'Cascadia Mono', Consolas, monospace; font-size: 12px;
  line-height: 1.6; padding: 8px 12px; white-space: pre-wrap; word-break: break-all;
}
</style>
