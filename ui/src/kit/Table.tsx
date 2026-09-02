import type { CSSProperties, KeyboardEvent, MouseEvent, ReactNode } from 'react'

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

export function Table({ children, minWidth = 520 }: { children: ReactNode; minWidth?: number }) {
  return (
    <table className="sm-table" style={{ '--sm-table-min-width': `${minWidth}px` } as CSSProperties}>
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
