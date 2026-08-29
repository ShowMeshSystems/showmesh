import { useState } from 'react'
import { ScopedButton } from '../components/ScopedButton'
import { declareNode, runDiscovery } from '../api'
import type { DiscoveryProposal, DiscoveryRun } from '../app/types'
import { describeApiError } from '../app/session'
import '../styles/monitor.css'

// BUILD-PLAN Step 7 seam B (RES-008 D2/D6): node discovery and
// declaration. Formerly the whole of the standalone `/nodes` list
// (NodesList); Monitor's Fleet facet (Monitor.tsx) now carries every
// declared node as a Fleet row, so this file's remaining, still-real job
// is the part a resource ROW cannot do -- surface an UNDECLARED
// candidate and let an operator declare it. `runDiscovery` performs NO
// active probing (spec: it reads what this coordinator already
// observes), so an empty proposal list after a run does not mean nothing
// exists.
//
// Removing a node's declaration moved to its own detail page
// (NodeDetail.tsx's "Remove this node" section, matching Node.dc.html) --
// undeclaring is a fact about ONE already-known node, which now has its
// own route, rather than something this fleet-wide panel needs to offer
// a second time.
export function NodeDiscoveryPanel() {
  const [discovering, setDiscovering] = useState(false)
  const [lastRun, setLastRun] = useState<DiscoveryRun | null>(null)
  const [proposals, setProposals] = useState<DiscoveryProposal[]>([])
  const [error, setError] = useState<string | null>(null)
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
      setProposals((prev) => prev.filter((p) => p.nodeId !== nodeId))
    } catch (err) {
      setError(describeApiError(err))
    } finally {
      setBusyId(null)
    }
  }

  return (
    <section className="monitor-section" aria-labelledby="monitor-discovery-title">
      <div className="monitor-section__header">
        <h2 id="monitor-discovery-title">Discovery</h2>
        <ScopedButton
          requiredScope="config:write"
          className="btn btn--secondary btn--compact"
          onClick={() => void handleRunDiscovery()}
          busy={discovering}
          busyReason="A discovery run is already in progress."
        >
          {discovering ? 'Running discovery…' : 'Run discovery'}
        </ScopedButton>
      </div>
      <p className="monitor-section__lede">
        Reads what this coordinator already observes; it does not probe the network and cannot
        find equipment that has never talked to ShowMesh.
      </p>

      {error !== null && (
        <p role="alert" className="ruled-strip ruled-strip--failed">
          {error}
        </p>
      )}

      {lastRun !== null && (
        <div className="card" style={{ marginTop: '12px', padding: '12px 14px' }}>
          <strong>Discovery run {lastRun.id}</strong>{' '}
          <span className="text-muted">
            ({lastRun.complete ? 'complete' : 'incomplete'}, found {lastRun.foundCount})
          </span>
          {!lastRun.complete && lastRun.reason !== null && (
            <p role="alert" className="ruled-strip ruled-strip--failed">
              Did not complete: {lastRun.reason}
            </p>
          )}
          {proposals.length === 0 ? (
            <p className="t-small text-muted">No undeclared entities observed.</p>
          ) : (
            <ul className="list-plain">
              {proposals.map((p) => (
                <li key={p.nodeId} style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                  <span>
                    {p.nodeId} <span className="text-muted">({p.source})</span>
                  </span>
                  <ScopedButton
                    requiredScope="config:write"
                    className="btn btn--secondary btn--compact"
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
    </section>
  )
}
