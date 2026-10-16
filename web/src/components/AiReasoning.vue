<template>
  <div class="think">
    <button type="button" class="think-head" @click="open = !open">
      <el-icon v-if="active" class="spin"><Loading /></el-icon>
      <el-icon v-else><MagicStick /></el-icon>
      <span class="think-label">思考过程</span>
      <span v-if="active" class="think-state">思考中…</span>
      <el-icon class="chev" :class="{ up: open }"><ArrowDown /></el-icon>
    </button>
    <div v-show="open" class="think-body">{{ text }}</div>
  </div>
</template>

<script setup lang="ts">
// 思考过程折叠块：推理模型（R1/Qwen3 类）的 reasoning_content 流式期间展开实时显示
// （让用户看得到进度而不是干等），正文开始输出时自动收起置灰，点击头部可随时手动展开/收起。
// hasAnswer=false 且流结束（active=false）时不收起——那是「思考耗尽上限、正文为空」的
// 截断场景，收起会让界面看起来空无一物，思考内容（含末尾截断提示）必须留在屏幕上。
import { ref, watch } from 'vue'
import { ArrowDown, Loading, MagicStick } from '@element-plus/icons-vue'

const props = defineProps<{ text: string; active: boolean; hasAnswer?: boolean }>()
const open = ref(props.active)
watch([() => props.active, () => props.hasAnswer], ([a, has]) => {
  if (a) open.value = true
  else if (has) open.value = false
})
</script>

<style scoped>
.think {
  border: 1px dashed var(--el-border-color); border-radius: 6px;
  background: rgba(0, 0, 0, 0.02); margin-bottom: 8px;
}
.think-head {
  width: 100%; display: flex; align-items: center; gap: 6px; padding: 5px 10px;
  background: none; border: none; cursor: pointer; font-size: 12px;
  color: var(--el-text-color-secondary);
}
.think-label { font-weight: 500; }
.think-state { color: var(--el-color-primary); }
.chev { margin-left: auto; transition: transform 0.2s; }
.chev.up { transform: rotate(180deg); }
.think-body {
  max-height: 300px; overflow: auto; padding: 6px 10px 8px;
  border-top: 1px dashed var(--el-border-color-lighter);
  white-space: pre-wrap; word-break: break-word;
  font-size: 12px; line-height: 1.65; color: var(--el-text-color-secondary);
  font-family: 'Consolas', 'Menlo', monospace;
}
.spin { animation: think-rot 1.2s linear infinite; }
@keyframes think-rot { to { transform: rotate(360deg); } }
</style>
