<template>
  <div class="terminal-wrapper">
    <div class="rec-indicator" :title="title">
      <span class="rec-dot" />
      <span class="rec-text">终端录像中</span>
    </div>
    <div ref="termRef" class="terminal-box"></div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { WsChannel } from '../utils/ws'

const props = defineProps<{
  cluster: string
  namespace: string
  pod: string
  container?: string
}>()

// 标题展示：明确告知用户当前会话正在被录像（震慑作用）
const title = computed(() =>
  `容器 ${props.pod} 的终端会话正在被全程录制，操作将被审计留存`)

const termRef = ref<HTMLElement>()
let term: Terminal | null = null
let fit: FitAddon | null = null
let channel: WsChannel | null = null
let resizeObserver: ResizeObserver | null = null
let pingTimer: number | null = null

onMounted(() => {
  term = new Terminal({
    fontSize: 13,
    cursorBlink: true,
    theme: { background: '#1e1e2e' },
  })
  fit = new FitAddon()
  term.loadAddon(fit)
  term.open(termRef.value!)
  term.attachCustomKeyEventHandler((event) => {
    // Let the browser IME handle composition keys instead of interpreting them as terminal input.
    return !event.isComposing && event.keyCode !== 229
  })
  fit.fit()

  channel = new WsChannel()
  channel.onBinary = (data) => term?.write(data)
  channel.onControl = (msg) => {
    if (msg.type === 'auth_ok') {
      // 连接建立后立即上报初始尺寸
      if (term) channel?.sendControl({ type: 'resize', cols: term.cols, rows: term.rows })
    } else if (msg.type === 'auth_failed' || msg.type === 'error') {
      term?.write(`\r\n\x1b[31m${msg.message || '连接失败'}\x1b[0m\r\n`)
    } else if (msg.type === 'end') {
      term?.write(`\r\n\x1b[33m会话已结束\x1b[0m\r\n`)
    }
  }
  term.onData((data) => channel?.sendBinary(data))

  // 尺寸回调必须在首次 fit 之前注册，否则初始 resize 事件丢失
  term.onResize(({ cols, rows }) => {
    channel?.sendControl({ type: 'resize', cols, rows })
  })
  fit.fit()

  const q = new URLSearchParams({
    cluster: props.cluster,
    namespace: props.namespace,
    pod: props.pod,
  })
  if (props.container) q.set('container', props.container)
  channel.connect(`/api/v1/ws/exec?${q.toString()}`)

  resizeObserver = new ResizeObserver(() => {
    fit?.fit()
  })
  resizeObserver.observe(termRef.value!)

  // 心跳
  pingTimer = window.setInterval(() => channel?.sendControl({ type: 'ping' }), 30000)
})

onBeforeUnmount(() => {
  if (pingTimer) clearInterval(pingTimer)
  resizeObserver?.disconnect()
  channel?.close()
  term?.dispose()
})
</script>

<style scoped>
.terminal-wrapper {
  position: relative;
  height: 100%;
}
.terminal-box {
  height: 100%;
  background: #1e1e2e;
  padding: 6px;
}
/* 右上角录像中指示：脉冲红点 + 文字，作为对操作者的明显震慑 */
.rec-indicator {
  position: absolute;
  top: 8px;
  right: 14px;
  z-index: 10;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  background: rgba(245, 108, 108, 0.92);
  color: #fff;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.5px;
  pointer-events: auto;
  user-select: none;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.3);
}
.rec-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: #fff;
  animation: rec-pulse 1.4s infinite;
}
.rec-text { line-height: 1; }
@keyframes rec-pulse {
  0%, 100% { opacity: 1; transform: scale(1); }
  50% { opacity: 0.4; transform: scale(0.7); }
}
</style>
