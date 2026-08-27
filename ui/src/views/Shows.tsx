import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { listConfigObjects } from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import type { ConfigObjectSummary } from '../app/types'

// Track G seam G-8 (TRACK-G-surface-parity.md "Class 3"/"G-8"): the show
// list. Same read posture as GET /config/show.action and GET
// /config/show.macro (show:macro:run OR config:write) — see Macros.tsx's
// own comment on why this must not be gated on config:write alone.
const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE = 'config:write'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; objects: ConfigObjectSummary[] }

export function Shows() {
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)

  const [state, setState] = useState<LoadState>({ kind: 'loading' })

  useEffect(() => {
    if (!readGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    listConfigObjects('show')
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

  return (
    <div className="operator-page authoring-page">
      <div className="operator-page__header">
        <h2 className="panel__title">Shows</h2>
        {writeGate.allowed ? (
          <Link className="entity-link" to="/config/show/new">
            New show
          </Link>
        ) : (
          <span className="scoped-button">
            <button type="button" disabled aria-disabled="true" title={writeGate.reason}>
              New show
            </button>
            <span className="scoped-button__reason">{writeGate.reason}</span>
          </span>
        )}
      </div>
      {/* A show is a namespace, not a container (ADR-027 decision 2): surfaces, actions, and macros
          each carry a reference to one, so programming one show cannot accidentally revise another. */}
      <p className="operator-page__lede text-muted">
        A Show is the authoring workspace and namespace for its Cues, Playlists, Assets, Automation,
        Presentation, Show Night, and Readiness. See{' '}
        <Link to="/config/show.active">the active show</Link> to change what is currently running.
      </p>

      {!readGate.allowed && (
        <p className="panel panel--error" role="status">
          {readGate.reason}
        </p>
      )}

      {readGate.allowed && state.kind === 'loading' && <p className="text-muted">Loading shows…</p>}
      {readGate.allowed && state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          {state.message}
        </p>
      )}
      {readGate.allowed && state.kind === 'loaded' && (
        <>
          {state.objects.length === 0 ? (
            <p className="text-muted">No shows are configured yet.</p>
          ) : (
            <div className="table-scroll">
              <table className="config-table">
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>Revision</th>
                    <th>Updated</th>
                  </tr>
                </thead>
                <tbody>
                  {state.objects.map((obj) => (
                    <tr key={obj.id}>
                      <td>
                        <Link className="entity-link" to={`/config/show/${encodeURIComponent(obj.id)}`}>
                          {obj.label}
                        </Link>
                      </td>
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
