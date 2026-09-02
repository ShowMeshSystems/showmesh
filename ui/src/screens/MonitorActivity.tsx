import { useEffect, useRef, useState } from 'react'
import { type AuditEntry, listAudit, type Model } from '../api'
import { Notice, RuledStrip, Section, StatusPair, Table, TableWrap } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { MonitorHead } from './Monitor'
import { mergedActivityRows } from './monitorModel'

type AuditState =
  | { kind: 'denied'; reason: string }
  | { kind: 'loading' }
  | { kind: 'loaded'; entries: AuditEntry[]; receivedAt: number }
  | { kind: 'failed'; reason: string; entries: AuditEntry[]; receivedAt: number | null }

/**
 * The operator-action half of the merged stream. `entries` carries the
 * last successful read even after a later failure, per the "a refresh
 * failure retains the last known state" rule.
 */
function useAuditEntries(model: Model): AuditState {
  const scope = evaluateScope(model.session, model.sessionFetchFailed, 'audit:read')
  const scopeReason = scope.allowed ? null : scope.reason
  const [state, setState] = useState<AuditState>(scope.allowed ? { kind: 'loading' } : { kind: 'denied', reason: scopeReason ?? '' })
  const lastGood = useRef<{ entries: AuditEntry[]; receivedAt: number } | null>(null)

  useEffect(() => {
    if (!scope.allowed) {
      setState({ kind: 'denied', reason: scopeReason ?? '' })
      return
    }
    let cancelled = false
    listAudit({ order: 'desc', limit: 100 })
      .then((response) => {
        if (cancelled) return
        lastGood.current = { entries: response.entries, receivedAt: Date.now() }
        setState({ kind: 'loaded', entries: response.entries, receivedAt: Date.now() })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState({
          kind: 'failed',
          reason: describeApiError(err),
          entries: lastGood.current?.entries ?? [],
          receivedAt: lastGood.current?.receivedAt ?? null,
        })
      })
    return () => {
      cancelled = true
    }
  }, [scope.allowed, scopeReason])

  return state
}

export function MonitorActivity() {
  const model = useModelContext()
  const audit = useAuditEntries(model)
  const auditEntries = audit.kind === 'loaded' || audit.kind === 'failed' ? audit.entries : []
  const rows = mergedActivityRows(model.events, auditEntries)

  return (
    <>
      <MonitorHead model={model} />

      <Section
        id="mo-activity-full"
        title="Activity"
        detail="System events and operator actions in one stream, you usually need to know both, in order."
      >
        {model.eventsGap && (
          <Notice
            tone="warn"
            live="status"
            headline="Some event history is permanently lost to retention."
            explanation={
              model.oldestRetainedSeq === null
                ? 'No event is currently retained.'
                : `The oldest event still retained is seq ${model.oldestRetainedSeq}. Anything earlier is gone, not merely unfetched.`
            }
          />
        )}

        {audit.kind === 'denied' && (
          <RuledStrip absence="noPermission" label="Operator actions not shown" fact={audit.reason} />
        )}
        {audit.kind === 'failed' && (
          <RuledStrip
            absence={audit.receivedAt === null ? 'failed' : 'stale'}
            label={audit.receivedAt === null ? 'Operator actions unread' : 'Operator actions stale'}
            fact={audit.reason}
            detail={
              audit.receivedAt === null
                ? 'No operator-action read has ever succeeded on this device.'
                : `Showing the operator actions last read at ${new Date(audit.receivedAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false })}.`
            }
          />
        )}

        {rows.length === 0 ? (
          <RuledStrip
            absence={model.snapshotReceivedAt === null ? 'loading' : 'empty'}
            label={model.snapshotReceivedAt === null ? 'Reading' : 'Empty'}
            fact={model.snapshotReceivedAt === null ? 'No event history has arrived yet.' : 'No event has been recorded.'}
          />
        ) : (
          <>
            <TableWrap label="Activity, scrollable">
              <Table minWidth={520}>
                <thead>
                  <tr>
                    <th scope="col">Time</th>
                    <th scope="col">What happened</th>
                    <th scope="col">Source</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((row) => (
                    <tr key={row.key}>
                      <td className="sm-data">{row.time}</td>
                      <td>
                        {row.state !== null && <StatusPair tone={row.tone} label={row.state} />}
                        {row.summary}
                      </td>
                      <td>{row.source}</td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            </TableWrap>
            <p className="sm-section__footnote">
              Operator actions are audit records and need an audit-read scope; system events do not.
            </p>
          </>
        )}
      </Section>
    </>
  )
}
