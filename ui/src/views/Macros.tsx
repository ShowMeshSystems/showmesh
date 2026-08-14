import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { listConfigObjects } from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { RunMacroButton } from '../components/RunMacroButton'
import type { ConfigObjectSummary } from '../app/types'

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

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; objects: ConfigObjectSummary[] }

export function Macros() {
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)

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

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', flexWrap: 'wrap', gap: '0.75rem' }}>
        <h2 className="panel__title">Show macros</h2>
        {writeGate.allowed && (
          <Link className="entity-link" to="/macros/new">
            New macro
          </Link>
        )}
      </div>

      {!readGate.allowed && (
        <p className="panel panel--error" role="status">
          {readGate.reason}
        </p>
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
            <table className="config-table">
              <thead>
                <tr>
                  <th>Label</th>
                  <th>Show</th>
                  <th>Revision</th>
                  <th>Updated</th>
                  <th aria-label="Run" />
                </tr>
              </thead>
              <tbody>
                {state.objects.map((obj) => (
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
                      <RunMacroButton macroId={obj.id} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </>
      )}

      <p className="text-muted" style={{ marginTop: '1rem' }}>
        <Link to="/actions">Show actions</Link> are what each macro step invokes.
      </p>
    </div>
  )
}
