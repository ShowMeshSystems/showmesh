import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import {
  ApiError,
  getShowActive,
  getShowActiveRevisions,
  listAssets,
  listConfigObjects,
  putShowActive,
  type ConfigRevisionMeta,
} from '../api'
import { describeApiError, evaluateAnyScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from '../components/ScopedButton'
import { showPath } from '../components/showWorkspacePaths'
import '../styles/shows.css'
import type { ConfigObjectSummary, ShowActiveConfigResponse } from '../app/types'

// Shows.dc.html + ROUTE-MAP.md owner ruling 2026-08-29: the Shows list
// carries what used to be the standalone ShowActive.tsx (activate a
// different show) directly on this page, positioned above "All shows" -
// activation has a ruled home here now, not a parked, separately-routed
// screen. `src/views/ShowActive.tsx` is kept only as a redirect to this
// page for the still-wired `/config/show.active` address (App.tsx is not
// this group's file to edit).
const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE = 'config:write'

type ShowsState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; objects: ConfigObjectSummary[] }

type ActiveState =
  | { kind: 'loading' }
  | { kind: 'not_configured' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; config: ShowActiveConfigResponse; revisions: ConfigRevisionMeta[] }

interface ShowContents {
  playlists: number
  cues: number
  surfaces: number
  assets: number
}

type ContentsState = Record<string, ShowContents | 'loading' | 'error'>

export function Shows() {
  const model = useModelContext()
  const navigate = useNavigate()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)

  const [state, setState] = useState<ShowsState>({ kind: 'loading' })
  const [contents, setContents] = useState<ContentsState>({})
  const [activeState, setActiveState] = useState<ActiveState>({ kind: 'loading' })
  const [reloadGeneration, setReloadGeneration] = useState(0)

  // Arming state for activation: a picked target show id not yet
  // confirmed. Only a second, distinct click actually submits (ADR-028:
  // activation changes what every declared node is expected to hold).
  const [selectedShow, setSelectedShow] = useState('')
  const [armedTarget, setArmedTarget] = useState<string | null>(null)
  const [activating, setActivating] = useState(false)
  const [activateError, setActivateError] = useState<string | null>(null)
  const activatingRef = useRef(false)

  useEffect(() => {
    if (!readGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    listConfigObjects('show')
      .then((resp) => {
        if (cancelled) return
        setState({ kind: 'loaded', objects: resp.objects })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [readGate.allowed])

  useEffect(() => {
    if (!readGate.allowed) return
    let cancelled = false
    setActiveState({ kind: 'loading' })
    Promise.all([getShowActive(), getShowActiveRevisions()])
      .then(([config, revisionsResp]) => {
        if (cancelled) return
        setActiveState({ kind: 'loaded', config, revisions: revisionsResp.revisions })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        if (err instanceof ApiError && err.status === 404) {
          setActiveState({ kind: 'not_configured' })
          return
        }
        setActiveState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [readGate.allowed, reloadGeneration])

  // Per-show contents summary ("2 playlists · 14 cues · 3 surfaces · 22
  // assets", Shows.dc.html): real inventory counts from the same list
  // endpoints every other tab uses, fetched per show once the show list
  // itself has loaded. Never invented, never a placeholder count.
  useEffect(() => {
    if (state.kind !== 'loaded') return
    let cancelled = false
    for (const show of state.objects) {
      setContents((prev) => (prev[show.id] === undefined ? { ...prev, [show.id]: 'loading' } : prev))
      Promise.all([
        listConfigObjects('show.playlist', show.id),
        listConfigObjects('show.cue', show.id),
        listConfigObjects('show.surface', show.id),
        listAssets({ show: show.id }),
      ])
        .then(([playlists, cues, surfaces, assets]) => {
          if (cancelled) return
          setContents((prev) => ({
            ...prev,
            [show.id]: {
              playlists: playlists.objects.length,
              cues: cues.objects.length,
              surfaces: surfaces.objects.length,
              assets: assets.assets.length,
            },
          }))
        })
        .catch(() => {
          if (!cancelled) setContents((prev) => ({ ...prev, [show.id]: 'error' }))
        })
    }
    return () => {
      cancelled = true
    }
  }, [state])

  const currentShowId = activeState.kind === 'loaded' ? activeState.config.payload.show : null

  function arm(): void {
    if (selectedShow.trim() === '') return
    setActivateError(null)
    setArmedTarget(selectedShow.trim())
  }

  function cancelArm(): void {
    setArmedTarget(null)
    setActivateError(null)
  }

  async function confirmActivate(): Promise<void> {
    if (activatingRef.current || armedTarget === null) return
    activatingRef.current = true
    setActivating(true)
    setActivateError(null)
    try {
      await putShowActive({ show: armedTarget })
      setArmedTarget(null)
      setReloadGeneration((g) => g + 1)
    } catch (err) {
      // Deliberately does not dismiss the confirmation: the refusal is
      // about the specific target the operator picked, not a reason to
      // make them re-pick it.
      setActivateError(describeApiError(err))
    } finally {
      activatingRef.current = false
      setActivating(false)
    }
  }

  return (
    <div className="operator-page">
      <header className="page-header">
        <div className="page-header__row">
          <div style={{ minWidth: 0 }}>
            <h1 className="t-display page-header__title">Shows</h1>
            <p className="page-header__meta">
              A show is a namespace. Its cues, playlists, surfaces and assets reference only each
              other, nothing crosses between shows, at authoring time or at runtime.
            </p>
          </div>
          <div className="page-header__actions">
            <ScopedButton
              requiredScope={CONFIG_WRITE_SCOPE}
              className="btn btn--primary"
              onClick={() => navigate('/shows/new')}
            >
              New show
            </ScopedButton>
          </div>
        </div>
      </header>

      <div className="page-body">
        <p className="ruled-strip">
          <span className="ruled-strip__state t-meta shows-active-note">● One active</span>
          <span className="ruled-strip__explanation">
            Only the active show can affect the running system. That is why next season&rsquo;s
            show can be prepared without touching tonight.
          </span>
        </p>

        <section aria-labelledby="shows-activate-heading" style={{ marginTop: 24 }}>
          <h2 id="shows-activate-heading" className="t-heading">
            Activate a show
          </h2>
          <p className="t-small shows-muted">
            Switching the active show invalidates the previous show&rsquo;s authority and requires
            readiness for the new one. It is an audited change, not a view filter.
          </p>

          {!readGate.allowed && (
            <p className="ruled-strip ruled-strip--no-permission" role="status">
              <span className="ruled-strip__state t-meta">No permission</span>
              <span className="ruled-strip__explanation">{readGate.reason}</span>
            </p>
          )}

          {readGate.allowed && armedTarget === null && (
            <div className="shows-activate-row">
              {state.kind === 'loading' && (
                <p className="ruled-strip ruled-strip--loading" role="status">
                  <span className="ruled-strip__state t-meta">Loading</span>
                  <span className="ruled-strip__explanation">Reading the show list.</span>
                </p>
              )}
              {state.kind === 'error' && (
                <p className="ruled-strip ruled-strip--failed" role="alert">
                  <span className="ruled-strip__state t-meta">Failed</span>
                  <span className="ruled-strip__explanation">
                    Could not load the show list ({state.message}); there is nothing to pick from.
                  </span>
                </p>
              )}
              {state.kind === 'loaded' && state.objects.length === 0 && (
                <p className="t-small shows-muted">No shows are configured yet. Create one first.</p>
              )}
              {state.kind === 'loaded' && state.objects.length > 0 && (
                <>
                  <label className="field shows-activate-select">
                    <span className="field__label">Show to activate</span>
                    <select value={selectedShow} onChange={(e) => setSelectedShow(e.target.value)}>
                      <option value="" disabled>
                        Choose a show
                      </option>
                      {state.objects.map((s) => (
                        <option key={s.id} value={s.id}>
                          {s.label} ({s.id}){s.id === currentShowId ? ' — currently active' : ''}
                        </option>
                      ))}
                    </select>
                  </label>
                  <ScopedButton
                    requiredScope={CONFIG_WRITE_SCOPE}
                    className="btn btn--secondary"
                    onClick={arm}
                  >
                    Select
                  </ScopedButton>
                </>
              )}
            </div>
          )}

          {readGate.allowed && armedTarget !== null && (
            <div className="ruled-strip ruled-strip--stale" role="alertdialog" aria-label="Confirm show activation">
              <span className="ruled-strip__state t-meta">Confirm</span>
              <div>
                <p className="t-subhead">About to activate &ldquo;{armedTarget}&rdquo;.</p>
                <p className="t-small shows-muted">
                  This replaces the active show{currentShowId !== null ? ` (currently "${currentShowId}")` : ''}.
                  Every declared node&rsquo;s expected asset set changes to what &ldquo;{armedTarget}
                  &rdquo; now requires. This is revisioned and audited like every other configuration
                  write.
                </p>
                {activateError !== null && (
                  <p role="alert" className="field__error">
                    {activateError}
                  </p>
                )}
                <div className="ruled-strip__actions">
                  <ScopedButton
                    requiredScope={CONFIG_WRITE_SCOPE}
                    className="btn btn--primary"
                    onClick={() => void confirmActivate()}
                    busy={activating}
                    busyReason="Activating…"
                  >
                    {activating ? 'Activating…' : `Confirm: activate "${armedTarget}"`}
                  </ScopedButton>
                  <button type="button" className="btn btn--quiet" onClick={cancelArm} disabled={activating}>
                    Cancel
                  </button>
                </div>
              </div>
            </div>
          )}
        </section>

        <section aria-labelledby="shows-list-heading" style={{ marginTop: 24 }}>
          <h2 id="shows-list-heading" className="t-heading">
            All shows
          </h2>

          {!readGate.allowed && (
            <p className="ruled-strip ruled-strip--no-permission" role="status">
              <span className="ruled-strip__state t-meta">No permission</span>
              <span className="ruled-strip__explanation">{readGate.reason}</span>
            </p>
          )}
          {readGate.allowed && state.kind === 'loading' && (
            <p className="ruled-strip ruled-strip--loading" role="status">
              <span className="ruled-strip__state t-meta">Loading</span>
              <span className="ruled-strip__explanation">Reading the show list.</span>
            </p>
          )}
          {readGate.allowed && state.kind === 'error' && (
            <p className="ruled-strip ruled-strip--failed" role="alert">
              <span className="ruled-strip__state t-meta">Failed</span>
              <span className="ruled-strip__explanation">{state.message}</span>
            </p>
          )}
          {readGate.allowed && state.kind === 'loaded' && state.objects.length === 0 && (
            <p className="ruled-strip ruled-strip--empty" role="status">
              <span className="ruled-strip__state t-meta">Empty</span>
              <span className="ruled-strip__explanation">No shows are configured yet.</span>
            </p>
          )}
          {readGate.allowed && state.kind === 'loaded' && state.objects.length > 0 && (
            <div className="card">
              <div className="table-wrap">
                <table className="table table--full" aria-label="Shows">
                  <thead>
                    <tr>
                      <th scope="col">Show</th>
                      <th scope="col">Contents</th>
                      <th scope="col">Last saved</th>
                    </tr>
                  </thead>
                  <tbody>
                    {state.objects.map((show) => {
                      const isActive = show.id === currentShowId
                      const rowContents = contents[show.id]
                      return (
                        <tr key={show.id} aria-current={isActive ? 'true' : undefined}>
                          <td>
                            <Link className="entity-link" to={showPath(show.id)}>
                              {show.label}
                            </Link>
                            {isActive && <span className="status-pair status-pair--good shows-active-badge">Active</span>}
                            <br />
                            <span className="t-data shows-id-meta">
                              {show.id} · rev {show.currentRevision}
                            </span>
                          </td>
                          <td>
                            {rowContents === undefined || rowContents === 'loading' ? (
                              <span className="t-small shows-muted">Loading…</span>
                            ) : rowContents === 'error' ? (
                              <span className="t-small shows-muted">Unavailable</span>
                            ) : rowContents.playlists + rowContents.cues + rowContents.surfaces + rowContents.assets === 0 ? (
                              <span className="t-small shows-faint">Empty</span>
                            ) : (
                              <span className="t-small shows-muted">
                                {rowContents.playlists} playlist{rowContents.playlists === 1 ? '' : 's'} ·{' '}
                                {rowContents.cues} cue{rowContents.cues === 1 ? '' : 's'} · {rowContents.surfaces} surface
                                {rowContents.surfaces === 1 ? '' : 's'} · {rowContents.assets} asset
                                {rowContents.assets === 1 ? '' : 's'}
                              </span>
                            )}
                          </td>
                          <td className="t-small shows-muted">
                            {formatAbsolute(show.updatedAt)}
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
              <div className="table__footer-note">
                Switching the active show invalidates the previous show&rsquo;s authority and requires
                readiness for the new one. It is an audited change, not a view filter.
              </div>
            </div>
          )}
        </section>

        {readGate.allowed && activeState.kind === 'loaded' && activeState.revisions.length > 0 && (
          <section aria-labelledby="shows-activation-history" style={{ marginTop: 24 }}>
            <h2 id="shows-activation-history" className="t-heading">
              Activation history
            </h2>
            <div className="card">
              <div className="table-wrap">
                <table className="table table--full" aria-label="Activation history">
                  <thead>
                    <tr>
                      <th scope="col">Revision</th>
                      <th scope="col">Active</th>
                      <th scope="col">Activated at</th>
                      <th scope="col">Activated by</th>
                    </tr>
                  </thead>
                  <tbody>
                    {activeState.revisions.map((rev) => (
                      <tr key={rev.revision}>
                        <td className="t-data">{rev.revision}</td>
                        <td>{rev.active ? 'active' : ''}</td>
                        <td className="t-small">{formatAbsolute(rev.createdAt)}</td>
                        <td className="t-small">{rev.createdByPrincipalName ?? '-'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          </section>
        )}
      </div>
    </div>
  )
}
