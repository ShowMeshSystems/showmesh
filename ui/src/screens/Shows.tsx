import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { listConfigObjects } from '../api'
import { Button, PageTitle, RuledStrip, Section, StatusPair, Table, TableWrap } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError } from '../domain/session'
import { formatClock } from '../domain/time'
import type { ConfigObjectSummary } from '../api'
import { fetchAllShowContents } from './showsData'
import { contentsCounts, contentsSummary, EMPTY_CONTENTS, showRows, type ShowContentsCounts } from './showsModel'

type ListState =
  | { kind: 'loading' }
  | { kind: 'loaded'; objects: ConfigObjectSummary[]; receivedAt: number }
  | { kind: 'failed'; reason: string; objects: ConfigObjectSummary[]; receivedAt: number | null }

function useShowList(): { state: ListState; refresh: () => void } {
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<ListState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    listConfigObjects('show')
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', objects: response.objects, receivedAt: Date.now() })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState((prev) => ({
          kind: 'failed',
          reason: describeApiError(err),
          objects: prev.kind === 'loading' ? [] : prev.objects,
          receivedAt: prev.kind === 'loaded' ? prev.receivedAt : prev.kind === 'failed' ? prev.receivedAt : null,
        }))
      })
    return () => {
      cancelled = true
    }
  }, [attempt])

  return { state, refresh: () => setAttempt((n) => n + 1) }
}

/**
 * Each row's contents count, fetched after the list itself so one show's
 * failed count-fetch never blocks the others or the list beneath it.
 */
/** One read per kind for the whole list. A read per show would storm the coordinator. */
function useContentsCounts(ids: readonly string[]): Map<string, ShowContentsCounts | 'failed'> {
  const key = ids.join(',')
  const [counts, setCounts] = useState<Map<string, ShowContentsCounts | 'failed'>>(new Map())

  useEffect(() => {
    if (key === '') return
    let cancelled = false
    const wanted = key.split(',')
    fetchAllShowContents()
      .then((byShow) => {
        if (cancelled) return
        const next = new Map<string, ShowContentsCounts | 'failed'>()
        for (const id of wanted) next.set(id, contentsCounts(byShow.get(id) ?? EMPTY_CONTENTS))
        setCounts(next)
      })
      .catch(() => {
        if (cancelled) return
        setCounts(new Map(wanted.map((id) => [id, 'failed' as const])))
      })
    return () => {
      cancelled = true
    }
  }, [key])

  return counts
}

export function Shows() {
  const model = useModelContext()
  const { state } = useShowList()
  const objects = state.kind === 'loading' ? [] : state.objects
  const rows = showRows(objects, model)
  const counts = useContentsCounts(rows.map((r) => r.id))

  return (
    <>
      <PageTitle
        title="Shows"
        lede="A show is a namespace. Its cues, playlists, surfaces and assets reference only each other, nothing crosses between shows, at authoring time or at runtime."
      />

      <RuledStrip
        absence="empty"
        label="One active"
        fact="Only the active show can affect the running system. That is why next season's show can be prepared without touching tonight, and why a Hallowed Hollow sequence playing in FPP will not activate its audio while Winter Ridge is active."
      />

      <Section
        id="sh-list"
        title="All shows"
        aside={
          <Button
            title="Show creation needs an id and name form the mock does not draw; see docs/ui-rebuild/OPEN-DECISIONS.md D-011."
            disabled
          >
            New show
          </Button>
        }
      >
        {state.kind === 'failed' && (
          <RuledStrip
            absence={state.receivedAt === null ? 'failed' : 'stale'}
            label={state.receivedAt === null ? 'Read failed' : 'Stale'}
            fact={state.reason}
            detail={
              state.receivedAt === null
                ? 'No show list read has ever succeeded on this device.'
                : `Showing the list last read at ${formatClock(new Date(state.receivedAt).toISOString()) ?? 'an unrecorded time'}.`
            }
          />
        )}

        {state.kind === 'loading' ? (
          <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for every configured show." />
        ) : rows.length === 0 && state.kind === 'loaded' ? (
          <RuledStrip absence="empty" label="None" fact="No show is configured." />
        ) : (
          <>
            <TableWrap label="Shows, scrollable">
              <Table>
                <thead>
                  <tr>
                    <th scope="col">Show</th>
                    <th scope="col">Contents</th>
                    <th scope="col">Last saved</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((row) => {
                    const rowCounts = counts.get(row.id)
                    return (
                      <tr key={row.id} aria-current={row.active ? 'true' : undefined} className={row.active ? 'sm-table__row--current' : undefined}>
                        <td>
                          <Link to={`/shows/${row.id}`}>{row.label}</Link>{' '}
                          {row.active && <StatusPair tone="good" label="Active" />}
                          <br />
                          <span className="sm-data sm-small sm-faint">
                            {row.id} · rev {row.revision}
                          </span>
                        </td>
                        <td>
                          {rowCounts === undefined ? (
                            <span className="sm-small sm-faint">reading&hellip;</span>
                          ) : rowCounts === 'failed' ? (
                            <span className="sm-small sm-faint">counts unavailable</span>
                          ) : (
                            <span className="sm-small sm-muted">{contentsSummary(rowCounts)}</span>
                          )}
                        </td>
                        <td className="sm-small sm-muted">{formatClock(row.updatedAt) ?? 'unrecorded'}</td>
                      </tr>
                    )
                  })}
                </tbody>
              </Table>
            </TableWrap>
            <p className="sm-section__footnote">
              Switching the active show invalidates the previous show&rsquo;s authority and requires readiness for the
              new one. It is an audited change, not a view filter.
            </p>
          </>
        )}
      </Section>
    </>
  )
}
