import { useState } from 'react'
import { Section, Segmented, SelectableRow, StatusPair, Table, TableWrap, RuledStrip } from '../kit'
import { useNavigate } from 'react-router-dom'
import { useModelContext } from '../app/ModelContext'
import { effectiveServerTimeIso } from '../domain/time'
import { MonitorHead } from './Monitor'
import { signalRows, signalSummary, type FleetKind, type SignalRow } from './monitorModel'

const KINDS: readonly { value: FleetKind; label: string }[] = [
  { value: 'all', label: 'All' },
  { value: 'node', label: 'Nodes' },
  { value: 'fpp', label: 'FPP' },
  { value: 'resolume', label: 'Resolume' },
]

export function MonitorSignals() {
  const model = useModelContext()
  const navigate = useNavigate()
  const nowIso = effectiveServerTimeIso(model.serverTime, model.serverTimeReceivedAt, Date.now())
  const [kind, setKind] = useState<FleetKind>('all')

  const rows = signalRows(model, nowIso)
  const shown = kind === 'all' ? rows : rows.filter((row) => row.kind === kind)

  return (
    <>
      <MonitorHead model={model} />

      <Section
        id="mo-signals"
        title="Signals"
        aside={<Segmented label="Resource kind" value={kind} options={KINDS} onChange={setKind} />}
        detail="Every observation the coordinator holds, across nodes, FPP and Resolume, in one table. Kind narrows it the same way Fleet does."
      >
        {shown.length === 0 ? (
          <RuledStrip
            absence={model.snapshotReceivedAt === null ? 'loading' : 'empty'}
            label={model.snapshotReceivedAt === null ? 'Reading' : 'None'}
            fact={
              model.snapshotReceivedAt === null
                ? 'No signal history has arrived yet.'
                : kind === 'all'
                  ? 'No resource has reported a signal.'
                  : `No ${kind} resource has reported a signal.`
            }
          />
        ) : (
          <>
            <TableWrap label="Signals, scrollable">
              <Table>
                <thead>
                  <tr>
                    <th scope="col">Resource</th>
                    <th scope="col">Signal</th>
                    <th scope="col">Value</th>
                    <th scope="col">State</th>
                    <th scope="col">Observed</th>
                  </tr>
                </thead>
                <tbody>
                  {shown.map((row) => (
                    <SignalTableRow key={row.key} row={row} onOpen={() => navigate(row.resourceTo)} />
                  ))}
                </tbody>
              </Table>
            </TableWrap>
            <p className="sm-section__footnote">{signalSummary(shown)}</p>
          </>
        )}
      </Section>
    </>
  )
}

function SignalTableRow({ row, onOpen }: { row: SignalRow; onOpen: () => void }) {
  return (
    <SelectableRow onActivate={onOpen} ariaLabel={`Open ${row.resource}`}>
      <td>
        <strong>{row.resource}</strong>
      </td>
      <td className="sm-data">{row.signal}</td>
      <td className="sm-data">{row.value}</td>
      <td>
        <StatusPair tone={row.tone} label={row.state} />
      </td>
      <td className="sm-data">{row.observed}</td>
    </SelectableRow>
  )
}
