import { Link } from 'react-router-dom'
import { useModelContext } from '../app/ModelContext'
import { presentModelObservations } from '../app/observationPresentation'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import { EvidenceValue } from '../components/EvidenceValue'
import { EmptyBlock, EvidenceTable, OperatorPageHeader } from '../components/SharedLayouts'
import '../styles/operator-pages.css'

export function Observations() {
  const model = useModelContext()
  const rows = presentModelObservations(model)
  const connected = model.connection.kind === 'live'

  return (
    <section className="operator-page monitor-observations" aria-label="Observations">
      <OperatorPageHeader
        title="Observations"
        lede="Latest reports from nodes, render surfaces, audio sessions, FPP players, and Resolume."
      />
      <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />
      {rows.length === 0 ? (
        <EmptyBlock title="No observations" reason="No observations have been recorded yet." />
      ) : (
        <EvidenceTable label="Latest observations">
          <table className="config-table monitor-observations__table">
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
                  <td><code>{row.signal}</code></td>
                  <td>
                    <EvidenceValue
                      evidence={row.evidence}
                      serverTime={model.serverTime}
                      serverTimeReceivedAt={model.serverTimeReceivedAt}
                      connected={connected}
                    />
                  </td>
                  <td>{row.evidence.source}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </EvidenceTable>
      )}
    </section>
  )
}
