import { useEffect, useRef, useState } from 'react'
import {
  ApiError,
  getNightSessionActiveConfig,
  getNightSessionActiveConfigRevisions,
  listConfigObjects,
  putNightSessionActiveConfig,
  type ConfigRevisionMeta,
} from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope, type ScopeGateResult } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from '../components/ScopedButton'
import { showNightSessionPath, showNightSessionNewPath } from '../components/showWorkspacePaths'
import type { ConfigObjectSummary, NightSessionActiveConfigResponse } from '../app/types'
import { Link } from 'react-router-dom'

const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE_GLOBAL = 'config:write'

// Show Night Session.dc.html's list view: the "Active definition" section
// and its "Activate a different one" flow, plus "Activation history"
// below it. Kept as its own component (this file predates the workspace
// tab, when it was routed at /config/night.session.active) because the
// arm/confirm activation flow is a distinct, previously reviewed unit of
// behavior, not because it is its own route any more: NightSessions.tsx
// mounts it inline as the list view's active-pointer section.
//
// night.session.active is a SINGLETON, global across the whole
// coordinator (schema.d.ts: GET/PUT /config/night.session.active takes
// no `show` parameter). This tab renders it inside one show's workspace
// because that is where an operator thinks to look for it, but the
// pointer itself, and its revision history, are not scoped to this show:
// the copy below says so rather than implying a per-show pointer that
// does not exist. `sessions` is this show's own night.session list
// (already loaded by the caller), used only to render the picker and to
// decide whether the current pointer names one of THIS show's
// definitions.
const CONFIG_WRITE_SCOPE = 'config:write'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'not_configured'; reason: string }
  | { kind: 'error'; message: string }
  | {
      kind: 'loaded'
      config: NightSessionActiveConfigResponse
      revisions: ConfigRevisionMeta[]
      // GET /config/night.session.active/revisions never 404s on its own
      // (200 with an empty list when nothing has ever been activated) but
      // it CAN fail independently of the pointer read that already
      // succeeded; carried here rather than rejecting the whole load, so
      // a revisions-history outage never hides the pointer this device
      // already confirmed.
      revisionsError: string | null
    }

// The armed target: a picked session id, or the empty string to
// explicitly clear the pointer (ConfigNightSessionActive's own "session
// is a REQUIRED key but may be the empty string" contract). `null` means
// nothing is armed.
type ArmedTarget = string | null

export function NightSessionActivePanel({
  showId,
  sessions,
  readAllowed,
  writeGate,
  onCurrentSessionChange,
}: {
  showId: string
  sessions: ConfigObjectSummary[]
  readAllowed: boolean
  writeGate: ScopeGateResult
  /** Lets the list table above mark each row Active/Inactive from the same read, rather than issuing a second fetch. */
  onCurrentSessionChange?: (sessionId: string | null) => void
}) {
  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [reloadGeneration, setReloadGeneration] = useState(0)

  const [pickerOpen, setPickerOpen] = useState(false)
  const [armedTarget, setArmedTarget] = useState<ArmedTarget>(null)
  const [selectedSession, setSelectedSession] = useState('')
  const [activating, setActivating] = useState(false)
  const [activateError, setActivateError] = useState<string | null>(null)
  const activatingRef = useRef(false)

  useEffect(() => {
    if (!readAllowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    getNightSessionActiveConfig()
      .then((config) => {
        if (cancelled) return
        setState({ kind: 'loaded', config, revisions: [], revisionsError: null })
        getNightSessionActiveConfigRevisions()
          .then((revisionsResp) => {
            if (cancelled) return
            setState((prev) =>
              prev.kind === 'loaded' ? { ...prev, revisions: revisionsResp.revisions, revisionsError: null } : prev,
            )
          })
          .catch((err: unknown) => {
            if (cancelled) return
            setState((prev) => (prev.kind === 'loaded' ? { ...prev, revisionsError: describeApiError(err) } : prev))
          })
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
  }, [readAllowed, reloadGeneration])

  const currentSession = state.kind === 'loaded' ? state.config.payload.session : null
  const currentIsThisShow = currentSession !== null && currentSession !== '' && sessions.some((s) => s.id === currentSession)

  useEffect(() => {
    onCurrentSessionChange?.(currentSession)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currentSession])

  function arm(target: string): void {
    setActivateError(null)
    setArmedTarget(target)
  }

  function cancel(): void {
    setArmedTarget(null)
    setActivateError(null)
  }

  function closePicker(): void {
    setPickerOpen(false)
    setSelectedSession('')
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
      setPickerOpen(false)
      setSelectedSession('')
      setReloadGeneration((g) => g + 1)
    } catch (err) {
      // Deliberately does not dismiss the confirmation panel: the
      // refusal is about THIS request, not a reason to make the operator
      // re-pick it.
      setActivateError(describeApiError(err))
    } finally {
      activatingRef.current = false
      setActivating(false)
    }
  }

  if (!readAllowed) return null

  return (
    <section aria-labelledby="ns-active" className="night-section">
      <h3 id="ns-active" className="t-meta night-eyebrow">
        Active definition
      </h3>

      {state.kind === 'loading' && (
        <p className="ruled-strip ruled-strip--loading" role="status">
          <span className="ruled-strip__state t-meta">Loading</span>
          <span className="ruled-strip__explanation">Reading the active night-session pointer.</span>
        </p>
      )}
      {state.kind === 'error' && (
        <p className="ruled-strip ruled-strip--failed" role="alert">
          <span className="ruled-strip__state t-meta">Failed</span>
          <span className="ruled-strip__explanation">Could not load the active night-session pointer. {state.message}</span>
        </p>
      )}
      {state.kind === 'not_configured' && (
        <p className="ruled-strip ruled-strip--empty" role="status">
          <span className="ruled-strip__state t-meta">Cleared</span>
          <span className="ruled-strip__explanation">No night-session definition has ever been activated. {state.reason}</span>
        </p>
      )}
      {state.kind === 'loaded' && (
        <div className="night-active-row">
          {currentSession === null || currentSession === '' ? (
            <>
              <span className="status-pair status-pair--warn t-meta">Cleared</span>
              <div className="night-active-row__body">
                <p className="t-body">
                  The pointer is cleared. The night lifecycle has nothing to run until a definition is activated.
                </p>
                <p className="t-small night-muted">
                  Cleared at {formatAbsolute(state.config.updatedAt)}
                  {state.config.createdByPrincipalName !== null && ` by ${state.config.createdByPrincipalName}`}.
                </p>
              </div>
            </>
          ) : (
            <>
              <span className="status-pair status-pair--good t-meta">Active</span>
              <div className="night-active-row__body">
                <p className="t-body">
                  {currentIsThisShow ? (
                    <>
                      <Link className="entity-link" to={showNightSessionPath(showId, currentSession)}>
                        <strong>{sessions.find((s) => s.id === currentSession)?.label ?? currentSession}</strong>
                      </Link>{' '}
                      <span className="t-data night-faint">{currentSession}</span>
                    </>
                  ) : (
                    <>
                      <strong className="t-data">{currentSession}</strong>
                      <span className="t-small night-muted"> · belongs to a different show</span>
                    </>
                  )}
                </p>
                <p className="t-small night-muted">
                  Pointed here at {formatAbsolute(state.config.updatedAt)}
                  {state.config.createdByPrincipalName !== null && ` by ${state.config.createdByPrincipalName}`}. This pointer is
                  global: one night session runs across the whole installation, not one per show.
                </p>
              </div>
            </>
          )}
          <div className="night-active-row__actions">
            {writeGate.allowed ? (
              <button type="button" className="btn btn--secondary" onClick={() => setPickerOpen((open) => !open)}>
                Activate a different one
              </button>
            ) : (
              <span className="scoped-button">
                <button type="button" className="btn btn--secondary" disabled={true} aria-disabled="true" title={writeGate.reason}>
                  Activate a different one
                </button>
                <span className="scoped-button__reason">{writeGate.reason}</span>
              </span>
            )}
          </div>
        </div>
      )}

      <p className="t-small night-muted night-lede">
        One definition is active at a time, and the pointer is its own configuration object with its own history:
        activating is an operational act, not an edit. Clearing it is allowed and leaves the night lifecycle with
        nothing to run.
      </p>

      {pickerOpen && armedTarget === null && writeGate.allowed && state.kind === 'loaded' && (
        <div className="night-activate-picker">
          {sessions.length === 0 ? (
            <p className="t-small night-muted">
              No night-session definitions are authored in this show yet.{' '}
              <Link to={showNightSessionNewPath(showId)}>Create one</Link> first.
            </p>
          ) : (
            <div className="night-activate-picker__row">
              <label className="field" style={{ maxWidth: '20rem' }}>
                <span className="field__label t-small">Definition to activate</span>
                <select
                  className="field__input"
                  value={selectedSession}
                  onChange={(e) => setSelectedSession(e.target.value)}
                >
                  <option value="" disabled>
                    Choose a definition
                  </option>
                  {sessions.map((s) => (
                    <option key={s.id} value={s.id}>
                      {s.label} ({s.id})
                      {s.id === currentSession ? ' — currently active' : ''}
                    </option>
                  ))}
                </select>
              </label>
              <div className="night-activate-picker__actions">
                <button
                  type="button"
                  className="btn btn--primary"
                  onClick={() => arm(selectedSession.trim())}
                  disabled={selectedSession.trim() === '' || selectedSession.trim() === currentSession}
                >
                  Activate this definition…
                </button>
                {currentSession !== null && currentSession !== '' && (
                  <button type="button" className="btn btn--quiet" onClick={() => arm('')}>
                    Clear the pointer…
                  </button>
                )}
                <button type="button" className="btn btn--quiet" onClick={closePicker}>
                  Cancel
                </button>
              </div>
            </div>
          )}
        </div>
      )}

      {armedTarget !== null && (
        <div className="night-confirm" role="alertdialog" aria-label="Confirm night-session activation">
          {armedTarget === '' ? (
            <p>
              <strong>About to clear the active night-session pointer.</strong> The coordinator will run no night
              session until a new one is activated.
            </p>
          ) : (
            <p>
              <strong>About to activate &ldquo;{armedTarget}&rdquo;.</strong>
              {currentSession !== null && currentSession !== '' && ` This replaces the active session (currently "${currentSession}").`}
            </p>
          )}
          {activateError !== null && (
            <p role="alert" className="field__error">
              {activateError}
            </p>
          )}
          <div className="night-confirm__actions">
            <ScopedButton
              requiredScope={CONFIG_WRITE_SCOPE}
              className="btn btn--primary"
              onClick={() => void confirmActivate()}
              busy={activating}
              busyReason="Activating…"
            >
              {activating ? 'Activating…' : armedTarget === '' ? 'Confirm: clear the pointer' : `Confirm: activate "${armedTarget}"`}
            </ScopedButton>
            <button type="button" className="btn btn--quiet" onClick={cancel} disabled={activating}>
              Cancel
            </button>
          </div>
        </div>
      )}

      {state.kind === 'loaded' && state.revisionsError !== null && (
        <p className="ruled-strip ruled-strip--failed" role="alert">
          <span className="ruled-strip__state t-meta">Failed</span>
          <span className="ruled-strip__explanation">The activation history could not be loaded. {state.revisionsError}</span>
        </p>
      )}
      {state.kind === 'loaded' && state.revisions.length > 0 && (
        <section aria-labelledby="ns-hist" className="night-section">
          <h3 id="ns-hist" className="t-meta night-eyebrow">
            Activation history
          </h3>
          <p className="t-small night-muted">
            Revisions of the active pointer, newest first. This history is global, not scoped to this show: the
            coordinator activates one night session at a time across the whole installation. A cleared pointer is a
            real revision, not a gap; which definition an older revision pointed to is not carried in this history,
            only the current one.
          </p>
          <ol className="night-history">
            {state.revisions.map((rev) => (
              <li key={rev.revision} className="night-history__row">
                <span className="t-meta night-history__revision">
                  {rev.active ? 'Active · ' : ''}
                  {rev.revision}
                </span>
                <p className="t-small night-muted">
                  {formatAbsolute(rev.createdAt)}
                  {rev.createdByPrincipalName !== null && ` by ${rev.createdByPrincipalName}`}
                  {rev.active &&
                    state.kind === 'loaded' &&
                    (currentSession === '' || currentSession === null
                      ? ' — cleared'
                      : ` — pointed at ${currentSession}`)}
                </p>
              </li>
            ))}
          </ol>
        </section>
      )}
    </section>
  )
}

/**
 * Compatibility shim for the pre-overhaul `/config/night.session.active`
 * route, still declared in App.tsx (which this group does not edit).
 * ROUTE-MAP.md's "old addresses, deliberately not redirected" table
 * marks `/config/night.session*` as going away entirely once the owner
 * wires the new `/shows/:showId/night-sessions*` routes this task adds;
 * until then this keeps the old address compiling and minimally
 * functional rather than crashing, by fetching every night.session
 * object system-wide (this old route carries no show scoping at all)
 * and reusing the same panel the new list view mounts.
 */
export function NightSessionActive() {
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE_GLOBAL)
  const [sessions, setSessions] = useState<ConfigObjectSummary[]>([])

  useEffect(() => {
    if (!readGate.allowed) return
    let cancelled = false
    listConfigObjects('night.session')
      .then((resp) => {
        if (!cancelled) setSessions(resp.objects)
      })
      .catch(() => {
        /* NightSessionActivePanel's own picker degrades to "none authored" on an empty list; this old route is on its way out. */
      })
    return () => {
      cancelled = true
    }
  }, [readGate.allowed])

  if (!readGate.allowed) {
    return (
      <p className="ruled-strip ruled-strip--no-permission" role="status">
        <span className="ruled-strip__state t-meta">No permission</span>
        <span className="ruled-strip__explanation">{readGate.reason}</span>
      </p>
    )
  }

  return (
    <div className="operator-page">
      <NightSessionActivePanel showId="" sessions={sessions} readAllowed={readGate.allowed} writeGate={writeGate} />
    </div>
  )
}
