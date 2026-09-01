import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { getServiceDescriptor, type ServiceDescriptor } from '../api'
import { DefinitionStrip, RuledStrip, Section, SelectableRow, Table, TableWrap } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError } from '../domain/session'
import { MonitorHead } from './Monitor'
import { capabilityGroups, type CapabilityGroup } from './monitorModel'

type BuildState = { kind: 'loading' } | { kind: 'loaded'; descriptor: ServiceDescriptor } | { kind: 'failed'; reason: string }

/** D-002: the coordinator build string lives here, as a definition row inside this page's one existing block, never a new section. */
function useCoordinatorBuild(): BuildState {
  const [state, setState] = useState<BuildState>({ kind: 'loading' })
  useEffect(() => {
    let cancelled = false
    getServiceDescriptor()
      .then((descriptor) => {
        if (!cancelled) setState({ kind: 'loaded', descriptor })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [])
  return state
}

export function MonitorCapabilities() {
  const model = useModelContext()
  const navigate = useNavigate()
  const groups = capabilityGroups(model)
  const total = groups.reduce((sum, group) => sum + group.capabilities.length, 0)
  const build = useCoordinatorBuild()

  return (
    <>
      <MonitorHead model={model} />

      <Section
        id="mo-capabilities"
        title="Capabilities"
        detail="What each node has actually advertised, grouped by node. A capability a node has never advertised is nothing to observe, not a failure."
      >
        {build.kind === 'failed' ? (
          <RuledStrip absence="failed" label="Build unread" fact={build.reason} detail="The rest of this page's node capabilities are unaffected." />
        ) : build.kind === 'loaded' ? (
          <DefinitionStrip
            items={[
              {
                term: 'Coordinator',
                value: <span className="sm-data">{build.descriptor.coordinator.version} · {build.descriptor.coordinator.commit}</span>,
                detail: <span className="sm-small sm-muted">Serving API version {build.descriptor.apiVersion}</span>,
              },
            ]}
          />
        ) : null}
        {groups.length === 0 ? (
          <RuledStrip
            absence={model.snapshotReceivedAt === null ? 'loading' : 'empty'}
            label={model.snapshotReceivedAt === null ? 'Reading' : 'None'}
            fact={model.snapshotReceivedAt === null ? 'No node history has arrived yet.' : 'No node is declared.'}
          />
        ) : (
          <>
            <TableWrap label="Node capabilities, scrollable">
              <Table minWidth={520}>
                <thead>
                  <tr>
                    <th scope="col">Node</th>
                    <th scope="col">Capabilities</th>
                  </tr>
                </thead>
                <tbody>
                  {groups.map((group) => (
                    <CapabilityTableRow key={group.key} group={group} onOpen={() => navigate(group.nodeTo)} />
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

function CapabilityTableRow({ group, onOpen }: { group: CapabilityGroup; onOpen: () => void }) {
  return (
    <SelectableRow onActivate={onOpen} ariaLabel={`Open ${group.node}`}>
      <td>
        <strong>{group.node}</strong>
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
    </SelectableRow>
  )
}
