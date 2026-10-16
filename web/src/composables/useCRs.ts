import { reactive } from 'vue'
import { listCRs, CRInfo } from '../api'

// 模块级共享缓存：跨组件、跨页面复用；按集群 key，5 分钟 TTL
const cache: Record<string, { items: CRInfo[]; loading: boolean; error: string; ts: number }> = reactive({})
const TTL = 5 * 60 * 1000

export function useCRs() {
  async function load(cluster: string, force = false): Promise<CRInfo[]> {
    if (!cluster) return []
    const e = cache[cluster]
    if (!force && e && e.items.length && Date.now() - e.ts < TTL) return e.items
    cache[cluster] = { items: e?.items || [], loading: true, error: '', ts: e?.ts || 0 }
    try {
      const items = await listCRs(cluster)
      cache[cluster] = { items, loading: false, error: '', ts: Date.now() }
    } catch (err: any) {
      // 失败时保留旧列表（避免切换集群后清空已加载项），仅记录错误供 UI 提示
      cache[cluster] = { items: cache[cluster]?.items || [], loading: false, error: err?.response?.data?.message || err?.message || 'failed', ts: Date.now() }
    }
    return cache[cluster].items
  }
  function get(cluster: string): CRInfo[] { return cache[cluster]?.items || [] }
  function clear(cluster?: string) {
    if (cluster) delete cache[cluster]
    else for (const k of Object.keys(cache)) delete cache[k]
  }
  return { load, get, clear, cache }
}
