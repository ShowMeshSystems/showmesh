import { useEffect, useId, useReducer, useRef, type ReactNode, type RefObject } from 'react'
import { createPortal } from 'react-dom'

type PopoverProps = {
  open: boolean
  /** The heading text `role="dialog"` is labelled by. Rendered inside the panel. */
  title: string
  /** The control that opened this popover. Positioning, outside-click and focus-return all key off it. */
  anchorRef: RefObject<HTMLElement | null>
  onClose: () => void
  children: ReactNode
}

/**
 * A dialog anchored under a chrome-bar control (D-020): the show pill and the
 * mode badge, per Eric's 2026-09-01 ruling, are the two exceptions to "nothing
 * is a modal". The chrome bar is a sticky container with its own stacking
 * context, so this panel portals into `document.body` and positions itself
 * with `fixed` coordinates measured from the anchor's `getBoundingClientRect`
 * (Eric, 2026-09-01: it clipped inside the chrome otherwise). The anchor is
 * always mounted by the parent regardless of `open`, so its rect is read
 * straight from `anchorRef` during render — no extra render round-trip
 * before the panel (and the focus-management effect below) sees real DOM.
 */
export function Popover({ open, title, anchorRef, onClose, children }: PopoverProps) {
  const panelRef = useRef<HTMLDivElement>(null)
  const titleId = useId()
  const [remeasureTick, remeasure] = useReducer((count: number) => count + 1, 0)

  useEffect(() => {
    if (!open) return
    window.addEventListener('resize', remeasure)
    window.addEventListener('scroll', remeasure, true)
    return () => {
      window.removeEventListener('resize', remeasure)
      window.removeEventListener('scroll', remeasure, true)
    }
  }, [open])

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
      // `panel` is the portaled node itself: DOM containment does not care
      // that it renders outside the anchor's React tree.
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
  const anchor = anchorRef.current
  if (anchor === null) return null

  void remeasureTick // read only to force this recompute on resize/scroll
  const rect = anchor.getBoundingClientRect()

  return createPortal(
    <div
      ref={panelRef}
      className="sm-popover"
      role="dialog"
      aria-labelledby={titleId}
      style={{ top: rect.bottom, left: rect.left }}
    >
      <p id={titleId} className="sm-popover__title">
        {title}
      </p>
      {children}
    </div>,
    document.body,
  )
}
