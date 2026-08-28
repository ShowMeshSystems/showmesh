import { useEffect, useState } from 'react'
import { getCurrentNightSession, getNightSessionConfigRevision } from '../api'
import { describeApiError, evaluateAnyScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import {
  NightCueOutcomeBadge,
  NightCueStateBadge,
  NightLifecycleBadge,
  NightPhaseEvidenceBadge,
  NightReadinessCheckBadge,
  NightReadinessOutcomeBadge,
} from '../components/DomainBadges'
import { PanelErrorBoundary } from '../components/PanelErrorBoundary'
import { EvidenceTable, OperatorPageHeader, OperatorSection, StatusStrip, StatusStripItem, UnavailableBlock } from '../components/SharedLayouts'
import type { ConfigNightSession, NightBackgroundAudioStep, NightCue, NightSessionState } from '../app/types'
import '../styles/operator-pages.css'

// Track F seam F2 (RESTING-MODE.md, ADR-038): the night-session operating
// view. Same posture as ResolumeView.tsx (this codebase's other
// live/operating view): per-section PanelErrorBoundary, evidence rendered
// with a state and a reason rather than omitted, and command buttons that
// stay reachable-but-disabled-with-a-reason rather than hidden
// (ADR-024 decision 12).
//
// Two fields are deliberately absent from this view and every one below
// it: brightness ceiling/multiplier, and interlock/site-control state.
// Neither capability exists in this coordinator yet (RESTING-MODE.md §10's
// siteControl/interlocks are specified but unimplemented, and the schema
// carries no brightness field at all) — omitted rather than rendered as an
// empty placeholder, matching this codebase's "an absent capability is not
// a blank control" posture elsewhere (Layout.tsx's own comment on why
// Control/Configure stayed absent from the nav until their first real
// behaviour shipped).
//
// A THIRD field, background audio's configured maxGainDb ceiling, is
// absent for a different reason: it is genuinely not part of
// NightSessionState.backgroundAudio (NightBackgroundAudio) on
// GET /night/session — it only exists on the WRITE-side
// ConfigNightSessionBackgroundAudio/-Write config schemas. Rendering it
// here would mean either fetching and cross-referencing the separate
// night.session config object (a config read next to a session-state
// view — a different resource with its own revision, not this endpoint's
// evidence) or inventing a field this response does not carry. Neither is
// this seam's contract, so it stays off this screen until GET
// /night/session actually carries it.

type LoadState =
  | { kind: 'loading' }
  // Only reachable when this view has NEVER successfully loaded a session
  // at all (the very first GET fails). Once any session has loaded, a
  // later failure degrades the 'loaded' state in place instead (see
  // `stale`/`staleError` below) — see this function's own header comment
  // and review finding 2 (ADR-024 constraint 23: a transient read failure
  // must never cost the operator visibility of the lifecycle state).
  | { kind: 'error'; message: string }
  | {
      kind: 'loaded'
      session: NightSessionState
      // True when the MOST RECENT background refresh (a reload, or the
      // periodic GET this view's own mount effect re-runs) failed —
      // `session` is then a stale, possibly-outdated last-known value
      // rather than freshly confirmed. Cleared back to false the next
      // time either a GET succeeds or a live frame is actually ADOPTED
      // (see `adoptSession` below) — mirroring
      // `Model.sessionFetchFailed`'s identical "stale renders as stale,
      // never silently as current" contract (app/session.ts).
      stale: boolean
      staleError: string | null
    }

type ConfiguredStepsState =
  | { kind: 'idle' }
  | { kind: 'loading' }
  | { kind: 'unavailable'; reason: string }
  | { kind: 'loaded'; payload: ConfigNightSession; revision: number; updatedAt: string }

const CONFIG_READ_SCOPES = ['show:macro:run', 'config:write']

function findNextCue(cues: NightCue[]): NightCue | null {
  return cues.find((c) => c.state === 'not_dispatched' || c.state === 'pending' || c.state === 'dispatched') ?? null
}

/**
 * Whichever of `current` (already on screen) and `incoming` (a fresh GET
 * response, a live `nightSession.changed` frame, or a command's own
 * response) is actually newer, compared by `updatedAt` rather than by
 * arrival order. Review finding 1: this view previously assumed a live
 * frame or a fresh GET always wins by virtue of arriving second, which a
 * frame landing WHILE a slower GET is still in flight silently violates
 * (the GET's `.then` would blindly overwrite the frame's newer state).
 * `current === null` (nothing loaded yet) always adopts `incoming`.
 * `NaN` from an unparseable timestamp is treated as "not newer" — never
 * as newer, which would let a malformed value evict good data.
 */
function newerSession(current: NightSessionState | null, incoming: NightSessionState): NightSessionState {
  if (current === null) return incoming
  const currentMs = Date.parse(current.updatedAt)
  const incomingMs = Date.parse(incoming.updatedAt)
  if (Number.isNaN(incomingMs)) return current
  if (Number.isNaN(currentMs)) return incoming
  return incomingMs > currentMs ? incoming : current
}

function ShowNightRunHeader({ session }: { session: NightSessionState }) {
  const nextCue = session.cues.state === 'recorded' ? findNextCue(session.cues.cues) : null
  return (
    <section className="show-night__runhead" aria-label="Current Show Night state">
      <div className="show-night__runstate">
        <span className="show-night__eyebrow">Now</span>
        <NightLifecycleBadge state={session.state} />
        <span className="show-night__runstate-detail">Cycle {session.cycle}</span>
      </div>
      <div className="show-night__now">
        <h2>{session.id === '' ? 'No active session' : 'Current Show Night'}</h2>
        <p>{session.id === '' ? 'No session has ever been created.' : `Session ${session.id}`}</p>
        <div className="show-night__runmeta">
          <span><b>State entered</b>{formatAbsolute(session.stateEnteredAt)}</span>
          <span><b>Configuration</b>{session.configObjectId === '' ? 'unavailable' : `${session.configObjectId} · revision ${session.configRevision}`}</span>
          <span><b>Last update</b>{formatAbsolute(session.updatedAt)}</span>
        </div>
      </div>
      <div className="show-night__next">
        <span className="show-night__eyebrow">Next</span>
        {nextCue === null ? (
          <strong>{session.cues.state === 'recorded' ? 'No pending Transition Step' : 'Unavailable'}</strong>
        ) : (
          <>
            <strong>{nextCue.name}</strong>
            <NightCueStateBadge state={nextCue.state} />
          </>
        )}
        <small>{session.cues.state === 'recorded' ? 'From the current cycle outbox.' : session.cues.reason}</small>
      </div>
    </section>
  )
}

export function NightSession() {
  const model = useModelContext()
  const configReadGate = evaluateAnyScope(model.session, model.sessionFetchFailed, CONFIG_READ_SCOPES)
  const configUnavailableReason = configReadGate.allowed ? '' : configReadGate.reason
  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [reloadGeneration, setReloadGeneration] = useState(0)
  const [configuredSteps, setConfiguredSteps] = useState<ConfiguredStepsState>({ kind: 'idle' })

  // The one place `state` is asked to adopt a NEW session, from any
  // source (initial GET, a reload's GET, a live stream frame, or a
  // command's own response) — routes every one of them through
  // [newerSession] so a frame that already won cannot be rolled back by
  // a slower-arriving GET, and vice versa (review finding 1). Always
  // clears staleness: adopting anything, even one [newerSession] decides
  // to keep the CURRENT session over, means this device just heard from
  // the coordinator successfully.
  function adoptSession(session: NightSessionState): void {
    setState((prev) => ({
      kind: 'loaded',
      session: newerSession(prev.kind === 'loaded' ? prev.session : null, session),
      stale: false,
      staleError: null,
    }))
  }

  useEffect(() => {
    let cancelled = false
    getCurrentNightSession()
      .then((resp) => {
        if (!cancelled) adoptSession(resp.session)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        // Review finding 2: a transient read failure degrades an
        // already-loaded state IN PLACE (marked stale, error carried
        // alongside) rather than replacing it — the operator keeps
        // seeing the last known lifecycle state instead of one error
        // line where the whole page used to be. Only the very first
        // load, which has nothing to fall back to, becomes the
        // dedicated 'error' state.
        setState((prev) =>
          prev.kind === 'loaded'
            ? { ...prev, stale: true, staleError: describeApiError(err) }
            : { kind: 'error', message: describeApiError(err) },
        )
      })
    return () => {
      cancelled = true
    }
  }, [reloadGeneration])

  // `model.nightSession` is not seeded from Snapshot (domain.ts's own
  // comment on Model.nightSession) — it only ever updates via a live
  // `nightSession.changed` frame, and the store clears it to null on
  // every resnapshot (reconnect/stream.reset — store.ts's applySnapshot).
  // Routed through [adoptSession] rather than a blind replace — see that
  // function's own comment and review finding 1.
  useEffect(() => {
    if (model.nightSession === null) return
    adoptSession(model.nightSession)
  }, [model.nightSession])

  const configId = state.kind === 'loaded' ? state.session.configObjectId : ''
  const configRevision = state.kind === 'loaded' ? state.session.configRevision : 0
  useEffect(() => {
    if (configId === '') {
      setConfiguredSteps({ kind: 'idle' })
      return
    }
    if (!configReadGate.allowed) {
      setConfiguredSteps({ kind: 'unavailable', reason: configUnavailableReason })
      return
    }
    let cancelled = false
    setConfiguredSteps({ kind: 'loading' })
    getNightSessionConfigRevision(configId, configRevision)
      .then((config) => {
        if (!cancelled) setConfiguredSteps({ kind: 'loaded', payload: config.payload, revision: config.revision, updatedAt: config.updatedAt })
      })
      .catch((err: unknown) => {
        if (!cancelled) setConfiguredSteps({ kind: 'unavailable', reason: describeApiError(err) })
      })
    return () => { cancelled = true }
  }, [configId, configRevision, configReadGate.allowed, configUnavailableReason])

  return (
    <div className="operator-page show-night-page">
      <OperatorPageHeader
        eyebrow={state.kind === 'loaded' && state.session.configObjectId !== '' ? `Show Night / ${state.session.configObjectId}` : 'Show Night'}
        title="Show Night"
        lede="The coordinator’s current Run of Show, with FPP retaining scheduling and playback authority."
        actions={state.kind === 'loaded' && state.session.configObjectId !== '' ? (
          <a className="button" href={`/config/night.session/${encodeURIComponent(state.session.configObjectId)}`}>Edit Show Night</a>
        ) : undefined}
      />
      <p className="show-night-page__contract text-muted">The lifecycle controller&rsquo;s own state is separate from observed playback evidence. The authored definition is pinned by the <code>night.session</code> configuration.</p>
      <OperatorSection title="FPP boundary" detail="Authority and unavailable evidence are shown literally.">
        <p className="show-night__fpp-boundary">
          FPP owns the night schedule, playlist selection, and playback progression. ShowMesh records lifecycle and plugin evidence; it does not infer an FPP playhead, schedule, or plugin result when that evidence is unavailable.
        </p>
      </OperatorSection>

      {state.kind === 'loading' && <p className="text-muted">Loading the night session…</p>}
      {state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          {state.message}
        </p>
      )}

      {state.kind === 'loaded' && state.stale && state.staleError !== null && (
        // Review finding 2: shown ALONGSIDE the last known state below,
        // never instead of it — a transient read failure must not cost
        // the operator visibility of the lifecycle state.
        <p className="panel panel--error show-night-page__stale" role="alert">
          The Run of Show below is the last one this device could confirm; the most recent refresh failed: {state.staleError}
        </p>
      )}

      {state.kind === 'loaded' && <ShowNightRunHeader session={state.session} />}

      {state.kind === 'loaded' && (
        <NightSessionDetail session={state.session} configuredSteps={configuredSteps} onReload={() => setReloadGeneration((g) => g + 1)} />
      )}
    </div>
  )
}

function NightSessionDetail({ session, configuredSteps, onReload }: { session: NightSessionState; configuredSteps: ConfiguredStepsState; onReload: () => void }) {
  const nextCue = session.cues.state === 'recorded' ? findNextCue(session.cues.cues) : null
  const currentCueIndex = session.cues.state === 'recorded'
    ? session.cues.cues.findIndex((cue) => cue.state === 'pending' || cue.state === 'dispatched' || cue.state === 'ambiguous' || cue.state === 'not_dispatched')
    : -1

  return (
    <div className="show-night__detail">
      <PanelErrorBoundary panelLabel="Lifecycle state">
        <section className="panel show-night__metadata">
          <dl className="field-list">
            <dt>Session id</dt>
            <dd>{session.id === '' ? 'none (no session has ever been created)' : session.id}</dd>
            <dt>State entered</dt>
            <dd>{formatAbsolute(session.stateEnteredAt)}</dd>
            <dt>Configuration</dt>
            <dd>
              {session.configObjectId === '' ? (
                'none'
              ) : (
                <>
                  {session.configObjectId} revision {session.configRevision}
                </>
              )}
            </dd>
            <dt>Shutdown intent</dt>
            <dd>{session.shutdownIntent === '' ? 'none' : session.shutdownIntent}</dd>
            <dt>Armed show</dt>
            <dd>
              {session.armedShowId === '' ? 'none' : session.armedShowId}
              {session.showCommitted ? ' (committed)' : ''}
            </dd>
          </dl>
          <button type="button" onClick={onReload}>
            Reload
          </button>
        </section>
      </PanelErrorBoundary>

      <OperatorSection
        title="Configured Transition Steps"
        detail="Authored work pinned for this Show Night. These are not runtime executions."
      >
        {configuredSteps.kind === 'idle' && <p className="text-muted">No Show Night configuration is pinned for this session.</p>}
        {configuredSteps.kind === 'loading' && <p className="text-muted">Loading configured Transition Steps…</p>}
        {configuredSteps.kind === 'unavailable' && (
          <UnavailableBlock title="Configured Transition Steps unavailable" reason={configuredSteps.reason} headingLevel={3} />
        )}
        {configuredSteps.kind === 'loaded' && (
          <>
            <StatusStrip label="Configured Transition Step evidence">
              <StatusStripItem label="Configuration" detail={`Last confirmed ${formatAbsolute(configuredSteps.updatedAt)}`}>
                Revision {configuredSteps.revision}
              </StatusStripItem>
              <StatusStripItem label="Enter show" detail="Configured, not executed">
                {configuredSteps.payload.enterShow.cues.length} Transition Steps
              </StatusStripItem>
              <StatusStripItem label="Enter resting" detail="Configured, not executed">
                {configuredSteps.payload.enterResting.cues.length} Transition Steps
              </StatusStripItem>
            </StatusStrip>
            <EvidenceTable label="Configured Transition Steps">
              <table className="show-night__table">
                <thead><tr><th>Phase</th><th>Transition Step</th><th>Target / action</th><th>Configured state</th><th>Last confirmed</th></tr></thead>
                <tbody>
                  {[
                    ...configuredSteps.payload.enterShow.cues.map((step) => ({ phase: 'Enter show', step })),
                    ...configuredSteps.payload.enterResting.cues.map((step) => ({ phase: 'Enter resting', step })),
                  ].map(({ phase, step }, index) => (
                    <tr key={`${phase}-${step.name}-${index}`}>
                      <td>{phase}</td><td><strong>{step.name}</strong><small>{step.offsetMs >= 0 ? '+' : ''}{step.offsetMs} ms, {step.onFailure} on failure</small></td>
                      <td><strong>{step.role}</strong><small>{step.action}</small></td>
                      <td>Configured</td><td>{formatAbsolute(configuredSteps.updatedAt)}</td>
                    </tr>
                  ))}
                  {configuredSteps.payload.enterShow.cues.length + configuredSteps.payload.enterResting.cues.length === 0 && (
                    <tr><td colSpan={5}>No Transition Steps are configured for this Show Night.</td></tr>
                  )}
                </tbody>
              </table>
            </EvidenceTable>
          </>
        )}
      </OperatorSection>

      <h2 className="show-night__board-title">Run of Show</h2>
      <p className="show-night__board-detail text-muted">Runtime executions from the current lifecycle. State and confirmation time belong to each item.</p>
      {/* Review finding 6: this list is every cue in the current cycle's
          outbox across all three phases, not scoped to any one of them —
          the heading no longer claims a filter this table does not apply.
          Each row's own Phase column (below) still says which phase it
          belongs to. */}
      <PanelErrorBoundary panelLabel="Transition Step evidence">
        <section className="show-night__board">
          {session.cues.state !== 'recorded' && (
            <p className="text-muted" role="status">
              {session.cues.reason}
            </p>
          )}
          {session.cues.state === 'recorded' &&
            (session.cues.cues.length === 0 ? (
              <p className="text-muted">No Transition Steps recorded for this cycle yet.</p>
            ) : (
              <EvidenceTable label="Run of Show runtime executions">
                <table className="show-night__table">
                  <thead>
                    <tr>
                      <th>When</th>
                      <th>Transition Step</th>
                      <th>Phase</th>
                      <th>Target / action</th>
                      <th>Runtime state</th>
                      <th>Last confirmed</th>
                    </tr>
                  </thead>
                  <tbody>
                    {session.cues.cues.map((cue, i) => {
                      const current = i === currentCueIndex
                      return (
                      <tr className={current ? 'show-night__row--current' : undefined} aria-current={current ? 'step' : undefined} key={`${cue.phase}-${cue.name}-${i}`}>
                        <td className="show-night__when">{current ? 'NOW' : `STEP ${i + 1}`}</td>
                        <td><strong>{cue.name}</strong><small>{cue.actionRevision === null ? 'Revision unavailable' : `Pinned revision ${cue.actionRevision}`}</small>{cue.reason !== undefined && <small>{cue.reason}</small>}</td>
                        <td>{cue.phase}</td>
                        <td><strong>{cue.role}</strong><small>{cue.action}</small></td>
                        <td><NightCueStateBadge state={cue.state} />{cue.outcome !== undefined && <span className="show-night__outcome"><NightCueOutcomeBadge outcome={cue.outcome} /></span>}</td>
                        <td>{cue.resolvedAt === null ? (cue.dispatchedAt === null ? 'Not confirmed: not dispatched' : `Dispatched ${formatAbsolute(cue.dispatchedAt)}, not confirmed`) : formatAbsolute(cue.resolvedAt)}</td>
                      </tr>
                      )
                    })}
                  </tbody>
                </table>
              </EvidenceTable>
            ))}
        </section>
      </PanelErrorBoundary>

      <h3 className="section-title">Next Transition Step</h3>
      <PanelErrorBoundary panelLabel="Next Transition Step">
        <section className="panel show-night__next-step" role="status">
          {session.cues.state !== 'recorded' && <p className="text-muted">{session.cues.reason}</p>}
          {session.cues.state === 'recorded' && nextCue === null && (
            <p className="text-muted">No pending Transition Step in the current cycle&rsquo;s outbox.</p>
          )}
          {session.cues.state === 'recorded' && nextCue !== null && (
            <dl className="field-list">
              <dt>Name</dt>
              <dd>{nextCue.name}</dd>
              <dt>Phase</dt>
              <dd>{nextCue.phase}</dd>
              <dt>State</dt>
              <dd><NightCueStateBadge state={nextCue.state} /></dd>
            </dl>
          )}
        </section>
      </PanelErrorBoundary>

      <h3 className="section-title">Final-cycle status</h3>
      <PanelErrorBoundary panelLabel="Final-cycle status">
        <section className="panel show-night__after-status">
          <dl className="field-list">
            <dt>Cycle</dt>
            <dd>{session.cycle}</dd>
            <dt>Final show requested</dt>
            <dd>
              {session.finalShowRequested ? 'yes' : 'no'}
              {session.finalShowRequestedAt !== null && `, at ${formatAbsolute(session.finalShowRequestedAt)}`}
            </dd>
            <dt>Admission closed</dt>
            <dd>
              {session.admissionClosed ? 'yes' : 'no'}
              {session.admissionClosedAt !== null && `, at ${formatAbsolute(session.admissionClosedAt)}`}
            </dd>
          </dl>
        </section>
      </PanelErrorBoundary>

      <h3 className="section-title">Transition evidence</h3>
      <PanelErrorBoundary panelLabel="Transition evidence">
        <section className="panel show-night__evidence-card">
          <div className="show-night__evidence-badge"><NightPhaseEvidenceBadge state={session.transition.state} /></div>
          <p className="text-muted">{session.transition.reason}</p>
        </section>
      </PanelErrorBoundary>

      <h3 className="section-title">Power phase evidence</h3>
      <PanelErrorBoundary panelLabel="Power phase evidence">
        <section className="panel show-night__evidence-card">
          <div className="show-night__evidence-badge"><NightPhaseEvidenceBadge state={session.powerPhase.state} /></div>
          <p className="text-muted">{session.powerPhase.reason}</p>
        </section>
      </PanelErrorBoundary>

      <h3 className="section-title">Background audio</h3>
      {/* resting.backgroundAudio's own durable step log — the
          resting bed's playback sequence plus every announcement's duck,
          restore/interrupt-stop step, all in one list keyed by their own
          `sequence` column (never a second parallel vocabulary — this
          reuses NightCueStateBadge/NightCueOutcomeBadge exactly as the Cues
          table above does, since NightBackgroundAudioStep's state/outcome
          enums are the identical wire vocabulary as NightCue's). An empty
          `steps` list with state "recorded" means either backgroundAudio
          is not configured at all, or it has never started this cycle
          (NightBackgroundAudio's own schema description) — the two cannot
          be told apart from this endpoint alone, so both render the same
          explicit line rather than a blank section, on the Cues table's
          own precedent just above. */}
      <PanelErrorBoundary panelLabel="Background audio evidence">
        <section className="panel show-night__evidence-card">
          {session.backgroundAudio.state !== 'recorded' && (
            <p className="text-muted" role="status">
              {session.backgroundAudio.reason}
            </p>
          )}
          {session.backgroundAudio.state === 'recorded' &&
            (session.backgroundAudio.steps.length === 0 ? (
              <p className="text-muted">
                No background audio steps recorded for this cycle yet, or none is configured.
              </p>
            ) : (
              <div className="table-scroll show-night__table-scroll">
                <table className="config-table">
                  <thead>
                    <tr>
                      <th>Sequence</th>
                      <th>Transition Step</th>
                      <th>Kind</th>
                      <th>Pinned revision</th>
                      <th>State</th>
                      <th>Outcome</th>
                      <th>Reason</th>
                      <th>Dispatched</th>
                      <th>Resolved</th>
                    </tr>
                  </thead>
                  <tbody>
                    {session.backgroundAudio.steps.map((step: NightBackgroundAudioStep, i) => (
                      <tr key={`${step.sequence}-${step.cueName}-${step.kind}-${i}`}>
                        <td>{step.sequence}</td>
                        <td>{step.cueName}</td>
                        <td>{step.kind}</td>
                        <td>{step.actionRevision}</td>
                        <td>
                          <NightCueStateBadge state={step.state} />
                        </td>
                        <td>
                          {/* Same posture as the Cues table's own comment
                              just above: a refused restore/resume step or
                              an unconfirmed gain step must not blend in
                              with a confirmed one — refused renders 'bad'
                              tone with its own icon, unconfirmed renders
                              its own distinct icon/label, and neither is
                              ever collapsed into 'confirmed'. */}
                          {step.outcome === undefined ? '-' : <NightCueOutcomeBadge outcome={step.outcome} />}
                        </td>
                        <td>{step.reason ?? '-'}</td>
                        <td>{step.dispatchedAt === null ? 'not dispatched' : formatAbsolute(step.dispatchedAt)}</td>
                        <td>{step.resolvedAt === null ? 'not resolved' : formatAbsolute(step.resolvedAt)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ))}
        </section>
      </PanelErrorBoundary>

      <h3 className="section-title">Readiness</h3>
      <PanelErrorBoundary panelLabel="Readiness">
        <section className="panel show-night__evidence-card">
          {session.readiness.state !== 'recorded' && (
            <p className="text-muted" role="status">
              {session.readiness.reason}
            </p>
          )}
          {session.readiness.state === 'recorded' && (
            <>
              <div className="show-night__evidence-badge">
                {session.readiness.outcome !== undefined && (
                  <NightReadinessOutcomeBadge outcome={session.readiness.outcome} />
                )}
              </div>
              <dl className="field-list">
                <dt>Reason</dt>
                <dd>{session.readiness.reason}</dd>
                <dt>Completed</dt>
                <dd>{session.readiness.completedAt !== undefined ? formatAbsolute(session.readiness.completedAt) : '-'}</dd>
                <dt>Same epoch</dt>
                <dd>{session.readiness.sameEpoch ? 'yes' : 'no'}</dd>
                <dt>Fresh</dt>
                <dd>{session.readiness.fresh ? 'yes' : 'no'}</dd>
              </dl>
              {session.readiness.checks.length > 0 && (
                <div className="table-scroll show-night__table-scroll">
                  <table className="config-table">
                    <thead>
                      <tr>
                        <th>Check</th>
                        <th>State</th>
                        <th>Reason</th>
                      </tr>
                    </thead>
                    <tbody>
                      {session.readiness.checks.map((check) => (
                        <tr key={check.name}>
                          <td>{check.name}</td>
                          <td>
                            <NightReadinessCheckBadge state={check.state} />
                          </td>
                          <td>{check.reason}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </>
          )}
        </section>
      </PanelErrorBoundary>

      <h3 className="section-title">Degraded state</h3>
      <PanelErrorBoundary panelLabel="Degraded state">
        <section className="panel show-night__evidence-card" role="status">
          <p>
            <strong>{session.degraded ? 'Degraded' : 'Not degraded'}</strong>
            {session.degraded && session.degradedReason !== undefined && `: ${session.degradedReason}`}
          </p>
          <p>
            <strong>{session.attributionDegraded ? 'Attribution degraded' : 'Attribution intact'}</strong>
            {session.attributionDegraded &&
              ': this session applied at least one command despite its audit entry failing to write, or ran an autonomous dispatch with no authorizing principal recorded. This never clears once true.'}
          </p>
        </section>
      </PanelErrorBoundary>

      <h3 className="section-title">Authorization</h3>
      <PanelErrorBoundary panelLabel="Authorization">
        <section className="panel show-night__evidence-card">
          {session.authorization.state === 'unknown' ? (
            <p className="text-muted" role="status">
              {session.authorization.reason ?? 'Nothing has been attributed yet.'}
            </p>
          ) : (
            <dl className="field-list">
              <dt>Principal</dt>
              <dd>{session.authorization.principalName ?? session.authorization.principalId ?? 'unknown'}</dd>
              <dt>Command</dt>
              <dd>{session.authorization.command ?? '-'}</dd>
              <dt>Recorded at</dt>
              <dd>{formatAbsolute(session.authorization.recordedAt)}</dd>
            </dl>
          )}
        </section>
      </PanelErrorBoundary>
    </div>
  )
}
