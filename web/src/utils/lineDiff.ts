// 行级 diff（LCS 最长公共子序列）：YAML 修改前后的人工确认视图。
// 只标注整行的 增/删/不变，不做行内细粒度合并——改动是否可接受由人判断，
// 敏感行（密钥/镜像）只要出现在增删里就会被看见。

export interface DiffLine {
  type: 'same' | 'add' | 'del'
  text: string
}

export function lineDiff(oldText: string, newText: string): DiffLine[] {
  const a = oldText.split('\n')
  const b = newText.split('\n')
  const n = a.length
  const m = b.length
  // LCS 动态规划表；YAML 通常几百行，O(n*m) 可接受（上限保护防极端大文件卡 UI）
  if (n * m > 4_000_000) {
    return [
      { type: 'del', text: '（内容过大，无法生成逐行 diff——请人工完整核对）' },
      ...b.map((t) => ({ type: 'add' as const, text: t })),
    ]
  }
  // dp[i][j] = a[i:] 与 b[j:] 的 LCS 长度；倒序填表，正序回溯输出
  const dp: number[][] = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0))
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1])
    }
  }
  const out: DiffLine[] = []
  let i = 0
  let j = 0
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      out.push({ type: 'same', text: a[i] })
      i++
      j++
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      out.push({ type: 'del', text: a[i] })
      i++
    } else {
      out.push({ type: 'add', text: b[j] })
      j++
    }
  }
  while (i < n) out.push({ type: 'del', text: a[i++] })
  while (j < m) out.push({ type: 'add', text: b[j++] })
  return out
}
