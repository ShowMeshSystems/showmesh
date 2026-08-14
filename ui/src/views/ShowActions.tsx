import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { listConfigObjects } from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import type { ConfigObjectSummary } from '../app/types'

// Same read posture as Macros.tsx (STEP-9-SPEC.md section 5.5: "Same
// read posture as GET /config/show.action"): show:macro:run OR
// config:write, because a macro's own detail view (MacroDetail.tsx)
// needs to resolve the actions it composes for a principal who can run
// macros but is not an admin.
const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE = 'config:write'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; objects: ConfigObjectSummary[] }

export function ShowActions() {
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)

  const [state, setState] = useState<LoadState>({ kind: 'loading' })

  useEffect(() => {
    if (!readGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    listConfigObjects('show.action')
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
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', flexWrap: 'wrap', gap: '0.75rem' }}>
        <h2 className="panel__title">Show actions</h2>
        {writeGate.allowed && (
          <Link className="entity-link" to="/actions/new">
            New action
          </Link>
        )}
      </div>
      <p className="text-muted">
        A show action is one logical step a macro can invoke — an FPP primitive or an external
        MQTT command. Macros compose actions; actions never appear on their own in a running
        show.
      </p>

      {!readGate.allowed && (
        <p className="panel panel--error" role="status">
          {readGate.reason}
        </p>
      )}

      {readGate.allowed && state.kind === 'loading' && <p className="text-muted">Loading actions…</p>}
      {readGate.allowed && state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          {state.message}
        </p>
      )}
      {readGate.allowed && state.kind === 'loaded' && (
        <>
          {state.objects.length === 0 ? (
            <p className="text-muted">No show actions are configured yet.</p>
          ) : (
            <table className="config-table">
              <thead>
                <tr>
                  <th>Label</th>
                  <th>Show</th>
                  <th>Revision</th>
                  <th>Updated</th>
                </tr>
              </thead>
              <tbody>
                {state.objects.map((obj) => (
                  <tr key={obj.id}>
                    <td>
                      <Link className="entity-link" to={`/actions/${encodeURIComponent(obj.id)}`}>
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
          )}
        </>
      )}
    </div>
  )
}
