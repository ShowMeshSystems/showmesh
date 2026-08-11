import { Link, useParams } from 'react-router-dom'
import { useModelContext } from '../app/ModelContext'
import { FPPHealthBadge } from '../components/DomainBadges'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import { EvidenceValue } from '../components/EvidenceValue'
import { PanelErrorBoundary } from '../components/PanelErrorBoundary'
import { formatAbsolute } from '../app/time'

// FPP instance detail. Not named in spec section 6.4's view list, which
// enumerates Dashboard/Nodes/Capabilities/Events, but OBSERVABILITY
// section 6.1 requires that "every aggregate health indicator must allow
// drill-down to its contributing evidence," and an FPPInstance's `health`
// is exactly such an aggregate over its `observations` list -- this view
// is that drill-down, in the same shape as the node detail view.
export function FPPDetail() {
  const { instanceId } = useParams<{ instanceId: string }>()
  const model = useModelContext()
  const instance = model.fpp.find((candidate) => candidate.instanceId === instanceId)
  const connected = model.connection.kind === 'live'

  return (
    <div>
      <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />
      <p>
        <Link to="/fpp">← All FPP instances</Link>
      </p>

      {!instance ? (
        <p className="text-muted">
          No FPP instance with ID "{instanceId}" is currently configured. It may have
          been removed, or the snapshot this view is showing is out of date.
        </p>
      ) : (
        <>
          <h2 className="panel__title">{instance.instanceId}</h2>

          <PanelErrorBoundary panelLabel="FPP instance summary">
            <section className="panel">
              <div style={{ marginBottom: '0.75rem' }}>
                <FPPHealthBadge health={instance.health} />
              </div>
              <dl className="field-list">
                <dt>Endpoint</dt>
                <dd>{instance.endpoint}</dd>
                <dt>Last poll</dt>
                <dd>{formatAbsolute(instance.lastPollAt)}</dd>
                <dt>Last poll error</dt>
                <dd>{instance.lastPollError ?? 'none'}</dd>
              </dl>
            </section>
          </PanelErrorBoundary>

          <h3 className="section-title">Observations</h3>
          {instance.observations.length === 0 ? (
            <p className="text-muted">This instance has no recorded observations.</p>
          ) : (
            <PanelErrorBoundary panelLabel="Observations">
              <section className="panel">
                {instance.observations.map((observation) => (
                  <EvidenceValue
                    key={observation.signal}
                    label={observation.signal}
                    evidence={observation}
                    serverTime={model.serverTime}
                    serverTimeReceivedAt={model.serverTimeReceivedAt}
                    connected={connected}
                  />
                ))}
              </section>
            </PanelErrorBoundary>
          )}
        </>
      )}
    </div>
  )
}
