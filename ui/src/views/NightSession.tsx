import { useEffect, useState } from 'react'
import { getCurrentNightSession } from '../api'
import { describeApiError } from '../app/session'
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
import { NightCommandButton } from '../components/NightCommandButton'
import { PanelErrorBoundary } from '../components/PanelErrorBoundary'
import type { NightCue, NightSessionState } from '../app/types'

// Track F seam F2 (RESTING-MODE.md, ADR-038): the night-session operating
// view. Same posture as ResolumeView.tsx (this codebase's other
// live/operating view): per-section PanelErrorBoundary, evidence rendered
// with a state and a reason rather than omitted, and command buttons that
// stay reachable-but-disabled-with-a-reason rather than hidden
// (ADR-024 decision 12).
//
// Three fields are deliberately absent from this view and every one below
// it: audio gain, brightness ceiling/multiplier, and interlock/site-control
// state. None of that capability exists in this coordinator yet
// (RESTING-MODE.md §10's siteControl/interlocks are specified but
// unimplemented, and the schema carries no gain/brightness fields at
// all) — omitted rather than rendered as an empty placeholder, matching
// this codebase's "an absent capability is not a blank control" posture
// elsewhere (Layout.tsx's own comment on why Control/Configure stayed
// absent from the nav until their first real behaviour shipped).

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; session: NightSessionState }

// Which of the ten RESTING-MODE.md §3 states cues are currently replayed
// under (RESTING-MODE.md §7): used only to pick a sensible "current
// phase" for the cue table's default filter, never to invent evidence the
// session itself does not carry.
function phaseForState(state: NightSessionState['state']): NightCue['phase'] {
  if (state === 'transition-to-resting' || state === 'end-of-night-resting') return 'enterResting'
  if (state === 'fading-out') return 'fadeOut'
  return 'enterShow'
}

function findNextCue(cues: NightCue[]): NightCue | null {
  return cues.find((c) => c.state === 'not_dispatched' || c.state === 'pending' || c.state === 'dispatched') ?? null
}

export function NightSession() {
  const model = useModelContext()
  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [reloadGeneration, setReloadGeneration] = useState(0)
  const [armEndSession, setArmEndSession] = useState(false)

  useEffect(() => {
    let cancelled = false
    setState((prev) => (prev.kind === 'loaded' ? prev : { kind: 'loading' }))
    getCurrentNightSession()
      .then((resp) => {
        if (!cancelled) setState({ kind: 'loaded', session: resp.session })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reloadGeneration])

  // `model.nightSession` is not seeded from Snapshot (domain.ts's own
  // comment on Model.nightSession) — it only ever updates via a live
  // `nightSession.changed` frame. Once one arrives, it is strictly fresher
  // than whatever this view's own GET produced, so it always wins.
  useEffect(() => {
    if (model.nightSession === null) return
    setState({ kind: 'loaded', session: model.nightSession })
  }, [model.nightSession])

  function handleApplied(session: NightSessionState): void {
    setState({ kind: 'loaded', session })
    setArmEndSession(false)
  }

  return (
    <div>
      <h2 className="panel__title">Night session</h2>
      <p className="text-muted">
        The RESTING-MODE lifecycle controller&rsquo;s own state — a dedicated closed state
        machine, never observed evidence. See <code>night.session</code> configuration for the
        authored definition this session pins.
      </p>

      {state.kind === 'loading' && <p className="text-muted">Loading the night session…</p>}
      {state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          {state.message}
        </p>
      )}

      {state.kind === 'loaded' && (
        <NightSessionDetail session={state.session} onReload={() => setReloadGeneration((g) => g + 1)} />
      )}

      <h3 className="section-title">Lifecycle commands</h3>
      <PanelErrorBoundary panelLabel="Night lifecycle commands">
        <section className="panel">
          <p className="text-muted">
            Each control is shown disabled with a stated reason when this device may not use it,
            never hidden. A refusal names one of three distinct causes —
            not yet ready, rejected by the session&rsquo;s current state, or the session is
            degraded and ambiguous — or, for the first four commands only, that the audit store
            is unavailable.
          </p>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem', maxWidth: '28rem' }}>
            <NightCommandButton command="prepare-site" label="Prepare site" onApplied={handleApplied} />
            <NightCommandButton command="run-readiness" label="Run readiness" onApplied={handleApplied} />
            <NightCommandButton command="start-preshow" label="Start preshow" onApplied={handleApplied} />
            <NightCommandButton command="start-night" label="Start night" onApplied={handleApplied} />
            {/* Exempt from the degraded-session gate (ADR-024 decision 11):
                these four are direction-safe and stay reachable even when
                the session is degraded, so one coordinator restart cannot
                brick the night. */}
            <NightCommandButton command="request-final-show" label="Request final show" onApplied={handleApplied} />
            <NightCommandButton command="fade-out-night" label="Fade out night" onApplied={handleApplied} />
            <NightCommandButton
              command="power-down-presentation"
              label="Power down presentation"
              onApplied={handleApplied}
            />

            {/* end-session abandons the session outright — arm-then-confirm,
                on ShowActive.tsx's precedent, rather than firing from a bare
                click. */}
            {!armEndSession && (
              <button type="button" onClick={() => setArmEndSession(true)}>
                End session…
              </button>
            )}
            {armEndSession && (
              <div className="panel panel--warning" role="alertdialog" aria-label="Confirm end session">
                <p>
                  <strong>About to end this session.</strong>
                </p>
                <p>
                  This abandons the current night session outright. It is the operator-recovery
                  path out of a degraded, ambiguous session, and is provisional — not yet part of
                  the closed lifecycle-command vocabulary.
                </p>
                <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'flex-start' }}>
                  <NightCommandButton command="end-session" label="Confirm: end session" onApplied={handleApplied} />
                  <button type="button" onClick={() => setArmEndSession(false)}>
                    Cancel
                  </button>
                </div>
              </div>
            )}
          </div>
        </section>
      </PanelErrorBoundary>
    </div>
  )
}

function NightSessionDetail({ session, onReload }: { session: NightSessionState; onReload: () => void }) {
  const currentPhase = phaseForState(session.state)
  const nextCue = session.cues.state === 'recorded' ? findNextCue(session.cues.cues) : null

  return (
    <>
      <PanelErrorBoundary panelLabel="Lifecycle state">
        <section className="panel">
          <div style={{ marginBottom: '0.75rem' }}>
            <NightLifecycleBadge state={session.state} />
          </div>
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

      <h3 className="section-title">Final-cycle status</h3>
      <PanelErrorBoundary panelLabel="Final-cycle status">
        <section className="panel">
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
        <section className="panel">
          <div style={{ marginBottom: '0.5rem' }}>
            <NightPhaseEvidenceBadge state={session.transition.state} />
          </div>
          <p className="text-muted">{session.transition.reason}</p>
        </section>
      </PanelErrorBoundary>

      <h3 className="section-title">Power phase evidence</h3>
      <PanelErrorBoundary panelLabel="Power phase evidence">
        <section className="panel">
          <div style={{ marginBottom: '0.5rem' }}>
            <NightPhaseEvidenceBadge state={session.powerPhase.state} />
          </div>
          <p className="text-muted">{session.powerPhase.reason}</p>
        </section>
      </PanelErrorBoundary>

      <h3 className="section-title">Next cue</h3>
      <PanelErrorBoundary panelLabel="Next cue">
        <section className="panel" role="status">
          {session.cues.state !== 'recorded' && (
            <p className="text-muted">
              {session.cues.state === 'not_configured' ? 'not configured' : session.cues.reason}
            </p>
          )}
          {session.cues.state === 'recorded' && nextCue === null && (
            <p className="text-muted">No pending cue in the current cycle&rsquo;s outbox.</p>
          )}
          {session.cues.state === 'recorded' && nextCue !== null && (
            <dl className="field-list">
              <dt>Name</dt>
              <dd>{nextCue.name}</dd>
              <dt>Phase</dt>
              <dd>{nextCue.phase}</dd>
              <dt>State</dt>
              <dd>
                <NightCueStateBadge state={nextCue.state} />
              </dd>
            </dl>
          )}
        </section>
      </PanelErrorBoundary>

      <h3 className="section-title">Cues ({currentPhase})</h3>
      <PanelErrorBoundary panelLabel="Cue evidence">
        <section className="panel">
          {session.cues.state !== 'recorded' && (
            <p className="text-muted" role="status">
              {session.cues.state === 'not_configured' ? 'not configured' : session.cues.reason}
            </p>
          )}
          {session.cues.state === 'recorded' &&
            (session.cues.cues.length === 0 ? (
              <p className="text-muted">No cues recorded for this cycle yet.</p>
            ) : (
              <div className="table-scroll">
                <table className="config-table">
                  <thead>
                    <tr>
                      <th>Name</th>
                      <th>Phase</th>
                      <th>Role</th>
                      <th>Action</th>
                      <th>Pinned revision</th>
                      <th>State</th>
                      <th>Outcome</th>
                      <th>Reason</th>
                      <th>Dispatched</th>
                      <th>Resolved</th>
                    </tr>
                  </thead>
                  <tbody>
                    {session.cues.cues.map((cue, i) => (
                      <tr key={`${cue.phase}-${cue.name}-${i}`}>
                        <td>{cue.name}</td>
                        <td>{cue.phase}</td>
                        <td>{cue.role}</td>
                        <td>{cue.action}</td>
                        <td>{cue.actionRevision ?? '—'}</td>
                        <td>
                          <NightCueStateBadge state={cue.state} />
                        </td>
                        <td>
                          {/* ADR-031 decision 3: completed and confirmed must
                              be visually distinct — a resolved-but-
                              unconfirmed cue is neither success nor
                              failure, and NightCueOutcomeBadge gives it its
                              own tone rather than folding it into either. */}
                          {cue.outcome === undefined ? '—' : <NightCueOutcomeBadge outcome={cue.outcome} />}
                        </td>
                        <td>{cue.reason ?? '—'}</td>
                        <td>{formatAbsolute(cue.dispatchedAt)}</td>
                        <td>{formatAbsolute(cue.resolvedAt)}</td>
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
        <section className="panel">
          {session.readiness.state !== 'recorded' && (
            <p className="text-muted" role="status">
              {session.readiness.state === 'not_configured' ? 'not configured' : session.readiness.reason}
            </p>
          )}
          {session.readiness.state === 'recorded' && (
            <>
              <div style={{ marginBottom: '0.5rem' }}>
                {session.readiness.outcome !== undefined && (
                  <NightReadinessOutcomeBadge outcome={session.readiness.outcome} />
                )}
              </div>
              <dl className="field-list">
                <dt>Reason</dt>
                <dd>{session.readiness.reason}</dd>
                <dt>Completed</dt>
                <dd>{session.readiness.completedAt !== undefined ? formatAbsolute(session.readiness.completedAt) : '—'}</dd>
                <dt>Same epoch</dt>
                <dd>{session.readiness.sameEpoch ? 'yes' : 'no'}</dd>
                <dt>Fresh</dt>
                <dd>{session.readiness.fresh ? 'yes' : 'no'}</dd>
              </dl>
              {session.readiness.checks.length > 0 && (
                <div className="table-scroll">
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
        <section className="panel" role="status">
          <p>
            <strong>{session.degraded ? 'Degraded' : 'Not degraded'}</strong>
            {session.degraded && session.degradedReason !== undefined && `: ${session.degradedReason}`}
          </p>
          <p>
            <strong>{session.attributionDegraded ? 'Attribution degraded' : 'Attribution intact'}</strong>
            {session.attributionDegraded &&
              ' — this session applied at least one command despite its audit entry failing to write, or ran an autonomous dispatch with no authorizing principal recorded. This never clears once true.'}
          </p>
        </section>
      </PanelErrorBoundary>

      <h3 className="section-title">Authorization</h3>
      <PanelErrorBoundary panelLabel="Authorization">
        <section className="panel">
          {session.authorization.state === 'unknown' ? (
            <p className="text-muted" role="status">
              {session.authorization.reason ?? 'Nothing has been attributed yet.'}
            </p>
          ) : (
            <dl className="field-list">
              <dt>Principal</dt>
              <dd>{session.authorization.principalName ?? session.authorization.principalId ?? 'unknown'}</dd>
              <dt>Command</dt>
              <dd>{session.authorization.command ?? '—'}</dd>
              <dt>Recorded at</dt>
              <dd>{formatAbsolute(session.authorization.recordedAt)}</dd>
            </dl>
          )}
        </section>
      </PanelErrorBoundary>
    </>
  )
}
