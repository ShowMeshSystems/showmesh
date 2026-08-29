import { useCallback, useEffect, useState } from 'react'

/* Three themes plus following the operating system, applied as a data-theme
 * attribute on the root element that styles/tokens.css keys its overrides off.
 *
 * 'system' deliberately writes no attribute at all, because the light palette is
 * defined under `:root:not([data-theme])` and a prefers-color-scheme query. An
 * explicit choice must therefore win over the system preference in both
 * directions, which is why 'dark' writes an attribute rather than clearing one.
 *
 * The preference is an operator setting, not a secret, so it lives in
 * localStorage. The API token does not: spec section 5.6 requires that in
 * sessionStorage so it cannot outlive the tab.
 */
export type Theme = 'system' | 'dark' | 'light' | 'contrast'

const STORAGE_KEY = 'showmesh-ui-theme'
const THEMES: readonly Theme[] = ['system', 'dark', 'light', 'contrast']

/* The pre-overhaul key, so a device that had high contrast turned on keeps it
 * through the upgrade instead of silently reverting on a show night. */
const LEGACY_CONTRAST_KEY = 'showmesh-ui-contrast'

function isTheme(value: string | null): value is Theme {
  return value !== null && (THEMES as readonly string[]).includes(value)
}

function readStoredTheme(): Theme {
  try {
    const stored = window.localStorage.getItem(STORAGE_KEY)
    if (isTheme(stored)) return stored
    if (window.localStorage.getItem(LEGACY_CONTRAST_KEY) === 'high') return 'contrast'
    return 'system'
  } catch {
    // Storage throws in a locked-down browser context. Render rather than fail.
    return 'system'
  }
}

function applyTheme(theme: Theme): void {
  const root = document.documentElement
  if (theme === 'system') {
    root.removeAttribute('data-theme')
  } else {
    root.setAttribute('data-theme', theme)
  }
  /* Kept in step for the CSS still keyed on the old attribute. Removed with the
   * transition alias block at the end of the overhaul. */
  root.setAttribute('data-contrast', theme === 'contrast' ? 'high' : 'normal')
}

export function useTheme(): [Theme, (theme: Theme) => void] {
  const [theme, setTheme] = useState<Theme>(readStoredTheme)

  useEffect(() => {
    applyTheme(theme)
    try {
      window.localStorage.setItem(STORAGE_KEY, theme)
    } catch {
      // Best effort; the in-memory state still governs this session.
    }
  }, [theme])

  return [theme, setTheme]
}

/* Retained so the rail's contrast toggle and its existing tests keep working
 * while screens migrate to the four-way picker in Settings, Appearance. */
export function useHighContrast(): [boolean, () => void] {
  const [theme, setTheme] = useTheme()
  const toggle = useCallback(() => {
    setTheme(theme === 'contrast' ? 'system' : 'contrast')
  }, [theme, setTheme])
  return [theme === 'contrast', toggle]
}
