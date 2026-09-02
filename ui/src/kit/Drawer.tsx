import { useEffect, useRef, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

const FOCUSABLE = 'button:not(:disabled), [href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])'

type Width = 'content' | 'wide' | number

type DrawerProps = {
  open: boolean
  onClose: () => void
  /** Id of the heading rendered inside `children`. This panel never renders its own heading. */
  labelledBy: string
  /** 'content' (min-content, capped at 720px), 'wide' (960px), or an exact pixel width. */
  width?: Width
  children: ReactNode
}

/**
 * D-021: the inspector floats over the page as a right-side drawer instead
 * of sitting in a `Panes` column. `aria-modal="false"` because the page
 * behind it stays readable and scrollable; only the scrim click and Escape
 * are modal-shaped conveniences, not an actual focus trap on the page.
 */
export function Drawer({ open, onClose, labelledBy, width = 'content', children }: DrawerProps) {
  const panelRef = useRef<HTMLDivElement>(null)
  const openerRef = useRef<Element | null>(null)

  useEffect(() => {
    if (!open) return

    openerRef.current = document.activeElement
    const panel = panelRef.current
    const focusable = panel?.querySelector<HTMLElement>(FOCUSABLE)
    focusable?.focus()

    function onKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        event.stopPropagation()
        onClose()
      }
    }
    document.addEventListener('keydown', onKeyDown, true)
    return () => {
      document.removeEventListener('keydown', onKeyDown, true)
      if (openerRef.current instanceof HTMLElement) openerRef.current.focus()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  if (!open) return null

  const widthClass = width === 'wide' ? ' sm-drawer--wide' : ''
  const style = typeof width === 'number' ? { width: `${width}px`, maxWidth: `${width}px` } : undefined

  return createPortal(
    <>
      <div className="sm-drawer-scrim" onClick={onClose} aria-hidden="true" />
      <div ref={panelRef} role="dialog" aria-modal="false" aria-labelledby={labelledBy} className={`sm-drawer${widthClass}`} style={style}>
        <div className="sm-drawer__bar">
          <button type="button" className="sm-drawer__close" onClick={onClose} aria-label="Close">
            <span aria-hidden="true">✕</span>
          </button>
        </div>
        <div className="sm-drawer__body">{children}</div>
      </div>
    </>,
    document.body,
  )
}
