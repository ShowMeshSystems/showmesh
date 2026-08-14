import { useEffect, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { getMacroRun } from '../api'
import { describeApiError, evaluateAnyScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { MacroRunOutcome } from '../components/MacroRunOutcome'
import { MacroRunStepRow } from '../components/MacroRunStepRow'
import type { MacroRun } from '../app/types'

// Deliverable 3 of this wave (STEP-9-SPEC.md section 6.6): "the run and
// its steps: per-step state, outcome, reason, and attributionDegraded."
// Read scope matches Macros.tsx/ShowActions.tsx (section 5.5/6.6: "Reads
// on runs require show:macro:run OR config:write").
const READ_SCOPES = ['show:macro:run', 'config:write']

/**
 * UNMEASURED SHOWMESH HYPOTHESIS: how often this view re-fetches a
 * RUNNING run's step detail while the operator has it open. Step
 * detail is deliberately fetched, not streamed (section 6.6: "a run
 * with 32 steps must not put 32 events on a stream every client
 * receives"), so THIS view is what has to go get it — the model's own
 * `macroRuns` (store.ts) only ever carries the run-level summary a
 * `macroRun.changed` frame updates, never steps. Nothing has measured a
 * real macro's per-step timing against this interval; it exists so an
 * operator watching a run in progress sees it move without needing to
 * press a button, and stops entirely once the run's own `state` is
 * "finished" (the effect below tears its interval down at that point,
 * not on a fixed number of polls).
 */
const RUNNING_POLL_INTERVAL_MS = 3_000

type LoadState = { kind: 'loading' } | { kind: 'error'; message: string } | { kind: 'loaded'; run: MacroRun }

export function MacroRunView() {
  const params = useParams<{ id: string; runId: string }>()
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const runId = params.runId
  const macroId = params.id

  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const stateRef = useRef(state)
  stateRef.current = state

  async function load(): Promise<void> {
    if (runId === undefined) return
    try {
      const resp = await getMacroRun(runId)
      setState({ kind: 'loaded', run: resp.run })
    } catch (err) {
      setState({ kind: 'error', message: describeApiError(err) })
    }
  }

  useEffect(() => {
    if (!readGate.allowed || runId === undefined) return
    setState({ kind: 'loading' })
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps -- runId/readGate.allowed are the only inputs this initial fetch cares about.
  }, [runId, readGate.allowed])

  useEffect(() => {
    if (!readGate.allowed || runId === undefined) return
    const interval = setInterval(() => {
      // Stop polling once this run has finished — a finished run's steps
      // do not change again, so continuing to poll would only cost
      // requests for no new information.
      if (stateRef.current.kind === 'loaded' && stateRef.current.run.state === 'finished') {
        clearInterval(interval)
        return
      }
      void load()
    }, RUNNING_POLL_INTERVAL_MS)
    return () => clearInterval(interval)
    // eslint-disable-next-line react-hooks/exhaustive-deps -- runId/readGate.allowed are the only inputs; `load` closes over the current runId via the outer scope and is stable per render of those.
  }, [runId, readGate.allowed])

  if (!readGate.allowed) {
    return (
      <div>
        <h2 className="panel__title">Macro run</h2>
        <p className="panel panel--error" role="status">
          {readGate.reason}
        </p>
      </div>
    )
  }

  if (state.kind === 'loading') {
    return <p className="text-muted">Loading run…</p>
  }
  if (state.kind === 'error') {
    return (
      <p className="panel panel--error" role="alert">
        {state.message}
      </p>
    )
  }

  const run = state.run

  return (
    <div>
      <h2 className="panel__title">
        Run of{' '}
        <Link to={`/macros/${encodeURIComponent(run.macroObjectId)}`}>{run.macroObjectId}</Link>
        {' '}(revision {run.macroRevision})
      </h2>
      <p className="text-muted">
        Started {formatAbsolute(run.createdAt)} by {run.issuerPrincipalName} ({run.trigger})
        {run.finishedAt !== null && <> — finished {formatAbsolute(run.finishedAt)}</>}
      </p>

      <MacroRunOutcome run={run} />

      {macroId !== undefined && macroId !== run.macroObjectId && (
        <p className="text-muted" role="status">
          This run belongs to macro {run.macroObjectId}, not {macroId}.
        </p>
      )}

      <h3 className="panel__title">Steps</h3>
      {run.steps.length === 0 ? (
        <p className="text-muted">This run has no steps recorded.</p>
      ) : (
        <ol className="list-plain macro-run-step-list">
          {run.steps.map((step) => (
            <MacroRunStepRow key={step.stepIndex} step={step} />
          ))}
        </ol>
      )}
    </div>
  )
}
