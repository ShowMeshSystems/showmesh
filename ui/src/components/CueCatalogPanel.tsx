import { useEffect, useState } from 'react'
import { deployNodeCueCatalog, getNodeCueCatalog } from '../api'
import type { CueCatalogDeployResult, CueCatalogResponse } from '../api'
import { useModelContext } from '../app/ModelContext'
import { describeApiError } from '../app/session'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from './ScopedButton'

// Track H seam H6 (TRACK-H-H3-SPEC.md section 8: "No Operator UI. H6 owns
// it."): the node's own resolved Cue catalog, and the deploy control that
// closes the show-night gap H3 left open — a node holding a stale catalog
// silently drops every Cue outside it (its own refusal posture, section 6)
// while every other indicator on this page still reads healthy.
export interface CueCatalogPanelProps {
  nodeId: string
}

// GET /nodes/{nodeId}/cue-catalog requires cuecatalog:deploy for
// nothing at all — it is a plain open read (observation:read, ADR-024's
// reads-stay-open posture), matching listResolumeInstances' identical
// note in store.ts. No ScopedButton gates the fetch itself, only the
// deploy control below.
type CatalogState =
  | { kind: 'loading' }
  | { kind: 'loaded'; response: CueCatalogResponse }
  | { kind: 'error'; message: string }

function useNodeCueCatalog(nodeId: string, refreshToken: number, snapshotReceivedAt: number | null): CatalogState {
  const [state, setState] = useState<CatalogState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    getNodeCueCatalog(nodeId)
      .then((response) => {
        if (cancelled) return
        setState({ kind: 'loaded', response })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
    // Same shape as PlaylistReadiness.tsx's own usePlaylistReadiness:
    // `snapshotReceivedAt` changes on every resnapshot (initial connect,
    // reconnect, stream.reset, per store.ts's applySnapshot), so this
    // re-asks the coordinator whenever this browser's connection to it
    // was re-established, without a second freshness mechanism.
    // `refreshToken` covers both this panel's existing post-deploy
    // re-check and its own explicit "Reload" control below.
  }, [nodeId, refreshToken, snapshotReceivedAt])

  return state
}

type DeployState =
  | { kind: 'idle' }
  | { kind: 'submitting' }
  | { kind: 'result'; result: CueCatalogDeployResult }
  | { kind: 'error'; message: string }

export function CueCatalogPanel({ nodeId }: CueCatalogPanelProps) {
  const model = useModelContext()
  // Bumped after a deploy resolves so the "required" side re-fetches —
  // the desired-versus-observed rule cuts both ways: a deploy can also
  // reveal that the active show moved again while it was in flight.
  // Also bumped by the explicit "Reload" control below: this is a
  // one-shot fetch with no polling precedent in this seam, so a manual
  // recheck plus the reconnect-triggered refetch in useNodeCueCatalog
  // are preferred over inventing an interval.
  const [refreshToken, setRefreshToken] = useState(0)
  const catalog = useNodeCueCatalog(nodeId, refreshToken, model.snapshotReceivedAt)
  const [deploy, setDeploy] = useState<DeployState>({ kind: 'idle' })

  async function runDeploy(): Promise<void> {
    if (deploy.kind === 'submitting') return
    setDeploy({ kind: 'submitting' })
    try {
      const result = await deployNodeCueCatalog(nodeId)
      setDeploy({ kind: 'result', result })
      setRefreshToken((n) => n + 1)
    } catch (err) {
      setDeploy({ kind: 'error', message: describeApiError(err) })
    }
  }

  return (
    <>
      {catalog.kind === 'loading' && (
        <p className="text-muted" role="status">
          Loading this node's Cue catalog…
        </p>
      )}
      {catalog.kind === 'error' && (
        <p role="alert" className="render-surface__error">
          Could not read this node's Cue catalog: {catalog.message}
        </p>
      )}
      {catalog.kind === 'loaded' && (
        <>
          {/* ADR-011: every observation carries freshness. `serverTime`
              is required on this response and is the coordinator's own
              clock at the moment it resolved THIS catalog, not the
              browser's: the only way an operator can tell a catalog
              read minutes ago from one read just now. */}
          <p className="text-muted">As of {formatAbsolute(catalog.response.serverTime)}.</p>
          <RequiredCatalog response={catalog.response} deploy={deploy} />
        </>
      )}

      <div className="render-surface__controls">
        <button type="button" onClick={() => setRefreshToken((n) => n + 1)}>
          Reload
        </button>
        <ScopedButton
          requiredScope="cuecatalog:deploy"
          onClick={() => void runDeploy()}
          busy={deploy.kind === 'submitting'}
          busyReason="Deploying…"
        >
          Deploy catalog
        </ScopedButton>
        {deploy.kind === 'result' && <CueCatalogDeployOutcome result={deploy.result} />}
        {deploy.kind === 'error' && (
          <p role="alert" className="render-surface__error">
            {deploy.message}
          </p>
        )}
      </div>
    </>
  )
}

// RequiredCatalog renders exactly what GET /nodes/{nodeId}/cue-catalog
// carries: the coordinator's OWN live resolution for this node, recomputed
// on every call — never a persisted acknowledgement (CueCatalogResponse
// has no such field; TRACK-H-H3-SPEC.md section 4 stores that beside the
// node's asset report instead, reachable only through the acknowledge and
// deploy routes). "configured: false" is the honest-absence case the spec
// names, never a fabricated generation.
function RequiredCatalog({ response, deploy }: { response: CueCatalogResponse; deploy: DeployState }) {
  if (!response.configured) {
    return (
      <p className="text-muted" role="status">
        No active show is configured for this coordinator; this catalog authorizes nothing on this node.
      </p>
    )
  }

  return (
    <>
      <dl className="field-list">
        <dt>Required by the active show</dt>
        <dd>
          show {response.show} · generation {response.generation} · revision{' '}
          <code>{response.revision}</code>
        </dd>
        <dt>Cues in this catalog</dt>
        <dd>{response.entries.length}</dd>
      </dl>
      <HeldCatalog response={response} deploy={deploy} />
    </>
  )
}

// HeldCatalog is the "what does this node actually hold" half of the
// panel. There is no route that reads a node's persisted acknowledgement
// on demand (only POST .../acknowledge, which is the node's own report,
// and POST .../deploy's own result) — so before this panel's own deploy
// resolves, the honest answer is "not observed from here", stated plainly
// rather than guessed, matching this codebase's ADR-020 absence-is-stated
// convention (NodeAssetManifest.observedAt's identical null case).
function HeldCatalog({ response, deploy }: { response: CueCatalogResponse; deploy: DeployState }) {
  if (deploy.kind !== 'result' || deploy.result.outcome !== 'confirmed' || deploy.result.acknowledgedRevision === undefined) {
    return (
      <p className="text-muted" role="status">
        Held revision: not observed from this panel yet. Deploy to confirm what this node currently holds.
      </p>
    )
  }

  const held = deploy.result
  const current =
    held.acknowledgedRevision === response.revision && held.generation === response.generation

  return (
    <p className={current ? 'render-surface__confirmed' : 'render-surface__unconfirmed'} role={current ? 'status' : 'alert'}>
      Held revision <code>{held.acknowledgedRevision}</code> (generation {held.generation}), acknowledged{' '}
      {formatAbsolute(held.resolvedAt ?? null)}:{' '}
      {current
        ? 'current: matches what the active show requires now.'
        : `stale: the active show now requires revision ${response.revision ?? 'unknown'} (generation ${response.generation ?? 'unknown'}).`}
    </p>
  )
}

// CueCatalogDeployOutcome renders result.outcome literally, never
// inferring success from a bare 200 (ADR-003) — the same rule
// RenderSurfacePanel's own RenderCommandOutcome enforces for render
// commands, and FPPStopPlaylistControl's own FPPCommandOutcome enforces
// for FPP.
function CueCatalogDeployOutcome({ result }: { result: CueCatalogDeployResult }) {
  return (
    <div role="status">
      {result.replay && (
        <p className="text-muted">
          This was already requested (idempotency key already used); nothing new was
          dispatched; showing the original result.
        </p>
      )}
      {result.outcome === 'confirmed' && (
        <p className="render-surface__confirmed">
          Confirmed: the node reports holding revision {result.acknowledgedRevision}.
        </p>
      )}
      {result.outcome === 'unconfirmed' && (
        <p role="alert" className="render-surface__unconfirmed">
          Unconfirmed: {result.reason ?? 'no confirmation reason was given.'}
        </p>
      )}
      {(result.outcome === 'refused' || result.outcome === 'failed') && (
        <p role="alert" className="render-surface__unconfirmed">
          {result.outcome === 'refused' ? 'Refused' : 'Failed'}: {result.reason ?? 'no reason was given.'}
        </p>
      )}
      {result.outcome === '' && <p className="text-muted">Pending: this command has not yet resolved.</p>}
    </div>
  )
}
