import { useCallback, useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { assetContentUrl, listAssets } from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { AssetUpload } from '../components/AssetUpload'
import type { Asset } from '../app/types'
import { showWorkspacePath } from '../components/showWorkspacePaths'

// Track G seam G-8: the asset browser (ADR-028). Metadata list, narrowable
// by show/sequence/node via query params (mirroring GET /assets' own
// parameters, same shareable-URL posture as ShowSurfaces.tsx's `?show=`).
// Reads share the show.action/show.macro read posture (show:macro:run OR
// config:write) per api/openapi.yaml's own description of GET /assets;
// upload is gated separately by asset:write (AssetUpload.tsx).
const READ_SCOPES = ['show:macro:run', 'config:write']
const ASSET_WRITE_SCOPE = 'asset:write'

type LoadState = { kind: 'loading' } | { kind: 'error'; message: string } | { kind: 'loaded'; assets: Asset[] }

export function Assets() {
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const uploadGate = evaluateScope(model.session, model.sessionFetchFailed, ASSET_WRITE_SCOPE)
  const [searchParams, setSearchParams] = useSearchParams()
  const showFilter = searchParams.get('show') ?? ''
  const sequenceFilter = searchParams.get('sequence') ?? ''
  const nodeFilter = searchParams.get('node') ?? ''

  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [reloadGeneration, setReloadGeneration] = useState(0)

  const load = useCallback((): (() => void) => {
    let cancelled = false
    setState({ kind: 'loading' })
    listAssets({
      ...(showFilter !== '' ? { show: showFilter } : {}),
      ...(sequenceFilter !== '' ? { sequence: sequenceFilter } : {}),
      ...(nodeFilter !== '' ? { node: nodeFilter } : {}),
    })
      .then((resp) => {
        if (cancelled) return
        setState({ kind: 'loaded', assets: resp.assets })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [showFilter, sequenceFilter, nodeFilter])

  useEffect(() => {
    if (!readGate.allowed) return
    return load()
  }, [readGate.allowed, load, reloadGeneration])

  function updateFilter(key: 'show' | 'sequence' | 'node', value: string): void {
    const next = new URLSearchParams(searchParams)
    if (value === '') next.delete(key)
    else next.set(key, value)
    setSearchParams(next)
  }

  return (
    <div className="operator-page authoring-page">
      <h2 className="panel__title">Assets</h2>
      {/* Metadata in SQLite, bytes never in it (ADR-028 decision 4). */}
      <p className="operator-page__lede text-muted">
        Every stored asset&rsquo;s metadata: bytes never live in this list. Identity is show +
        sequence + target + content hash, never a filename: three different targets&rsquo;
        artifacts for one xLights sequence may legitimately share one filename.
      </p>

      {!readGate.allowed && (
        <p className="panel panel--error" role="status">
          {readGate.reason}
        </p>
      )}

      {readGate.allowed && (
        <>
          {uploadGate.allowed ? (
            <AssetUpload onUploaded={() => setReloadGeneration((g) => g + 1)} />
          ) : (
            <p className="text-muted" role="status">
              Uploading requires the <code>asset:write</code> scope. {uploadGate.reason}
            </p>
          )}

          <h3 className="panel__title">Narrow the list</h3>
          <div style={{ display: 'flex', gap: '1rem', flexWrap: 'wrap' }}>
            <label className="form-field">
              Show
              <input type="text" value={showFilter} onChange={(e) => updateFilter('show', e.target.value)} />
            </label>
            <label className="form-field">
              Sequence
              <input type="text" value={sequenceFilter} onChange={(e) => updateFilter('sequence', e.target.value)} />
            </label>
            <label className="form-field">
              Node
              <input type="text" value={nodeFilter} onChange={(e) => updateFilter('node', e.target.value)} />
            </label>
          </div>

          {state.kind === 'loading' && <p className="text-muted">Loading assets…</p>}
          {state.kind === 'error' && (
            <p className="panel panel--error" role="alert">
              {state.message}
            </p>
          )}
          {state.kind === 'loaded' && (
            <>
              {state.assets.length === 0 ? (
                <p className="text-muted">No assets match this filter.</p>
              ) : (
                <div className="table-scroll">
                  <table className="config-table">
                    <thead>
                      <tr>
                        <th>Sequence</th>
                        <th>Show</th>
                        <th>Target</th>
                        <th>Media type</th>
                        <th>Filename</th>
                        <th>Size</th>
                        <th>Current</th>
                        <th>Uploaded</th>
                        <th aria-label="Download" />
                      </tr>
                    </thead>
                    <tbody>
                      {state.assets.map((a) => (
                        <tr key={a.id}>
                          <td>{a.sequence}</td>
                          <td><Link className="entity-link" to={showWorkspacePath(a.show)}>{a.show}</Link></td>
                          <td>{a.targetKind === 'node' ? a.target : 'show-wide'}</td>
                          <td>{a.mediaType}</td>
                          <td>{a.runtimeFilename}</td>
                          <td>{a.sizeBytes.toLocaleString()} B</td>
                          <td>{a.current ? 'current' : 'superseded'}</td>
                          <td>
                            {formatAbsolute(a.createdAt)}
                            {a.createdByPrincipalName !== null && `: ${a.createdByPrincipalName}`}
                          </td>
                          <td>
                            <a href={assetContentUrl(a.id)} download={a.runtimeFilename}>
                              Download
                            </a>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </>
          )}
        </>
      )}

      <p className="text-muted" style={{ marginTop: '1rem' }}>
        See <Link to="/assets/manifest">the asset manifest</Link> for whether each node actually
        holds what the active show currently expects.
      </p>
    </div>
  )
}
