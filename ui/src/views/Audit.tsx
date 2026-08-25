import { useEffect, useState } from 'react'
import { listAudit } from '../api'
import { describeApiError, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import type { AuditEntry } from '../app/types'

// Track G seam G-8: the audit log (ADR-024 decision 11), behind the
// audit:read scope always — GET /audit is never one of the open-by-default
// reads (api/openapi.yaml's own description), so this page (unlike every
// other Class 3 view) is gated on a single scope rather than
// evaluateAnyScope.
const AUDIT_READ_SCOPE = 'audit:read'
const PAGE_SIZE = 100
const MAX_LIMIT = 500

type LoadState = { kind: 'loading' } | { kind: 'error'; message: string } | { kind: 'loaded'; entries: AuditEntry[] }

function outcomeLabel(entry: AuditEntry): string {
  if (entry.kind !== 'outcome') return '—'
  return entry.outcome === '' ? '(no evidence-bearing outcome recorded)' : entry.outcome
}

export function Audit() {
  const model = useModelContext()
  const scopeGate = evaluateScope(model.session, model.sessionFetchFailed, AUDIT_READ_SCOPE)
  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [limit, setLimit] = useState(PAGE_SIZE)

  useEffect(() => {
    if (!scopeGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    // Oldest-first, ascending by an internal id GET /audit does not expose
    // on the wire (store.ListAuditEntries's own doc comment) — so `since`
    // cannot be reconstructed from a page of results, only from 0. This
    // view widens `limit` (capped at the API's own 500-entry maximum,
    // api/openapi.yaml's `limit` parameter) rather than pretending to page
    // past it; see this app's build report for why that cap is a real,
    // reported API gap rather than a UI shortcut.
    listAudit({ since: 0, limit })
      .then((resp) => {
        if (cancelled) return
        setState({ kind: 'loaded', entries: resp.entries })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [scopeGate.allowed, limit])

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
          {/* ADR-020: absent evidence is stated, never omitted. GET /audit
              pages from the OLDEST entry, so a full window is the oldest
              window the API exposes and the most recent activity may lie
              beyond it — said out loud rather than presenting an old
              window as the audit log. */}
          {state.entries.length === limit && (
            <p className="panel panel--error" role="status">
              This window is full: it holds the <strong>oldest</strong> {limit} retained entries the
              API exposes, and newer entries beyond this window may exist and are not shown,
              including the most recent activity.
              {limit < MAX_LIMIT
                ? ' Use "Show more" to widen the window.'
                : ` ${MAX_LIMIT} is the API's own maximum window; it cannot page further.`}
            </p>
          )}
          {state.entries.length === 0 ? (
            <p className="text-muted">No audit entries retained.</p>
          ) : (
            <div className="table-scroll">
              <p className="text-muted">Entries are shown latest-first within this fetched window.</p>
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
                  {/* Latest-of-this-window first for readability — the wire
                      order (store's own doc comment: ascending by an id
                      this response never exposes) is oldest-first, reversed
                      here purely for display. NOT labeled "newest first":
                      when the window is full, its last entry is only the
                      newest of the oldest window, not the newest retained. */}
                  {[...state.entries].reverse().map((entry, index) => (
                    <tr key={`${entry.timestamp}-${index}`}>
                      <td>{formatAbsolute(entry.timestamp)}</td>
                      <td>
                        {entry.principalName} ({entry.form})
                      </td>
                      <td>{entry.kind}</td>
                      <td>{entry.action}</td>
                      <td>{entry.target}</td>
                      <td>{outcomeLabel(entry)}</td>
                      <td>{entry.outcomeState === '' ? '—' : entry.outcomeState}</td>
                      <td>{entry.outcomeReason === '' ? '—' : entry.outcomeReason}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {state.entries.length === limit && limit < MAX_LIMIT && (
            <button type="button" onClick={() => setLimit((l) => Math.min(l + PAGE_SIZE, MAX_LIMIT))}>
              Show more
            </button>
          )}
        </>
      )}
    </div>
  )
}
