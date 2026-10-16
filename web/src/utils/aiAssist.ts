// 全局打开 AI 排查助手（Layout 的 AiAssistBridge 监听并转发给抽屉）
export function openAiAssist() {
  window.dispatchEvent(new CustomEvent('ky-open-ai-assist'))
}
