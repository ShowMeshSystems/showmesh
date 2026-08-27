import { useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { listConfigObjects } from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import type { ConfigObjectSummary } from '../app/types'
import { showWorkspacePath } from '../components/showWorkspacePaths'

// Track H seam H6 (TRACK-H-cues-and-playlists.md "H6"): the show.cue list,
// narrowable by show (?show=<id>, api/openapi.yaml's own
// `GET /config/show.cue` parameter). Same read posture, filter shape, and
// URL-is-state posture as ShowSurfaces.tsx: a Cue is authored the same way
// a Surface is, one level down from the show it belongs to.
const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE = 'config:write'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; objects: ConfigObjectSummary[] }

export function ShowCues() {
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
    listConfigObjects('show.cue', showFilter === '' ? undefined : showFilter)
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
    <div className="operator-page authoring-page">
      <div className="operator-page__header">
        <h2 className="panel__title">Cues</h2>
        {writeGate.allowed ? (
          <Link className="entity-link" to="/config/show.cue/new">
            New cue
          </Link>
        ) : (
          <span className="scoped-button">
            <button type="button" disabled aria-disabled="true" title={writeGate.reason}>
              New cue
            </button>
            <span className="scoped-button__reason">{writeGate.reason}</span>
          </span>
        )}
      </div>
      {/* A Cue is the show-scoped unit render, audio, LTC and announcement
          activation share (ADR-043, TRACK-H-cues-and-playlists.md H1/H4): a
          Playlist's entries each name one, and never define output directly
          themselves. */}
      <p className="operator-page__lede text-muted">
        A Cue declares what one activation does: a render sequence, a local
        audio asset and its LTC offset, and an announcement policy. A
        Playlist&rsquo;s entries each name a Cue; a Cue never appears without
        at least one output declared.
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

      {readGate.allowed && state.kind === 'loading' && <p className="text-muted">Loading cues…</p>}
      {readGate.allowed && state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          {state.message}
        </p>
      )}
      {readGate.allowed && state.kind === 'loaded' && (
        <>
          {state.objects.length === 0 ? (
            <p className="text-muted">No cues are configured yet.</p>
          ) : (
            <div className="table-scroll">
              <table className="config-table" aria-label="Cues">
                <thead>
                  <tr>
                    <th scope="col">Name</th>
                    <th scope="col">Show</th>
                    <th scope="col">Revision</th>
                    <th scope="col">Updated</th>
                  </tr>
                </thead>
                <tbody>
                  {state.objects.map((obj) => (
                    <tr key={obj.id}>
                      <th scope="row">
                        <Link className="entity-link" to={`/config/show.cue/${encodeURIComponent(obj.id)}`}>
                          {obj.label}
                        </Link>
                      </th>
                      <td><Link className="entity-link" to={showWorkspacePath(obj.show)}>{obj.show}</Link></td>
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
