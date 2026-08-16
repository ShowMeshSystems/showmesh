import type { ResolumeActionResult } from '../app/types'

// The Resolume sibling of FPPCommandOutcome.tsx, reusing that component's
// own rules (build contract §2.3: "reuse FPPCommandOutcome's patterns
// rather than writing a second outcome renderer") over a DIFFERENT,
// five-word vocabulary (ResolumeActionResult.outcome) rather than FPP's
// two: "confirmed", "unconfirmed", "unconfirmable", "refused", "failed",
// and the one accepted empty-string replay race, rendered as pending —
// never silently treated as either (ADR-003).
//
// `unconfirmable` renders as neither success nor failure (ADR-029: "an
// action whose effect cannot be observed reports as unconfirmable ...
// never as success" — a step that always reports success is worse than no
// step, because the operator stops reading it). `refused` and `failed`
// are visually distinct from each other too: a refusal never reached
// Resolume at all (a clip's deck was not selected, an unresolvable
// reference, an audit-write fail-closed refusal), while a failure means
// dispatch was attempted and the attempt itself failed.
export interface ResolumeActionOutcomeProps {
  result: ResolumeActionResult
}

export function ResolumeActionOutcome({ result }: ResolumeActionOutcomeProps) {
  return (
    <div role="status" className="resolume-action-outcome">
      {result.replay && (
        <p className="text-muted">
          This was already requested (idempotency key already used) — nothing new was
          dispatched; showing the original result.
        </p>
      )}
      {result.outcome === 'confirmed' && (
        <p className="resolume-action-outcome__confirmed">Confirmed: {result.outcomeReason}</p>
      )}
      {result.outcome === 'unconfirmed' && (
        <p role="alert" className="resolume-action-outcome__unconfirmed">
          Unconfirmed: {result.outcomeReason}
        </p>
      )}
      {result.outcome === 'unconfirmable' && (
        <p className="resolume-action-outcome__unconfirmable">
          Unconfirmable — neither success nor failure: {result.outcomeReason}
        </p>
      )}
      {result.outcome === 'refused' && (
        <p role="alert" className="resolume-action-outcome__refused">
          Refused — nothing was dispatched to Resolume: {result.outcomeReason}
        </p>
      )}
      {result.outcome === 'failed' && (
        <p role="alert" className="resolume-action-outcome__failed">
          Failed: {result.outcomeReason}
        </p>
      )}
      {result.outcome === '' && (
        <p className="text-muted">Pending: this action has not yet resolved.</p>
      )}
      <dl className="field-list">
        <dt>Selected deck changed during confirmation</dt>
        {/* selectedDeckChanged is boolean | null -- null renders as "not
            known", never as "no" (build contract §2.3, and this project's
            standing rule that null is not false). Meaningful only for a
            confirmed launchClip; every other action carries null here,
            which is also rendered as "not known" rather than
            special-cased, since this field's own contract makes no
            distinction between "not applicable" and "could not be read". */}
        <dd>
          {result.selectedDeckChanged === null
            ? 'not known'
            : result.selectedDeckChanged
              ? 'yes'
              : 'no'}
        </dd>
        {result.resolvedId !== undefined && (
          <>
            {/* ADR-037 removes the id from what an operator TYPES, not
                from the record -- kept visible here for debugging. */}
            <dt>Resolved to</dt>
            <dd>{result.resolvedId}</dd>
          </>
        )}
      </dl>
      {result.attributionDegraded && (
        <p className="text-muted">
          Note: the coordinator could not record this action in its audit log; it ran
          anyway.
        </p>
      )}
    </div>
  )
}
