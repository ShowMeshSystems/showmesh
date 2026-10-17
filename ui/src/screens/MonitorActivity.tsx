import { useCallback, useEffect, useRef, useState } from 'react'
import { type AuditEntry, listAudit, type Model } from '../api'
import { Button, ButtonRow, Field, FieldGrid, Input, Notice, RuledStrip, Section, Select, StatusPair, Table, TableWrap } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { MonitorHead } from './Monitor'
import { mergedActivityRows } from './monitorModel'

const PAGE_SIZE = 100

type TypeFilter = 'all' | 'event' | 'audit'

type AuditState =
  | { kind: 'denied'; reason: string }
  | { kind: 'loading' }
  | { kind: 'loaded'; entries: AuditEntry[]; receivedAt: number }
  | { kind: 'failed'; reason: string; entries: AuditEntry[]; receivedAt: number | null }

type AuditFeed = { state: AuditState; hasMore: boolean; loadingMore: boolean; moreError: string | null; loadMore: () => void }

/**
 * The operator-action half of the merged stream. `entries` carries the
 * last successful read even after a later failure, per the "a refresh
 * failure retains the last known state" rule.
 */
function useAuditEntries(model: Model): AuditFeed {
  const scope = evaluateScope(model.session, model.sessionFetchFailed, 'audit:read')
  const scopeReason = scope.allowed ? null : scope.reason
  const [state, setState] = useState<AuditState>(scope.allowed ? { kind: 'loading' } : { kind: 'denied', reason: scopeReason ?? '' })
  const [hasMore, setHasMore] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [moreError, setMoreError] = useState<string | null>(null)
  const lastGood = useRef<{ entries: AuditEntry[]; receivedAt: number } | null>(null)
  const loaded = useRef<AuditEntry[]>([])

  useEffect(() => {
    if (!scope.allowed) {
      setState({ kind: 'denied', reason: scopeReason ?? '' })
      return
    }
    let cancelled = false
    listAudit({ order: 'desc', limit: PAGE_SIZE })
      .then((response) => {
        if (cancelled) return
        loaded.current = response.entries
        lastGood.current = { entries: response.entries, receivedAt: Date.now() }
        setHasMore(olderExists(response.entries, response.oldestRetainedId))
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

  const loadMore = useCallback(() => {
    const last = loaded.current[loaded.current.length - 1]
    if (last === undefined) return
    setLoadingMore(true)
    setMoreError(null)
    listAudit({ order: 'desc', before: last.id, limit: PAGE_SIZE })
      .then((response) => {
        loaded.current = [...loaded.current, ...response.entries]
        lastGood.current = { entries: loaded.current, receivedAt: Date.now() }
        setHasMore(olderExists(loaded.current, response.oldestRetainedId))
        setState({ kind: 'loaded', entries: loaded.current, receivedAt: Date.now() })
      })
      .catch((err: unknown) => setMoreError(describeApiError(err)))
      .finally(() => setLoadingMore(false))
  }, [])

  return { state, hasMore, loadingMore, moreError, loadMore }
}

function olderExists(entries: readonly AuditEntry[], oldestRetainedId: number | null): boolean {
  const last = entries[entries.length - 1]
  return last !== undefined && oldestRetainedId !== null && last.id > oldestRetainedId
}

export function MonitorActivity() {
  const model = useModelContext()
  const feed = useAuditEntries(model)
  const audit = feed.state
  const auditEntries = audit.kind === 'loaded' || audit.kind === 'failed' ? audit.entries : []
  const allRows = mergedActivityRows(model.events, auditEntries)
  const [typeFilter, setTypeFilter] = useState<TypeFilter>('all')
  const [search, setSearch] = useState('')
  const needle = search.trim().toLowerCase()
  const rows = allRows.filter(
    (row) => (typeFilter === 'all' || row.origin === typeFilter) && (needle === '' || row.summary.toLowerCase().includes(needle)),
  )

  return (
    <>
      <MonitorHead model={model} />

      <Section
        id="mo-activity-full"
        title="Activity"
      >
        {model.eventsGap && (
          <Notice
            tone="warn"
            live="status"
            headline="Some event history is permanently lost to retention."
            explanation={
              model.oldestRetainedSeq === null
                ? 'No event is currently retained.'
                : `The oldest event kept is seq ${model.oldestRetainedSeq}. Earlier events are gone.`
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

        {allRows.length > 0 && (
          <FieldGrid>
            <Field label="Show">
              {(props) => (
                <Select {...props} value={typeFilter} onChange={(e) => setTypeFilter(e.target.value as TypeFilter)}>
                  <option value="all">System events and operator actions</option>
                  <option value="event">System events only</option>
                  <option value="audit">Operator actions only</option>
                </Select>
              )}
            </Field>
            <Field label="Search what happened">
              {(props) => <Input {...props} type="search" value={search} onChange={(e) => setSearch(e.target.value)} />}
            </Field>
          </FieldGrid>
        )}

        {allRows.length > 0 && rows.length === 0 ? (
          <RuledStrip absence="empty" label="No match" fact="No loaded activity matches the filter. Clear the search or load older activity." />
        ) : rows.length === 0 ? (
          <RuledStrip
            absence={model.snapshotReceivedAt === null ? 'loading' : 'empty'}
            label={model.snapshotReceivedAt === null ? 'Reading' : 'Empty'}
            fact={model.snapshotReceivedAt === null ? 'No event history has arrived yet.' : 'No event has been recorded.'}
          />
        ) : (
          <>
            <TableWrap label="Activity, scrollable">
              <Table minWidth={640}>
                <thead>
                  <tr>
                    <th scope="col">Time</th>
                    <th scope="col">State</th>
                    <th scope="col">What happened</th>
                    <th scope="col">Source</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((row) => (
                    <tr key={row.key}>
                      <td className="sm-data">{row.time}</td>
                      <td>{row.state !== null && <StatusPair tone={row.tone} label={row.state} />}</td>
                      <td>{row.summary}</td>
                      <td>{row.source}</td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            </TableWrap>
          </>
        )}

        {feed.moreError !== null && <RuledStrip absence="failed" label="Older activity unread" fact={feed.moreError} />}
        {feed.hasMore && (
          <ButtonRow>
            <Button onClick={feed.loadMore} disabled={feed.loadingMore}>
              {feed.loadingMore ? 'Loading older activity' : 'Load older operator actions'}
            </Button>
          </ButtonRow>
        )}
      </Section>
    </>
  )
}
