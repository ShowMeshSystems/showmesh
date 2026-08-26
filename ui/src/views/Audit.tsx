import { useCallback, useEffect, useState } from 'react'
import { listAudit } from '../api'
import { describeApiError, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import type { AuditEntry } from '../app/types'

// Track G seam G-8: the audit log (ADR-024 decision 11), behind the
// audit:read scope always: GET /audit is never one of the open-by-default
// reads (api/openapi.yaml's own description), so this page (unlike every
// other Class 3 view) is gated on a single scope rather than
// evaluateAnyScope.
const AUDIT_READ_SCOPE = 'audit:read'
const PAGE_SIZE = 100

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; entries: AuditEntry[]; oldestRetainedId: number | null; atBeginning: boolean }

// olderState is deliberately separate from LoadState: a failure while
// paging further back must not discard the entries already on screen, and
// must stay distinguishable from "there is nothing older".
type OlderState = { kind: 'idle' } | { kind: 'loading' } | { kind: 'error'; message: string }

function outcomeLabel(entry: AuditEntry): string {
  if (entry.kind !== 'outcome') return '-'
  return entry.outcome === '' ? '(no evidence-bearing outcome recorded)' : entry.outcome
}

// atBeginningOfHistory is true only when the coordinator's own
// oldestRetainedId says so, or when a backward page came back empty. A
// short page alone never proves it: retention can trim below the cursor
// between two requests.
function atBeginningOfHistory(entries: AuditEntry[], oldestRetainedId: number | null): boolean {
  const oldestOnScreen = entries.at(-1)
  if (oldestOnScreen === undefined) return true
  if (oldestRetainedId === null) return false
  return oldestOnScreen.id <= oldestRetainedId
}

export function Audit() {
  const model = useModelContext()
  const scopeGate = evaluateScope(model.session, model.sessionFetchFailed, AUDIT_READ_SCOPE)
  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [older, setOlder] = useState<OlderState>({ kind: 'idle' })

  useEffect(() => {
    if (!scopeGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    setOlder({ kind: 'idle' })
    // One request, newest first (GET /audit's own `order=desc`), so this
    // screen opens on what just happened instead of walking retained
    // history forward to reach it.
    listAudit({ order: 'desc', limit: PAGE_SIZE })
      .then((resp) => {
        if (cancelled) return
        const entries = resp.entries
        setState({
          kind: 'loaded',
          entries,
          oldestRetainedId: resp.oldestRetainedId,
          atBeginning: atBeginningOfHistory(entries, resp.oldestRetainedId),
        })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [scopeGate.allowed])

  // loadOlder pages BACKWARD on the last entry's own id (never on a count
  // of entries: ids are never reused and retention prunes from the oldest
  // end, so a count would re-read the same page indefinitely).
  const loadOlder = useCallback(() => {
    if (state.kind !== 'loaded') return
    const oldestOnScreen = state.entries.at(-1)
    if (oldestOnScreen === undefined) return
    const before = oldestOnScreen.id
    setOlder({ kind: 'loading' })
    listAudit({ order: 'desc', before, limit: PAGE_SIZE })
      .then((resp) => {
        setOlder({ kind: 'idle' })
        setState((prev) => {
          if (prev.kind !== 'loaded') return prev
          const entries = [...prev.entries, ...resp.entries]
          return {
            kind: 'loaded',
            entries,
            oldestRetainedId: resp.oldestRetainedId,
            atBeginning: resp.entries.length === 0 || atBeginningOfHistory(entries, resp.oldestRetainedId),
          }
        })
      })
      .catch((err: unknown) => {
        setOlder({ kind: 'error', message: describeApiError(err) })
      })
  }, [state])

  return (
    <div>
      <h2 className="panel__title">Audit log</h2>
      {/* ADR-024 decision 11. */}
      <p className="text-muted">
        Append-only record of every authenticated write and access-control decision. Requires the{' '}
        <code>audit:read</code> scope.
      </p>

      {!scopeGate.allowed && (
        <p className="panel panel--error" role="status">
          {scopeGate.reason}
        </p>
      )}

      {scopeGate.allowed && state.kind === 'loading' && <p className="text-muted">Loading the audit log…</p>}
      {scopeGate.allowed && state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          {state.message}
        </p>
      )}
      {scopeGate.allowed && state.kind === 'loaded' && (
        <>
          {state.entries.length === 0 ? (
            <p className="text-muted">No audit entries retained.</p>
          ) : (
            <>
              {/* ADR-020: absent evidence is stated, never omitted. This
                  window is bounded, so it says exactly what is on it and
                  whether anything older exists. */}
              <p className="text-muted">
                Showing the <strong>{state.entries.length}</strong> most recent retained{' '}
                {state.entries.length === 1 ? 'entry' : 'entries'}, newest first.
                {state.atBeginning
                  ? ' This is the beginning of retained history: there is nothing older to load.'
                  : ' Older entries exist beyond this window and are not shown.'}
                {state.oldestRetainedId !== null &&
                  ` The oldest entry still retained has id ${state.oldestRetainedId}.`}
              </p>
              <div className="table-scroll">
                <table className="config-table">
                  <thead>
                    <tr>
                      <th>Time</th>
                      <th>Principal</th>
                      <th>Kind</th>
                      <th>Action</th>
                      <th>Target</th>
                      <th>Outcome</th>
                      <th>Evidence state</th>
                      <th>Reason</th>
                    </tr>
                  </thead>
                  <tbody>
                    {state.entries.map((entry) => (
                      <tr key={entry.id}>
                        <td>{formatAbsolute(entry.timestamp)}</td>
                        <td>
                          {entry.principalName} ({entry.form})
                        </td>
                        <td>{entry.kind}</td>
                        <td>{entry.action}</td>
                        <td>{entry.target}</td>
                        <td>{outcomeLabel(entry)}</td>
                        <td>{entry.outcomeState === '' ? '-' : entry.outcomeState}</td>
                        <td>{entry.outcomeReason === '' ? '-' : entry.outcomeReason}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              {older.kind === 'error' && (
                <p className="panel panel--error" role="alert">
                  {older.message} The entries above are still what was loaded before this failure.
                </p>
              )}
              {!state.atBeginning && (
                <button type="button" onClick={loadOlder} disabled={older.kind === 'loading'}>
                  {older.kind === 'loading' ? 'Loading older entries…' : 'Show older entries'}
                </button>
              )}
            </>
          )}
        </>
      )}
    </div>
  )
}
