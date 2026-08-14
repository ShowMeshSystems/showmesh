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

// This task's finding 5: `loaded` used to be "run", full stop — a failed
// poll had nowhere to go but `error`, which REPLACED the entire rendered
// run (outcome badges, step list, everything) with a bare error line
// until the next tick happened to succeed. OPERATOR-UI section 7 is
// explicit that a connectivity problem must "retain last known state
// where it remains useful, show when that state was last updated,"
// never erase it — and during a live show, this exact view is what an
// operator is staring at when a poll blips. `loaded` now carries its own
// `pollError`: non-null means the MOST RECENT refresh attempt failed, but
// the `run`/`loadedAt` here are still the last one that succeeded, kept
// exactly as they were rather than discarded. `error` (no `run` at all)
// stays for the one case with nothing to retain: the very first fetch for
// this runId failed.
type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; run: MacroRun; loadedAt: number; pollError: string | null }

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
      setState({ kind: 'loaded', run: resp.run, loadedAt: Date.now(), pollError: null })
    } catch (err) {
      const message = describeApiError(err)
      // Functional update, not a closure over `state`: this can fire from
      // the polling interval below, arbitrarily long after this render.
      // A run already on screen (`loaded`) keeps it, with the failure
      // recorded alongside rather than instead of it; only a state with
      // NOTHING to retain (`loading`, or an earlier `error`) has nowhere
      // to go but `error`.
      setState((prev) => (prev.kind === 'loaded' ? { ...prev, pollError: message } : { kind: 'error', message }))
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

  const { run, loadedAt, pollError } = state

  return (
    <div>
      <h2 className="panel__title">
        Run of{' '}
        <Link to={`/macros/${encodeURIComponent(run.macroObjectId)}`}>{run.macroObjectId}</Link>
        {' '}(revision {run.macroRevision})
      </h2>

      {/* This task's finding 5: the failure signal is a banner, not a
          replacement — the run below it is always what this browser last
          successfully fetched, never blanked by a poll error alone. */}
      {pollError !== null && (
        <p className="panel panel--error" role="alert">
          Could not refresh this run just now: {pollError} What is shown below is the last known
          state, not necessarily current.
        </p>
      )}
      <p className="text-muted">Last updated {formatAbsolute(new Date(loadedAt).toISOString())}.</p>

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
