import { Link } from 'react-router-dom'
import { useModelContext } from '../app/ModelContext'
import { presentModelObservations } from '../app/observationPresentation'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import { EvidenceValue } from '../components/EvidenceValue'
import { EmptyBlock, EvidenceTable, OperatorPageHeader } from '../components/SharedLayouts'
import { MonitorTabs } from './monitor/MonitorTabs'
import '../styles/operator-pages.css'
import '../styles/monitor.css'

// Monitor / Signals (UI-DESIGN-GUIDE.md section 3, Monitor.dc.html):
// "by observation, across every resource." The mock names this facet and
// its count but does not draw its own panel, so this is today's
// Observations.tsx table, restyled to the design's `.table` primitive
// and given the facet strip. Every filter and control the old view had
// is carried forward -- there were none beyond the table itself, so
// nothing here was dropped.
export function Observations() {
  const model = useModelContext()
  const rows = presentModelObservations(model)
  const connected = model.connection.kind === 'live'

  return (
    <div className="operator-page monitor-observations">
      <OperatorPageHeader title="Monitor" />
      <MonitorTabs active="signals" counts={{ signals: rows.length }} />
      <div className="page-body" style={{ padding: '20px 28px 48px' }}>
        <h1 className="t-display" style={{ margin: 0 }}>Signals</h1>
        <p className="t-small text-muted" style={{ marginTop: '6px', maxWidth: '76ch' }}>
          Latest reports from nodes, render surfaces, audio sessions, FPP players, and Resolume,
          by observation, across every resource.
        </p>
        <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />
        {rows.length === 0 ? (
          <EmptyBlock title="No observations" reason="No observations have been recorded yet." />
        ) : (
          <EvidenceTable label="Latest observations">
            <table className="table table--full monitor-observations__table" aria-label="Latest observations">
              <caption className="visually-hidden">Latest observations</caption>
              <thead>
                <tr>
                  <th scope="col">Resource</th>
                  <th scope="col">Meaning</th>
                  <th scope="col">Signal</th>
                  <th scope="col">Report</th>
                  <th scope="col">Source</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr key={`${row.resource.kind}:${row.resource.id}:${row.signal}`}>
                    <th scope="row">
                      {row.href === null ? `${row.resource.kind}: ${row.resource.id}` : <Link className="entity-link" to={row.href}>{row.resource.id}</Link>}
                    </th>
                    <td>{row.meaning}</td>
                    <td className="t-data">{row.signal}</td>
                    <td>
                      <EvidenceValue
                        evidence={row.evidence}
                        serverTime={model.serverTime}
                        serverTimeReceivedAt={model.serverTimeReceivedAt}
                        connected={connected}
                      />
                    </td>
                    <td className="t-small text-muted">{row.evidence.source}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </EvidenceTable>
        )}
      </div>
    </div>
  )
}
