import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { getAssetManifest } from '../api'
import { describeApiError, evaluateAnyScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { formatBytes } from './assets/assetGrouping'
import type { NodeAssetManifest } from '../app/types'
import '../styles/assets.css'

// Track G seam G-8 (TRACK-G-surface-parity.md's own rule 3): "the manifest
// view keeps not_ready and unknown visually distinct, never collapsed
// into one 'not ok' state." showmeshctl spends two separate exit codes
// (20 and 21) keeping them apart for the identical reason: conflating "I
// checked and it is missing" with "I cannot tell" will either start a
// show it should not, or block one it should not. This screen also
// surfaces `gaps` (a sequence with no coverage at all) and `extra` (bytes
// a node holds that nothing expects) by name — see the owner's ruling in
// BUILDER-BRIEF.md: neither field has a mock yet, so this keeps them
// reachable and correctly labelled rather than inventing a home for them.
const READ_SCOPES = ['show:macro:run', 'config:write']

type LoadState = { kind: 'loading' } | { kind: 'error'; message: string } | { kind: 'loaded'; nodes: NodeAssetManifest[] }

function StateLabel({ state }: { state: NodeAssetManifest['state'] }) {
  if (state === 'ready') return <span className="status-pair status-pair--good">Ready</span>
  if (state === 'not_ready') return <span className="status-pair status-pair--bad">Not ready</span>
  // 'unknown' is never a soft ok and never rendered as broken (§4): the check could not be performed at all.
  return <span className="status-pair status-pair--unknown">Unknown</span>
}

function NodeManifestCard({ manifest }: { manifest: NodeAssetManifest }) {
  return (
    <li className="manifest-node">
      <div className="manifest-node__head">
        <Link className="entity-link t-subhead" to={`/monitor/fleet/node/${encodeURIComponent(manifest.node)}`}>
          {manifest.node}
        </Link>
        <StateLabel state={manifest.state} />
      </div>
      <div className="manifest-node__body">
        <p className="t-small text-muted">{manifest.reason ?? 'This coordinator considers this node ready.'}</p>
        <p className="t-small text-muted">
          {/* An `unknown` verdict is never dated by evidence it does not have — observedAt is null exactly when state is unknown. */}
          {manifest.observedAt !== null ? `Observed ${formatAbsolute(manifest.observedAt)}` : 'No evidence to date this by.'}
        </p>

        {manifest.missing.length > 0 && (
          <div className="manifest-node__group">
            <h4>Missing ({manifest.missing.length})</h4>
            <ul className="manifest-node__list">
              {manifest.missing.map((m) => (
                <li key={m.assetId}>
                  {m.sequence}: {m.filename} ({formatBytes(m.sizeBytes)})
                </li>
              ))}
            </ul>
          </div>
        )}

        {manifest.gaps.length > 0 && (
          <div className="manifest-node__group">
            <h4>Sequences with no coverage at all ({manifest.gaps.length})</h4>
            <ul className="manifest-node__list">
              {manifest.gaps.map((g) => (
                <li key={g.sequence}>
                  {g.sequence} (surfaces: {g.surfaces.join(', ')})
                </li>
              ))}
            </ul>
          </div>
        )}

        {manifest.extra.length > 0 && (
          <div className="manifest-node__group manifest-node__group--extra">
            <h4>Extra: held but not expected, never a basis for deletion ({manifest.extra.length})</h4>
            <ul className="manifest-node__list">
              {manifest.extra.map((e) => (
                <li key={e.contentHash}>
                  {e.filename} ({formatBytes(e.sizeBytes)})
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>
    </li>
  )
}

export function AssetManifest() {
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const [state, setState] = useState<LoadState>({ kind: 'loading' })

  useEffect(() => {
    if (!readGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    getAssetManifest()
      .then((resp) => {
        if (cancelled) return
        setState({ kind: 'loaded', nodes: resp.nodes })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState((prev) => (prev.kind === 'loaded' ? prev : { kind: 'error', message: describeApiError(err) }))
      })
    return () => {
      cancelled = true
    }
  }, [readGate.allowed])

  return (
    <div className="page-body">
      <header className="page-header" style={{ padding: 0 }}>
        <h1 className="page-header__title t-display">Asset manifest</h1>
        {/* ADR-028 seam E5: "what should this node hold" versus "what does it actually hold". */}
        <p className="assets-page__lede">
          What the active show expects every declared node to hold, compared against what each node actually reports. &ldquo;Unknown&rdquo; is
          never rendered as &ldquo;not ready&rdquo;: there is no evidence an unknown verdict rests on, so it is stated as unknown rather than
          guessed either way. See <Link to="/assets">Assets</Link> for the same identities&rsquo; upload and rollback history.
        </p>
      </header>

      {!readGate.allowed && (
        <p className="panel panel--error" role="status">
          {readGate.reason}
        </p>
      )}

      {readGate.allowed && state.kind === 'loading' && (
        <p className="t-small text-muted" role="status" aria-busy="true">
          Loading the manifest…
        </p>
      )}
      {readGate.allowed && state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          {state.message}
        </p>
      )}
      {readGate.allowed && state.kind === 'loaded' && (
        <>
          {state.nodes.length === 0 ? (
            <p className="t-small text-muted">No declared nodes.</p>
          ) : (
            <ul className="manifest-node-list" style={{ listStyle: 'none', margin: '18px 0 0', padding: 0 }}>
              {state.nodes.map((n) => (
                <NodeManifestCard key={n.node} manifest={n} />
              ))}
            </ul>
          )}
        </>
      )}
    </div>
  )
}
