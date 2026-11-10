// Light, dark, or whatever the system says. public/theme.js applies the choice
// before the first paint; this is the same rule for when it changes later.
import { useEffect, useState } from 'preact/hooks'

export type Mode = 'system' | 'light' | 'dark'

const KEY = 'holdfast:theme'
/** The page ground in each theme, for the browser's own chrome. Keep in step with styles.css. */
const GROUND = { light: '#f5f7f8', dark: '#0e1316' } as const

function stored(): Mode {
  try {
    const m = localStorage.getItem(KEY)
    if (m === 'light' || m === 'dark') return m
  } catch {
    /* storage is blocked: follow the system */
  }
  return 'system'
}

function apply(mode: Mode): void {
  const light = mode === 'light' || (mode !== 'dark' && matchMedia('(prefers-color-scheme: light)').matches)
  document.documentElement.dataset.theme = light ? 'light' : 'dark'
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', light ? GROUND.light : GROUND.dark)
}

const NEXT: Record<Mode, Mode> = { system: 'light', light: 'dark', dark: 'system' }

/** The chosen mode, and a way to move to the next one. */
export function useTheme(): [Mode, () => void] {
  const [mode, setMode] = useState<Mode>(stored)
  useEffect(() => {
    apply(mode)
    if (mode !== 'system') return
    const system = matchMedia('(prefers-color-scheme: light)')
    const follow = () => apply('system')
    system.addEventListener('change', follow)
    return () => system.removeEventListener('change', follow)
  }, [mode])
  const cycle = () => {
    const next = NEXT[mode]
    try {
      if (next === 'system') localStorage.removeItem(KEY)
      else localStorage.setItem(KEY, next)
    } catch {
      /* the choice lasts for this visit only */
    }
    setMode(next)
  }
  return [mode, cycle]
}
