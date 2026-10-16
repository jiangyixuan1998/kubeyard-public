<template>
  <!-- 无 UI：全局 window 事件桥，让任意页面（Dashboard 巡检结果等）能打开 AI 排查助手 -->
  <span style="display: none" />
</template>

<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue'
// utils/aiAssist.ts 的 openAiAssist() dispatch 'ky-open-ai-assist'，这里转成 Vue 事件给 Layout
const emit = defineEmits<{ (e: 'open'): void }>()
function onOpen() {
  emit('open')
}
onMounted(() => window.addEventListener('ky-open-ai-assist', onOpen))
onUnmounted(() => window.removeEventListener('ky-open-ai-assist', onOpen))
</script>
