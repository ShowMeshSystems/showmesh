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
// The API's own page-size ceiling (api/openapi.yaml's `limit` parameter).
const MAX_LIMIT = 500
// GET /audit pages forward from the oldest retained entry and exposes no
// cursor on the wire, only an opaque `since` the caller supplies (store's
// ListAuditEntries: entries with id > since). Because retained ids are
// contiguous, a full page's own length is a valid next `since` offset, so
// this view walks forward from 0 in MAX_LIMIT-sized pages until a short
// page marks the end of retained history, then renders newest first.
// REQUEST_CAP bounds that walk so a very long retained history cannot hang
// this screen or hammer the coordinator with requests.
const REQUEST_CAP = 20

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; entries: AuditEntry[]; capped: boolean }

function outcomeLabel(entry: AuditEntry): string {
  if (entry.kind !== 'outcome') return '—'
  return entry.outcome === '' ? '(no evidence-bearing outcome recorded)' : entry.outcome
}

export function Audit() {
  const model = useModelContext()
  const scopeGate = evaluateScope(model.session, model.sessionFetchFailed, AUDIT_READ_SCOPE)
  const [state, setState] = useState<LoadState>({ kind: 'loading' })

  useEffect(() => {
    if (!scopeGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })

    async function walkToEnd(): Promise<{ entries: AuditEntry[]; capped: boolean }> {
      const collected: AuditEntry[] = []
      let since = 0
      let requestCount = 0
      let capped = false
      // Walk oldest-first pages forward until a short response marks the
      // end of retained history, or REQUEST_CAP is reached first.
      for (;;) {
        requestCount += 1
        const resp = await listAudit({ since, limit: MAX_LIMIT })
        collected.push(...resp.entries)
        if (resp.entries.length < MAX_LIMIT) break
        since += resp.entries.length
        if (requestCount >= REQUEST_CAP) {
          capped = true
          break
        }
      }
      return { entries: collected, capped }
    }

    walkToEnd()
      .then(({ entries, capped }) => {
        if (cancelled) return
        setState({ kind: 'loaded', entries, capped })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [scopeGate.allowed])

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

      {scopeGate.allowed && state.kind === 'loading' && (
        <p className="text-muted">Loading the audit log, most recent first…</p>
      )}
      {scopeGate.allowed && state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          {state.message}
        </p>
      )}
      {scopeGate.allowed && state.kind === 'loaded' && (
        <>
          {state.capped && (
            <p className="panel panel--error" role="status">
              Stopped after {REQUEST_CAP} requests: showing the oldest {state.entries.length} entries
              fetched, not the most recent activity.
            </p>
          )}
          {state.entries.length === 0 ? (
            <p className="text-muted">No audit entries retained.</p>
          ) : (
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
                  {/* Newest first: entries arrive oldest-first off the wire
                      (ascending by an id GET /audit never exposes), reversed
                      here purely for display. */}
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
        </>
      )}
    </div>
  )
}
