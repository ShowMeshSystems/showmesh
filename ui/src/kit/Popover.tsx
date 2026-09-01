import { useEffect, useId, useRef, type ReactNode, type RefObject } from 'react'

type PopoverProps = {
  open: boolean
  /** The heading text `role="dialog"` is labelled by. Rendered inside the panel. */
  title: string
  /** The control that opened this popover. Outside-click and focus-return both key off it. */
  anchorRef: RefObject<HTMLElement | null>
  onClose: () => void
  children: ReactNode
}

/**
 * A dialog anchored under a chrome-bar control (D-020): the show pill and the
 * mode badge, per Eric's 2026-09-01 ruling, are the two exceptions to "nothing
 * is a modal". The parent renders the anchor and wraps it in `position:
 * relative`; this panel is `position: absolute; top: 100%` inside that
 * wrapper, so it never needs the anchor's screen coordinates.
 */
export function Popover({ open, title, anchorRef, onClose, children }: PopoverProps) {
  const panelRef = useRef<HTMLDivElement>(null)
  const titleId = useId()

  useEffect(() => {
    if (!open) return

    const panel = panelRef.current
    const anchor = anchorRef.current
    const focusable = panel?.querySelector<HTMLElement>(
      'button:not(:disabled), [href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])',
    )
    focusable?.focus()

    function onKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        event.stopPropagation()
        onClose()
      }
    }
    function onPointerDown(event: MouseEvent) {
      const target = event.target as Node
      if (panel?.contains(target) === true) return
      if (anchor?.contains(target) === true) return
      onClose()
    }
    document.addEventListener('keydown', onKeyDown, true)
    document.addEventListener('mousedown', onPointerDown, true)
    return () => {
      document.removeEventListener('keydown', onKeyDown, true)
      document.removeEventListener('mousedown', onPointerDown, true)
      anchor?.focus()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  if (!open) return null

  return (
    <div ref={panelRef} className="sm-popover" role="dialog" aria-labelledby={titleId}>
      <p id={titleId} className="sm-popover__title">
        {title}
      </p>
      {children}
    </div>
  )
}
