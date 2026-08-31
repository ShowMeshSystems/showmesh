import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { ApiError, getShowActive, getShowActiveRevisions, listConfigObjects, putShowActive } from '../api'
import { Button, ButtonRow, DefinitionStrip, Field, FieldGrid, PageTitle, RevisionHistory, RuledStrip, Section, Select, StatusPair, Table, TableWrap } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { guardedSave, type SaveOutcome } from '../domain/save'
import { formatClock } from '../domain/time'
import type { ConfigObjectSummary, ShowActiveConfigResponse } from '../api'
import { StaleWriteStrip } from './StaleWrite'
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

/** No `show.active` object has ever existed: the 404 the store documents, translated into the same shape every other read produces so `guardedSave` never special-cases it. */
function emptyShowActive(): ShowActiveConfigResponse {
  return {
    serverTime: '',
    kind: 'show.active',
    id: 'show.active',
    revision: 0,
    payload: { show: '' },
    updatedAt: '',
    createdByPrincipalId: null,
    createdByPrincipalName: null,
    source: 'api',
  }
}

function readShowActiveOrEmpty(): Promise<ShowActiveConfigResponse> {
  return getShowActive().catch((err: unknown) => {
    if (err instanceof ApiError && err.status === 404) return emptyShowActive()
    throw err
  })
}

type ShowActiveLoadState =
  | { kind: 'loading' }
  | { kind: 'loaded'; response: ShowActiveConfigResponse }
  | { kind: 'failed'; reason: string }

/**
 * `/config/show.active` (ADR-027 decision 3): which show can affect the
 * running system, and the control to point it at a different one. `show` is
 * required with `minLength: 1` in the contract, unlike the sibling
 * `night.session.active` pointer, so this never offers a clear control.
 */
function ShowActivation({ objects }: { objects: ConfigObjectSummary[] }) {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<ShowActiveLoadState>({ kind: 'loading' })
  const [selected, setSelected] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [stale, setStale] = useState<Extract<SaveOutcome<ShowActiveConfigResponse>, { kind: 'stale' }> | null>(null)

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    readShowActiveOrEmpty()
      .then((active) => {
        if (cancelled) return
        setState({ kind: 'loaded', response: active })
        setSelected(active.payload.show)
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [attempt])

  const activate = (show: string) => {
    if (state.kind !== 'loaded' || show === '') return
    setSaving(true)
    setSaveError(null)
    setStale(null)
    guardedSave({
      loaded: state.response,
      read: readShowActiveOrEmpty,
      write: () => putShowActive({ show }),
    })
      .then((outcome) => {
        if (outcome.kind === 'saved') {
          setState({ kind: 'loaded', response: outcome.response })
          setSelected(outcome.response.payload.show)
          return
        }
        if (outcome.kind === 'stale') {
          setStale(outcome)
          return
        }
        setSaveError(outcome.reason)
      })
      .catch((err: unknown) => setSaveError(describeApiError(err)))
      .finally(() => setSaving(false))
  }

  return (
    <Section id="sh-active" title="Show activation" aside={<span className="sm-small sm-muted">Which show can affect the running system</span>}>
      {state.kind === 'loading' ? (
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator which show is active." />
      ) : state.kind === 'failed' ? (
        <RuledStrip absence="failed" label="Read failed" fact={state.reason} />
      ) : (
        <>
          <DefinitionStrip
            items={[
              {
                term: 'Active show',
                value:
                  state.response.payload.show === '' ? (
                    <span className="sm-muted">none - nothing has ever been activated</span>
                  ) : (
                    <span className="sm-data">{state.response.payload.show}</span>
                  ),
              },
              {
                term: 'Revision',
                value: state.response.revision === 0 ? 'never activated' : <span className="sm-data">{state.response.revision}</span>,
              },
              {
                term: 'Updated',
                value:
                  state.response.revision === 0
                    ? 'never'
                    : `${formatClock(state.response.updatedAt) ?? 'at an unrecorded time'} by ${state.response.createdByPrincipalName ?? 'an unnamed principal'}`,
              },
            ]}
          />

          {objects.length === 0 ? (
            <RuledStrip absence="empty" label="Nothing to activate" fact="No show is configured yet." />
          ) : (
            <>
              <FieldGrid>
                <Field label="Activate a show" help="Switches which show can affect the running system. This is an audited change, not a view filter.">
                  {(props) => (
                    <Select {...props} value={selected} onChange={(e) => setSelected(e.target.value)}>
                      <option value="">Choose a show…</option>
                      {objects.map((object) => (
                        <option key={object.id} value={object.id}>
                          {object.label} ({object.id})
                        </option>
                      ))}
                    </Select>
                  )}
                </Field>
              </FieldGrid>
              <ButtonRow>
                <Button
                  variant="primary"
                  onClick={() => activate(selected)}
                  disabled={!gate.allowed || saving || selected === '' || selected === state.response.payload.show}
                  title={gate.allowed ? undefined : gate.reason}
                >
                  {saving ? 'Activating…' : 'Activate'}
                </Button>
              </ButtonRow>
            </>
          )}

          <p className="sm-small sm-faint">
            The show.active pointer cannot be cleared once set: the contract requires a non-empty show name.
          </p>
        </>
      )}
      {stale !== null && (
        <StaleWriteStrip
          stale={stale}
          onReload={() => {
            setStale(null)
            setAttempt((n) => n + 1)
          }}
        />
      )}
      {saveError !== null && <RuledStrip absence="failed" label="Save failed" fact={saveError} />}
      <RevisionHistory fetch={getShowActiveRevisions} reloadKey={attempt} />
    </Section>
  )
}

export function Shows() {
  const model = useModelContext()
  const navigate = useNavigate()
  const { state } = useShowList()
  const objects = state.kind === 'loading' ? [] : state.objects
  const rows = showRows(objects, model)
  const counts = useContentsCounts(rows.map((r) => r.id))
  const createGate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')

  return (
    <div className="sm-shows">
      <PageTitle
        title="Shows"
        lede="A show is a namespace. Its cues, playlists, surfaces and assets reference only each other, nothing crosses between shows, at authoring time or at runtime."
      />

      <RuledStrip
        absence="empty"
        label="One active"
        fact="Only the active show can affect the running system. That is why next season's show can be prepared without touching tonight, and why a Hallowed Hollow sequence playing in FPP will not activate its audio while Winter Ridge is active."
      />

      <ShowActivation objects={objects} />

      <Section
        id="sh-list"
        title="All shows"
        aside={
          <Button onClick={() => navigate('/shows/new')} disabled={!createGate.allowed} title={createGate.allowed ? undefined : createGate.reason}>
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
                          <Link className="sm-linkbutton" to={`/shows/${row.id}/playlists`}>
                            Edit show
                          </Link>{' '}
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
    </div>
  )
}
