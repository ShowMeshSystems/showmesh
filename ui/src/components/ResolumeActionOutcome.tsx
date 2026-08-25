import type { ResolumeActionResult, ResolumeCompositionResponse } from '../app/types'
import { sanitizeResolumeValueString } from '../app/resolumeComposition'

// The Resolume sibling of FPPCommandOutcome.tsx (build contract §2.3),
// over ResolumeActionResult.outcome's own five-word vocabulary plus the
// accepted empty-string replay race, rendered as pending (ADR-003).
// `unconfirmable` renders as neither success nor failure (ADR-029).
export interface ResolumeActionOutcomeProps {
  result: ResolumeActionResult
  composition: ResolumeCompositionResponse | null
}

export function ResolumeActionOutcome({ result, composition }: ResolumeActionOutcomeProps) {
  // Review finding 3: outcomeReason is server-built and can embed a raw
  // Arena object id (deckSelectionRefusal's formatRef) — sanitize before
  // ever rendering it, matching every other Resolume string surface.
  const outcomeReason = sanitizeResolumeValueString(result.outcomeReason, composition)
  return (
    <div role="status" className="resolume-action-outcome">
      {result.replay && (
        <p className="text-muted">
          This was already requested (idempotency key already used); nothing new was
          dispatched; showing the original result.
        </p>
      )}
      {result.outcome === 'confirmed' && (
        <p className="resolume-action-outcome__confirmed">Confirmed: {outcomeReason}</p>
      )}
      {result.outcome === 'unconfirmed' && (
        <p role="alert" className="resolume-action-outcome__unconfirmed">
          Unconfirmed: {outcomeReason}
        </p>
      )}
      {result.outcome === 'unconfirmable' && (
        <p className="resolume-action-outcome__unconfirmable">
          Unconfirmable, neither success nor failure: {outcomeReason}
        </p>
      )}
      {result.outcome === 'refused' && (
        <p role="alert" className="resolume-action-outcome__refused">
          Refused, nothing was dispatched to Resolume: {outcomeReason}
        </p>
      )}
      {result.outcome === 'failed' && (
        <p role="alert" className="resolume-action-outcome__failed">
          Failed: {outcomeReason}
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
