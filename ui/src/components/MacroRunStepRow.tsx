import { useState } from 'react'
import { StatusBadge, type StatusTone } from './StatusBadge'
import { MacroRunCommandDetail } from './MacroRunCommandDetail'
import { formatAbsolute } from '../app/time'
import type { MacroRunStep } from '../app/types'

// One row of a run's step list (STEP-9-SPEC.md section 6.4's five-value
// step outcome vocabulary: confirmed | unconfirmed | unconfirmable |
// failed | skipped, plus `null` while a step has not yet resolved).
// `outcome` IS a closed enum (plus `null`) in the generated schema
// (api/openapi.yaml's own MacroRunStep.outcome). The switch below still
// falls through to a generic, still-visible rendering for anything
// outside that closed set, but not because the wire type is open today: a
// value that reaches this component without matching what TypeScript
// checked at compile time (a malformed response, or a value from a
// future, additive-only API major version this build predates) must
// never render blank, matching OPERATOR-UI section 9's standing
// "unrecognized value degrades to a generic panel" rule — that is a
// defense against a value escaping the type system, not evidence the
// type system does not close this set.
//
// step.state is a closed, three-member vocabulary — "pending", "resolved",
// "skipped" — with no "dispatched" intermediate: the run executor always
// resolves a step to a terminal outcome before it is ever written, so a
// value this producer cannot emit is not published here (a prior
// "dispatched" state was removed from the schema for exactly that
// reason). outcome is null exactly while state is "pending", so the one
// lifecycle distinction worth rendering — has this step run yet — is
// already carried by outcome being null and needs no separate branch on
// step.state.
export interface MacroRunStepRowProps {
  step: MacroRunStep
}

function outcomeBadge(outcome: MacroRunStep['outcome']): { tone: StatusTone; icon: string; label: string } {
  switch (outcome) {
    case 'confirmed':
      return { tone: 'good', icon: '✓', label: 'Confirmed' }
    case 'unconfirmed':
      // Amber, not red — see MacroRunOutcome's own comment: an
      // unconfirmed step is a statement about evidence, not a statement
      // that the show did the wrong thing.
      return { tone: 'warn', icon: '?', label: 'Unconfirmed' }
    case 'unconfirmable':
      return { tone: 'warn', icon: '-', label: 'Unconfirmable (no response was ever expected)' }
    case 'failed':
      return { tone: 'bad', icon: '✕', label: 'Failed' }
    case 'skipped':
      return { tone: 'unknown', icon: '⏭', label: 'Skipped' }
    case null:
      return { tone: 'unknown', icon: '…', label: 'Pending' }
    default:
      return { tone: 'unknown', icon: '?', label: outcome }
  }
}

export function MacroRunStepRow({ step }: MacroRunStepRowProps) {
  const [expanded, setExpanded] = useState(false)
  const badge = outcomeBadge(step.outcome)

  return (
    <li className="macro-run-step">
      <div className="macro-run-step__summary">
        <span className="macro-run-step__index">{step.stepIndex + 1}</span>
        <strong>{step.stepId}</strong>
        <span className="text-muted">{step.actionObjectId}</span>
        <StatusBadge tone={badge.tone} icon={badge.icon} label={badge.label} />
        {step.attributionDegraded && (
          <StatusBadge tone="warn" icon="!" label="Audit not recorded" />
        )}
        <button
          type="button"
          className="icon-button"
          aria-expanded={expanded}
          onClick={() => setExpanded((e) => !e)}
        >
          {expanded ? 'Hide detail' : 'Show detail'}
        </button>
      </div>
      {/* outcomeReason is server-stated, never invented here, and shown
          whenever the outcome is anything other than a clean confirm —
          matching FPPCommandOutcome's own "the server's own words win"
          convention for the identical field name at a different layer. */}
      {step.outcomeReason !== '' && step.outcome !== 'confirmed' && (
        <p className="macro-run-outcome__reason">{step.outcomeReason}</p>
      )}
      {expanded && (
        <div className="macro-run-step__detail">
          <dl className="macro-run-step__facts">
            <dt>Integration</dt>
            <dd>{step.integration}</dd>
            <dt>Safety class</dt>
            <dd>{step.safetyClass}</dd>
            <dt>Local fallback if the coordinator refuses or is unreachable</dt>
            <dd>{describeLocalFallback(step.localFallbackClass)}</dd>
            <dt>Action revision</dt>
            <dd>{step.actionRevision}</dd>
            {/* This task's finding 7: dispatchedAt/resolvedAt were in hand
                on MacroRunStep and never rendered anywhere, so an operator
                had no timing evidence for a step beyond its current
                badge. */}
            <dt>Dispatched at</dt>
            <dd>{step.dispatchedAt !== null ? formatAbsolute(step.dispatchedAt) : 'Not yet dispatched.'}</dd>
            <dt>Resolved at</dt>
            <dd>{step.resolvedAt !== null ? formatAbsolute(step.resolvedAt) : 'Not yet resolved.'}</dd>
          </dl>
          <MacroRunCommandDetail command={step.command} integration={step.integration} state={step.state} />
        </div>
      )}
    </li>
  )
}

function describeLocalFallback(cls: MacroRunStep['localFallbackClass']): string {
  switch (cls) {
    case 'none':
      return 'Nothing runs locally.'
    case 'coordinator-required':
      return 'The coordinator dispatches this step; it cannot run without one.'
    case 'silence':
      return 'The reduced local behavior is deliberately nothing (silence), not a handover.'
    default:
      return cls
  }
}
