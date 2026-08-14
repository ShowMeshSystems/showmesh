import { useState } from 'react'
import { StatusBadge, type StatusTone } from './StatusBadge'
import { MacroRunCommandDetail } from './MacroRunCommandDetail'
import type { MacroRunStep } from '../app/types'

// One row of a run's step list (STEP-9-SPEC.md section 6.4's five-value
// step outcome vocabulary: confirmed | unconfirmed | unconfirmable |
// failed | skipped). `outcome` is `type: string` on the wire (not a
// closed enum — api/openapi.yaml's own MacroRunStep.outcome), so this
// switches on the five known literals and falls through to a generic,
// still-visible rendering for anything else, matching this codebase's
// standing "unrecognized value degrades to a generic panel, never
// blank" rule (OPERATOR-UI section 9) rather than assuming the five
// values are exhaustive forever.
export interface MacroRunStepRowProps {
  step: MacroRunStep
}

function outcomeBadge(outcome: string): { tone: StatusTone; icon: string; label: string } {
  switch (outcome) {
    case 'confirmed':
      return { tone: 'good', icon: '✓', label: 'Confirmed' }
    case 'unconfirmed':
      // Amber, not red — see MacroRunOutcome's own comment: an
      // unconfirmed step is a statement about evidence, not a statement
      // that the show did the wrong thing.
      return { tone: 'warn', icon: '?', label: 'Unconfirmed' }
    case 'unconfirmable':
      return { tone: 'warn', icon: '—', label: 'Unconfirmable (no response was ever expected)' }
    case 'failed':
      return { tone: 'bad', icon: '✕', label: 'Failed' }
    case 'skipped':
      return { tone: 'unknown', icon: '⏭', label: 'Skipped' }
    case '':
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
          </dl>
          <MacroRunCommandDetail command={step.command} />
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
