import type { ReactNode } from 'react'
import { evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'

// ADR-024 decision 12 / OPERATOR-UI section 14: "a control the principal
// may not use is rendered disabled with a stated reason rather than
// hidden. An absent control is indistinguishable from a feature that does
// not exist." Built (and tested — ScopedButton.test.tsx) in Step 6 even
// though nothing called it yet, because Step 6 shipped no write endpoint
// of its own — Step 7 seam A's "Save configuration" button
// (views/Configuration.tsx) is this component's first real caller.
export interface ScopedButtonProps {
  /** The scope this action requires, e.g. "show:macro:run" (ADR-024 decision 4). */
  requiredScope: string
  onClick: () => void
  children: ReactNode
  className?: string
}

export function ScopedButton({ requiredScope, onClick, children, className }: ScopedButtonProps) {
  const model = useModelContext()
  const result = evaluateScope(model.session, model.sessionFetchFailed, requiredScope)

  if (result.allowed) {
    return (
      <button type="button" className={className} onClick={onClick}>
        {children}
      </button>
    )
  }

  return (
    <span className="scoped-button">
      <button type="button" className={className} disabled aria-disabled="true" title={result.reason}>
        {children}
      </button>
      <span className="scoped-button__reason">{result.reason}</span>
    </span>
  )
}
