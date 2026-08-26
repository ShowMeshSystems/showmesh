import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { listConfigObjects } from '../api'
import { describeApiError, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import type { ConfigObjectSummary } from '../app/types'

// ADR-018/ADR-039: the audio.node object list. `config:write` gates both
// reads and writes alike, matching audio.node's own GET/PUT (no separate
// read scope exists for this kind).
const CONFIG_WRITE_SCOPE = 'config:write'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; objects: ConfigObjectSummary[] }

export function AudioNodes() {
  const model = useModelContext()
  const scopeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)

  const [state, setState] = useState<LoadState>({ kind: 'loading' })

  useEffect(() => {
    if (!scopeGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    listConfigObjects('audio.node')
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
  }, [scopeGate.allowed])

  return (
    <div>
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'baseline',
          flexWrap: 'wrap',
          gap: '0.75rem',
        }}
      >
        <h2 className="panel__title">Audio nodes</h2>
        {scopeGate.allowed ? (
          <Link className="entity-link" to="/config/audio.node/new">
            New audio node
          </Link>
        ) : (
          <span className="scoped-button">
            <button type="button" disabled aria-disabled="true" title={scopeGate.reason}>
              New audio node
            </button>
            <span className="scoped-button__reason">{scopeGate.reason}</span>
          </span>
        )}
      </div>
      <p className="text-muted">
        Each node&rsquo;s own program route, LTC route, channel assignment, and declared clock
        domain: the audio.node configuration kind, previously reachable only through{' '}
        <code>showmeshctl</code>. A node with no row here has never had one written for it.
      </p>

      {!scopeGate.allowed && (
        <p className="panel panel--error" role="status">
          {scopeGate.reason}
        </p>
      )}

      {scopeGate.allowed && state.kind === 'loading' && <p className="text-muted">Loading audio nodes…</p>}
      {scopeGate.allowed && state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          {state.message}
        </p>
      )}
      {scopeGate.allowed && state.kind === 'loaded' && (
        <>
          {state.objects.length === 0 ? (
            <p className="text-muted">No audio.node object is configured yet.</p>
          ) : (
            <div className="table-scroll">
              <table className="config-table">
                <thead>
                  <tr>
                    <th>Node id</th>
                    <th>Program route</th>
                    <th>Revision</th>
                    <th>Updated</th>
                  </tr>
                </thead>
                <tbody>
                  {state.objects.map((obj) => (
                    <tr key={obj.id}>
                      <td>
                        <Link className="entity-link" to={`/config/audio.node/${encodeURIComponent(obj.id)}`}>
                          {obj.id}
                        </Link>
                      </td>
                      <td>{obj.label}</td>
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
