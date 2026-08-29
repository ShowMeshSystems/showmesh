import { useCallback, useEffect, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { getAssetManifest, getShow, listAssets } from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { ageMs, formatAge } from '../app/time'
import { AssetUpload } from '../components/AssetUpload'
import type { Asset, NodeAssetManifest } from '../app/types'
import { AssetsSequenceTable } from './assets/AssetsSequenceTable'
import { AssetHistoryPanel } from './assets/AssetHistoryPanel'
import { manifestByNode, showWideGapNodes, verdictForNodeTarget } from './assets/nodeVerdict'
import { SequenceCoverageSection } from './assets/SequenceCoverageSection'
import '../styles/assets.css'

// Track G seam G-8: the asset browser (ADR-028). Show-scoped
// (`/shows/:showId/assets`) is the workspace tab, grouped by logical
// sequence per Show Assets.dc.html; cross-show (`/assets`) is the rail
// destination, the owner's ruling for which is "the same table with a
// Show column added", plus this view's own existing show/sequence/node
// filter. Reads share the show.action/show.macro read posture
// (show:macro:run OR config:write) per api/openapi.yaml's own description
// of GET /assets; upload is gated separately by asset:write.
const READ_SCOPES = ['show:macro:run', 'config:write']
const ASSET_WRITE_SCOPE = 'asset:write'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; assets: Asset[]; manifestNodes: NodeAssetManifest[] }

type PaneMode = { kind: 'history'; row: Asset } | { kind: 'upload' }

const WORKSPACE_TABS: Array<{ id: string; label: string; path: (showId: string) => string }> = [
  { id: 'playlists', label: 'Playlists', path: (id) => `/shows/${encodeURIComponent(id)}/playlists` },
  { id: 'cues', label: 'Cues', path: (id) => `/shows/${encodeURIComponent(id)}/cues` },
  { id: 'assets', label: 'Assets', path: (id) => `/shows/${encodeURIComponent(id)}/assets` },
  { id: 'presentation', label: 'Presentation', path: (id) => `/shows/${encodeURIComponent(id)}/presentation` },
  { id: 'automation', label: 'Automation', path: (id) => `/shows/${encodeURIComponent(id)}/automation` },
]

export function Assets() {
  const { showId } = useParams<{ showId?: string }>()
  return showId !== undefined ? <ShowScopedAssets showId={showId} /> : <CrossShowAssets />
}

function useLoadAssets(filter: { show?: string; sequence?: string; node?: string }, gate: { allowed: boolean }) {
  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [generation, setGeneration] = useState(0)
  const reload = useCallback(() => setGeneration((g) => g + 1), [])

  const load = useCallback((): (() => void) => {
    let cancelled = false
    setState({ kind: 'loading' })
    Promise.all([listAssets(filter), getAssetManifest()])
      .then(([assetsResp, manifestResp]) => {
        if (cancelled) return
        setState({ kind: 'loaded', assets: assetsResp.assets, manifestNodes: manifestResp.nodes })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState((prev) =>
          // A refresh failure retains the last known state and says when it was read; it never blanks the region.
          prev.kind === 'loaded' ? prev : { kind: 'error', message: describeApiError(err) },
        )
      })
    return () => {
      cancelled = true
    }
  }, [filter.show, filter.sequence, filter.node])

  useEffect(() => {
    if (!gate.allowed) return
    return load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [gate.allowed, load, generation])

  return { state, reload }
}

function ShowScopedAssets({ showId }: { showId: string }) {
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const uploadGate = evaluateScope(model.session, model.sessionFetchFailed, ASSET_WRITE_SCOPE)
  const { state, reload } = useLoadAssets({ show: showId }, readGate)
  const [showName, setShowName] = useState<string | null>(null)
  const [pane, setPane] = useState<PaneMode>({ kind: 'upload' })
  const [kindFilter, setKindFilter] = useState<'all' | 'fseq' | 'audio' | 'needs-sync'>('all')

  useEffect(() => {
    let cancelled = false
    getShow(showId)
      .then((resp) => {
        if (!cancelled) setShowName(resp.payload.name)
      })
      .catch(() => undefined)
    return () => {
      cancelled = true
    }
  }, [showId])

  if (!readGate.allowed) {
    return (
      <div className="page-body">
        <p className="panel panel--error" role="status">
          {readGate.reason}
        </p>
      </div>
    )
  }

  return (
    <div>
      <header className="page-header">
        <p className="page-header__breadcrumb">
          <Link to="/shows">Shows</Link> / {showName ?? showId}
        </p>
        <div className="page-header__row">
          <h1 className="page-header__title t-display">{showName ?? showId}</h1>
        </div>
      </header>

      <nav className="assets-workspace-tabs" aria-label="Show workspace">
        {WORKSPACE_TABS.map((tab) =>
          tab.id === 'assets' ? (
            <span key={tab.id} aria-current="page">
              {tab.label}
              {state.kind === 'loaded' && <span className="assets-workspace-tabs__count">{new Set(state.assets.filter((a) => a.current).map((a) => `${a.sequence}`)).size}</span>}
            </span>
          ) : (
            <Link key={tab.id} to={tab.path(showId)}>
              {tab.label}
            </Link>
          ),
        )}
      </nav>

      {state.kind === 'loading' && (
        <div className="page-body">
          <p className="t-small text-muted" role="status" aria-busy="true">
            Loading assets…
          </p>
        </div>
      )}
      {state.kind === 'error' && (
        <div className="page-body">
          <p className="panel panel--error" role="alert">
            {state.message}
          </p>
        </div>
      )}
      {state.kind === 'loaded' && (
        <div className="panes">
          <section style={{ minWidth: 0, padding: '20px 24px 0 28px' }} aria-labelledby="assets-section-heading">
            <h2 id="assets-section-heading" style={{ position: 'absolute', width: 1, height: 1, overflow: 'hidden', clip: 'rect(0 0 0 0)' }}>
              Assets
            </h2>
            <AssetStatsStrip assets={state.assets} manifestNodes={state.manifestNodes} serverTime={model.serverTime} />

            <p className="asset-note">
              Sync runs on upload and on a timer, never because a show started. Nodes always play from their own disk, so a node missing an asset
              is a readiness fault found before a show, not during one.
            </p>

            <div className="asset-filter-bar">
              <div className="asset-filter-bar__controls">
                <div className="segmented" role="group" aria-label="Filter by kind">
                  {(['all', 'fseq', 'audio', 'needs-sync'] as const).map((k) => (
                    <button
                      key={k}
                      type="button"
                      className="segmented__option"
                      aria-pressed={kindFilter === k}
                      onClick={() => setKindFilter(k)}
                    >
                      {k === 'all' ? 'All' : k === 'fseq' ? 'FSEQ' : k === 'audio' ? 'Audio' : 'Needs sync'}
                    </button>
                  ))}
                </div>
              </div>
              <button type="button" className="btn btn--primary" onClick={() => setPane({ kind: 'upload' })}>
                Upload
              </button>
            </div>

            <p className="t-small text-muted" style={{ maxWidth: '74ch', marginTop: '14px' }}>
              Grouped by <strong style={{ color: 'var(--text)' }}>logical sequence</strong>, because one sequence produces a different file per
              target and xLights gives them all the same name. The filename belongs to the group; identity belongs to the row.
            </p>

            <div style={{ marginTop: '14px' }}>
              <AssetsSequenceTable
                assets={filterByKind(state.assets, kindFilter, state.manifestNodes)}
                manifestNodes={state.manifestNodes}
                selectedRowId={pane.kind === 'history' ? pane.row.id : null}
                onSelectRow={(row) => setPane({ kind: 'history', row })}
              />
            </div>

            <SequenceCoverageSection
              showId={showId}
              assets={state.assets}
              manifestNodes={state.manifestNodes}
              allowed={readGate.allowed}
              onUploadFor={() => setPane({ kind: 'upload' })}
            />
          </section>

          <aside>
            {pane.kind === 'history' ? (
              <AssetHistoryPanel
                row={pane.row}
                allAssets={state.assets}
                onChanged={() => {
                  reload()
                }}
              />
            ) : uploadGate.allowed ? (
              <AssetUpload
                lockedShow={showName ?? showId}
                knownSequences={Array.from(new Set(state.assets.map((a) => a.sequence))).sort()}
                identityCandidates={state.assets}
                onUploaded={() => reload()}
              />
            ) : (
              <p className="t-small text-muted" role="status">
                Uploading requires the <code>asset:write</code> scope. {uploadGate.reason}
              </p>
            )}
          </aside>
        </div>
      )}
    </div>
  )
}

function filterByKind(assets: Asset[], kind: 'all' | 'fseq' | 'audio' | 'needs-sync', manifestNodes: NodeAssetManifest[]): Asset[] {
  if (kind === 'all') return assets
  if (kind === 'fseq') return assets.filter((a) => a.mediaType === 'fseq')
  if (kind === 'audio') return assets.filter((a) => a.mediaType === 'audio')
  // needs-sync: keep every asset in a group that has at least one non-matching current row.
  const byNode = manifestByNode(manifestNodes)
  const needySequences = new Set<string>()
  for (const a of assets) {
    if (!a.current) continue
    if (a.targetKind === 'node') {
      const v = verdictForNodeTarget(a, byNode)
      if (v.kind !== 'matches') needySequences.add(`${a.show} ${a.sequence}`)
    } else {
      if (showWideGapNodes(a.id, a.runtimeFilename, manifestNodes).length > 0) needySequences.add(`${a.show} ${a.sequence}`)
    }
  }
  return assets.filter((a) => needySequences.has(`${a.show} ${a.sequence}`))
}

function AssetStatsStrip({ assets, manifestNodes, serverTime }: { assets: Asset[]; manifestNodes: NodeAssetManifest[]; serverTime: string | null }) {
  const byNode = manifestByNode(manifestNodes)
  let onTarget = 0
  let mismatch = 0
  let notSynced = 0
  for (const a of assets) {
    if (!a.current) continue
    if (a.targetKind === 'node') {
      const v = verdictForNodeTarget(a, byNode)
      if (v.kind === 'matches') onTarget += 1
      else if (v.kind === 'hash_mismatch') mismatch += 1
      else if (v.kind === 'not_synced') notSynced += 1
    } else {
      const gaps = showWideGapNodes(a.id, a.runtimeFilename, manifestNodes)
      if (gaps.length === 0) onTarget += 1
      for (const g of gaps) {
        if (g.verdict.kind === 'hash_mismatch') mismatch += 1
        else notSynced += 1
      }
    }
  }
  const observedTimes = manifestNodes.map((n) => n.observedAt).filter((t): t is string => t !== null)
  const lastSyncIso = observedTimes.length > 0 ? observedTimes.sort().at(-1)! : null
  const lastSyncAge = lastSyncIso !== null ? ageMs(lastSyncIso, serverTime) : null

  return (
    <div className="asset-stats">
      <div className="asset-stats__item">
        <p className="t-meta asset-stats__label asset-stats__label--good">On target</p>
        <p className="asset-stats__value asset-stats__value--good">{onTarget}</p>
      </div>
      <div className="asset-stats__item">
        <p className="t-meta asset-stats__label asset-stats__label--warn">Hash mismatch</p>
        <p className="asset-stats__value asset-stats__value--warn">{mismatch}</p>
      </div>
      <div className="asset-stats__item">
        <p className="t-meta asset-stats__label">Not synced</p>
        <p className="asset-stats__value">{notSynced}</p>
      </div>
      <div className="asset-stats__item">
        <p className="t-meta asset-stats__label">Last sync</p>
        <p className="asset-stats__value">{lastSyncAge !== null ? formatAge(lastSyncAge) : 'No evidence'}</p>
      </div>
    </div>
  )
}

function CrossShowAssets() {
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const uploadGate = evaluateScope(model.session, model.sessionFetchFailed, ASSET_WRITE_SCOPE)
  const [searchParams, setSearchParams] = useSearchParams()
  const showFilter = searchParams.get('show') ?? ''
  const sequenceFilter = searchParams.get('sequence') ?? ''
  const nodeFilter = searchParams.get('node') ?? ''
  const { state, reload } = useLoadAssets(
    {
      ...(showFilter !== '' ? { show: showFilter } : {}),
      ...(sequenceFilter !== '' ? { sequence: sequenceFilter } : {}),
      ...(nodeFilter !== '' ? { node: nodeFilter } : {}),
    },
    readGate,
  )

  function updateFilter(key: 'show' | 'sequence' | 'node', value: string): void {
    const next = new URLSearchParams(searchParams)
    if (value === '') next.delete(key)
    else next.set(key, value)
    setSearchParams(next)
  }

  return (
    <div className="page-body">
      <header className="page-header" style={{ padding: 0 }}>
        <h1 className="page-header__title t-display">Assets</h1>
        <p className="assets-page__lede">
          Every stored asset&rsquo;s metadata across every show: bytes never live in this list. Identity is show + sequence + target + content
          hash, never a filename: different targets&rsquo; artifacts for one xLights sequence may legitimately share one filename.
        </p>
      </header>

      {!readGate.allowed && (
        <p className="panel panel--error" role="status">
          {readGate.reason}
        </p>
      )}

      {readGate.allowed && (
        <>
          <div className="asset-filter-bar" style={{ marginTop: '18px' }}>
            <div className="asset-filter-bar__controls">
              <label className="field" style={{ gap: '4px' }}>
                <span className="field__label t-small">Show</span>
                <input className="asset-filter-bar__search" type="text" value={showFilter} onChange={(e) => updateFilter('show', e.target.value)} />
              </label>
              <label className="field" style={{ gap: '4px' }}>
                <span className="field__label t-small">Sequence</span>
                <input
                  className="asset-filter-bar__search"
                  type="text"
                  value={sequenceFilter}
                  onChange={(e) => updateFilter('sequence', e.target.value)}
                />
              </label>
              <label className="field" style={{ gap: '4px' }}>
                <span className="field__label t-small">Node</span>
                <input className="asset-filter-bar__search" type="text" value={nodeFilter} onChange={(e) => updateFilter('node', e.target.value)} />
              </label>
            </div>
          </div>

          {state.kind === 'loading' && (
            <p className="t-small text-muted" role="status" aria-busy="true">
              Loading assets…
            </p>
          )}
          {state.kind === 'error' && (
            <p className="panel panel--error" role="alert">
              {state.message}
            </p>
          )}
          {state.kind === 'loaded' && (
            <div style={{ marginTop: '14px' }}>
              <AssetsSequenceTable assets={state.assets} manifestNodes={state.manifestNodes} showColumn />
            </div>
          )}

          <div style={{ marginTop: '20px', maxWidth: '480px' }}>
            {uploadGate.allowed ? (
              <AssetUpload onUploaded={() => reload()} identityCandidates={state.kind === 'loaded' ? state.assets : []} />
            ) : (
              <p className="t-small text-muted" role="status">
                Uploading requires the <code>asset:write</code> scope. {uploadGate.reason}
              </p>
            )}
          </div>
        </>
      )}

      <p className="t-small text-muted" style={{ marginTop: '1rem' }}>
        See <Link to="/assets/manifest">the asset manifest</Link> for whether each node actually holds what the active show currently expects.
      </p>
    </div>
  )
}
