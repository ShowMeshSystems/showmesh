import { useModelContext } from '../app/ModelContext'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import { SeverityBadge } from '../components/DomainBadges'
import { PanelErrorBoundary } from '../components/PanelErrorBoundary'
import { formatAbsolute } from '../app/time'

// Event history (spec section 6.4): newest first (model.events is already
// ordered newest-first per spec section 5.5), severity-distinguished,
// with the resource reference. `gap: true` is surfaced as permanently
// lost history, never retried -- there is nothing to retry, the events
// no longer exist anywhere in the system (api/openapi.yaml's top-level
// description). `occurredAt: null` is rendered as an unknown occurrence
// time, distinct from `recordedAt` (when the coordinator learned about
// it), never silently substituted for it.
export function Events() {
  const model = useModelContext()

  return (
    <div>
      <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />
      <h2 className="panel__title">Events</h2>

      {model.eventsGap && (
        <p className="panel panel--error" role="status">
          Event history before this point has been permanently lost to retention.
          {model.oldestRetainedSeq !== null &&
            ` The oldest event still retained has sequence ${model.oldestRetainedSeq}.`}{' '}
          Reconnecting will not recover it.
        </p>
      )}

      {model.events.length === 0 ? (
        <p className="text-muted">No events recorded yet.</p>
      ) : (
        // One row per event, so severity, message, subject and timing line
        // up in columns instead of each event being its own scrolled panel.
        <div className="table-scroll">
          <table className="config-table">
            <thead>
              <tr>
                <th scope="col">Severity</th>
                <th scope="col">Message</th>
                <th scope="col">Subject</th>
                <th scope="col">Occurred</th>
                <th scope="col">Recorded</th>
                <th scope="col">Category</th>
                <th scope="col">Source</th>
              </tr>
            </thead>
            <tbody>
              {model.events.map((event) => (
                <PanelErrorBoundary key={event.seq} panelLabel={`Event ${event.seq}`}>
                  <tr>
                    <th scope="row">
                      <SeverityBadge severity={event.severity} />
                    </th>
                    <td>{event.summary}</td>
                    <td>
                      {event.resource.kind}: {event.resource.id}
                    </td>
                    <td>{event.occurredAt !== null ? formatAbsolute(event.occurredAt) : 'occurrence time unknown'}</td>
                    <td>{formatAbsolute(event.recordedAt)}</td>
                    <td>{event.category}</td>
                    <td>{event.source}</td>
                  </tr>
                </PanelErrorBoundary>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
