import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useModelContext } from '../app/ModelContext'
import { observationDisplayState, presentModelObservations } from '../app/observationPresentation'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import { StatusBadge } from '../components/StatusBadge'
import { OperatorPageHeader } from '../components/SharedLayouts'
import { MonitorTabs } from './monitor/MonitorTabs'
import { buildFleetRows, buildNeedsOperatorItems, formatLastReport } from './monitor/fleetRows'
import { NodeDiscoveryPanel } from './NodesList'
import { FPPVersionSkewNotice } from './FPPList'
import '../styles/monitor.css'

// Monitor / Fleet (UI-DESIGN-GUIDE.md section 3, Monitor.dc.html):
// replaces the separate Nodes/FPP/Resolume lists with ONE table across
// every resource, Kind as a column. "Select any resource for its full
// evidence" -- rows are links into the detail routes, never a second
// summary. Health is exactly what the resource itself reported
// (fleetRows.ts); a ShowMesh-side binding/import problem is a distinct
// `annotation`, and the "Needs an operator" strips above the table are
// the only other place that class of fact appears.
const KIND_FILTERS = ['All', 'Node', 'FPP', 'Resolume'] as const
type KindFilter = (typeof KIND_FILTERS)[number]

export function Monitor() {
  const model = useModelContext()
  const [kindFilter, setKindFilter] = useState<KindFilter>('All')
  const allRows = buildFleetRows(model)
  const rows = kindFilter === 'All' ? allRows : allRows.filter((row) => row.kind === kindFilter)
  const needsOperator = buildNeedsOperatorItems(model)
  const observations = presentModelObservations(model)

  const nodeCount = model.nodes.length
  const nodesOnline = model.nodes.filter((n) => n.controlPlane.state === 'online').length
  const fppCount = model.fpp.length
  const fppHealthy = model.fpp.filter((f) => f.health === 'healthy').length
  const resolumeCount = model.resolume.length
  const resolumeHealthy = model.resolume.filter((r) => r.health === 'healthy').length
  const current = observations.filter((r) => observationDisplayState(r.evidence) === 'current').length
  const stale = observations.filter((r) => observationDisplayState(r.evidence) === 'stale').length
  const unobserved = observations.filter((r) => observationDisplayState(r.evidence) === 'unobserved').length

  return (
    <div className="operator-page monitor-page">
      <OperatorPageHeader
        title="Monitor"
        lede="Every resource the coordinator observes, in one place. Health is what was reported, never what was assumed."
        actions={
          <StatusBadge
            tone={model.connection.kind === 'live' ? 'good' : 'unknown'}
            icon={model.connection.kind === 'live' ? '●' : '?'}
            label={model.connection.kind === 'live' ? 'Coordinator live' : 'Coordinator not live'}
          />
        }
      />
      <MonitorTabs active="fleet" counts={{ fleet: allRows.length, signals: observations.length }} />

      <div className="page-body" style={{ padding: '20px 28px 48px' }}>
        <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />

        <dl className="monitor-stats" aria-label="Fleet summary">
          <div className="monitor-stats__item">
            <p className={`monitor-stats__label${nodesOnline < nodeCount ? ' monitor-stats__label--bad' : ' monitor-stats__label--good'}`}>Nodes</p>
            <p className="monitor-stats__value">{nodesOnline} / {nodeCount}</p>
            <p className="monitor-stats__detail">{nodeCount - nodesOnline} offline or unknown</p>
          </div>
          <div className="monitor-stats__item">
            <p className={`monitor-stats__label${fppHealthy < fppCount ? ' monitor-stats__label--warn' : ' monitor-stats__label--good'}`}>FPP players</p>
            <p className="monitor-stats__value">{fppHealthy} / {fppCount}</p>
            <p className="monitor-stats__detail">healthy</p>
          </div>
          <div className="monitor-stats__item">
            <p className={`monitor-stats__label${resolumeHealthy < resolumeCount ? ' monitor-stats__label--warn' : ' monitor-stats__label--good'}`}>Resolume</p>
            <p className="monitor-stats__value">{resolumeHealthy} / {resolumeCount}</p>
            <p className="monitor-stats__detail">healthy</p>
          </div>
          <div className="monitor-stats__item">
            <p className="monitor-stats__label">Signals current</p>
            <p className="monitor-stats__value">{current} / {observations.length}</p>
            <p className="monitor-stats__detail">{stale} stale · {unobserved} unobserved</p>
          </div>
        </dl>

        <section className="monitor-section" aria-labelledby="monitor-needs-title">
          <h2 id="monitor-needs-title">Needs an operator</h2>
          {needsOperator.length === 0 ? (
            <p className="t-small text-muted" style={{ marginTop: '10px' }}>
              Nothing needs an operator right now. That is not proof the show looks right, only
              that nothing has asked for you.
            </p>
          ) : (
            <div className="monitor-needs-operator">
              {needsOperator.map((item) => (
                <div key={item.key} className="monitor-needs-operator__row">
                  <span className={`monitor-needs-operator__state monitor-needs-operator__state--${item.tone}`}>
                    {item.stateLabel}
                  </span>
                  <div>
                    <p className="monitor-needs-operator__headline">
                      <Link to={item.headlineHref}>{item.headline}</Link>
                    </p>
                    <p className="monitor-needs-operator__explanation">{item.explanation}</p>
                  </div>
                </div>
              ))}
            </div>
          )}
        </section>

        <NodeDiscoveryPanel />

        <section className="monitor-section" aria-labelledby="monitor-fleet-title">
          <div className="monitor-section__header">
            <h2 id="monitor-fleet-title">Fleet</h2>
            <div className="segmented" role="group" aria-label="Filter by kind">
              {KIND_FILTERS.map((kind) => (
                <button
                  key={kind}
                  type="button"
                  className="segmented__option"
                  aria-pressed={kindFilter === kind}
                  onClick={() => setKindFilter(kind)}
                >
                  {kind}
                </button>
              ))}
            </div>
          </div>
          <p className="monitor-section__lede">
            One table instead of three lists. Select any resource for its full evidence.
          </p>
          <FPPVersionSkewNotice />

          {allRows.length === 0 ? (
            <p className="t-small text-muted">No resources are configured on this coordinator yet.</p>
          ) : rows.length === 0 ? (
            <p className="t-small text-muted">No {kindFilter} resources are configured on this coordinator.</p>
          ) : (
            <div className="table-wrap">
              <table className="table table--full" aria-label="Fleet">
                <thead>
                  <tr>
                    <th scope="col">Resource</th>
                    <th scope="col">Kind</th>
                    <th scope="col">Health</th>
                    <th scope="col" style={{ textAlign: 'right' }}>Last report</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((row) => (
                    <tr key={row.key} data-clickable>
                      <td>
                        <Link className="entity-link t-body" to={row.href} style={{ fontWeight: 600 }}>
                          {row.name}
                        </Link>
                        {(row.sub !== null || row.annotation !== null) && (
                          <span className="monitor-resource-cell__sub">
                            {row.sub}
                            {row.sub !== null && row.annotation !== null && ' · '}
                            {row.annotation !== null && <span style={{ color: 'var(--warn-fg)' }}>{row.annotation}</span>}
                          </span>
                        )}
                      </td>
                      <td>
                        <span className="monitor-kind-chip">{row.kind}</span>
                      </td>
                      <td>
                        <StatusBadge tone={row.health.tone} icon={row.health.icon} label={row.health.label} />
                      </td>
                      <td className="t-data" style={{ textAlign: 'right', fontSize: 11, color: 'var(--text-muted)' }}>
                        {formatLastReport(row.lastReportAt, model)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <p className="monitor-table-note">
                {allRows.length} resources · {nodeCount} node{nodeCount === 1 ? '' : 's'}, {fppCount} FPP player
                {fppCount === 1 ? '' : 's'}, {resolumeCount} Resolume instance{resolumeCount === 1 ? '' : 's'}. Health
                is each resource&rsquo;s own report; binding and import problems are separate signals.
              </p>
            </div>
          )}
        </section>
      </div>
    </div>
  )
}
