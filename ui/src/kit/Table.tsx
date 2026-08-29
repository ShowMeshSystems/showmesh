import type { ReactNode } from 'react'

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

export function Table({ children }: { children: ReactNode }) {
  return <table className="sm-table">{children}</table>
}

/** Freshness rides in the row, never in a banner above the table. */
export function Freshness({ text, stale = false }: { text: string; stale?: boolean }) {
  return <span className={stale ? 'sm-table__fresh sm-table__fresh--stale' : 'sm-table__fresh'}>{text}</span>
}
