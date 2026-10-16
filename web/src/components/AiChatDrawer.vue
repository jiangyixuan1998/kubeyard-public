<template>
  <el-drawer v-model="visible" size="520px" :with-header="false" @closed="onClosed">
    <div class="chat-wrap">
      <div class="chat-head">
        <div class="chat-title">
          <el-icon><ChatDotRound /></el-icon> AI 排查助手
        </div>
        <div class="chat-sub">只读助手：不接触未授权数据，不执行任何变更；粘事件/状态/错误给我分析</div>
      </div>
      <div ref="listRef" class="chat-list">
        <template v-if="msgs.length === 0">
          <div class="chat-empty">
            <p>描述你遇到的问题，或粘贴事件 / 状态 / YAML 让我分析。例如：</p>
            <div v-for="s in samples" :key="s" class="sample" @click="ask(s)">{{ s }}</div>
            <p class="dim">我能看到的受你的权限限制——无权资源的数据我拿不到，会提示你去申请。</p>
          </div>
        </template>
        <div v-for="(m, i) in msgs" :key="i" class="bubble" :class="m.role">
          <div v-if="m.role === 'user'" class="bubble-text">{{ m.content }}</div>
          <div v-else class="bubble-text ai">
            <AiReasoning v-if="partsOf(m).reasoning" :text="partsOf(m).reasoning"
              :active="i === msgs.length - 1 && thinking && !partsOf(m).content"
              :has-answer="!!partsOf(m).content" />
            <AiMarkdown v-if="partsOf(m).content" :text="partsOf(m).content" />
            <span v-if="i === msgs.length - 1 && thinking" class="caret">▌</span>
          </div>
        </div>
        <div v-if="err" class="chat-err">
          <el-icon><WarningFilled /></el-icon> {{ err }}
        </div>
      </div>
      <div class="chat-input">
        <el-input v-model="draft" type="textarea" :rows="2" resize="none" placeholder="输入问题，Enter 发送 / Shift+Enter 换行"
          :disabled="thinking" @keydown.enter.exact.prevent="send" />
        <div class="chat-actions">
          <el-button link type="danger" size="small" :disabled="!msgs.length || thinking" @click="clearAll">清空会话</el-button>
          <el-button type="primary" size="small" :loading="thinking" @click="send">发送</el-button>
        </div>
      </div>
    </div>
  </el-drawer>
</template>

<script setup lang="ts">
// 全局 AI 排查助手（只读多轮对话）：历史保存在组件内存，刷新即清——
// 不落库，避免对话内容成为平台的留存数据；配额/审计由后端统一管。
import { nextTick, ref } from 'vue'
import { ChatDotRound, WarningFilled } from '@element-plus/icons-vue'
import { aiChat } from '../api'
import AiMarkdown from './AiMarkdown.vue'
import AiReasoning from './AiReasoning.vue'
import { splitReasoning } from '../utils/aimd'

type Msg = { role: 'user' | 'assistant'; content: string }

// 助手消息的思考/正文拆分（模板渲染用；拆分本身只是字符串定位，流式期间反复调用开销可忽略）
function partsOf(m: Msg) {
  return splitReasoning(m.content)
}

const visible = ref(false)
const msgs = ref<Msg[]>([])
const draft = ref('')
const thinking = ref(false)
const err = ref('')
const listRef = ref<HTMLElement>()

const samples = [
  'Pod 一直 CrashLoopBackOff，常见原因有哪些？怎么排查？',
  'K8s 里 requests 和 limits 分别是什么？不设置会怎样？',
  'Deployment 滚动更新卡住不动了，一般是什么问题？',
]

function scrollBottom() {
  nextTick(() => listRef.value?.scrollTo({ top: listRef.value.scrollHeight }))
}

function open() {
  visible.value = true
}

// 请求代次防竞态（外部评审核实）：抽屉关闭（onClosed 只清 thinking）后旧请求的
// 流式回调仍在追加 reply；关闭期间清空对话或重开抽屉再发问，旧响应会混进新会话。
// 代次不匹配的回调/收尾一律丢弃；ctrl 真正中断底层请求（后端随请求 ctx 级联
// 取消上游模型调用，不浪费额度）
let chatSeq = 0
let chatCtrl: AbortController | null = null

function onClosed() {
  // 关闭即中断等待态、使进行中的流式回调失效并取消底层请求
  chatSeq++
  chatCtrl?.abort()
  chatCtrl = null
  thinking.value = false
}

function clearAll() {
  msgs.value = []
  err.value = ''
}

function ask(text: string) {
  if (thinking.value) return
  draft.value = text
  send()
}

async function send() {
  const text = draft.value.trim()
  if (!text || thinking.value) return
  draft.value = ''
  err.value = ''
  const my = ++chatSeq
  chatCtrl?.abort() // 上一条还在生成：先取消再发新问题
  chatCtrl = new AbortController()
  const signal = chatCtrl.signal
  msgs.value.push({ role: 'user', content: text })
  const reply: Msg = { role: 'assistant', content: '' }
  msgs.value.push(reply)
  thinking.value = true
  scrollBottom()
  try {
    // 发送全部历史（后端截尾保留最近 12 条并做配额/审计）；
    // 助手消息只回传正文——思考过程标记对模型是噪音，剥离后再发
    const hist = msgs.value.slice(0, -1)
      .map((m) => ({ role: m.role, content: m.role === 'assistant' ? partsOf(m).content : m.content }))
      .filter((m) => m.content.trim())
    await aiChat(hist, (d) => {
      if (my !== chatSeq) return // 抽屉已关闭重开：旧流丢弃
      reply.content += d
      scrollBottom()
    }, signal)
  } catch (e: any) {
    if (my !== chatSeq || e?.name === 'AbortError') return
    err.value = e?.message || 'AI 调用失败'
    if (!reply.content) msgs.value.pop() // 空回复的占位气泡撤掉
  } finally {
    if (my === chatSeq) thinking.value = false
    if (my === chatSeq && !reply.content && msgs.value[msgs.value.length - 1] === reply) msgs.value.pop()
  }
}

defineExpose({ open })
</script>

<style scoped>
.chat-wrap { display: flex; flex-direction: column; height: 100%; }
.chat-head { padding: 4px 0 12px; border-bottom: 1px solid var(--el-border-color-lighter); }
.chat-title { font-size: 15px; font-weight: 600; display: flex; align-items: center; gap: 6px; }
.chat-sub { font-size: 12px; color: #909399; margin-top: 4px; }
.chat-list { flex: 1; overflow: auto; padding: 12px 2px; }
.chat-empty { font-size: 13px; color: #606266; }
.chat-empty .sample {
  margin: 8px 0; padding: 8px 12px; border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px; cursor: pointer; font-size: 13px;
}
.chat-empty .sample:hover { border-color: var(--el-color-primary); color: var(--el-color-primary); }
.bubble { margin-bottom: 12px; display: flex; }
.bubble.user { justify-content: flex-end; }
.bubble-text {
  max-width: 88%; white-space: pre-wrap; word-break: break-word; font-size: 13px;
  line-height: 1.65; padding: 8px 12px; border-radius: 10px;
  background: #f4f4f5; color: #303133;
}
.bubble-text.ai { white-space: normal; min-width: 0; }
.bubble.user .bubble-text { background: var(--el-color-primary); color: #fff; }
.caret { animation: blink 1s steps(1) infinite; margin-left: 1px; }
@keyframes blink { 50% { opacity: 0; } }
.chat-err { color: var(--el-color-danger); font-size: 12.5px; display: flex; align-items: center; gap: 4px; margin-bottom: 8px; }
.chat-input { border-top: 1px solid var(--el-border-color-lighter); padding-top: 10px; }
.chat-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 8px; }
.dim { color: #909399; font-size: 12px; }
</style>
