import { Link } from 'react-router-dom'
import { useModelContext } from '../app/ModelContext'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import { PanelErrorBoundary } from '../components/PanelErrorBoundary'
import type { Node } from '../app/types'

// Navigation by function rather than by machine (spec section 6.4,
// OPERATOR-UI section 8.1): grouped by advertised capability identifier,
// never by a fixed node class -- there is no `if (node.type === ...)`
// anywhere in this file or this build. The grouping key is exactly the
// string a node advertised (ADR-002); an identifier this UI has never
// seen before gets its own group exactly like a familiar one.
interface CapabilityGroup {
  id: string
  members: Array<{ node: Node; version: number }>
}

function groupByCapability(nodes: Node[]): CapabilityGroup[] {
  const groups = new Map<string, CapabilityGroup>()
  for (const node of nodes) {
    for (const capability of node.capabilities) {
      let group = groups.get(capability.id)
      if (!group) {
        group = { id: capability.id, members: [] }
        groups.set(capability.id, group)
      }
      group.members.push({ node, version: capability.version })
    }
  }
  return [...groups.values()].sort((a, b) => a.id.localeCompare(b.id))
}

export function Capabilities() {
  const model = useModelContext()
  const groups = groupByCapability(model.nodes)

  return (
    <div>
      <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />
      <h2 className="panel__title">Capabilities</h2>
      {groups.length === 0 ? (
        <p className="text-muted">No node has advertised any capability yet.</p>
      ) : (
        groups.map((group) => (
          <PanelErrorBoundary key={group.id} panelLabel={group.id}>
            <section className="panel">
              <h3 className="panel__title">{group.id}</h3>
              <ul className="list-plain">
                {group.members.map(({ node, version }) => (
                  <li key={node.nodeId}>
                    <Link className="entity-link" to={`/nodes/${node.nodeId}`}>
                      <strong>{node.label ?? node.nodeId}</strong>{' '}
                      <span className="text-muted">v{version}</span>
                    </Link>
                  </li>
                ))}
              </ul>
            </section>
          </PanelErrorBoundary>
        ))
      )}
    </div>
  )
}
