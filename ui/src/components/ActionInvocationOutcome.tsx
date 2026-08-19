import type { ActionInvocationResult } from '../app/types'

// The action-invoke sibling of ResolumeActionOutcome, over
// ActionInvocationResult.outcome's own five-word vocabulary. outcome is
// null exactly when state is "pending" (SM-100) — never a blank string
// pretending to be a real outcome. "unconfirmable" renders with its own
// distinct word — never merged into "confirmed" — so an operator can
// tell the two apart at a glance (ADR-029 decision 4: an operator who
// cannot stops reading them).
export interface ActionInvocationOutcomeProps {
  result: ActionInvocationResult
}

// The complete wire vocabulary this component renders explicitly — any
// other value falls through to the fallback branch below rather than
// rendering nothing, since a blank reads as fine (CLAUDE.md's own
// standing rule) and a future sixth outcome word must never go silent.
const KNOWN_OUTCOMES = new Set(['confirmed', 'unconfirmed', 'unconfirmable', 'refused', 'failed'])

export function ActionInvocationOutcome({ result }: ActionInvocationOutcomeProps) {
  return (
    <div role="status" className="action-invocation-outcome">
      {result.replay && (
        <p className="text-muted">
          This was already requested (idempotency key already used) — nothing new was
          dispatched; showing the original result.
        </p>
      )}
      {result.outcome === 'confirmed' && (
        <p className="action-invocation-outcome__confirmed">Confirmed: {result.outcomeReason}</p>
      )}
      {result.outcome === 'unconfirmed' && (
        <p role="alert" className="action-invocation-outcome__unconfirmed">
          Unconfirmed: {result.outcomeReason}
        </p>
      )}
      {result.outcome === 'unconfirmable' && (
        <p className="action-invocation-outcome__unconfirmable">
          Unconfirmable — neither success nor failure: {result.outcomeReason}
        </p>
      )}
      {result.outcome === 'refused' && (
        <p role="alert" className="action-invocation-outcome__refused">
          Refused — nothing was dispatched: {result.outcomeReason}
        </p>
      )}
      {result.outcome === 'failed' && (
        <p role="alert" className="action-invocation-outcome__failed">
          Failed: {result.outcomeReason}
        </p>
      )}
      {result.outcome === null && (
        <p className="text-muted">Pending: {result.outcomeReason}</p>
      )}
      {result.outcome !== null && !KNOWN_OUTCOMES.has(result.outcome) && (
        <p role="alert" className="action-invocation-outcome__unrecognized">
          Unrecognized outcome {JSON.stringify(result.outcome)}: {result.outcomeReason || '(no reason given)'}
        </p>
      )}
      {result.attributionDegraded && (
        <p className="text-muted">
          Note: the coordinator could not record this invocation in its audit log; it ran
          anyway.
        </p>
      )}
    </div>
  )
}
