import { useState } from 'react'
import { useModelContext } from '../app/ModelContext'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import { SeverityBadge } from '../components/DomainBadges'
import { PanelErrorBoundary } from '../components/PanelErrorBoundary'
import { OperatorPageHeader } from '../components/SharedLayouts'
import { MonitorTabs } from './monitor/MonitorTabs'
import { auditOutcomeLabel, auditPagingCursor, useAuditLog } from './Audit'
import { formatAbsolute } from '../app/time'
import type { AuditEntry, ShowMeshEvent } from '../app/types'
import '../styles/monitor.css'

// Monitor / Activity (UI-DESIGN-GUIDE.md section 3, Monitor.dc.html):
// "by event." Today's Events.tsx (system events) and Audit.tsx (operator
// actions) merged into ONE stream, newest first. Audit rows need the
// audit:read scope; system events do not -- gated ONLY on the audit
// portion (useAuditLog, Audit.tsx), never on the whole stream: a
// principal with no audit:read still sees every system event.
//
// The mock collapses many columns into Time / What happened / Source,
// but nothing the old two views tracked is lost: severity, subject,
// category, outcome and evidence state stay reachable per row through a
// disclosure button (`aria-expanded`), matching a system event's own
// category/source/occurred-vs-recorded distinction and an audit entry's
// own outcome/outcomeState/reason.
type ActivityRow =
  | { kind: 'event'; key: string; timeIso: string; headline: string; source: string; event: ShowMeshEvent }
  | { kind: 'audit'; key: string; timeIso: string; headline: string; source: string; entry: AuditEntry }

function eventRow(event: ShowMeshEvent): ActivityRow {
  return {
    kind: 'event',
    key: `event:${event.seq}`,
    timeIso: event.occurredAt ?? event.recordedAt,
    headline: event.summary,
    source: event.source,
    event,
  }
}

function auditRow(entry: AuditEntry): ActivityRow {
  return {
    kind: 'audit',
    key: `audit:${entry.id}`,
    timeIso: entry.timestamp,
    headline: `${entry.principalName} ${entry.action} ${entry.target}`.trim(),
    source: entry.principalName,
    entry,
  }
}

export function Events() {
  const model = useModelContext()
  const audit = useAuditLog()
  const [expanded, setExpanded] = useState<string | null>(null)

  const eventRows = model.events.map(eventRow)
  const auditRows = audit.state.kind === 'loaded' ? audit.state.entries.map(auditRow) : []
  const rows = [...eventRows, ...auditRows].sort((a, b) => (a.timeIso < b.timeIso ? 1 : a.timeIso > b.timeIso ? -1 : 0))

  return (
    <div className="operator-page monitor-activity">
      <OperatorPageHeader title="Monitor" />
      <MonitorTabs active="activity" />
      <div className="page-body" style={{ padding: '20px 28px 48px' }}>
        <h1 className="t-display" style={{ margin: 0 }}>Activity</h1>
        <p className="t-small text-muted" style={{ marginTop: '6px', maxWidth: '76ch' }}>
          System events and operator actions in one stream, newest first. Operator actions are
          audit records and need an audit-read scope; system events do not.
        </p>
        <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />

        {model.eventsGap && (
          <p role="status" className="ruled-strip ruled-strip--failed">
            Event history before this point has been permanently lost to retention.
            {model.oldestRetainedSeq !== null &&
              ` The oldest event still retained has sequence ${model.oldestRetainedSeq}.`}{' '}
            Reconnecting will not recover it.
          </p>
        )}

        {!audit.scopeGate.allowed && (
          <p className="t-small text-muted" role="status" style={{ marginTop: '10px' }}>
            Operator-action (audit) rows are not shown: {audit.scopeGate.reason} System events below
            are unaffected.
          </p>
        )}
        {audit.scopeGate.allowed && audit.state.kind === 'error' && (
          <p role="alert" className="ruled-strip ruled-strip--failed">
            Could not load operator-action rows: {audit.state.message}. System events below are
            unaffected.
          </p>
        )}
        {audit.scopeGate.allowed && audit.state.kind === 'unconfirmed-order' && (
          <p role="alert" className="ruled-strip ruled-strip--failed">
            {audit.state.message}
          </p>
        )}

        {rows.length === 0 ? (
          <p className="t-small text-muted">No activity recorded yet.</p>
        ) : (
          <div className="table-wrap" style={{ marginTop: '14px' }}>
            <table className="table table--full" aria-label="Activity">
              <thead>
                <tr>
                  <th scope="col" style={{ width: 90 }}>Time</th>
                  <th scope="col">What happened</th>
                  <th scope="col" style={{ width: 140 }}>Source</th>
                  <th scope="col" style={{ width: 40 }} />
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <PanelErrorBoundary key={row.key} panelLabel={row.headline}>
                    <ActivityRowView row={row} expanded={expanded === row.key} onToggle={() => setExpanded(expanded === row.key ? null : row.key)} />
                  </PanelErrorBoundary>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {audit.scopeGate.allowed && audit.state.kind === 'loaded' && !audit.state.atBeginning && auditPagingCursor(audit.state.entries) !== null && (
          <button
            type="button"
            className="btn btn--secondary btn--compact"
            style={{ marginTop: '12px' }}
            onClick={audit.loadOlder}
            disabled={audit.older.kind === 'loading'}
          >
            {audit.older.kind === 'loading' ? 'Loading older entries…' : 'Show older operator-action entries'}
          </button>
        )}
        {audit.older.kind === 'error' && (
          <p role="alert" className="ruled-strip ruled-strip--failed">
            {audit.older.message} The entries above are still what was loaded before this failure.
          </p>
        )}
      </div>
    </div>
  )
}

function ActivityRowView({ row, expanded, onToggle }: { row: ActivityRow; expanded: boolean; onToggle: () => void }) {
  return (
    <>
      <tr data-audit={row.kind === 'audit' ? true : undefined}>
        <td className="t-data" style={{ fontSize: 11, color: 'var(--text-muted)' }}>
          {new Date(row.timeIso).toLocaleTimeString()}
        </td>
        <td>{row.headline}</td>
        <td>
          {row.kind === 'event' ? (
            <span className="t-meta" style={{ color: 'var(--text-faint)' }}>{row.source}</span>
          ) : (
            <span className="t-meta" style={{ color: 'var(--accent)' }}>{row.source}</span>
          )}
        </td>
        <td>
          <button
            type="button"
            className="monitor-activity__expand"
            aria-expanded={expanded}
            onClick={onToggle}
          >
            {expanded ? 'Hide' : 'Detail'}
          </button>
        </td>
      </tr>
      {expanded && (
        <tr>
          <td colSpan={4} className="monitor-activity__detail">
            {row.kind === 'event' ? (
              <dl>
                <dt>Severity</dt>
                <dd><SeverityBadge severity={row.event.severity} /></dd>
                <dt>Subject</dt>
                <dd>{row.event.resource.kind}: {row.event.resource.id}</dd>
                <dt>Category</dt>
                <dd>{row.event.category}</dd>
                <dt>Occurred</dt>
                <dd>{row.event.occurredAt !== null ? formatAbsolute(row.event.occurredAt) : 'occurrence time unknown'}</dd>
                <dt>Recorded</dt>
                <dd>{formatAbsolute(row.event.recordedAt)}</dd>
              </dl>
            ) : (
              <dl>
                <dt>Principal</dt>
                <dd>{row.entry.principalName} ({row.entry.form})</dd>
                <dt>Kind</dt>
                <dd>{row.entry.kind}</dd>
                <dt>Target</dt>
                <dd>{row.entry.target}</dd>
                <dt>Outcome</dt>
                <dd>{auditOutcomeLabel(row.entry)}</dd>
                <dt>Evidence state</dt>
                <dd>{row.entry.outcomeState === '' ? '-' : row.entry.outcomeState}</dd>
                <dt>Reason</dt>
                <dd>{row.entry.outcomeReason === '' ? '-' : row.entry.outcomeReason}</dd>
                <dt>Recorded at</dt>
                <dd>{formatAbsolute(row.entry.timestamp)}</dd>
              </dl>
            )}
          </td>
        </tr>
      )}
    </>
  )
}
