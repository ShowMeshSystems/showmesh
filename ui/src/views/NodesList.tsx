import { Link } from 'react-router-dom'
import { useModelContext } from '../app/ModelContext'
import { ControlPlaneBadge } from '../components/DomainBadges'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'

export function NodesList() {
  const model = useModelContext()

  return (
    <div>
      <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />
      <h2 className="panel__title">Nodes</h2>
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
                  <ControlPlaneBadge state={node.controlPlane.state} />
                </div>
                <div className="text-muted">
                  {node.platform ?? 'platform unknown'} · {node.capabilities.length}{' '}
                  capabilit{node.capabilities.length === 1 ? 'y' : 'ies'} advertised
                </div>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
