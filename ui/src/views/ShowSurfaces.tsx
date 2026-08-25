import { useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { listConfigObjects } from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import type { ConfigObjectSummary } from '../app/types'

// Track G seam G-8: the show.surface list, narrowable by show
// (?show=<id>, api/openapi.yaml's own `GET /config/show.surface`
// parameter) — mirrored here as a query param so the filter is
// shareable/bookmarkable, matching this app's existing URL-is-state
// posture elsewhere (route params on every other detail view).
const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE = 'config:write'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; objects: ConfigObjectSummary[] }

export function ShowSurfaces() {
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)
  const [searchParams, setSearchParams] = useSearchParams()
  const showFilter = searchParams.get('show') ?? ''

  const [state, setState] = useState<LoadState>({ kind: 'loading' })

  useEffect(() => {
    if (!readGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    listConfigObjects('show.surface', showFilter === '' ? undefined : showFilter)
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
  }, [readGate.allowed, showFilter])

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', flexWrap: 'wrap', gap: '0.75rem' }}>
        <h2 className="panel__title">Surfaces</h2>
        {writeGate.allowed ? (
          <Link className="entity-link" to="/config/show.surface/new">
            New surface
          </Link>
        ) : (
          <span className="scoped-button">
            <button type="button" disabled aria-disabled="true" title={writeGate.reason}>
              New surface
            </button>
            <span className="scoped-button__reason">{writeGate.reason}</span>
          </span>
        )}
      </div>
      {/* A surface owns a logical canvas, its virtual-matrix channel extraction, and its output
          (ADR-026); a manual channel range is a permanent, first-class configuration path, not a
          fallback (ADR-027 decision 4). */}
      <p className="text-muted">
        A surface owns one node&rsquo;s canvas, its virtual-matrix channel extraction, and its
        output. A manual channel range is a permanent, first-class way to configure one, not a
        fallback.
      </p>

      <label className="form-field" style={{ maxWidth: '20rem' }}>
        Narrow by show
        <input
          type="text"
          placeholder="show id, or leave blank for every show"
          value={showFilter}
          onChange={(e) => {
            const value = e.target.value
            setSearchParams(value === '' ? {} : { show: value })
          }}
        />
      </label>

      {!readGate.allowed && (
        <p className="panel panel--error" role="status">
          {readGate.reason}
        </p>
      )}

      {readGate.allowed && state.kind === 'loading' && <p className="text-muted">Loading surfaces…</p>}
      {readGate.allowed && state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          {state.message}
        </p>
      )}
      {readGate.allowed && state.kind === 'loaded' && (
        <>
          {state.objects.length === 0 ? (
            <p className="text-muted">No surfaces are configured yet.</p>
          ) : (
            <div className="table-scroll">
              <table className="config-table">
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>Show</th>
                    <th>Revision</th>
                    <th>Updated</th>
                  </tr>
                </thead>
                <tbody>
                  {state.objects.map((obj) => (
                    <tr key={obj.id}>
                      <td>
                        <Link className="entity-link" to={`/config/show.surface/${encodeURIComponent(obj.id)}`}>
                          {obj.label}
                        </Link>
                      </td>
                      <td>{obj.show}</td>
                      <td>{obj.currentRevision}</td>
                      <td>{formatAbsolute(obj.updatedAt)}</td>
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
