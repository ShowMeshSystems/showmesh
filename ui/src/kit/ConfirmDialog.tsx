import { useEffect, useId, useRef, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { Button, ButtonRow } from './Button'

const FOCUSABLE = 'button:not(:disabled), [href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])'

type ConfirmDialogProps = {
  open: boolean
  title: string
  detail: ReactNode
  confirmLabel: string
  cancelLabel?: string
  onConfirm: () => void
  onCancel: () => void
}

/**
 * The in-page replacement for window.confirm: a real aria-modal that traps
 * focus and returns it to the opener. Unlike Drawer and Popover it demands
 * a decision before the page behind it can be used again.
 */
export function ConfirmDialog({ open, title, detail, confirmLabel, cancelLabel = 'Cancel', onConfirm, onCancel }: ConfirmDialogProps) {
  const panelRef = useRef<HTMLDivElement>(null)
  const openerRef = useRef<Element | null>(null)
  const titleId = useId()

  useEffect(() => {
    if (!open) return

    openerRef.current = document.activeElement
    const panel = panelRef.current
    const focusable = panel?.querySelector<HTMLElement>(FOCUSABLE)
    focusable?.focus()

    function onKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        event.stopPropagation()
        onCancel()
        return
      }
      if (event.key !== 'Tab') return
      const nodes = panelRef.current?.querySelectorAll<HTMLElement>(FOCUSABLE)
      if (nodes === undefined || nodes.length === 0) return
      const first = nodes[0]
      const last = nodes[nodes.length - 1]
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault()
        last?.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first?.focus()
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

  return createPortal(
    <>
      <div className="sm-confirm-scrim" onClick={onCancel} aria-hidden="true" />
      <div ref={panelRef} role="dialog" aria-modal="true" aria-labelledby={titleId} className="sm-confirm">
        <p id={titleId} className="sm-confirm__title">{title}</p>
        <div className="sm-confirm__detail">{detail}</div>
        <ButtonRow>
          <Button variant="danger" onClick={onConfirm}>{confirmLabel}</Button>
          <Button variant="quiet" onClick={onCancel}>{cancelLabel}</Button>
        </ButtonRow>
      </div>
    </>,
    document.body,
  )
}
