// 通用小工具：年龄格式化 / 状态颜色
export function ageOf(ts?: string): string {
  if (!ts) return '-'
  const t = new Date(ts).getTime()
  if (!t) return '-'
  const s = Math.max(0, Math.floor((Date.now() - t) / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h${m % 60}m`
  const d = Math.floor(h / 24)
  return `${d}d${h % 24}h`
}

export function statusColor(status?: string): string {
  switch (status) {
    case 'Healthy': case 'Running': case 'Active': case 'Succeeded': case 'success': return 'success'
    case 'Degraded': case 'Pending': case 'Terminating': case 'ContainerCreating': return 'warning'
    // 高频故障状态显性标红：镜像拉取失败 / 驱逐 / OOM / 限流退避 / 创建运行失败
    case 'Failed': case 'Error': case 'CrashLoopBackOff': case 'denied': return 'danger'
    case 'Evicted': case 'OOMKilled': case 'ImagePullBackOff': case 'ErrImagePull':
    case 'CreateContainerConfigError': case 'CreateContainerError': case 'RunContainerError':
    case 'InvalidImageName': case 'DeadlineExceeded': return 'danger'
    default: return 'info'
  }
}

// Pod 就绪统计
export function podReady(item: any): string {
  const cs = item?.status?.conditions || []
  let ready = 0
  let restarts = 0
  let total = 0
  for (const c of item?.spec?.containers || []) {
    total++
    const st = (item?.status?.containerStatuses || []).find((x: any) => x.name === c.name)
    if (st?.ready) ready++
    restarts += st?.restartCount || 0
  }
  // 中点分隔不用全角括号：全角括号一对 24px，省下后窄列宽少占一行
  return `${ready}/${total} · 重启 ${restarts}`
}

// Pod 展示状态：phase 只有五种粗粒度值，真实故障原因藏在 containerStatuses[].state 里
// （waiting.reason 如 ImagePullBackOff/CreateContainerError；terminated.reason 如 Evicted/OOMKilled）。
// 优先展示具体原因（多容器取第一个异常的），否则回退 phase——列表里直接看到「拉不到镜像/被驱逐」。
// 与总览 podAbnormalReason 同口径：ContainerCreating/PodInitializing 属正常启动过程不透出；
// init 容器一并检查（init 卡住 = Pod 卡住）
export function podPhase(item: any): string {
  const cs = [...(item?.status?.initContainerStatuses || []), ...(item?.status?.containerStatuses || [])]
  for (const c of cs) {
    const waiting = c.state?.waiting?.reason
    if (waiting && waiting !== 'ContainerCreating' && waiting !== 'PodInitializing') return waiting
    const term = c.state?.terminated?.reason
    if (term && term !== 'Completed') return term
  }
  return item?.status?.reason || item?.status?.phase || 'Unknown'
}

// Deployment/StatefulSet 就绪；DaemonSet 无 spec.replicas/readyReplicas，
// 期望数用 desiredNumberScheduled、就绪数用 numberReady
export function deployReady(item: any): string {
  const s = item?.status || {}
  const spec = item?.spec?.replicas ?? s.desiredNumberScheduled ?? 0
  return `${s.readyReplicas ?? s.numberReady ?? 0}/${spec}`
}
