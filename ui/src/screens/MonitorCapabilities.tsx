import { Link } from 'react-router-dom'
import { RuledStrip, Section, Table, TableWrap } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { MonitorHead } from './Monitor'
import { capabilityGroups, type CapabilityGroup } from './monitorModel'

export function MonitorCapabilities() {
  const model = useModelContext()
  const groups = capabilityGroups(model)
  const total = groups.reduce((sum, group) => sum + group.capabilities.length, 0)

  return (
    <>
      <MonitorHead model={model} />

      <Section
        id="mo-capabilities"
        title="Capabilities"
        detail="What each node has actually advertised, grouped by node. A capability a node has never advertised is nothing to observe, not a failure."
      >
        {groups.length === 0 ? (
          <RuledStrip
            absence={model.snapshotReceivedAt === null ? 'loading' : 'empty'}
            label={model.snapshotReceivedAt === null ? 'Reading' : 'None'}
            fact={model.snapshotReceivedAt === null ? 'No node history has arrived yet.' : 'No node is declared.'}
          />
        ) : (
          <>
            <TableWrap label="Node capabilities, scrollable">
              <Table>
                <thead>
                  <tr>
                    <th scope="col">Node</th>
                    <th scope="col">Capabilities</th>
                  </tr>
                </thead>
                <tbody>
                  {groups.map((group) => (
                    <CapabilityTableRow key={group.key} group={group} />
                  ))}
                </tbody>
              </Table>
            </TableWrap>
            <p className="sm-section__footnote">
              {groups.length} {groups.length === 1 ? 'node' : 'nodes'} · {total}{' '}
              {total === 1 ? 'capability' : 'capabilities'} advertised in total.
            </p>
          </>
        )}
      </Section>
    </>
  )
}

function CapabilityTableRow({ group }: { group: CapabilityGroup }) {
  return (
    <tr>
      <td>
        <Link to={group.nodeTo}>{group.node}</Link>
      </td>
      <td>
        {group.capabilities.length === 0 ? (
          <RuledStrip
            absence="unobserved"
            label="Never advertised"
            fact="Nothing to observe"
            detail="This node has never advertised a capability. Distinct from a capability that is failing."
          />
        ) : (
          <span className="sm-data">
            {group.capabilities.map((capability) => `${capability.id} · v${capability.version}`).join(', ')}
          </span>
        )}
      </td>
    </tr>
  )
}
