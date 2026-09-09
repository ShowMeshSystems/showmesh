import { useEffect, useRef, type CSSProperties, type KeyboardEvent, type MouseEvent, type ReactNode, type RefObject } from 'react'

/**
 * Wide tables scroll inside this wrapper; the page never gains horizontal
 * scrolling. Keep the table's min-width low enough to fit the 1280 spine.
 */
export function TableWrap({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="sm-table-wrap" tabIndex={0} role="region" aria-label={label}>
      {children}
    </div>
  )
}

/**
 * Below the restack breakpoint each cell needs its column heading to label
 * its stacked card row (`table.css`'s `content: attr(data-label)`). Call
 * sites author raw `<thead>`/`<tbody>` markup, so this reads its own
 * rendered header row after each render and stamps the heading onto the
 * matching cell in every body row, rather than asking every call site to
 * repeat the heading on each `<td>`.
 */
function useColumnLabels(ref: RefObject<HTMLTableElement | null>) {
  useEffect(() => {
    const table = ref.current
    if (table === null) return
    const headRow = table.tHead?.rows[0]
    if (headRow === undefined) return
    const labels = Array.from(headRow.cells).map((cell) => cell.textContent ?? '')
    for (const body of Array.from(table.tBodies)) {
      for (const row of Array.from(body.rows)) {
        Array.from(row.cells).forEach((cell, index) => {
          const label = labels[index]
          if (label !== undefined && label !== '') cell.setAttribute('data-label', label)
        })
      }
    }
  })
}

export function Table({ children, minWidth = 520 }: { children: ReactNode; minWidth?: number }) {
  const ref = useRef<HTMLTableElement>(null)
  useColumnLabels(ref)
  return (
    <table ref={ref} className="sm-table" style={{ '--sm-table-min-width': `${minWidth}px` } as CSSProperties}>
      {children}
    </table>
  )
}

const INTERACTIVE_DESCENDANT = 'a, button, input, select, textarea, summary, [role="button"], [role="link"]'

/** A table row that opens its record without disguising one cell as the action. */
export function SelectableRow({
  children,
  onActivate,
  selected = false,
  className,
  ariaLabel,
}: {
  children: ReactNode
  onActivate: () => void
  selected?: boolean
  className?: string
  ariaLabel?: string
}) {
  const activateFromPointer = (event: MouseEvent<HTMLTableRowElement>) => {
    if ((event.target as Element).closest(INTERACTIVE_DESCENDANT)) return
    onActivate()
  }
  const activateFromKeyboard = (event: KeyboardEvent<HTMLTableRowElement>) => {
    if (event.target !== event.currentTarget || (event.key !== 'Enter' && event.key !== ' ')) return
    event.preventDefault()
    onActivate()
  }

  return (
    <tr
      tabIndex={0}
      aria-label={ariaLabel}
      aria-current={selected ? 'true' : undefined}
      className={['sm-table__row--selectable', selected ? 'sm-table__row--current' : '', className ?? ''].filter(Boolean).join(' ')}
      onClick={activateFromPointer}
      onKeyDown={activateFromKeyboard}
    >
      {children}
    </tr>
  )
}

/** Freshness rides in the row, never in a banner above the table. */
export function Freshness({ text, stale = false }: { text: string; stale?: boolean }) {
  return <span className={stale ? 'sm-table__fresh sm-table__fresh--stale' : 'sm-table__fresh'}>{text}</span>
}
