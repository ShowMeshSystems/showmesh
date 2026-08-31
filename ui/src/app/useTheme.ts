import { useEffect, useState } from 'react'

/**
 * Theme and density ride on the document root, where the kit's token
 * overrides are keyed. 'system' resolves to dark or light rather than
 * writing no attribute: the kit defines dark on bare :root, so an
 * unresolved 'system' would ignore a light-mode operating system.
 */
export type Theme = 'system' | 'dark' | 'light' | 'contrast'
export type Density = 'default' | 'compact'

const THEME_KEY = 'showmesh-ui-theme'
const DENSITY_KEY = 'showmesh-ui-density'
const THEMES: readonly Theme[] = ['system', 'dark', 'light', 'contrast']

function read<T extends string>(key: string, allowed: readonly T[], fallback: T): T {
  try {
    const stored = window.localStorage.getItem(key)
    return stored !== null && (allowed as readonly string[]).includes(stored) ? (stored as T) : fallback
  } catch {
    return fallback
  }
}

function write(key: string, value: string): void {
  try {
    window.localStorage.setItem(key, value)
  } catch {
    // Locked-down browser context. The in-memory state still governs this session.
  }
}

function prefersLight(): boolean {
  return typeof window.matchMedia === 'function' && window.matchMedia('(prefers-color-scheme: light)').matches
}

export function resolveTheme(theme: Theme): Exclude<Theme, 'system'> {
  if (theme !== 'system') return theme
  return prefersLight() ? 'light' : 'dark'
}

export function useTheme(): [Theme, (theme: Theme) => void] {
  // Show-time starts dark unless this browser already has an explicit choice.
  // Light remains a deliberate daylight setting; a stored `system` choice is
  // preserved for existing operators rather than being silently rewritten.
  const [theme, setTheme] = useState<Theme>(() => read(THEME_KEY, THEMES, 'dark'))

  useEffect(() => {
    document.documentElement.setAttribute('data-theme', resolveTheme(theme))
    write(THEME_KEY, theme)
  }, [theme])

  return [theme, setTheme]
}

export function useDensity(): [Density, (density: Density) => void] {
  const densities: readonly Density[] = ['default', 'compact']
  const [density, setDensity] = useState<Density>(() => read(DENSITY_KEY, densities, 'default'))

  useEffect(() => {
    document.documentElement.setAttribute('data-density', density)
    write(DENSITY_KEY, density)
  }, [density])

  return [density, setDensity]
}
