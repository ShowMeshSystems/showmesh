import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'

interface UnsavedChangesContextValue {
  clearUnsavedChanges: (owner: string) => void
}

interface OwnedUnsavedChangesContextValue {
  clearUnsavedChanges: () => void
}

const UnsavedChangesContext = createContext<UnsavedChangesContextValue>({
  clearUnsavedChanges: () => undefined,
})

/**
 * Settings editors opt in with `data-unsaved-form`. The provider deliberately
 * observes edits rather than owning form values, so it never submits, mutates,
 * or interprets coordinator-backed configuration during a navigation warning.
 */
export function UnsavedChangesProvider({ children }: { children: ReactNode }) {
  const navigate = useNavigate()
  const location = useLocation()
  const [dirtyOwners, setDirtyOwners] = useState<ReadonlySet<string>>(new Set())
  const [pendingDestination, setPendingDestination] = useState<string | null>(null)
  const dirtySources = useRef(new Map<string, HTMLElement>())
  const dirtyControls = useRef(new Map<string, HTMLElement>())
  const stayButton = useRef<HTMLButtonElement | null>(null)
  const dialog = useRef<HTMLDivElement | null>(null)
  const focusBeforeDialog = useRef<HTMLElement | null>(null)
  const pendingHistoryDelta = useRef<number | null>(null)
  const ignoringPop = useRef(false)
  const historyIndex = useRef<number | null>(typeof window.history.state?.idx === 'number' ? window.history.state.idx : null)
  const dirty = dirtyOwners.size > 0

  const clearUnsavedChanges = useCallback((owner: string) => {
    dirtySources.current.delete(owner)
    dirtyControls.current.delete(owner)
    setDirtyOwners((owners) => {
      if (!owners.has(owner)) return owners
      const next = new Set(owners)
      next.delete(owner)
      return next
    })
  }, [])

  const openWarning = useCallback((destination: string, historyDelta: number | null = null) => {
    focusBeforeDialog.current = Array.from(dirtyControls.current.values()).at(-1)
      ?? (document.activeElement instanceof HTMLElement ? document.activeElement : null)
    pendingHistoryDelta.current = historyDelta
    setPendingDestination(destination)
  }, [])

  const stay = useCallback(() => {
    pendingHistoryDelta.current = null
    setPendingDestination(null)
  }, [])

  useEffect(() => {
    if (pendingDestination === null) focusBeforeDialog.current?.focus()
  }, [pendingDestination])

  useEffect(() => {
    if (typeof window.history.state?.idx === 'number') historyIndex.current = window.history.state.idx
  }, [location])

  useEffect(() => {
    function noteEdit(event: Event): void {
      const target = event.target
      if (!(target instanceof HTMLElement)) return
      if (target.matches('input, select, textarea') && !(target as HTMLInputElement).readOnly && !(target as HTMLInputElement).disabled) {
        const source = target.closest<HTMLElement>('[data-unsaved-form]')
        const owner = source?.dataset.unsavedForm
        if (source !== null && owner) {
          dirtySources.current.set(owner, source)
          dirtyControls.current.set(owner, target)
          setDirtyOwners((owners) => owners.has(owner) ? owners : new Set(owners).add(owner))
        }
      }
    }

    document.addEventListener('input', noteEdit, true)
    document.addEventListener('change', noteEdit, true)
    return () => {
      document.removeEventListener('input', noteEdit, true)
      document.removeEventListener('change', noteEdit, true)
    }
  }, [])

  useEffect(() => {
    if (!dirty) return
    let active = true
    const observer = new MutationObserver(() => {
      if (!active) return
      for (const [owner, source] of dirtySources.current) {
        if (!source.isConnected) clearUnsavedChanges(owner)
      }
    })
    observer.observe(document.body, { childList: true, subtree: true })
    return () => {
      active = false
      observer.disconnect()
    }
  }, [clearUnsavedChanges, dirty])

  useEffect(() => {
    function beforeUnload(event: BeforeUnloadEvent): void {
      if (!dirty) return
      event.preventDefault()
      event.returnValue = ''
    }
    window.addEventListener('beforeunload', beforeUnload)
    return () => window.removeEventListener('beforeunload', beforeUnload)
  }, [dirty])

  useEffect(() => {
    function interceptLink(event: MouseEvent): void {
      if (!dirty || event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
      const source = event.target
      if (!(source instanceof Element)) return
      const anchor = source.closest('a[href]')
      if (!(anchor instanceof HTMLAnchorElement) || anchor.target !== '' || anchor.hasAttribute('download')) return
      const url = new URL(anchor.href, window.location.href)
      if (url.origin !== window.location.origin) return
      if (url.pathname === window.location.pathname && url.search === window.location.search && url.hash === window.location.hash) return
      event.preventDefault()
      event.stopImmediatePropagation()
      openWarning(`${url.pathname}${url.search}${url.hash}`)
    }
    document.addEventListener('click', interceptLink, true)
    return () => document.removeEventListener('click', interceptLink, true)
  }, [dirty, openWarning])

  useEffect(() => {
    function interceptHistoryNavigation(event: PopStateEvent): void {
      if (ignoringPop.current) {
        ignoringPop.current = false
        return
      }
      if (!dirty) return
      const previousIndex = historyIndex.current
      const nextIndex = event.state?.idx
      if (typeof previousIndex !== 'number' || typeof nextIndex !== 'number' || previousIndex === nextIndex) return

      const delta = nextIndex - previousIndex
      event.stopImmediatePropagation()
      ignoringPop.current = true
      window.history.go(-delta)
      openWarning(`${window.location.pathname}${window.location.search}${window.location.hash}`, delta)
    }

    window.addEventListener('popstate', interceptHistoryNavigation, true)
    return () => window.removeEventListener('popstate', interceptHistoryNavigation, true)
  }, [dirty, openWarning])

  useEffect(() => {
    if (pendingDestination !== null) stayButton.current?.focus()
  }, [pendingDestination])

  useEffect(() => {
    if (pendingDestination === null) return
    function keepFocusInDialog(event: FocusEvent): void {
      if (dialog.current !== null && event.target instanceof Node && !dialog.current.contains(event.target)) stayButton.current?.focus()
    }
    document.addEventListener('focusin', keepFocusInDialog, true)
    return () => document.removeEventListener('focusin', keepFocusInDialog, true)
  }, [pendingDestination])

  const discard = useCallback(() => {
    const destination = pendingDestination
    for (const owner of dirtySources.current.keys()) clearUnsavedChanges(owner)
    setPendingDestination(null)
    const historyDelta = pendingHistoryDelta.current
    pendingHistoryDelta.current = null
    if (historyDelta !== null) {
      ignoringPop.current = true
      window.history.go(historyDelta)
    } else if (destination !== null) {
      navigate(destination)
    }
  }, [clearUnsavedChanges, navigate, pendingDestination])

  const value = useMemo(() => ({ clearUnsavedChanges }), [clearUnsavedChanges])

  return (
    <UnsavedChangesContext.Provider value={value}>
      {children}
      {pendingDestination !== null && (
        <div ref={dialog} className="panel panel--warning" role="alertdialog" aria-modal="true" aria-labelledby="unsaved-navigation-title" aria-describedby="unsaved-navigation-detail" tabIndex={-1} onKeyDown={(event) => {
          if (event.key === 'Escape') {
            event.preventDefault()
            stay()
            return
          }
          if (event.key !== 'Tab') return
          const controls = Array.from(dialog.current?.querySelectorAll<HTMLElement>('button:not([disabled])') ?? [])
          const first = controls[0]
          const last = controls.at(-1)
          if (!first || !last) return
          if (event.shiftKey && document.activeElement === first) {
            event.preventDefault()
            last.focus()
          } else if (!event.shiftKey && document.activeElement === last) {
            event.preventDefault()
            first.focus()
          }
        }}>
          <h2 id="unsaved-navigation-title" className="panel__title">Discard unsaved changes?</h2>
          <p id="unsaved-navigation-detail">You have changes that have not been saved. Stay on this page to keep them, or discard changes and leave this page.</p>
          <div className="config-save-row">
            <button ref={stayButton} className="button button--secondary" type="button" onClick={stay}>Stay</button>
            <button className="button-danger" type="button" onClick={discard}>Discard changes</button>
          </div>
        </div>
      )}
    </UnsavedChangesContext.Provider>
  )
}

export function useUnsavedChanges(owner: string): OwnedUnsavedChangesContextValue {
  const context = useContext(UnsavedChangesContext)
  const clearUnsavedChanges = useCallback(() => context.clearUnsavedChanges(owner), [context, owner])
  return useMemo(() => ({ clearUnsavedChanges }), [clearUnsavedChanges])
}
