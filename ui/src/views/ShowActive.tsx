import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { ApiError, getShowActive, getShowActiveRevisions, listConfigObjects, putShowActive, type ConfigRevisionMeta } from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from '../components/ScopedButton'
import type { ConfigObjectSummary, ShowActiveConfigResponse } from '../app/types'

// Track G seam G-8 (TRACK-G-surface-parity.md "G-8"): the active-show
// pointer (ADR-027 decision 3). "Activation is the sharp control": making
// a show active changes what every declared node is expected to hold
// (ADR-028), so this view never fires PUT /config/show.active from a bare
// click. Picking a show only ARMS a confirmation panel that states the
// current active show, the show about to become active, and what changes
// — a second, distinct click actually submits.
const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE = 'config:write'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'not_configured'; reason: string }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; config: ShowActiveConfigResponse; revisions: ConfigRevisionMeta[] }

type ShowsState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; shows: ConfigObjectSummary[] }

export function ShowActive() {
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)

  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [showsState, setShowsState] = useState<ShowsState>({ kind: 'loading' })
  const [reloadGeneration, setReloadGeneration] = useState(0)

  // Arming state: a picked target show id that has not yet been confirmed.
  // `null` means no activation is armed — the confirmation panel below
  // renders only while this is non-null, and clearing it (Cancel, or a
  // successful/failed submit) is the only thing that dismisses it.
  const [armedTarget, setArmedTarget] = useState<string | null>(null)
  const [selectedShow, setSelectedShow] = useState('')
  const [activating, setActivating] = useState(false)
  const [activateError, setActivateError] = useState<string | null>(null)
  const activatingRef = useRef(false)

  useEffect(() => {
    if (!readGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    Promise.all([getShowActive(), getShowActiveRevisions()])
      .then(([config, revisionsResp]) => {
        if (cancelled) return
        setState({ kind: 'loaded', config, revisions: revisionsResp.revisions })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        if (err instanceof ApiError && err.status === 404) {
          setState({ kind: 'not_configured', reason: err.message })
          return
        }
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [readGate.allowed, reloadGeneration])

  useEffect(() => {
    if (!readGate.allowed) return
    let cancelled = false
    listConfigObjects('show')
      .then((resp) => {
        if (!cancelled) setShowsState({ kind: 'loaded', shows: resp.objects })
      })
      .catch((err: unknown) => {
        if (!cancelled) setShowsState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [readGate.allowed])

  const currentShow = state.kind === 'loaded' ? state.config.payload.show : null

  function arm(): void {
    if (selectedShow.trim() === '') return
    setActivateError(null)
    setArmedTarget(selectedShow.trim())
  }

  function cancel(): void {
    setArmedTarget(null)
    setActivateError(null)
  }

  async function confirmActivate(): Promise<void> {
    if (activatingRef.current) return
    if (armedTarget === null) return
    activatingRef.current = true
    setActivating(true)
    setActivateError(null)
    try {
      await putShowActive({ show: armedTarget })
      setArmedTarget(null)
      setReloadGeneration((g) => g + 1)
    } catch (err) {
      // Deliberately does not dismiss the confirmation panel: the operator
      // asked to activate a specific show and the refusal is about THAT
      // request, not a reason to make them re-pick it.
      setActivateError(describeApiError(err))
    } finally {
      activatingRef.current = false
      setActivating(false)
    }
  }

  return (
    <div>
      <h2 className="panel__title">Active show</h2>
      {/* The active-show pointer is itself revisioned/audited configuration (ADR-027 decision 3);
          activating a different show changes what every declared node is expected to hold
          (ADR-028). */}
      <p className="text-muted">
        The show every declared node is currently expected to hold assets for. Activating a
        different show is revisioned and audited like every other configuration write here, so
        that programming one show cannot silently break another. See{' '}
        <Link to="/assets/manifest">the asset manifest</Link> for whether nodes actually hold what
        the active show now expects.
      </p>

      {!readGate.allowed && (
        <p className="panel panel--error" role="status">
          {readGate.reason}
        </p>
      )}

      {readGate.allowed && state.kind === 'loading' && <p className="text-muted">Loading the active show…</p>}
      {readGate.allowed && state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          {state.message}
        </p>
      )}
      {readGate.allowed && state.kind === 'not_configured' && (
        <p className="panel" role="status">
          {state.reason}
        </p>
      )}
      {readGate.allowed && state.kind === 'loaded' && (
        <div className="panel" role="status">
          <dl className="field-list">
            <dt>Currently active</dt>
            <dd>
              <Link className="entity-link" to={`/config/show/${encodeURIComponent(state.config.payload.show)}`}>
                {state.config.payload.show}
              </Link>
            </dd>
            <dt>Revision</dt>
            <dd>
              {state.config.revision}
              {state.config.createdByPrincipalName !== null && `, activated by ${state.config.createdByPrincipalName}`}
              {' at '}
              {formatAbsolute(state.config.updatedAt)}
            </dd>
          </dl>
        </div>
      )}

      {readGate.allowed && (
        <>
          <h3 className="panel__title">Activate a different show</h3>
          {!writeGate.allowed && (
            <p className="text-muted" role="status">
              Requires the <code>config:write</code> scope. {writeGate.reason}
            </p>
          )}
          {writeGate.allowed && armedTarget === null && (
            <div>
              {showsState.kind === 'loading' && <p className="text-muted">Loading shows…</p>}
              {showsState.kind === 'error' && (
                <p className="panel panel--error" role="alert">
                  {showsState.message}
                </p>
              )}
              {showsState.kind === 'loaded' && (
                <>
                  {showsState.shows.length === 0 ? (
                    <p className="text-muted">
                      No shows are configured yet. <Link to="/config/show/new">Create one</Link> first.
                    </p>
                  ) : (
                    <>
                      <label className="form-field" style={{ maxWidth: '20rem' }}>
                        Show to activate
                        <select value={selectedShow} onChange={(e) => setSelectedShow(e.target.value)}>
                          <option value="" disabled>
                            Choose a show
                          </option>
                          {showsState.shows.map((s) => (
                            <option key={s.id} value={s.id}>
                              {s.label} ({s.id})
                              {s.id === currentShow ? ' (currently active)' : ''}
                            </option>
                          ))}
                        </select>
                      </label>
                      <button
                        type="button"
                        onClick={arm}
                        disabled={selectedShow.trim() === '' || selectedShow.trim() === currentShow}
                      >
                        Activate this show…
                      </button>
                    </>
                  )}
                </>
              )}
            </div>
          )}

          {/* The sharp control itself: this panel only exists once a
              target has been picked, states what is about to happen and
              what changes, and requires a SECOND, distinct click to
              actually submit — the picker above never submits by itself. */}
          {writeGate.allowed && armedTarget !== null && (
            <div className="panel panel--warning" role="alertdialog" aria-label="Confirm show activation">
              <p>
                <strong>About to activate &ldquo;{armedTarget}&rdquo;.</strong>
              </p>
              <p>
                This replaces the active show
                {currentShow !== null ? ` (currently "${currentShow}")` : ''}. Every declared node&rsquo;s
                expected asset set changes to what &ldquo;{armedTarget}&rdquo; now requires; the{' '}
                <Link to="/assets/manifest">asset manifest</Link> will show which nodes are ready and
                which are not once this takes effect. This is revisioned and audited like every other
                configuration write.
              </p>
              {activateError !== null && (
                <p role="alert" className="session-form__error">
                  {activateError}
                </p>
              )}
              <div style={{ display: 'flex', gap: '0.75rem' }}>
                <ScopedButton
                  requiredScope={CONFIG_WRITE_SCOPE}
                  onClick={() => void confirmActivate()}
                  busy={activating}
                  busyReason="Activating…"
                >
                  {activating ? 'Activating…' : `Confirm: activate "${armedTarget}"`}
                </ScopedButton>
                <button type="button" onClick={cancel} disabled={activating}>
                  Cancel
                </button>
              </div>
            </div>
          )}
        </>
      )}

      {readGate.allowed && state.kind === 'loaded' && state.revisions.length > 0 && (
        <>
          <h3 className="panel__title">Activation history</h3>
          <table className="config-table">
            <thead>
              <tr>
                <th>Revision</th>
                <th>Active</th>
                <th>Activated at</th>
                <th>Activated by</th>
              </tr>
            </thead>
            <tbody>
              {state.revisions.map((rev) => (
                <tr key={rev.revision}>
                  <td>{rev.revision}</td>
                  <td>{rev.active ? 'active' : ''}</td>
                  <td>{formatAbsolute(rev.createdAt)}</td>
                  <td>{rev.createdByPrincipalName ?? '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </div>
  )
}
