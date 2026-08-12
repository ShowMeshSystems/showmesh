import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useModelContext } from '../app/ModelContext'
import { ControlPlaneBadge, DeclarationBadge } from '../components/DomainBadges'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import { ScopedButton } from '../components/ScopedButton'
import { declareNode, deleteNodeDeclaration, runDiscovery } from '../api'
import type { DiscoveryProposal, DiscoveryRun } from '../app/types'
import { describeApiError } from '../app/session'

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
    <div>
      <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />
      <h2 className="panel__title">Nodes</h2>

      <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', marginBottom: '1rem' }}>
        <ScopedButton requiredScope="config:write" onClick={() => void handleRunDiscovery()}>
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
                  <ScopedButton requiredScope="config:write" onClick={() => void handleDeclare(p.nodeId)}>
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
        <ul className="list-plain">
          {model.nodes.map((node) => (
            <li key={node.nodeId}>
              <Link className="entity-link" to={`/nodes/${node.nodeId}`}>
                <div
                  style={{
                    display: 'flex',
                    justifyContent: 'space-between',
                    gap: '0.75rem',
                    flexWrap: 'wrap',
                  }}
                >
                  <strong>{node.label ?? node.nodeId}</strong>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <ControlPlaneBadge state={node.controlPlane.state} />
                    <DeclarationBadge declared={node.declaration.declared} discoveryState={node.declaration.discoveryState} />
                  </div>
                </div>
                <div className="text-muted">
                  {node.platform ?? 'platform unknown'} · {node.capabilities.length}{' '}
                  capabilit{node.capabilities.length === 1 ? 'y' : 'ies'} advertised
                </div>
              </Link>
              {node.declaration.declared && (
                <div style={{ marginTop: '0.25rem' }}>
                  <ScopedButton
                    requiredScope="config:write"
                    className="button-danger"
                    onClick={() => void handleUndeclare(node.nodeId)}
                  >
                    {busyId === node.nodeId ? 'Removing…' : 'Remove declaration'}
                  </ScopedButton>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
