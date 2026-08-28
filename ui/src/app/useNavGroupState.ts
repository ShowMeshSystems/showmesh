import { useCallback, useEffect, useState } from 'react'

// Sidebar nav groups are collapsible (owner-reported: 25 top-level links
// across 5 groups, with Configure alone holding 13, made the expanded
// column too dense to scan even with #114's internal scroll). Persisted
// per viewer in localStorage, same reasoning as useHighContrast.ts's own
// header comment: an operator preference, not a secret, so it belongs in
// localStorage rather than sessionStorage.
const STORAGE_KEY = 'showmesh-ui-nav-groups'

// Operate is open on a first visit (no stored preference at all) because it
// is the operator's primary route group. Every other group defaults closed.
const DEFAULT_OPEN: Record<string, boolean> = { Operate: true }

export interface NavGroupItem {
  to: string
  end: boolean
}

export interface NavGroup {
  heading: string
  items: NavGroupItem[]
}

function readStoredState(): Record<string, boolean> {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY)
    if (!raw) return {}
    const parsed: unknown = JSON.parse(raw)
    if (parsed !== null && typeof parsed === 'object' && !Array.isArray(parsed)) {
      return parsed as Record<string, boolean>
    }
    return {}
  } catch {
    // Storage can throw in a locked-down browser context (private window,
    // disabled site data). Falling back to "nothing stored" still renders
    // correctly: every group just uses its default open state below.
    return {}
  }
}

function writeStoredState(state: Record<string, boolean>): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(state))
  } catch {
    // Best effort; the in-memory state still governs this session.
  }
}

// Mirrors react-router-dom's own NavLink matching rule (this file's only
// reason to duplicate it): `end: true` matches the path exactly, `end:
// false` also matches any sub-path past a `/` boundary. Needed here,
// independent of NavLink, to find which GROUP holds the current route.
function isItemActive(pathname: string, item: NavGroupItem): boolean {
  if (item.end) return pathname === item.to
  if (pathname === item.to) return true
  return pathname.startsWith(item.to.endsWith('/') ? item.to : `${item.to}/`)
}

export interface NavGroupOpenState {
  /** Whether `heading`'s group should render expanded right now. */
  isOpen: (heading: string) => boolean
  /** Flips `heading`'s remembered preference. */
  toggle: (heading: string) => void
}

/**
 * Tracks which nav groups are expanded. The group holding the current
 * route is ALWAYS reported open, regardless of what is stored -- an
 * operator must never have to hunt for the page they are already on, and
 * that requirement overrides a remembered collapsed state rather than
 * merely seeding it.
 */
export function useNavGroupOpenState(groups: NavGroup[], pathname: string): NavGroupOpenState {
  const [stored, setStored] = useState<Record<string, boolean>>(readStoredState)

  useEffect(() => {
    writeStoredState(stored)
  }, [stored])

  const activeHeading = groups.find((group) =>
    group.items.some((item) => isItemActive(pathname, item)),
  )?.heading

  const isOpen = useCallback(
    (heading: string) => {
      if (heading === activeHeading) return true
      if (heading in stored) return stored[heading] === true
      return DEFAULT_OPEN[heading] === true
    },
    [stored, activeHeading],
  )

  const toggle = useCallback((heading: string) => {
    setStored((previous) => {
      const current = heading in previous ? previous[heading] === true : DEFAULT_OPEN[heading] === true
      return { ...previous, [heading]: !current }
    })
  }, [])

  return { isOpen, toggle }
}
