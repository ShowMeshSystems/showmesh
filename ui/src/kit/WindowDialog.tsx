import { useEffect, useId, useRef, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { Button } from './Button'

type WindowDialogProps = {
  open: boolean
  title: string
  children: ReactNode
  onClose: () => void
}

/** A large window over the page: title bar with an X, closed by Escape or a click outside. Focus returns to the opener. */
export function WindowDialog({ open, title, children, onClose }: WindowDialogProps) {
  const windowRef = useRef<HTMLDivElement>(null)
  const titleId = useId()

  useEffect(() => {
    if (!open) return
    const opener = document.activeElement
    windowRef.current?.querySelector('button')?.focus()
    function onKeyDown(event: KeyboardEvent) {
      if (event.key !== 'Escape') return
      event.stopPropagation()
      onClose()
    }
    document.addEventListener('keydown', onKeyDown, true)
    return () => {
      document.removeEventListener('keydown', onKeyDown, true)
      if (opener instanceof HTMLElement) opener.focus()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  if (!open) return null

  return createPortal(
    <>
      <div className="sm-window-scrim" onClick={onClose} aria-hidden="true" />
      <div ref={windowRef} role="dialog" aria-modal="true" aria-labelledby={titleId} className="sm-window">
        <div className="sm-window__bar">
          <h2 id={titleId} className="sm-window__title sm-subhead sm-truncate">{title}</h2>
          <Button size="icon" variant="quiet" aria-label="Close" title="Close" onClick={onClose}>
            <span aria-hidden="true">✕</span>
          </Button>
        </div>
        <div className="sm-window__body">{children}</div>
      </div>
    </>,
    document.body,
  )
}
