import { Link } from 'react-router-dom'
import { useModelContext } from '../app/ModelContext'
import { observationDisplayState, presentModelObservations } from '../app/observationPresentation'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import { StatusBadge } from '../components/StatusBadge'
import '../styles/operator-pages.css'

const MONITOR_DESTINATIONS = [
  { href: '/monitor', label: 'Overview', detail: 'A shallow view of fleet status and evidence coverage.' },
  { href: '/nodes', label: 'Nodes', detail: 'Node liveness, capabilities, render, and audio evidence.' },
  { href: '/fpp', label: 'FPP players', detail: 'FPP health, playback, controller, and port evidence.' },
  { href: '/observations', label: 'Observations', detail: 'Every latest report with its state, source, and freshness.' },
] as const

export function Monitor() {
  const model = useModelContext()
  const observations = presentModelObservations(model)
  const current = observations.filter((row) => observationDisplayState(row.evidence) === 'current').length
  const stale = observations.filter((row) => observationDisplayState(row.evidence) === 'stale').length
  const failed = observations.filter((row) => observationDisplayState(row.evidence) === 'failed').length
  const unobserved = observations.filter((row) => observationDisplayState(row.evidence) === 'unobserved').length
  const offlineNodes = model.nodes.filter((node) => node.controlPlane.state === 'offline').length
  const unknownNodes = model.nodes.filter((node) => node.controlPlane.state === 'unknown').length
  const unhealthyFpp = model.fpp.filter((instance) => instance.health === 'failed' || instance.health === 'degraded').length

  return (
    <section className="operator-page monitor-page" aria-labelledby="monitor-title">
      <header className="operator-page__header monitor-page__header">
        <div>
          <h1 id="monitor-title" className="operator-page__title">Monitor</h1>
          <p className="operator-page__lede text-muted">
            Current operational evidence from the coordinator. Open a resource for its full report and controls.
          </p>
        </div>
        <StatusBadge
          tone={model.connection.kind === 'live' ? 'good' : 'unknown'}
          icon={model.connection.kind === 'live' ? '●' : '?'}
          label={model.connection.kind === 'live' ? 'coordinator connected' : 'coordinator not live'}
        />
      </header>

      <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />

      <nav className="monitor-page__nav" aria-label="Monitor sections">
        <ul>
          {MONITOR_DESTINATIONS.map((destination) => (
            <li key={destination.href}>
              <Link to={destination.href} className="monitor-page__nav-link">
                <span>
                  <strong>{destination.label}</strong>
                  <span className="monitor-page__nav-detail">{destination.detail}</span>
                </span>
                <span aria-hidden="true">→</span>
              </Link>
            </li>
          ))}
        </ul>
      </nav>

      <section className="monitor-page__section" aria-labelledby="monitor-summary-title">
        <div className="monitor-page__section-heading">
          <div>
            <h2 id="monitor-summary-title">At a glance</h2>
            <p className="text-muted">Counts stay shallow here. Details and reasons live on the resource views.</p>
          </div>
        </div>
        <dl className="monitor-page__summary">
          <div>
            <dt>Nodes</dt>
            <dd>{model.nodes.length}</dd>
            <span>{offlineNodes} offline, {unknownNodes} unknown</span>
          </div>
          <div>
            <dt>FPP players</dt>
            <dd>{model.fpp.length}</dd>
            <span>{unhealthyFpp} degraded or failed</span>
          </div>
          <div>
            <dt>Latest evidence</dt>
            <dd>{observations.length}</dd>
            <span>{current} current, {stale} stale</span>
          </div>
          <div>
            <dt>Needs follow-up</dt>
            <dd>{failed + unobserved}</dd>
            <span>{failed} failed, {unobserved} unobserved</span>
          </div>
        </dl>
      </section>
    </section>
  )
}
