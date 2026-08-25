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
          {/* One row per instance, so endpoint, health and version line up
              in columns instead of each instance being its own panel. */}
          <div className="table-scroll">
            <table className="config-table">
              <thead>
                <tr>
                  <th scope="col">Instance</th>
                  <th scope="col">Endpoint</th>
                  <th scope="col">Health</th>
                  <th scope="col">Version</th>
                  <th scope="col">Last poll error</th>
                </tr>
              </thead>
              <tbody>
                {model.fpp.map((instance) => {
                  const version = findObservation(instance.observations, 'fpp.version')
                  return (
                    <tr key={instance.instanceId}>
                      <th scope="row">
                        <Link className="entity-link" to={`/fpp/${instance.instanceId}`}>
                          {instance.instanceId}
                        </Link>
                      </th>
                      <td>{instance.endpoint}</td>
                      <td>
                        <FPPHealthBadge health={instance.health} />
                      </td>
                      <td>
                        {/* Step 5 review finding 6: this used to print
                            version.value bare, ignoring its Evidence.state --
                            so a version pulled from a retained/unknown-age
                            source (the fpp-ghost ghost's exact shape) rendered as
                            a confident version string. FleetSignalBadge is the
                            same state-carrying renderer FleetSignalBadge/
                            PortGrid already use elsewhere for this exact
                            reason (its own header comment). */}
                        <FleetSignalBadge label="version" evidence={version} />
                      </td>
                      <td>{instance.lastPollError !== null && <span className="evidence__reason">{instance.lastPollError}</span>}</td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </>
      )}
    </div>
  )
}
