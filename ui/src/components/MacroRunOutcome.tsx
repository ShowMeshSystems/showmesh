import { StatusBadge } from './StatusBadge'
import type { MacroRunSummary } from '../app/types'

// The single most important visual requirement this wave was briefed
// on: `completed` and `confirmed` are two separate facts about a run and
// must never collapse into one green tick. A macro whose MQTT step
// declares no expected response is structurally unconfirmable and
// reports completed: true, confirmed: false EVERY TIME IT RUNS
// CORRECTLY — if this component rendered one badge for both, an
// operator would learn that the second badge is always red/amber for
// that macro and stop reading it, which is exactly the outcome ADR-029
// decision 4 exists to prevent. So this renders two INDEPENDENT
// StatusBadge instances, never one derived from the other, and prints
// the run's own `reason` (server-stated, never invented here) whenever
// either is false — matching FPPCommandOutcome's own "the server's own
// words win" rule for outcomeReason.
export interface MacroRunOutcomeProps {
  run: Pick<MacroRunSummary, 'state' | 'completed' | 'confirmed' | 'reason' | 'attributionDegraded'>
}

export function MacroRunOutcome({ run }: MacroRunOutcomeProps) {
  return (
    <div className="macro-run-outcome" role="status">
      <div className="macro-run-outcome__facts">
        <CompletedBadge completed={run.completed} state={run.state} />
        <ConfirmedBadge confirmed={run.confirmed} state={run.state} />
      </div>
      {/* completed and confirmed can each be false for a DIFFERENT
          reason (a step that failed vs. a step that could not be
          confirmed); this run carries exactly one `reason` naming
          whichever cause applies (STEP-9-SPEC.md sections 2.2/2.3), so
          it is shown once here rather than duplicated under each badge. */}
      {run.reason !== '' && <p className="macro-run-outcome__reason">{run.reason}</p>}
      {run.attributionDegraded && (
        <p className="text-muted">
          Note: at least one step in this run could not be recorded in the coordinator&rsquo;s
          audit log; it ran anyway.
        </p>
      )}
    </div>
  )
}

function CompletedBadge({
  completed,
  state,
}: {
  completed: boolean | null
  state: MacroRunSummary['state']
}) {
  if (completed === null || state === 'running') {
    return <StatusBadge tone="unknown" icon="…" label="Run in progress" />
  }
  if (completed) {
    return <StatusBadge tone="good" icon="✓" label="Completed" />
  }
  return <StatusBadge tone="bad" icon="✕" label="Did not complete" />
}

function ConfirmedBadge({
  confirmed,
  state,
}: {
  confirmed: boolean | null
  state: MacroRunSummary['state']
}) {
  if (confirmed === null || state === 'running') {
    return <StatusBadge tone="unknown" icon="…" label="Confirmation pending" />
  }
  if (confirmed) {
    return <StatusBadge tone="good" icon="✓" label="Confirmed" />
  }
  // Deliberately "warn", never "bad": an unconfirmed run is not
  // necessarily a failed one — a run can legitimately, correctly,
  // repeatedly report confirmed: false because one of its steps declares
  // no observable response (MacroRunStep's own "unconfirmable" outcome).
  // Painting this the same red as a genuine failure would re-create the
  // exact "operator stops reading it" defect this component exists to
  // avoid, one color away.
  return <StatusBadge tone="warn" icon="?" label="Not confirmed" />
}
