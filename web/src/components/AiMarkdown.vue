<template>
  <div class="md" v-html="html"></div>
</template>

<script setup lang="ts">
// AI 输出的 Markdown 渲染（marked 解析 + DOMPurify 消毒，见 utils/aimd）。
// 独立成组件而非父级 computed：流式期间只有最后一个气泡/对话框在变，
// 历史气泡的 text prop 不变即不重渲染，避免每个 delta 全量重解析。
import { computed } from 'vue'
import { renderMd } from '../utils/aimd'

const props = defineProps<{ text: string }>()
const html = computed(() => renderMd(props.text))
</script>

<style scoped>
.md { font-size: 13px; line-height: 1.7; color: var(--el-text-color-primary); word-break: break-word; }
.md :deep(p) { margin: 0 0 8px; }
.md > :deep(:last-child) { margin-bottom: 0; }
.md :deep(h1), .md :deep(h2), .md :deep(h3), .md :deep(h4) { margin: 12px 0 6px; font-weight: 600; line-height: 1.4; }
.md :deep(h1) { font-size: 17px; }
.md :deep(h2) { font-size: 15.5px; }
.md :deep(h3) { font-size: 14px; }
.md :deep(h4) { font-size: 13.5px; }
.md :deep(ul), .md :deep(ol) { margin: 4px 0 8px; padding-left: 20px; }
.md :deep(li) { margin: 2px 0; }
.md :deep(code) {
  background: rgba(0, 0, 0, 0.06); padding: 1px 5px; border-radius: 4px;
  font-size: 12px; font-family: 'Consolas', 'Menlo', monospace;
}
/* 代码块与平台内 Monaco 深色主题同风格 */
.md :deep(pre) {
  background: #282c34; color: #abb2bf; padding: 10px 12px; border-radius: 6px;
  overflow: auto; margin: 8px 0;
}
.md :deep(pre code) { background: none; padding: 0; color: inherit; }
.md :deep(table) { border-collapse: collapse; margin: 8px 0; font-size: 12.5px; }
.md :deep(th), .md :deep(td) { border: 1px solid var(--el-border-color-lighter); padding: 4px 10px; text-align: left; }
.md :deep(th) { background: rgba(0, 0, 0, 0.03); font-weight: 600; }
.md :deep(blockquote) {
  margin: 8px 0; padding: 2px 12px; border-left: 3px solid var(--el-border-color);
  color: var(--el-text-color-secondary);
}
.md :deep(a) { color: var(--el-color-primary); }
.md :deep(hr) { border: none; border-top: 1px solid var(--el-border-color-lighter); margin: 10px 0; }
</style>
