import axios from 'axios'
import { ElMessage } from 'element-plus'

export const http = axios.create({ baseURL: '/api/v1', timeout: 60000 })

let refreshing: Promise<string | null> | null = null

http.interceptors.request.use((config) => {
  const token = localStorage.getItem('ky_access')
  if (token) config.headers.Authorization = `Bearer ${token}`
  // params 里的 _silent 标志提升到 config（供响应拦截器识别），不进查询串
  if ((config.params as any)?._silent) {
    ;(config as any)._silent = true
    delete config.params._silent
  }
  return config
})

http.interceptors.response.use(
  (res) => res,
  async (err) => {
    const { response, config } = err
    if (response?.status === 401 && !config._retried) {
      // access 过期：尝试用 refresh 换新
      config._retried = true
      refreshing ||= doRefresh()
      const token = await refreshing
      refreshing = null
      if (token) {
        config.headers.Authorization = `Bearer ${token}`
        return http(config)
      }
      localStorage.removeItem('ky_access')
      localStorage.removeItem('ky_refresh')
      localStorage.removeItem('ky_user')
      if (location.pathname !== '/login') location.href = '/login'
    }
    const msg = response?.data?.message || err.message || '请求失败'
    // _silent：调用方自行兜底的预加载（如存储类下拉），不弹全局错误提示
    if (response?.status !== 401 && !(config as any)._silent) ElMessage.error(msg)
    return Promise.reject(err)
  },
)

async function doRefresh(): Promise<string | null> {
  const refresh = localStorage.getItem('ky_refresh')
  if (!refresh) return null
  try {
    const res = await axios.post('/api/v1/auth/refresh', { refresh_token: refresh })
    localStorage.setItem('ky_access', res.data.access_token)
    localStorage.setItem('ky_refresh', res.data.refresh_token)
    return res.data.access_token
  } catch {
    return null
  }
}

export function errMsg(e: any): string {
  return e?.response?.data?.message || e?.message || '请求失败'
}

// AI 流式请求：fetch + SSE 解析（axios 浏览器端不支持流式读响应体）。
// 后端逐帧 data: {"delta":"..."}，失败帧 {"error":"..."}、结束帧 {"done":true}。
// 401 时不走刷新重试（AI 请求通常由用户主动触发失败可见，直接抛错即可）。
// signal：调用方（对话框关闭/停止）可中断——后端 AI 处理器全部以 c.Request.Context()
// 调模型，浏览器 abort 会级联取消上游请求，不浪费模型额度
export async function aiStream(url: string, body: any, onDelta: (t: string) => void, signal?: AbortSignal): Promise<void> {
  const token = localStorage.getItem('ky_access')
  const res = await fetch(url, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: JSON.stringify(body),
    signal,
  })
  if (!res.ok || !res.body) {
    let msg = `请求失败（${res.status}）`
    try { msg = (await res.json()).message || msg } catch { /* 保持默认 */ }
    throw new Error(msg)
  }
  const reader = res.body.getReader()
  const dec = new TextDecoder()
  let buf = ''
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buf += dec.decode(value, { stream: true })
    const parts = buf.split('\n\n') // SSE 事件以空行分隔
    buf = parts.pop() || ''
    for (const p of parts) {
      const line = p.split('\n').find((l) => l.startsWith('data: '))
      if (!line) continue
      let ev: any
      try { ev = JSON.parse(line.slice(6)) } catch { continue }
      if (ev.error) throw new Error(ev.error)
      if (ev.delta) onDelta(ev.delta)
    }
  }
}
