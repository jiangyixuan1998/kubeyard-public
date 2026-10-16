<template>
  <el-dialog v-model="visible" :title="title" width="720px" top="8vh" :close-on-click-modal="false" @closed="stop">
    <div ref="outRef" class="ai-out">
      <div v-if="!text && !err" class="ai-hint">
        <el-icon class="is-loading"><Loading /></el-icon>
        正在分析，输出将逐段显示…
      </div>
      <div v-if="err" class="ai-err">
        <el-icon><WarningFilled /></el-icon>
        {{ err }}
      </div>
      <template v-if="text">
        <AiReasoning v-if="parts.reasoning" :text="parts.reasoning"
          :active="running && !parts.content" :has-answer="!!parts.content" />
        <AiMarkdown v-if="parts.content" :text="parts.content" />
      </template>
    </div>
    <template #footer>
      <span v-if="running" class="ai-running">生成中…</span>
      <el-button @click="visible = false">关闭</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
// AI 流式输出对话框：调用方传一个流式 runner（内部用 api 层的 aiStream 封装），
// 组件负责增量渲染、自动滚底与错误展示。正文按 Markdown 渲染（工具输出多为
// 标题/列表/表格结构），推理模型的思考过程单独折叠展示（见 AiReasoning）。
// AI 严格只读，这里没有任何执行动作。
import { computed, nextTick, ref } from 'vue'
import { Loading, WarningFilled } from '@element-plus/icons-vue'
import AiMarkdown from './AiMarkdown.vue'
import AiReasoning from './AiReasoning.vue'
import { splitReasoning } from '../utils/aimd'

defineProps<{ title: string }>()

const visible = ref(false)
const text = ref('')
const err = ref('')
const running = ref(false)
const outRef = ref<HTMLElement>()

// 思考/正文拆分：无思考标记（非推理模型）时 reasoning 为空、content 为全文
const parts = computed(() => splitReasoning(text.value))

// start 打开对话框并执行流式请求；onDelta 由 runner 内部转发到这里。
// seq 防竞态（外部评审核实）：关闭后重新发起时，前一次请求的流式回调还在跑，
// 旧响应会追加进新一轮文本；stop 后旧回调按代次丢弃。
// signal 传给 runner → aiStream 的 fetch：关闭/停止即真正取消请求（后端随请求
// ctx 级联取消上游模型调用，不浪费额度），不再等 3 分钟自然超时
let seq = 0
let ctrl: AbortController | null = null
async function start(run: (onDelta: (t: string) => void, signal: AbortSignal) => Promise<void>) {
  const my = ++seq
  ctrl?.abort() // 上一轮还在跑：先取消再开新一轮
  ctrl = new AbortController()
  visible.value = true
  text.value = ''
  err.value = ''
  running.value = true
  try {
    await run((d: string) => {
      if (my !== seq) return // 对话框已关闭重开：旧流丢弃，不再污染新会话
      text.value += d
      nextTick(() => outRef.value?.scrollTo({ top: outRef.value.scrollHeight }))
    }, ctrl.signal)
  } catch (e: any) {
    if (my === seq && e?.name !== 'AbortError') err.value = e?.message || 'AI 调用失败'
  } finally {
    if (my === seq) running.value = false
  }
}

function stop() {
  seq++ // 使进行中回调全部失效
  ctrl?.abort() // 真正中断底层请求
  ctrl = null
}

defineExpose({ start })
</script>

<style scoped>
.ai-out {
  min-height: 200px;
  max-height: 62vh;
  overflow: auto;
  background: #f7f8fa;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
  padding: 12px 16px;
}
.ai-hint {
  color: var(--el-text-color-secondary);
  font-size: 13px;
  display: flex;
  align-items: center;
  gap: 6px;
}
.ai-err {
  color: var(--el-color-danger);
  font-size: 13px;
  display: flex;
  align-items: center;
  gap: 6px;
}
.ai-running {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-right: 12px;
}
</style>
