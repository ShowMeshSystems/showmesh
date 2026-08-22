import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  ApiError,
  getNightSessionActiveConfig,
  getNightSessionActiveConfigRevisions,
  listConfigObjects,
  putNightSessionActiveConfig,
  type ConfigRevisionMeta,
} from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from '../components/ScopedButton'
import type { ConfigObjectSummary, NightSessionActiveConfigResponse } from '../app/types'

// Track F seam F1 (ADR-039 rule 4): the night.session.active singleton
// pointer, on ShowActive.tsx's exact precedent — "activation is the sharp
// control," so this view never fires the PUT from a bare click. Picking
// a session, or clearing the pointer, only ARMS a confirmation panel; a
// second, distinct click actually submits.
const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE = 'config:write'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'not_configured'; reason: string }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; config: NightSessionActiveConfigResponse; revisions: ConfigRevisionMeta[] }

type SessionsState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; sessions: ConfigObjectSummary[] }

// The armed target: a picked session id, or the empty string to
// explicitly clear the pointer (ConfigNightSessionActive's own "session
// is a REQUIRED key but may be the empty string" contract). `null` means
// nothing is armed.
type ArmedTarget = string | null

export function NightSessionActive() {
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)

  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [sessionsState, setSessionsState] = useState<SessionsState>({ kind: 'loading' })
  const [reloadGeneration, setReloadGeneration] = useState(0)

  const [armedTarget, setArmedTarget] = useState<ArmedTarget>(null)
  const [selectedSession, setSelectedSession] = useState('')
  const [activating, setActivating] = useState(false)
  const [activateError, setActivateError] = useState<string | null>(null)
  const activatingRef = useRef(false)

  useEffect(() => {
    if (!readGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    Promise.all([getNightSessionActiveConfig(), getNightSessionActiveConfigRevisions()])
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
    listConfigObjects('night.session')
      .then((resp) => {
        if (!cancelled) setSessionsState({ kind: 'loaded', sessions: resp.objects })
      })
      .catch((err: unknown) => {
        if (!cancelled) setSessionsState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [readGate.allowed])

  const currentSession = state.kind === 'loaded' ? state.config.payload.session : null

  function arm(target: string): void {
    setActivateError(null)
    setArmedTarget(target)
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
      await putNightSessionActiveConfig({ session: armedTarget })
      setArmedTarget(null)
      setReloadGeneration((g) => g + 1)
    } catch (err) {
      // Deliberately does not dismiss the confirmation panel — matching
      // ShowActive.tsx's identical reasoning: the refusal is about THIS
      // request, not a reason to make the operator re-pick it.
      setActivateError(describeApiError(err))
    } finally {
      activatingRef.current = false
      setActivating(false)
    }
  }

  return (
    <div>
      <h2 className="panel__title">Active night session</h2>
      <p className="text-muted">
        The night session the coordinator is currently running. Activating a different session, or
        clearing the pointer, is revisioned and audited like every other configuration write here.
        See <Link to="/night">the night session</Link> for the currently running lifecycle state.
      </p>

      {!readGate.allowed && (
        <p className="panel panel--error" role="status">
          {readGate.reason}
        </p>
      )}

      {readGate.allowed && state.kind === 'loading' && <p className="text-muted">Loading the active night session…</p>}
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
              {state.config.payload.session === '' ? (
                'none (the pointer is cleared)'
              ) : (
                <Link className="entity-link" to={`/config/night.session/${encodeURIComponent(state.config.payload.session)}`}>
                  {state.config.payload.session}
                </Link>
              )}
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
          <h3 className="panel__title">Activate a different night session</h3>
          {!writeGate.allowed && (
            <p className="text-muted" role="status">
              Requires the <code>config:write</code> scope. {writeGate.reason}
            </p>
          )}
          {writeGate.allowed && armedTarget === null && (
            <div>
              {sessionsState.kind === 'loading' && <p className="text-muted">Loading night sessions…</p>}
              {sessionsState.kind === 'error' && (
                <p className="panel panel--error" role="alert">
                  {sessionsState.message}
                </p>
              )}
              {sessionsState.kind === 'loaded' && (
                <>
                  {sessionsState.sessions.length === 0 ? (
                    <p className="text-muted">
                      No night sessions are configured yet. <Link to="/config/night.session/new">Create one</Link> first.
                    </p>
                  ) : (
                    <>
                      <label className="form-field" style={{ maxWidth: '20rem' }}>
                        Night session to activate
                        <select value={selectedSession} onChange={(e) => setSelectedSession(e.target.value)}>
                          <option value="" disabled>
                            Choose a night session
                          </option>
                          {sessionsState.sessions.map((s) => (
                            <option key={s.id} value={s.id}>
                              {s.label} ({s.id})
                              {s.id === currentSession ? ' — currently active' : ''}
                            </option>
                          ))}
                        </select>
                      </label>
                      <div style={{ display: 'flex', gap: '0.75rem' }}>
                        <button
                          type="button"
                          onClick={() => arm(selectedSession.trim())}
                          disabled={selectedSession.trim() === '' || selectedSession.trim() === currentSession}
                        >
                          Activate this session…
                        </button>
                        {currentSession !== null && currentSession !== '' && (
                          <button type="button" onClick={() => arm('')}>
                            Clear the pointer…
                          </button>
                        )}
                      </div>
                    </>
                  )}
                </>
              )}
            </div>
          )}

          {/* The sharp control itself, matching ShowActive.tsx's identical
              shape: this panel only exists once a target has been picked
              (or clearing has been chosen), states what is about to
              happen, and requires a SECOND, distinct click to actually
              submit. */}
          {writeGate.allowed && armedTarget !== null && (
            <div className="panel panel--warning" role="alertdialog" aria-label="Confirm night session activation">
              {armedTarget === '' ? (
                <p>
                  <strong>About to clear the active night session pointer.</strong> The
                  coordinator will run no night session at all until a new one is activated.
                </p>
              ) : (
                <p>
                  <strong>About to activate &ldquo;{armedTarget}&rdquo;.</strong>
                  {currentSession !== null && currentSession !== '' && ` This replaces the active session (currently "${currentSession}").`}
                </p>
              )}
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
                  {activating ? 'Activating…' : armedTarget === '' ? 'Confirm: clear the pointer' : `Confirm: activate "${armedTarget}"`}
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
