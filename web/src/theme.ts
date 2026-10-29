// 主题 composable：晨雾(day)/月影(night) 双主题，data-theme + 650ms 全局缓变
// （体系移植自 yuca-next stores/theme.ts；zhenshu 无 Pinia，模块级 ref 即全局态）
import { computed, ref } from 'vue'
import { darkTheme } from 'naive-ui'
import type { GlobalTheme, GlobalThemeOverrides } from 'naive-ui'

export type RhineTheme = 'day' | 'night'

const THEME_KEY = 'zhenshu-theme'
const THEME_TRANSITION_MS = 650

function initialTheme(): RhineTheme {
  try {
    const saved = localStorage.getItem(THEME_KEY)
    if (saved === 'day' || saved === 'night') return saved
  } catch { /* ignore */ }
  return 'night'
}

const theme = ref<RhineTheme>(initialTheme())
const theming = ref(false)
let themingTimer: number | null = null

// 模块加载时对齐 DOM（index.html 内联脚本已先行处理，这里兜底）
document.documentElement.dataset.theme = theme.value

function setTheme(t: RhineTheme) {
  if (t === theme.value) return
  // 切换期间给根节点加 .theming，让全站颜色做 650ms 缓变
  document.documentElement.classList.add('theming')
  document.documentElement.dataset.theme = t
  try { localStorage.setItem(THEME_KEY, t) } catch { /* ignore */ }
  theme.value = t
  theming.value = true
  if (themingTimer) clearTimeout(themingTimer)
  themingTimer = window.setTimeout(() => {
    document.documentElement.classList.remove('theming')
    theming.value = false
  }, THEME_TRANSITION_MS)
}

function cssVar(name: string, fallback: string): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  return v || fallback
}

// Naive UI 组件库主题接线：day → 亮色(null)，night → darkTheme。
// overrides 每次切换后现读 CSS 令牌生成，令牌改一处组件库即跟随。
const naiveTheme = computed<GlobalTheme | null>(() => (theme.value === 'night' ? darkTheme : null))

const naiveOverrides = computed<GlobalThemeOverrides>(() => {
  // 显式依赖 theme：cssVar 读的是ComputedStyle（非响应式），
  // 不摸 theme.value 这个 computed 永远只求值一次，切换主题后组件库拿的还是旧色
  const day = theme.value === 'day'
  void day
  const ink = cssVar('--ink', '#eef1f5')
  const muted = cssVar('--muted', '#8a93a3')
  const accent = cssVar('--accent', '#d0a04c')
  const surface = cssVar('--surface', '#141d2b')
  const line = cssVar('--line', 'rgba(255,255,255,.14)')
  const lineSoft = cssVar('--line-soft', 'rgba(255,255,255,.07)')
  return {
    common: {
      fontFamily: "'MiSans VF', 'MiSans', 'PingFang SC', 'Microsoft YaHei', system-ui, sans-serif",
      primaryColor: accent,
      primaryColorHover: cssVar('--accent-hover', '#ddb268'),
      primaryColorPressed: cssVar('--accent-pressed', '#b3873c'),
      primaryColorSuppl: accent,
      borderRadius: '0px',
      borderRadiusSmall: '0px',
      bodyColor: cssVar('--bg', '#0c121d'),
      cardColor: surface,
      modalColor: surface,
      popoverColor: cssVar('--panel', 'rgba(19,27,41,.97)'),
      inputColor: 'transparent',
      tableColor: surface,
      tableHeaderColor: surface,
      borderColor: line,
      dividerColor: lineSoft,
      textColorBase: ink,
      textColor1: ink,
      textColor2: ink,
      textColor3: muted,
      hoverColor: `color-mix(in srgb, ${ink} 4%, transparent)`,
      successColor: cssVar('--ok', '#7ee2a8'),
      errorColor: cssVar('--err', '#ff6b6b'),
      warningColor: cssVar('--warn', '#d9b25f'),
      infoColor: cssVar('--info', '#8fc7ff'),
      scrollbarColor: line,
      scrollbarColorHover: muted,
      closeIconColor: muted,
      closeIconColorHover: ink,
    },
    Button: { borderRadiusTiny: '0px', borderRadiusSmall: '0px', borderRadiusMedium: '0px', borderRadiusLarge: '0px' },
    Card: { borderRadius: '0px' },
    Modal: { borderRadius: '0px' },
    Input: { borderRadius: '0px' },
    Tag: { borderRadius: '0px' },
    Checkbox: { borderRadius: '0px' },
  }
})

export function useTheme() {
  return { theme, theming, setTheme }
}

export { naiveTheme, naiveOverrides }
