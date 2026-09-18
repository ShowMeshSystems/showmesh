import type { ReactNode } from 'react'

/**
 * Two-column control layout: the driving controls on the left, the state they
 * act on (status, log, results) on the right. Collapses to one column at the
 * shell's responsive breakpoint. Lives inside `.sm-main` and sets no page
 * width. `sideLabel` names the right column for assistive technology.
 */
export function Workbench({
  main,
  side,
  sideLabel = 'Status',
}: {
  main: ReactNode
  side: ReactNode
  sideLabel?: string
}) {
  return (
    <div className="sm-workbench">
      <div className="sm-workbench__main">{main}</div>
      <aside className="sm-workbench__side" aria-label={sideLabel}>
        {side}
      </aside>
    </div>
  )
}
