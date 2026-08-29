import { Link } from 'react-router-dom'
import { useModelContext } from '../app/ModelContext'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import { PanelErrorBoundary } from '../components/PanelErrorBoundary'
import { OperatorPageHeader } from '../components/SharedLayouts'
import { MonitorTabs } from './monitor/MonitorTabs'
import type { Node } from '../app/types'
import '../styles/monitor.css'

// Monitor / Capabilities (UI-DESIGN-GUIDE.md section 3, Monitor.dc.html):
// "by capability, across nodes." Grouped by advertised capability
// identifier, never by a fixed node class -- there is no
// `if (node.type === ...)` anywhere in this file or this build. The
// grouping key is exactly the string a node advertised (ADR-002); an
// identifier this UI has never seen before gets its own group exactly
// like a familiar one.
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
    <div className="operator-page monitor-capabilities">
      <OperatorPageHeader title="Monitor" />
      <MonitorTabs active="capabilities" counts={{ capabilities: groups.length }} />
      <div className="page-body" style={{ padding: '20px 28px 48px' }}>
        <h1 className="t-display" style={{ margin: 0 }}>Capabilities</h1>
        <p className="t-small text-muted" style={{ marginTop: '6px', maxWidth: '76ch' }}>
          Every advertised capability, grouped across nodes rather than by a fixed node class.
        </p>
        <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />
        {groups.length === 0 ? (
          <p className="t-small text-muted">No node has advertised any capability yet.</p>
        ) : (
          <div className="panel-grid" style={{ marginTop: '14px' }}>
            {groups.map((group) => (
              <PanelErrorBoundary key={group.id} panelLabel={group.id}>
                <section className="card" style={{ padding: '14px' }}>
                  <h2 className="t-subhead" style={{ margin: 0 }}>{group.id}</h2>
                  <ul className="list-plain">
                    {group.members.map(({ node, version }) => (
                      <li key={node.nodeId}>
                        <Link className="entity-link" to={`/monitor/fleet/node/${encodeURIComponent(node.nodeId)}`}>
                          <strong>{node.label ?? node.nodeId}</strong>{' '}
                          <span className="text-muted">v{version}</span>
                        </Link>
                      </li>
                    ))}
                  </ul>
                </section>
              </PanelErrorBoundary>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
