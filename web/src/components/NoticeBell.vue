<template>
  <!-- 顶栏铃铛：站内消息入口（权限新增/移除/续期通知 + <7 天到期一次性预警），60s 轮询未读数 -->
  <el-badge :value="unread" :hidden="!unread" :max="99" class="notice-badge">
    <el-tooltip content="站内消息（权限变更与到期提醒）" placement="bottom">
      <button class="support-btn" type="button" @click="open = true">
        <el-icon><BellFilled /></el-icon>消息
      </button>
    </el-tooltip>
  </el-badge>

  <el-drawer v-model="open" title="站内消息" size="420px" @open="load">
    <div v-loading="loading" class="notice-body">
      <div v-if="items.length" class="notice-toolbar">
        <span class="dim">{{ unread ? `${unread} 条未读` : '全部已读' }}</span>
        <el-button v-if="unread" size="small" text type="primary" @click="readAll">全部已读</el-button>
      </div>
      <div v-for="n in items" :key="n.id" class="notice-item" :class="{ unread: !n.read_at }" @click="readOne(n)">
        <el-icon class="notice-icon" :color="metaOf(n.type).color"><component :is="metaOf(n.type).icon" /></el-icon>
        <div class="notice-main">
          <div class="notice-title">
            <span class="unread-dot" v-if="!n.read_at"></span>{{ n.title }}
          </div>
          <div class="notice-content">{{ n.content }}</div>
          <!-- 到期预警：给出「统一权限门户」申请入口（点击不触发已读外的其他行为） -->
          <a v-if="isExpiring(n.type)" class="notice-link" href="https://portal.example.com" target="_blank" rel="noopener" @click.stop>
            前往统一权限门户申请 →
          </a>
          <div class="notice-time dim">{{ fmtTime(n.created_at) }}</div>
        </div>
      </div>
      <el-empty v-if="!items.length && !loading" description="暂无消息" :image-size="72" />
    </div>
  </el-drawer>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { listNotifications, readNotifications, type NotificationItem } from '../api'

const open = ref(false)
const loading = ref(false)
const items = ref<NotificationItem[]>([])
const unread = ref(0)

// 类型 → 图标与颜色（与后端 notify 包的类型常量一一对应）
const typeMeta: Record<string, { icon: string; color: string }> = {
  grant_added: { icon: 'CircleCheck', color: 'var(--el-color-success)' },
  grant_removed: { icon: 'CircleClose', color: 'var(--el-color-danger)' },
  grant_renewed: { icon: 'RefreshRight', color: 'var(--el-color-primary)' },
  grant_expiring: { icon: 'WarningFilled', color: 'var(--el-color-warning)' },
  account_expiring: { icon: 'WarningFilled', color: 'var(--el-color-warning)' },
}
function metaOf(t: string) {
  return typeMeta[t] || { icon: 'InfoFilled', color: 'var(--el-color-info)' }
}

// 到期预警类消息（授权/权限组/账号）显示「统一权限门户」申请跳转
function isExpiring(t: string): boolean {
  return t === 'grant_expiring' || t === 'account_expiring'
}

async function load() {
  loading.value = true
  try {
    const res = await listNotifications()
    items.value = res.data?.items || []
    unread.value = res.data?.unread || 0
  } catch { /* 轮询静默失败（_silent 已抑制全局报错） */ } finally { loading.value = false }
}

async function readOne(n: NotificationItem) {
  if (n.read_at) return
  try {
    await readNotifications({ ids: [n.id] })
    n.read_at = new Date().toISOString()
    unread.value = Math.max(0, unread.value - 1)
  } catch { /* 单条已读失败不打扰，下次轮询自然校正 */ }
}

async function readAll() {
  try {
    await readNotifications({ all: true })
    for (const n of items.value) n.read_at = n.read_at || new Date().toISOString()
    unread.value = 0
  } catch { /* 同上 */ }
}

function fmtTime(t: string): string {
  return new Date(t).toLocaleString('zh-CN', { hour12: false })
}

// 60s 轮询未读数与列表（Layout 常驻，登录期间持续刷新）
let timer: number | undefined
onMounted(() => {
  load()
  timer = window.setInterval(load, 60_000)
})
onUnmounted(() => { if (timer) window.clearInterval(timer) })
</script>

<style scoped>
/* 角标与顶栏按钮的间距 */
.notice-badge { margin-right: 2px; }
/* 顶栏按钮样式与 Layout.vue 的 .support-btn 同款（scoped 样式跨组件不生效，复制一份） */
.support-btn {
  display: inline-flex; align-items: center; gap: 5px;
  border: none; background: transparent; cursor: pointer;
  font-size: 13px; color: var(--header-text, #606266); padding: 6px 10px; border-radius: 6px;
  transition: color .2s, background .2s;
}
.support-btn:hover { color: var(--header-btn-hover-color, var(--el-color-primary)); background: var(--header-btn-hover-bg); }
.notice-body { padding: 0 4px; }
.notice-toolbar {
  display: flex; align-items: center; justify-content: space-between;
  font-size: 12px; margin-bottom: 8px;
}
.notice-item {
  display: flex; gap: 10px; padding: 10px 8px; border-radius: 6px;
  cursor: pointer; transition: background .15s;
}
.notice-item:hover { background: var(--el-fill-color-light); }
.notice-item.unread .notice-title { font-weight: 600; }
.notice-icon { font-size: 18px; margin-top: 2px; flex-shrink: 0; }
.notice-main { flex: 1; min-width: 0; }
.notice-title {
  display: flex; align-items: center; gap: 6px;
  font-size: 14px; color: var(--el-text-color-primary);
}
.unread-dot { width: 6px; height: 6px; border-radius: 50%; background: var(--el-color-danger); flex-shrink: 0; }
.notice-content {
  font-size: 13px; color: var(--el-text-color-regular);
  line-height: 1.5; margin-top: 3px; word-break: break-all;
}
/* 到期消息的申请跳转链接（@click.stop 防触发整行已读点击的副作用——已读仍由整行点击处理） */
.notice-link {
  display: inline-block; margin-top: 4px;
  font-size: 12px; text-decoration: none;
}
.notice-link:hover { text-decoration: underline; }
.notice-time { font-size: 12px; margin-top: 3px; }
.dim { color: var(--el-text-color-placeholder); }
</style>
