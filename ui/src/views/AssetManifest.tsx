import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { getAssetManifest } from '../api'
import { describeApiError, evaluateAnyScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { StatusBadge, type StatusTone } from '../components/StatusBadge'
import type { NodeAssetManifest } from '../app/types'

// Track G seam G-8 (TRACK-G-surface-parity.md's own rule 3): "the manifest
// view keeps not_ready and unknown visually distinct, never collapsed
// into one 'not ok' state." showmeshctl spends two separate exit codes
// (20 and 21) keeping them apart for the identical reason: conflating "I
// checked and it is missing" with "I cannot tell" will either start a
// show it should not, or block one it should not. StatusBadge already
// carries four distinct tones (good/warn/bad/unknown) with icon+text
// pairing (never color alone) — ready maps to good, not_ready to bad, and
// unknown to its own tone, never bad.
const READ_SCOPES = ['show:macro:run', 'config:write']

type LoadState = { kind: 'loading' } | { kind: 'error'; message: string } | { kind: 'loaded'; nodes: NodeAssetManifest[] }

function stateBadge(state: NodeAssetManifest['state']): { tone: StatusTone; icon: string; label: string } {
  if (state === 'ready') return { tone: 'good', icon: '✓', label: 'Ready' }
  if (state === 'not_ready') return { tone: 'bad', icon: '✗', label: 'Not ready' }
  return { tone: 'unknown', icon: '?', label: 'Unknown' }
}

function NodeManifestRow({ manifest }: { manifest: NodeAssetManifest }) {
  const badge = stateBadge(manifest.state)
  return (
    <li className="panel" style={{ marginBottom: '0.75rem' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', flexWrap: 'wrap', gap: '0.5rem' }}>
        <Link className="entity-link" to={`/nodes/${encodeURIComponent(manifest.node)}`}>
          {manifest.node}
        </Link>
        <StatusBadge tone={badge.tone} icon={badge.icon} label={badge.label} />
      </div>
      {manifest.reason !== null && <p className="text-muted">{manifest.reason}</p>}
      <p className="text-muted">
        {/* An `unknown` verdict is never dated by evidence it does not have (ADR-020) — observedAt
            is null exactly when state is unknown, and this renders that honestly rather than
            omitting the line. */}
        {manifest.observedAt !== null ? `Observed ${formatAbsolute(manifest.observedAt)}` : 'No evidence to date this by.'}
      </p>

      {manifest.missing.length > 0 && (
        <>
          <h4>Missing ({manifest.missing.length})</h4>
          <ul className="list-plain">
            {manifest.missing.map((m) => (
              <li key={m.assetId}>
                {m.sequence} — {m.filename} ({m.sizeBytes.toLocaleString()} B)
              </li>
            ))}
          </ul>
        </>
      )}

      {manifest.gaps.length > 0 && (
        <>
          <h4>Sequences with no coverage at all ({manifest.gaps.length})</h4>
          <ul className="list-plain">
            {manifest.gaps.map((g) => (
              <li key={g.sequence}>
                {g.sequence} (surfaces: {g.surfaces.join(', ')})
              </li>
            ))}
          </ul>
        </>
      )}

      {manifest.extra.length > 0 && (
        <>
          <h4>Extra — held but not expected, never a basis for deletion ({manifest.extra.length})</h4>
          <ul className="list-plain">
            {manifest.extra.map((e) => (
              <li key={e.contentHash}>
                {e.filename} ({e.sizeBytes.toLocaleString()} B)
              </li>
            ))}
          </ul>
        </>
      )}
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
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [readGate.allowed])

  return (
    <div>
      <h2 className="panel__title">Asset manifest</h2>
      {/* ADR-028 seam E5: "what should this node hold" versus "what does it actually hold". */}
      <p className="text-muted">
        What the <Link to="/config/show.active">active show</Link> expects every declared node to
        hold, compared against what each node actually reports. &ldquo;Unknown&rdquo; is never
        rendered as &ldquo;not ready&rdquo; — there is no evidence an unknown verdict rests on, so
        it is stated as unknown rather than guessed either way.
      </p>

      {!readGate.allowed && (
        <p className="panel panel--error" role="status">
          {readGate.reason}
        </p>
      )}

      {readGate.allowed && state.kind === 'loading' && <p className="text-muted">Loading the manifest…</p>}
      {readGate.allowed && state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          {state.message}
        </p>
      )}
      {readGate.allowed && state.kind === 'loaded' && (
        <>
          {state.nodes.length === 0 ? (
            <p className="text-muted">No declared nodes.</p>
          ) : (
            <ul className="list-plain">
              {state.nodes.map((n) => (
                <NodeManifestRow key={n.node} manifest={n} />
              ))}
            </ul>
          )}
        </>
      )}
    </div>
  )
}
