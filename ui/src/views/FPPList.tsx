import { Link } from 'react-router-dom'
import { useModelContext } from '../app/ModelContext'
import { FPPHealthBadge } from '../components/DomainBadges'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'

export function FPPList() {
  const model = useModelContext()

  return (
    <div>
      <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />
      <h2 className="panel__title">FPP instances</h2>
      {model.fpp.length === 0 ? (
        <p className="text-muted">No FPP instances are configured on this coordinator.</p>
      ) : (
        <ul className="list-plain">
          {model.fpp.map((instance) => (
            <li key={instance.instanceId}>
              <Link className="entity-link" to={`/fpp/${instance.instanceId}`}>
                <div
                  style={{
                    display: 'flex',
                    justifyContent: 'space-between',
                    gap: '0.75rem',
                    flexWrap: 'wrap',
                  }}
                >
                  <strong>{instance.instanceId}</strong>
                  <FPPHealthBadge health={instance.health} />
                </div>
                <div className="text-muted">{instance.endpoint}</div>
                {instance.lastPollError !== null && (
                  <div className="evidence__reason">{instance.lastPollError}</div>
                )}
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
