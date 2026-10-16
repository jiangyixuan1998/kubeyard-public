// AI 输出渲染辅助：思考过程/正文拆分 + Markdown 安全渲染。
// 分隔标记与后端 internal/ai/provider.go 的流式输出保持一致：
//   “—— 思考过程 ——\n” <思考> “\n\n—— 回答 ——\n” <正文>
// 非流式兜底头「—— 思考过程（正文为空…）——」按行首前缀同样命中。
// 标记由后端整帧发出（不随模型 delta 拆碎），流式期间不会出现半个标记的中间态。
import { marked } from 'marked'
import DOMPurify from 'dompurify'

const THINK_LINE = /(?:^|\n)—— 思考过程[^\n]*/
const ANSWER_LINE = /(?:^|\n)—— 回答 ——/

// splitReasoning 把一段流式累计文本拆成思考过程与正文。
// 无思考标记（非推理模型）时 reasoning 为空、content 为原文；
// 有标记但「—— 回答 ——」还没到时 content 为空（思考仍在流式中）。
export function splitReasoning(raw: string): { reasoning: string; content: string } {
  const head = raw.match(THINK_LINE)
  if (!head) return { reasoning: '', content: raw }
  const after = raw.slice((head.index ?? 0) + head[0].length)
  const sep = after.match(ANSWER_LINE)
  if (!sep) return { reasoning: after.trim(), content: '' }
  return {
    reasoning: after.slice(0, sep.index).trim(),
    content: after.slice((sep.index ?? 0) + sep[0].length).replace(/^\n+/, ''),
  }
}

// renderMd Markdown → 消毒后的 HTML。AI 输出不可信（可能包含集群数据回显），
// 必须过 DOMPurify 再进 v-html；breaks 让单换行也换行（流式输出常用单行分段）。
marked.setOptions({ gfm: true, breaks: true })
DOMPurify.addHook('afterSanitizeAttributes', (node) => {
  // 链接一律新标签页打开，防止覆盖平台页面
  if (node.nodeName === 'A') {
    node.setAttribute('target', '_blank')
    node.setAttribute('rel', 'noopener noreferrer')
  }
})

export function renderMd(src: string): string {
  if (!src) return ''
  return DOMPurify.sanitize(marked.parse(src) as string)
}
