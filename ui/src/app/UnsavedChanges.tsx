import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'

interface UnsavedChangesContextValue {
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
  const [dirty, setDirty] = useState(false)
  const [pendingDestination, setPendingDestination] = useState<string | null>(null)
  const dirtySource = useRef<HTMLElement | null>(null)
  const stayButton = useRef<HTMLButtonElement | null>(null)

  const clearUnsavedChanges = useCallback(() => {
    dirtySource.current = null
    setDirty(false)
  }, [])

  useEffect(() => {
    function noteEdit(event: Event): void {
      const target = event.target
      if (!(target instanceof HTMLElement)) return
      if (target.matches('input, select, textarea') && !(target as HTMLInputElement).readOnly && !(target as HTMLInputElement).disabled) {
        const source = target.closest<HTMLElement>('[data-unsaved-form]')
        if (source !== null) {
          dirtySource.current = source
          setDirty(true)
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
      if (active && dirtySource.current !== null && !dirtySource.current.isConnected) clearUnsavedChanges()
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
      setPendingDestination(`${url.pathname}${url.search}${url.hash}`)
    }
    document.addEventListener('click', interceptLink, true)
    return () => document.removeEventListener('click', interceptLink, true)
  }, [dirty])

  useEffect(() => {
    if (pendingDestination !== null) stayButton.current?.focus()
  }, [pendingDestination])

  const discard = useCallback(() => {
    const destination = pendingDestination
    clearUnsavedChanges()
    setPendingDestination(null)
    if (destination !== null) navigate(destination)
  }, [clearUnsavedChanges, navigate, pendingDestination])

  const value = useMemo(() => ({ clearUnsavedChanges }), [clearUnsavedChanges])

  return (
    <UnsavedChangesContext.Provider value={value}>
      {children}
      {pendingDestination !== null && (
        <div className="panel panel--warning" role="alertdialog" aria-modal="true" aria-labelledby="unsaved-navigation-title" aria-describedby="unsaved-navigation-detail" tabIndex={-1} onKeyDown={(event) => { if (event.key === 'Escape') setPendingDestination(null) }}>
          <h2 id="unsaved-navigation-title" className="panel__title">Discard unsaved changes?</h2>
          <p id="unsaved-navigation-detail">You have changes that have not been saved. Stay on this page to keep them, or discard changes and leave this page.</p>
          <div className="config-save-row">
            <button ref={stayButton} className="button button--secondary" type="button" onClick={() => setPendingDestination(null)}>Stay</button>
            <button className="button-danger" type="button" onClick={discard}>Discard changes</button>
          </div>
        </div>
      )}
    </UnsavedChangesContext.Provider>
  )
}

export function useUnsavedChanges(): UnsavedChangesContextValue {
  return useContext(UnsavedChangesContext)
}
