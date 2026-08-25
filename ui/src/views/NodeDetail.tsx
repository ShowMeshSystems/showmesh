import { Link, useParams } from 'react-router-dom'
import { useModelContext } from '../app/ModelContext'
import { ControlPlaneBadge } from '../components/DomainBadges'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import { EvidenceValue } from '../components/EvidenceValue'
import { resolveCapabilityPanel } from '../components/capabilityPanelRegistry'
import { PanelErrorBoundary } from '../components/PanelErrorBoundary'
import { RenderSurfacePanel } from '../components/RenderSurfacePanel'
import { formatAbsolute } from '../app/time'

// Node detail (spec section 6.4 / OPERATOR-UI section 8.1): control-plane
// state with its reason, agent version, platform, boot ID, started-at,
// first-seen, last update, and every advertised capability with its own
// status. Each capability renders through resolveCapabilityPanel's lookup
// table (CapabilityPanel.tsx), individually wrapped in an error boundary
// so a capability with a surprising shape cannot blank the rest of the page.
export function NodeDetail() {
  const { nodeId } = useParams<{ nodeId: string }>()
  const model = useModelContext()
  const node = model.nodes.find((candidate) => candidate.nodeId === nodeId)
  const connected = model.connection.kind === 'live'

  return (
    <div>
      <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />
      <p>
        <Link to="/nodes">← All nodes</Link>
      </p>

      {!node ? (
        <p className="text-muted">
          No node with ID "{nodeId}" is in the current inventory. It may have been removed,
          or the snapshot this view is showing is out of date.
        </p>
      ) : (
        <>
          <h2 className="panel__title">{node.label ?? node.nodeId}</h2>

          {/* Node summary, control-plane evidence, and render are genuine
              sibling panels for this node, so they share the wide-display
              two-column grid (.panel-grid, global.css). */}
          <div className="panel-grid">
            <PanelErrorBoundary panelLabel="Node summary">
              <section className="panel">
                <div style={{ marginBottom: '0.75rem' }}>
                  <ControlPlaneBadge state={node.controlPlane.state} />
                  {node.controlPlane.reason !== null && (
                    <p className="evidence__reason">{node.controlPlane.reason}</p>
                  )}
                </div>
                <dl className="field-list">
                  <dt>Node ID</dt>
                  <dd>{node.nodeId}</dd>
                  <dt>Platform</dt>
                  <dd>{node.platform ?? 'unknown'}</dd>
                  <dt>Agent version</dt>
                  <dd>{node.agentVersion ?? 'unknown'}</dd>
                  <dt>Boot ID</dt>
                  <dd>{node.bootId ?? 'unknown'}</dd>
                  <dt>Started at</dt>
                  <dd>{formatAbsolute(node.startedAt)}</dd>
                  <dt>First seen</dt>
                  <dd>{formatAbsolute(node.firstSeenAt)}</dd>
                  <dt>Last update</dt>
                  <dd>{formatAbsolute(node.updatedAt)}</dd>
                </dl>
              </section>
            </PanelErrorBoundary>

            <PanelErrorBoundary panelLabel="Control-plane evidence">
              <section className="panel">
                <h3 className="panel__title">Control-plane evidence</h3>
                {/* One row per signal, so twenty signals scan as a column
                    instead of a wall of stacked evidence blocks. */}
                <div className="table-scroll">
                  <table className="config-table" aria-label="Control-plane evidence">
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

            <PanelErrorBoundary panelLabel="Render">
              <section className="panel">
                <h3 className="panel__title">Render</h3>
                <RenderSurfacePanel nodeId={node.nodeId} entries={node.render} />
              </section>
            </PanelErrorBoundary>
          </div>

          <h3 className="section-title">Capabilities</h3>
          {node.capabilities.length === 0 ? (
            <p className="text-muted">This node advertises no capabilities.</p>
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
        </>
      )}
    </div>
  )
}
