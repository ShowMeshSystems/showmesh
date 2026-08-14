import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { listConfigObjects } from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { RunMacroButton } from '../components/RunMacroButton'
import { StatusBadge } from '../components/StatusBadge'
import type { ConfigObjectSummary, MacroRunSummary } from '../app/types'

// Deliverable 1 of this wave (STEP-9-SPEC.md section 9 / section 5.5):
// the macro list. "Reads require show:macro:run OR config:write — an
// operator-role principal holds the former and NOT the latter, and this
// list must render for them. A list that renders empty or 403 for the
// role the actual operator signs in as is the defect this surface
// exists to avoid." So this view's fetch is gated by [evaluateAnyScope],
// never by config:write alone the way Configuration.tsx (an admin-only
// surface) is gated — that would be exactly the mistake the specification
// names by name.
const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE = 'config:write'
const RUN_SCOPE = 'show:macro:run'
const RUN_GATE_REASON_ID = 'macros-run-gate-reason'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; objects: ConfigObjectSummary[] }

/**
 * This task's finding 1, the highest-priority one: `model.macroRuns` (the
 * live snapshot-plus-change-stream window ADR-020 decision 3 requires —
 * store.ts's applySnapshot/applyMacroRunChanged) had NO consumer anywhere
 * in the UI before this fix. The concrete failure it names: the FPP
 * plugin fires the show-start macro at 17:00; an operator with this list
 * open sees nothing running, presses Run, and only THEN learns — from a
 * 409 — that a run already exists. This is the fix: whether a macro is
 * currently running is read straight off the live model, so it updates
 * from the snapshot on first load AND from every `macroRun.changed`
 * frame afterward, with no fetch of its own. Returns the most recently
 * started running run, if more than one is somehow present (should not
 * happen given the run-overlap guard, but this must not crash or pick
 * arbitrarily if it does).
 */
function runningRunFor(macroId: string, macroRuns: MacroRunSummary[]): MacroRunSummary | undefined {
  return macroRuns
    .filter((r) => r.macroObjectId === macroId && r.state === 'running')
    .sort((a, b) => (a.createdAt < b.createdAt ? 1 : -1))[0]
}

export function Macros() {
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)
  // Computed once per render, reused by the "running now" panel below AND
  // by every row's Status cell, rather than each row re-deriving it —
  // see runningRunFor's own comment.
  const runGate = evaluateScope(model.session, model.sessionFetchFailed, RUN_SCOPE)

  const [state, setState] = useState<LoadState>({ kind: 'loading' })

  useEffect(() => {
    if (!readGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    listConfigObjects('show.macro')
      .then((resp) => {
        if (cancelled) return
        setState({ kind: 'loaded', objects: resp.objects })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [readGate.allowed])

  const runningRuns = model.macroRuns.filter((r) => r.state === 'running')

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', flexWrap: 'wrap', gap: '0.75rem' }}>
        <h2 className="panel__title">Show macros</h2>
        {/* This task's finding 9: a nav link to an authoring page used to
            be HIDDEN outright for a principal without config:write, the
            one place in this app that deviated from the standing rule
            (OPERATOR-UI section 14 / ADR-024 decision 12) that a control
            the principal may not use is rendered disabled with a stated
            reason rather than hidden. An absent control reads as "this
            capability does not exist," not "you may not use it" — the
            same distinction ScopedButton exists to make for every write
            button in this app. Made consistent rather than kept as a
            special case: a real link when permitted, a disabled,
            reason-carrying control when not. */}
        {writeGate.allowed ? (
          <Link className="entity-link" to="/macros/new">
            New macro
          </Link>
        ) : (
          // Mirrors ScopedButton's own disabled rendering exactly (same
          // classes, same shape) rather than inventing a second "disabled
          // control" style for a nav link — same reasoning as ScopedButton
          // itself.
          <span className="scoped-button">
            <button type="button" disabled aria-disabled="true" title={writeGate.reason}>
              New macro
            </button>
            <span className="scoped-button__reason">{writeGate.reason}</span>
          </span>
        )}
      </div>

      {!readGate.allowed && (
        <p className="panel panel--error" role="status">
          {readGate.reason}
        </p>
      )}

      {/* This task's finding 1, the highest-priority one: nothing in the
          UI read `model.macroRuns` before this fix, so an in-flight run
          — including one the FPP plugin fired with no browser open at
          the time — was invisible until the operator pressed Run and hit
          the overlap 409 (STEP-9-SPEC.md section 6.6's own failure
          story). Sourced from the live model, so it updates from the
          snapshot on first load and from every `macroRun.changed` frame
          afterward with no fetch of its own — a client connecting for
          the first time during an in-flight run sees it here
          immediately. */}
      {readGate.allowed && runningRuns.length > 0 && (
        <div className="panel" role="status">
          <h3 className="panel__title">Running now</h3>
          <ul className="list-plain">
            {runningRuns.map((run) => (
              <li key={run.id}>
                <Link
                  className="entity-link"
                  to={`/macros/${encodeURIComponent(run.macroObjectId)}/runs/${encodeURIComponent(run.id)}`}
                >
                  {run.macroObjectId} — started {formatAbsolute(run.createdAt)} by {run.issuerPrincipalName} (
                  {run.trigger})
                </Link>
              </li>
            ))}
          </ul>
        </div>
      )}

      {readGate.allowed && state.kind === 'loading' && <p className="text-muted">Loading macros…</p>}
      {readGate.allowed && state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          {state.message}
        </p>
      )}
      {readGate.allowed && state.kind === 'loaded' && (
        <>
          {state.objects.length === 0 ? (
            <p className="text-muted">No show macros are configured yet.</p>
          ) : (
            <>
              {/* This task's finding 11: a five-column table with a run
                  button in the last cell had no horizontal-overflow
                  wrapper, on a surface OPERATOR-UI section 13 makes a
                  show-time phone view. */}
              <div className="table-scroll">
                <table className="config-table">
                  <thead>
                    <tr>
                      <th>Label</th>
                      <th>Show</th>
                      <th>Revision</th>
                      <th>Updated</th>
                      <th>Status</th>
                      <th aria-label="Run" />
                    </tr>
                  </thead>
                  <tbody>
                    {state.objects.map((obj) => {
                      const running = runningRunFor(obj.id, model.macroRuns)
                      return (
                        <tr key={obj.id}>
                          <td>
                            <Link className="entity-link" to={`/macros/${encodeURIComponent(obj.id)}`}>
                              {obj.label}
                            </Link>
                          </td>
                          <td>{obj.show}</td>
                          <td>{obj.currentRevision}</td>
                          <td>{formatAbsolute(obj.updatedAt)}</td>
                          <td>
                            {running !== undefined ? (
                              <Link
                                to={`/macros/${encodeURIComponent(obj.id)}/runs/${encodeURIComponent(running.id)}`}
                              >
                                <StatusBadge tone="unknown" icon="▶" label="Running" />
                              </Link>
                            ) : (
                              <span className="text-muted">—</span>
                            )}
                          </td>
                          <td>
                            {/* This task's finding 11: RunMacroButton
                                (via ScopedButton) prints its OWN full
                                refusal sentence per row, which repeats
                                identical text once per macro when the
                                run scope is absent — a wall of paragraphs
                                on a table already tight for width. The
                                gate is evaluated ONCE above (`runGate`);
                                when it fails, every row instead shows a
                                compact disabled control that points at
                                ONE shared, visible explanation
                                (`RUN_GATE_REASON_ID`) via
                                aria-describedby, rather than hiding the
                                reason (still stated, per the standing
                                rule) or repeating it N times. */}
                            {runGate.allowed ? (
                              <RunMacroButton macroId={obj.id} />
                            ) : (
                              <button
                                type="button"
                                disabled
                                aria-disabled="true"
                                aria-describedby={RUN_GATE_REASON_ID}
                                title={runGate.reason}
                              >
                                Run
                              </button>
                            )}
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
              {!runGate.allowed && (
                <p id={RUN_GATE_REASON_ID} className="text-muted">
                  {runGate.reason}
                </p>
              )}
            </>
          )}
        </>
      )}

      <p className="text-muted" style={{ marginTop: '1rem' }}>
        <Link to="/actions">Show actions</Link> are what each macro step invokes.
      </p>
    </div>
  )
}
