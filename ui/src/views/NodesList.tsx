import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useModelContext } from '../app/ModelContext'
import { ControlPlaneBadge, DeclarationBadge } from '../components/DomainBadges'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import { ScopedButton } from '../components/ScopedButton'
import { declareNode, deleteNodeDeclaration, runDiscovery } from '../api'
import type { DiscoveryProposal, DiscoveryRun } from '../app/types'
import { describeApiError } from '../app/session'
import '../styles/operator-pages.css'

// BUILD-PLAN Step 7 seam B (RES-008 D2/D6): node discovery and
// declaration, laid on top of Step 4's read-only node list. The honest
// consequence B1 requires stated, not implied — discovery reads what this
// coordinator already observes (agent hellos, configured FPP instances)
// and performs NO active probing, so a "Run discovery" pass that finds
// nothing does not mean nothing exists, only that nothing new has ever
// talked to ShowMesh — see the text under an empty proposal list below.
//
// Every write here goes through ScopedButton (ADR-024 decision 12): a
// stale or unavailable scope list renders the control disabled with a
// stated reason, never silently enabled, and the control is never simply
// omitted for a principal who cannot use it.

export function NodesList() {
  const model = useModelContext()
  const [discovering, setDiscovering] = useState(false)
  const [lastRun, setLastRun] = useState<DiscoveryRun | null>(null)
  const [proposals, setProposals] = useState<DiscoveryProposal[]>([])
  const [error, setError] = useState<string | null>(null)
  // busyId names the one node id currently mid-write (declare/undeclare),
  // so this view never issues two overlapping writes for the same node
  // from a double click, without needing a per-row state object.
  const [busyId, setBusyId] = useState<string | null>(null)

  async function handleRunDiscovery() {
    setDiscovering(true)
    setError(null)
    try {
      const resp = await runDiscovery()
      setLastRun(resp.run)
      setProposals(resp.proposals)
    } catch (err) {
      setError(describeApiError(err))
    } finally {
      setDiscovering(false)
    }
  }

  async function handleDeclare(nodeId: string) {
    setBusyId(nodeId)
    setError(null)
    try {
      await declareNode(nodeId, '', '')
      // The declaration itself, and this node's rendering, arrive through
      // the ordinary snapshot/stream path (contract section 6.5) — never
      // guessed at here. Removing the now-declared entry from the local
      // proposal list is this component's own bookkeeping only.
      setProposals((prev) => prev.filter((p) => p.nodeId !== nodeId))
    } catch (err) {
      setError(describeApiError(err))
    } finally {
      setBusyId(null)
    }
  }

  async function handleUndeclare(nodeId: string) {
    // A UI-level confirmation in ADDITION to, never instead of, the
    // server's own required {"confirm":true} body (BUILD-PLAN Step 7 seam
    // B B2) — deleteNodeDeclaration always sends that regardless of what
    // happens here.
    if (!window.confirm(`Remove the declaration for "${nodeId}"? This does not affect anything the node is currently doing.`)) {
      return
    }
    setBusyId(nodeId)
    setError(null)
    try {
      await deleteNodeDeclaration(nodeId)
    } catch (err) {
      setError(describeApiError(err))
    } finally {
      setBusyId(null)
    }
  }

  return (
    <section className="monitor-detail monitor-node-list" aria-labelledby="nodes-title">
      <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />
      <header className="monitor-detail__header">
        <div>
          <p className="monitor-detail__eyebrow">Monitor</p>
          <h1 id="nodes-title">Nodes</h1>
          <p className="operator-page__lede text-muted">Node liveness, declarations, capabilities, and available controls.</p>
        </div>
      </header>

      <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', marginBottom: '1rem' }}>
        {/* BUILD-PLAN Step 7 seam B review defect 7: a double-click here
            used to start two overlapping discovery runs, which can leave
            every declared node in the installation reading not_seen (see
            api/discovery.go's handleStartDiscoveryRun doc comment for the
            interleaving failure this, and the server's own serialization,
            both guard against). `busy` disables the control the instant
            the first click's request is in flight, distinctly from the
            requiredScope-denied path ScopedButton already handles. */}
        <ScopedButton
          requiredScope="config:write"
          onClick={() => void handleRunDiscovery()}
          busy={discovering}
          busyReason="A discovery run is already in progress."
        >
          {discovering ? 'Running discovery…' : 'Run discovery'}
        </ScopedButton>
        <span className="text-muted">
          Reads what this coordinator already observes; it does not probe the network and cannot find
          equipment that has never talked to ShowMesh.
        </span>
      </div>

      {error !== null && <p className="text-error">{error}</p>}

      {lastRun !== null && (
        <div className="panel" style={{ marginBottom: '1rem' }}>
          <strong>Discovery run {lastRun.id}</strong>{' '}
          <span className="text-muted">
            ({lastRun.complete ? 'complete' : 'INCOMPLETE'}, found {lastRun.foundCount})
          </span>
          {!lastRun.complete && lastRun.reason !== null && (
            <p className="text-error">Did not complete: {lastRun.reason}</p>
          )}
          {proposals.length === 0 ? (
            <p className="text-muted">No undeclared entities observed.</p>
          ) : (
            <ul className="list-plain">
              {proposals.map((p) => (
                <li key={p.nodeId} style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                  <span>
                    {p.nodeId} <span className="text-muted">({p.source})</span>
                  </span>
                  <ScopedButton
                    requiredScope="config:write"
                    onClick={() => void handleDeclare(p.nodeId)}
                    busy={busyId === p.nodeId}
                    busyReason="Already declaring this node."
                  >
                    {busyId === p.nodeId ? 'Declaring…' : 'Declare'}
                  </ScopedButton>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      {model.nodes.length === 0 ? (
        <p className="text-muted">No nodes have advertised themselves yet.</p>
      ) : (
        // A table so twenty nodes' platforms, capability counts and status
        // badges line up in columns instead of each being its own panel.
        <div className="table-scroll">
          <table className="config-table">
            <thead>
              <tr>
                <th scope="col">Node</th>
                <th scope="col">Platform</th>
                <th scope="col">Capabilities</th>
                <th scope="col">Status</th>
                <th scope="col">Actions</th>
              </tr>
            </thead>
            <tbody>
              {model.nodes.map((node) => (
                <tr key={node.nodeId}>
                  <th scope="row">
                    <Link className="entity-link" to={`/nodes/${node.nodeId}`}>
                      {node.label ?? node.nodeId}
                    </Link>
                  </th>
                  <td>{node.platform ?? 'platform unknown'}</td>
                  <td>
                    {node.capabilities.length} capabilit{node.capabilities.length === 1 ? 'y' : 'ies'} advertised
                  </td>
                  <td>
                    <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                      <ControlPlaneBadge state={node.controlPlane.state} />
                      <DeclarationBadge declared={node.declaration.declared} discoveryState={node.declaration.discoveryState} />
                    </div>
                  </td>
                  <td>
                    {node.declaration.declared && (
                      <ScopedButton
                        requiredScope="config:write"
                        className="button-danger"
                        onClick={() => void handleUndeclare(node.nodeId)}
                        busy={busyId === node.nodeId}
                        busyReason="Already removing this declaration."
                      >
                        {busyId === node.nodeId ? 'Removing…' : 'Remove declaration'}
                      </ScopedButton>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}
