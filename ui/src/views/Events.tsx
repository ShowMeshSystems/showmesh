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
        <ul className="list-plain">
          {model.events.map((event) => (
            <PanelErrorBoundary key={event.seq} panelLabel={`Event ${event.seq}`}>
              <li className="panel">
                <div
                  style={{ display: 'flex', justifyContent: 'space-between', gap: '0.75rem', flexWrap: 'wrap' }}
                >
                  <SeverityBadge severity={event.severity} />
                  <span className="text-muted">
                    {event.resource.kind}: {event.resource.id}
                  </span>
                </div>
                <p style={{ margin: '0.5rem 0' }}>{event.summary}</p>
                <dl className="field-list">
                  <dt>Occurred</dt>
                  <dd>{event.occurredAt !== null ? formatAbsolute(event.occurredAt) : 'occurrence time unknown'}</dd>
                  <dt>Recorded</dt>
                  <dd>{formatAbsolute(event.recordedAt)}</dd>
                  <dt>Category</dt>
                  <dd>{event.category}</dd>
                  <dt>Source</dt>
                  <dd>{event.source}</dd>
                </dl>
              </li>
            </PanelErrorBoundary>
          ))}
        </ul>
      )}
    </div>
  )
}
