// 后端文件流下载：GET blob → 触发浏览器保存
// 错误分支特殊处理：responseType 为 blob 时，拦截器读不到错误 JSON（response.data 是 Blob），这里补一次解析
import { ElMessage } from 'element-plus'
import { http } from '../api/http'

function localDate(): string {
  const d = new Date()
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}` // 本地日期，与后端文件名口径一致
}

export async function downloadGet(url: string, base: string) {
  try {
    const res = await http.get(url, { responseType: 'blob' })
    const blobUrl = URL.createObjectURL(res.data)
    const a = document.createElement('a')
    a.href = blobUrl
    a.download = `${base}-${localDate()}.xlsx`
    a.click()
    URL.revokeObjectURL(blobUrl)
  } catch (e: any) {
    const data = e?.response?.data
    if (data instanceof Blob && data.type.includes('json')) {
      try {
        const msg = JSON.parse(await data.text())?.message
        if (msg) {
          ElMessage.error(msg)
          return
        }
      } catch { /* 非 JSON 内容，退回通用提示 */ }
    }
    ElMessage.error(e?.message || '下载失败')
  }
}
