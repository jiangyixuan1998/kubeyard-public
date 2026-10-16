<template>
  <el-tooltip :disabled="!tip" :content="tip" placement="top">
    <el-tag size="small" :type="tagType">{{ label }}</el-tag>
  </el-tooltip>
</template>

<script setup lang="ts">
// 授权时限标签（统一分色口径）：已过期红 / 7 天内到期黄「即将到期」/ 有时限绿「剩 N 天」/ 永久红
// —— 永久标红是有意为之：临时授权到期自愈回收是常态，永久授权需要定期审视
import { computed } from 'vue'
import { expiryStateOf, daysLeft } from '../utils/grant'

const props = withDefaults(defineProps<{
  expiresAt?: Date | string | null
  /** 永久是否显示红标（规则编辑卡传 false：未设时限是常态，不渲染标签噪音） */
  showPermanent?: boolean
}>(), { showPermanent: true })

const state = computed(() => expiryStateOf(props.expiresAt))
const tagType = computed(() => ({ expired: 'danger', soon: 'warning', limited: 'success', permanent: 'danger' }[state.value]))

const label = computed(() => {
  if (state.value === 'expired') return '已过期'
  if (state.value === 'soon') return '即将到期'
  if (state.value === 'limited') return `剩 ${daysLeft(props.expiresAt)} 天`
  return '永久'
})

const tip = computed(() => {
  if (state.value === 'permanent' || !props.expiresAt) return ''
  const t = props.expiresAt instanceof Date
    ? props.expiresAt.toLocaleString('zh-CN', { hour12: false })
    : new Date(props.expiresAt).toLocaleString('zh-CN', { hour12: false })
  if (state.value === 'expired') return `已于 ${t} 过期（联系管理员改期续授）`
  if (state.value === 'soon') return `剩 ${daysLeft(props.expiresAt)} 天，${t} 到期，请及时续期`
  return `${t} 到期`
})
</script>
