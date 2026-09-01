import { Link } from 'react-router-dom'
import { RuledStrip, Section } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { MonitorHead } from './Monitor'
import { capabilityGroups, capabilityLine, type CapabilityGroup } from './monitorModel'

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
            {groups.map((group) => (
              <CapabilityNodeGroup key={group.key} group={group} />
            ))}
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

function CapabilityNodeGroup({ group }: { group: CapabilityGroup }) {
  const headingId = `mo-cap-${group.key}`
  return (
    <section className="sm-subsection" aria-labelledby={headingId}>
      <h3 id={headingId} className="sm-subsection__title">
        <Link to={group.nodeTo}>{group.heading}</Link>
      </h3>
      {group.capabilities.length === 0 ? (
        <RuledStrip
          absence="unobserved"
          label="Never advertised"
          fact="Nothing to observe"
          detail="This node has never advertised a capability. Distinct from a capability that is failing."
        />
      ) : (
        group.capabilities.map((capability, index) => (
          <p key={`${group.key}:${capability.id}:${index}`} className="sm-data">
            {capabilityLine(capability)}
          </p>
        ))
      )}
    </section>
  )
}
