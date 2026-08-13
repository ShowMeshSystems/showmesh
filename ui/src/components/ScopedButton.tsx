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
  /**
   * True while an in-flight submission of THIS action makes it temporarily
   * unavailable, independent of whether `requiredScope` is held — Step 7
   * seam A review defect 8's own fix. Kept as a SEPARATE reason from
   * "not permitted" rather than folded into it: ADR-024 decision 12
   * requires a disabled control to state why, and "you may not do this"
   * and "this is already in progress" are different facts an operator
   * needs told apart, the same way a `403` (forbidden) and a rate limit
   * are never presented as the same thing elsewhere in this codebase (see
   * ForbiddenError vs. TooManyRequestsError, api/errors.ts). Ignored while
   * the scope itself is not held — a control that is not permitted stays
   * not-permitted regardless of `busy`.
   */
  busy?: boolean
  /** Shown while `busy` is true. Defaults to a generic in-progress reason. */
  busyReason?: string
}

const DEFAULT_BUSY_REASON = 'This action is already in progress.'

export function ScopedButton({
  requiredScope,
  onClick,
  children,
  className,
  busy = false,
  busyReason,
}: ScopedButtonProps) {
  const model = useModelContext()
  const result = evaluateScope(model.session, model.sessionFetchFailed, requiredScope)

  if (result.allowed && !busy) {
    return (
      <button type="button" className={className} onClick={onClick}>
        {children}
      </button>
    )
  }

  // Two distinct disabled reasons collapse to the same markup shape
  // (ADR-024 decision 12: stated, visible, never a blank or a tooltip
  // only a mouse can reach — see ScopedButton.test.tsx's own comment on
  // this), but never the same TEXT: "not permitted" always wins when both
  // are somehow true (a control cannot be usefully "busy" doing something
  // it is not allowed to do), and `aria-busy` is set only for the busy
  // case so assistive tech does not describe a scope refusal as transient.
  const reason = result.allowed ? (busyReason ?? DEFAULT_BUSY_REASON) : result.reason
  const isBusy = result.allowed && busy
  return (
    <span className="scoped-button">
      <button
        type="button"
        className={className}
        disabled
        aria-disabled="true"
        aria-busy={isBusy ? 'true' : undefined}
        title={reason}
      >
        {children}
      </button>
      <span className="scoped-button__reason">{reason}</span>
    </span>
  )
}
