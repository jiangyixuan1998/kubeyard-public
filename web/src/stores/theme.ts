import { defineStore } from 'pinia'

// 配色方案：classic 极简白 / brand 品牌红 / slate 雅致黑 / azure 静谧蓝 / green 青竹绿
export type ThemeName = 'classic' | 'brand' | 'slate' | 'azure' | 'green'
export const THEMES: { value: ThemeName; label: string; dot: string }[] = [
  { value: 'classic', label: '极简白', dot: '#C84158' },
  { value: 'brand', label: '品牌红', dot: '#BE0915' },
  { value: 'slate', label: '雅致黑', dot: '#1d2129' },
  { value: 'azure', label: '静谧蓝', dot: '#4080FF' },
  { value: 'green', label: '青竹绿', dot: '#10A37F' },
]
const KEY = 'ky_theme'

export const useThemeStore = defineStore('theme', {
  state: () => ({ name: 'brand' as ThemeName }),
  actions: {
    init() {
      const saved = localStorage.getItem(KEY) as ThemeName | null
      this.apply(saved && THEMES.some((t) => t.value === saved) ? saved : 'brand')
    },
    apply(name: ThemeName) {
      this.name = name
      localStorage.setItem(KEY, name)
      document.documentElement.setAttribute('data-theme', name)
    },
  },
})
