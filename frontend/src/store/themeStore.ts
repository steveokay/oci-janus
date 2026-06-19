/**
 * themeStore — theme preference with three modes: light, dark, system.
 *
 * Persistence: `localStorage` keyed by `janus.theme`. This is the one
 * preference we DO persist client-side (unlike the JWT) — it's a UI
 * preference, not a credential, and the cost of asking on every reload
 * is poor UX.
 *
 * Apply mechanism: toggles a `.dark` class on `<html>`. `globals.css`
 * declares all dark-mode token overrides under `.dark`, so adding /
 * removing the class swaps the semantic colour palette atomically.
 *
 * System mode: tracks `prefers-color-scheme: dark` via `matchMedia` and
 * reapplies the class when the OS preference flips. We register the
 * listener at module load so it's active before the first React render.
 */
import { create } from 'zustand'

export type ThemeMode = 'light' | 'dark' | 'system'

interface ThemeState {
  mode: ThemeMode
  setMode: (m: ThemeMode) => void
}

const STORAGE_KEY = 'janus.theme'

function safeReadMode(): ThemeMode {
  if (typeof window === 'undefined') return 'system'
  const stored = window.localStorage.getItem(STORAGE_KEY)
  if (stored === 'light' || stored === 'dark' || stored === 'system') {
    return stored
  }
  return 'system'
}

function effectiveDark(mode: ThemeMode): boolean {
  if (mode === 'dark') return true
  if (mode === 'light') return false
  return (
    typeof window !== 'undefined' &&
    window.matchMedia('(prefers-color-scheme: dark)').matches
  )
}

function applyTheme(mode: ThemeMode): void {
  if (typeof document === 'undefined') return
  document.documentElement.classList.toggle('dark', effectiveDark(mode))
}

export const useThemeStore = create<ThemeState>((set) => ({
  mode: safeReadMode(),
  setMode: (mode) => {
    if (typeof window !== 'undefined') {
      window.localStorage.setItem(STORAGE_KEY, mode)
    }
    applyTheme(mode)
    set({ mode })
  },
}))

// Apply the persisted (or default) mode immediately on module load so the
// page doesn't flash light-then-dark on first paint.
applyTheme(safeReadMode())

// Re-apply when the OS preference changes IF we're tracking the system.
if (typeof window !== 'undefined' && window.matchMedia) {
  const mql = window.matchMedia('(prefers-color-scheme: dark)')
  const onChange = () => {
    if (useThemeStore.getState().mode === 'system') {
      applyTheme('system')
    }
  }
  // addEventListener is the modern API; older Safari needs addListener.
  if (mql.addEventListener) {
    mql.addEventListener('change', onChange)
  } else {
    mql.addListener(onChange)
  }
}
