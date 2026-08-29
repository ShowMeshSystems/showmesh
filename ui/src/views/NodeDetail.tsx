import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useModelContext } from '../app/ModelContext'
import { ControlPlaneBadge } from '../components/DomainBadges'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import { EvidenceValue } from '../components/EvidenceValue'
import { resolveCapabilityPanel } from '../components/capabilityPanelRegistry'
import { PanelErrorBoundary } from '../components/PanelErrorBoundary'
import { RenderSurfacePanel } from '../components/RenderSurfacePanel'
import { AudioSessionPanel } from '../components/AudioSessionPanel'
import { CueCatalogPanel } from '../components/CueCatalogPanel'
import { ScopedButton } from '../components/ScopedButton'
import { PlannedFeature } from '../components/SharedLayouts'
import { formatAbsolute } from '../app/time'
import { describeApiError } from '../app/session'
import { declareNode, deleteNodeDeclaration, getNodeAssetManifest, runDiscovery } from '../api'
import type { DiscoveryRun, NodeAssetManifest } from '../app/types'
import '../styles/monitor.css'

// Node detail (Node.dc.html; spec section 6.4 / OPERATOR-UI section
// 8.1): identity, control-plane evidence, capabilities, render/audio
// evidence, the Cue catalog (a panel the mock has no counterpart for --
// owner ruling: it stays, carried over unchanged), assets held locally
// with their match verdicts, and the "Remove this node" danger zone
// whose copy names what removal orphans.
export function NodeDetail() {
  const { nodeId } = useParams<{ nodeId: string }>()
  const model = useModelContext()
  const node = model.nodes.find((candidate) => candidate.nodeId === nodeId)
  const connected = model.connection.kind === 'live'

  return (
    <section className="monitor-detail monitor-node-detail" aria-labelledby="node-detail-title">
      <div className="page-body" style={{ padding: '22px 28px 80px', maxWidth: '1120px' }}>
        <p className="t-small text-muted">
          <Link to="/monitor/fleet">Monitor</Link> <span aria-hidden="true">/</span> Fleet{' '}
          <span aria-hidden="true">/</span> {nodeId}
        </p>
        <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />

        {!node ? (
          <p className="t-small text-muted">
            No node with ID "{nodeId}" is in the current inventory. It may have been removed, or
            the snapshot this view is showing is out of date.
          </p>
        ) : (
          <>
            <div className="monitor-detail-header">
              <div style={{ minWidth: 0 }}>
                <div className="monitor-detail-header__title">
                  <h1 id="node-detail-title" className="t-display" style={{ margin: 0 }}>
                    {node.label ?? node.nodeId}
                  </h1>
                  <ControlPlaneBadge state={node.controlPlane.state} />
                </div>
                <p className="monitor-detail-header__sub">
                  {node.render.length > 0 ? 'Render node' : node.audio.length > 0 ? 'Audio node' : 'Node'} ·
                  everything configured about this box, in one place
                </p>
              </div>
              <div className="monitor-detail-header__actions">
                <NodeRunDiscoveryButton />
              </div>
            </div>

            {node.controlPlane.state !== 'online' && (
              <div className="monitor-field-row" style={{ marginTop: '20px', borderTop: '1px solid var(--border)' }}>
                <span
                  className="monitor-needs-operator__state"
                  style={{ color: node.controlPlane.state === 'offline' ? 'var(--bad-fg)' : 'var(--unk-fg)' }}
                >
                  {node.controlPlane.state === 'offline' ? '✕ Not reporting' : '? Control-plane unknown'}
                </span>
                <p className="t-small text-muted" style={{ margin: 0 }}>
                  {node.controlPlane.reason ?? 'Everything below is its last known state; configuration is still stored and still valid.'}
                </p>
              </div>
            )}

            <section aria-labelledby="nd-ident" className="monitor-section" style={{ maxWidth: '760px' }}>
              <h2 id="nd-ident">Identity</h2>
              <IdentityFields nodeId={node.nodeId} />
              <div className="monitor-field-list">
                <div className="monitor-field-row">
                  <span className="monitor-field-row__label">Node id</span>
                  <span className="monitor-field-row__value">{node.nodeId}</span>
                </div>
                <div className="monitor-field-row">
                  <span className="monitor-field-row__label">Declared</span>
                  <span className="t-small">
                    {node.declaration.declared
                      ? `${formatAbsolute(node.declaration.declaredAt)}${node.declaration.declaredByPrincipalName !== null ? ` by ${node.declaration.declaredByPrincipalName}` : ''}`
                      : 'Not declared'}
                  </span>
                </div>
                <div className="monitor-field-row">
                  <span className="monitor-field-row__label">Agent build</span>
                  <span>
                    <span className="t-data">{node.agentVersion ?? 'unknown'}</span>
                    <br />
                    <span className="t-small" style={{ color: 'var(--text-faint)' }}>
                      Platform {node.platform ?? 'unknown'} · boot {node.bootId ?? 'unknown'} · started{' '}
                      {formatAbsolute(node.startedAt)}. As of its last report, not now.
                    </span>
                  </span>
                </div>
              </div>
            </section>

            <PanelErrorBoundary panelLabel="Control-plane evidence">
              <section className="monitor-section" aria-labelledby="nd-signals">
                <h2 id="nd-signals">Control-plane evidence</h2>
                <div className="table-wrap" style={{ marginTop: '12px' }}>
                  <table className="table" aria-label="Control-plane evidence">
                    <thead>
                      <tr>
                        <th scope="col">Signal</th>
                        <th scope="col">Value</th>
                      </tr>
                    </thead>
                    <tbody>
                      <tr>
                        <th scope="row">hello (advertisement)</th>
                        <td>
                          <EvidenceValue
                            evidence={node.evidence.hello}
                            serverTime={model.serverTime}
                            serverTimeReceivedAt={model.serverTimeReceivedAt}
                            connected={connected}
                          />
                        </td>
                      </tr>
                      <tr>
                        <th scope="row">last will</th>
                        <td>
                          <EvidenceValue
                            evidence={node.evidence.lastWill}
                            serverTime={model.serverTime}
                            serverTimeReceivedAt={model.serverTimeReceivedAt}
                            connected={connected}
                          />
                        </td>
                      </tr>
                      <tr>
                        <th scope="row">heartbeat</th>
                        <td>
                          <EvidenceValue
                            evidence={node.evidence.heartbeat}
                            serverTime={model.serverTime}
                            serverTimeReceivedAt={model.serverTimeReceivedAt}
                            connected={connected}
                          />
                        </td>
                      </tr>
                    </tbody>
                  </table>
                </div>
              </section>
            </PanelErrorBoundary>

            <section className="monitor-section" aria-labelledby="nd-caps" style={{ maxWidth: '760px' }}>
              <div className="monitor-section__header">
                <h2 id="nd-caps">Capabilities</h2>
                <span className="t-small text-muted">What the node says it can do, not what we assume</span>
              </div>
              {node.capabilities.length === 0 ? (
                <p className="t-small text-muted">This node advertises no capabilities.</p>
              ) : (
                <div className="panel-grid">
                  {node.capabilities.map((capability) => {
                    const Panel = resolveCapabilityPanel(capability.id)
                    return (
                      <PanelErrorBoundary key={`${capability.id}@${capability.version}`} panelLabel={capability.id}>
                        <Panel capability={capability} />
                      </PanelErrorBoundary>
                    )
                  })}
                </div>
              )}
            </section>

            <PanelErrorBoundary panelLabel="Cue catalog">
              <section className="monitor-section" aria-labelledby="nd-cues">
                <h2 id="nd-cues">Cue catalog</h2>
                <CueCatalogPanel nodeId={node.nodeId} />
              </section>
            </PanelErrorBoundary>

            <PanelErrorBoundary panelLabel="Render">
              <section className="monitor-section" aria-labelledby="nd-surf">
                <div className="monitor-section__header">
                  <h2 id="nd-surf">Surfaces on this node</h2>
                  <span className="t-small text-muted">Authored per show, not per node</span>
                </div>
                <RenderSurfacePanel nodeId={node.nodeId} entries={node.render} />
              </section>
            </PanelErrorBoundary>

            <PanelErrorBoundary panelLabel="Audio">
              <section className="monitor-section" aria-labelledby="nd-audio">
                <h2 id="nd-audio">Audio</h2>
                <AudioSessionPanel nodeId={node.nodeId} entries={node.audio} />
              </section>
            </PanelErrorBoundary>

            <PanelErrorBoundary panelLabel="Assets held locally">
              <NodeAssetsPanel nodeId={node.nodeId} snapshotReceivedAt={model.snapshotReceivedAt} />
            </PanelErrorBoundary>

            <NodeDangerZone nodeId={node.nodeId} declared={node.declaration.declared} />
          </>
        )}
      </div>
    </section>
  )
}

// The mock's editable Label field (Node.dc.html): declareNode's own
// POST /nodes/{id}/declaration is idempotent on an already-declared node
// (it updates label/notes rather than re-declaring), so this reuses that
// one write rather than inventing a second "rename" endpoint.
function IdentityFields({ nodeId }: { nodeId: string }) {
  const model = useModelContext()
  const node = model.nodes.find((n) => n.nodeId === nodeId)
  const [label, setLabel] = useState(node?.label ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  if (node === undefined) return null

  async function handleSave() {
    setBusy(true)
    setError(null)
    setSaved(false)
    try {
      await declareNode(nodeId, label, node!.declaration.notes ?? '')
      setSaved(true)
    } catch (err) {
      setError(describeApiError(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="monitor-field-row" style={{ borderTop: '1px solid var(--border-strong)', marginTop: '10px' }}>
      <span className="monitor-field-row__label">Label</span>
      <div style={{ display: 'flex', alignItems: 'center', gap: '8px', flexWrap: 'wrap' }}>
        <input
          type="text"
          className="field__input"
          style={{ maxWidth: '340px' }}
          value={label}
          onChange={(e) => {
            setLabel(e.target.value)
            setSaved(false)
          }}
          aria-label="Node label"
        />
        <ScopedButton
          requiredScope="config:write"
          className="btn btn--secondary btn--compact"
          onClick={() => void handleSave()}
          busy={busy}
          busyReason="Already saving."
        >
          {busy ? 'Saving…' : 'Save label'}
        </ScopedButton>
        {saved && <span className="t-small" style={{ color: 'var(--good-fg)' }}>Saved.</span>}
        {error !== null && (
          <span role="alert" className="t-small" style={{ color: 'var(--bad-fg)' }}>
            {error}
          </span>
        )}
      </div>
    </div>
  )
}

function NodeRunDiscoveryButton() {
  const [discovering, setDiscovering] = useState(false)
  const [result, setResult] = useState<DiscoveryRun | null>(null)
  const [error, setError] = useState<string | null>(null)

  async function handleRun() {
    setDiscovering(true)
    setError(null)
    try {
      const resp = await runDiscovery()
      setResult(resp.run)
    } catch (err) {
      setError(describeApiError(err))
    } finally {
      setDiscovering(false)
    }
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: '4px' }}>
      <ScopedButton
        requiredScope="config:write"
        className="btn btn--secondary"
        onClick={() => void handleRun()}
        busy={discovering}
        busyReason="A discovery run is already in progress."
      >
        {discovering ? 'Running discovery…' : 'Run discovery'}
      </ScopedButton>
      {result !== null && (
        <span className="t-small text-muted">
          Run {result.id}: {result.complete ? `complete, found ${result.foundCount}` : `incomplete${result.reason !== null ? ` (${result.reason})` : ''}`}
        </span>
      )}
      {error !== null && (
        <span role="alert" className="t-small" style={{ color: 'var(--bad-fg)' }}>
          {error}
        </span>
      )}
    </div>
  )
}

type AssetsState = { kind: 'loading' } | { kind: 'error'; message: string } | { kind: 'loaded'; manifest: NodeAssetManifest }

function useNodeAssetManifest(nodeId: string, snapshotReceivedAt: number | null): AssetsState {
  const [state, setState] = useState<AssetsState>({ kind: 'loading' })
  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    getNodeAssetManifest(nodeId)
      .then((resp) => {
        if (!cancelled) setState({ kind: 'loaded', manifest: resp.manifest })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [nodeId, snapshotReceivedAt])
  return state
}

// "Assets held locally" (Node.dc.html): readiness is not "the file
// exists," it is that the variant assigned to THIS node matches the
// expected artifact. `NodeAssetManifest` (schema.d.ts) states this as
// exceptions -- missing/gaps/extra -- not a full per-sequence match
// table, so this renders exactly those three lists rather than inventing
// a "Matches" row the API does not return for assets that are simply
// fine.
function NodeAssetsPanel({ nodeId, snapshotReceivedAt }: { nodeId: string; snapshotReceivedAt: number | null }) {
  const state = useNodeAssetManifest(nodeId, snapshotReceivedAt)
  return (
    <section className="monitor-section" aria-labelledby="nd-assets">
      <div className="monitor-section__header">
        <h2 id="nd-assets">Assets held locally</h2>
      </div>
      <p className="monitor-section__lede">
        Readiness is not "the file exists" -- it is that the variant assigned to this node
        matches the expected artifact. The node plays from its own disk, so this is what will
        actually run.
      </p>
      {state.kind === 'loading' && <p className="t-small text-muted">Loading…</p>}
      {state.kind === 'error' && (
        <p role="alert" className="ruled-strip ruled-strip--failed">
          {state.message}
        </p>
      )}
      {state.kind === 'loaded' && (
        <div className="table-wrap" style={{ marginTop: '12px' }}>
          <table className="table" aria-label="Assets held locally">
            <thead>
              <tr>
                <th scope="col">Sequence / file</th>
                <th scope="col">Hash</th>
                <th scope="col">State</th>
              </tr>
            </thead>
            <tbody>
              {state.manifest.missing.length === 0 && state.manifest.extra.length === 0 && state.manifest.gaps.length === 0 ? (
                <tr>
                  <td colSpan={3} className="t-small text-muted">
                    No exceptions: this node holds every asset its manifest expects, at the expected hash.
                  </td>
                </tr>
              ) : (
                <>
                  {state.manifest.missing.map((m) => (
                    <tr key={`missing:${m.assetId}`}>
                      <td>{m.sequence} · {m.filename}</td>
                      <td className="t-data" style={{ fontSize: 11, color: 'var(--text-muted)' }}>{m.contentHash.slice(0, 4)}…{m.contentHash.slice(-2)}</td>
                      <td className="t-meta" style={{ color: 'var(--text-faint)' }}>Not synced</td>
                    </tr>
                  ))}
                  {state.manifest.gaps.map((g) => (
                    <tr key={`gap:${g.sequence}`}>
                      <td>{g.sequence} (surfaces: {g.surfaces.join(', ')})</td>
                      <td>-</td>
                      <td className="t-meta" style={{ color: 'var(--warn-fg)' }}>No coverage</td>
                    </tr>
                  ))}
                  {state.manifest.extra.map((e) => (
                    <tr key={`extra:${e.contentHash}`}>
                      <td>{e.filename}</td>
                      <td className="t-data" style={{ fontSize: 11, color: 'var(--text-muted)' }}>{e.contentHash.slice(0, 4)}…{e.contentHash.slice(-2)}</td>
                      <td className="t-meta" style={{ color: 'var(--text-faint)' }}>Held, not expected</td>
                    </tr>
                  ))}
                </>
              )}
            </tbody>
          </table>
          <div className="monitor-table-note">
            {state.manifest.state === 'unknown'
              ? 'Unknown: no evidence to date this manifest by.'
              : state.manifest.observedAt !== null
                ? `Observed ${formatAbsolute(state.manifest.observedAt)}.`
                : 'No evidence to date this by.'}
            {state.manifest.reason !== null && ` ${state.manifest.reason}`}
          </div>
        </div>
      )}
      {/* Node.dc.html draws a disabled "Re-sync all" button here. No
          endpoint exists to command a node to re-fetch its assets on
          demand (grep of api/generated/schema.d.ts turns up
          GET /nodes/{nodeId}/assets, the read this panel already uses,
          and nothing that writes). */}
      <PlannedFeature
        title="Re-sync all"
        why="No endpoint exists to command a node to re-fetch its held assets on demand. GET /nodes/{nodeId}/assets (used above) is read-only; asset delivery itself is driven by the node's own sync loop, not an operator button."
        preview={
          <button type="button" className="btn btn--secondary btn--compact" disabled>
            Re-sync all
          </button>
        }
      />
    </section>
  )
}

// "Remove this node" (Node.dc.html): copy names what removal orphans.
// This build's model has no cross-show surface index (fleetRows.ts /
// RenderSurfacePanel both note this same absence), so the orphaned-asset
// sentence states the general consequence rather than inventing a named
// surface this checkout cannot look up.
function NodeDangerZone({ nodeId, declared }: { nodeId: string; declared: boolean }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [removed, setRemoved] = useState(false)

  if (!declared) return null

  async function handleRemove() {
    if (!window.confirm(`Remove the declaration for "${nodeId}"? This does not affect anything the node is currently doing.`)) {
      return
    }
    setBusy(true)
    setError(null)
    try {
      await deleteNodeDeclaration(nodeId)
      setRemoved(true)
    } catch (err) {
      setError(describeApiError(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className="monitor-section" aria-labelledby="nd-danger" style={{ maxWidth: '760px' }}>
      <h2 id="nd-danger">Remove this node</h2>
      <div className="monitor-danger">
        <p>
          Undeclaring drops this node from inventory. Any surface or audio routing assigned to it
          would render or route nowhere until it is pointed at another node. Files already on the
          node&rsquo;s disk are not removed.
        </p>
        {removed && (
          <p role="status" className="t-small" style={{ color: 'var(--good-fg)', marginTop: '10px' }}>
            Declaration removed.
          </p>
        )}
        {error !== null && (
          <p role="alert" className="t-small" style={{ color: 'var(--bad-fg)', marginTop: '10px' }}>
            {error}
          </p>
        )}
        <ScopedButton
          requiredScope="config:write"
          className="btn btn--destructive"
          onClick={() => void handleRemove()}
          busy={busy}
          busyReason="Already removing this declaration."
        >
          {busy ? 'Removing…' : 'Remove declaration'}
        </ScopedButton>
        <p className="t-small monitor-danger__footnote">
          Asks for the node id before it proceeds. This is not the unsaved-changes dialog.
        </p>
      </div>
    </section>
  )
}
