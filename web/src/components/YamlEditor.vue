<template>
  <VueMonacoEditor
    :value="modelValue"
    :language="language"
    theme="vs"
    :options="editorOptions"
    height="100%"
    @update:value="(v: string) => $emit('update:modelValue', v)"
  />
</template>

<script setup lang="ts">
import { defineAsyncComponent } from 'vue'
import { loader } from '@guolao/vue-monaco-editor'

// Monaco 本地打包（不走 CDN）+ 按需异步加载：此前在 main.ts 静态引入整个
// monaco-editor，全部代码进了主包——登录/总览页也要下载解析 4.7MB 主 JS。
// 异步工厂里先动态 import 本地 monaco 并 loader.config（必须在组件挂载、
// loader.init() 之前完成，否则 loader 回退 CDN 且内网不可达），再返回组件；
// 工厂只执行一次，多实例共享同一次加载
const VueMonacoEditor = defineAsyncComponent(async () => {
  const [monaco, { VueMonacoEditor: Comp }] = await Promise.all([
    import('monaco-editor'),
    import('@guolao/vue-monaco-editor'),
  ])
  loader.config({ monaco })
  return Comp
})

const props = withDefaults(defineProps<{
  modelValue: string
  language?: string
  readOnly?: boolean
}>(), { language: 'yaml', readOnly: false })

defineEmits<{ (e: 'update:modelValue', v: string): void }>()

const editorOptions = {
  readOnly: props.readOnly,
  minimap: { enabled: false },
  fontSize: 13,
  lineNumbers: 'on' as const,
  scrollBeyondLastLine: false,
  automaticLayout: true,
  tabSize: 2,
  wordWrap: 'on' as const,
  renderWhitespace: 'none' as const,
  theme: 'vs' as const,
}
</script>
