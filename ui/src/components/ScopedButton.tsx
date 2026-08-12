import type { ReactNode } from 'react'
import { evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'

// ADR-024 decision 12 / OPERATOR-UI section 14: "a control the principal
// may not use is rendered disabled with a stated reason rather than
// hidden. An absent control is indistinguishable from a feature that does
// not exist." This is the reusable wrapper a write control uses to get
// that behavior for free, built (and tested — ScopedButton.test.tsx) in
// this step even though nothing calls it yet: BUILD-PLAN Step 6 adds no
// show write endpoint, so there is no real button to wire it to. It is
// deliberately not rendered anywhere in this application today, the same
// posture Layout.tsx's unrendered "Control"/"Configure" nav groups take —
// see that file's own comment — rather than being demonstrated against a
// fabricated action that doesn't exist.
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
