import { Link } from 'react-router-dom'
import { useModelContext } from '../app/ModelContext'
import { FPPHealthBadge } from '../components/DomainBadges'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import { FleetSignalBadge } from '../components/FleetSignalBadge'
import { findObservation, summarizeFleetVersions } from '../app/fppSignals'

export function FPPList() {
  const model = useModelContext()

  // Presentation of collected facts only (spec section 6 "Version skew"):
  // this states which fpp.version values were reported and by whom, and
  // whether they disagree. It never ranks a version as right or wrong, and
  // it is not folded into FPPHealthBadge -- disagreement is a stated
  // condition of a fleet that legitimately runs a master build on one
  // remote, not a synthesized health verdict.
  const versionSummary = summarizeFleetVersions(model.fpp)

  return (
    <div>
      <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />
      <h2 className="panel__title">FPP instances</h2>
      {model.fpp.length === 0 ? (
        <p className="text-muted">No FPP instances are configured on this coordinator.</p>
      ) : (
        <>
          {versionSummary.disagreement && (
            <p className="text-muted" role="status">
              Reported FPP versions do not agree across the fleet:{' '}
              {versionSummary.versions
                .map((entry) => `${entry.version} (${entry.instanceIds.length})`)
                .join(', ')}
              . This states what each instance reported, not a fault.
            </p>
          )}
          <ul className="list-plain">
            {model.fpp.map((instance) => {
              const version = findObservation(instance.observations, 'fpp.version')
              return (
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
                    {/* Step 5 review finding 6: this used to print
                        version.value bare, ignoring its Evidence.state --
                        so a version pulled from a retained/unknown-age
                        source (the fpp-ghost ghost's exact shape) rendered as
                        a confident version string. FleetSignalBadge is the
                        same state-carrying renderer FleetSignalBadge/
                        PortGrid already use elsewhere for this exact
                        reason (its own header comment). */}
                    <div className="text-muted">
                      <FleetSignalBadge label="version" evidence={version} />
                    </div>
                    {instance.lastPollError !== null && (
                      <div className="evidence__reason">{instance.lastPollError}</div>
                    )}
                  </Link>
                </li>
              )
            })}
          </ul>
        </>
      )}
    </div>
  )
}
