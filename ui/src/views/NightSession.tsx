import { Link } from 'react-router-dom'
import { useEffect, useState } from 'react'
import { getNightSessionConfigRevision } from '../api'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateAnyScope } from '../app/session'
import { formatAbsolute } from '../app/time'
import {
  NightCueOutcomeBadge,
  NightCueStateBadge,
  NightPhaseEvidenceBadge,
} from '../components/DomainBadges'
import { PanelErrorBoundary } from '../components/PanelErrorBoundary'
import { EvidenceTable, OperatorPageHeader, OperatorSection, StatusStrip, StatusStripItem, UnavailableBlock } from '../components/SharedLayouts'
import type { ConfigNightSession, NightBackgroundAudioStep, NightCue, NightSessionState } from '../app/types'
import { LifecycleTimeline } from './operate/LifecycleTimeline'
import { NightLifecycleCommands } from './operate/NightLifecycleCommands'
import { useNightSessionState } from './operate/useNightSessionState'
import '../styles/operator-pages.css'
import '../styles/operate.css'

// Track F seam F2 (RESTING-MODE.md, ADR-038), rebuilt for the Operator UI
// overhaul (Show Night.dc.html): the RUNNING night-session view only
// (`/night`). The night-session DEFINITION editor, its list, and
// activation are a separate screen the owner is mocking; this view links
// out to the working `/config/night.session/:id` route rather than
// building that editor.
//
// Two fields are deliberately absent from this view: brightness
// ceiling/multiplier, and interlock/site-control state. Neither
// capability exists in this coordinator's schema yet.

type ConfiguredStepsState =
  | { kind: 'idle' }
  | { kind: 'loading' }
  | { kind: 'unavailable'; reason: string }
  | { kind: 'loaded'; payload: ConfigNightSession; revision: number; updatedAt: string }

const CONFIG_READ_SCOPES = ['show:macro:run', 'config:write']

function findNextCue(cues: NightCue[]): NightCue | null {
  return cues.find((c) => c.state === 'not_dispatched' || c.state === 'pending' || c.state === 'dispatched') ?? null
}

function NowAndNext({ session }: { session: NightSessionState }) {
  const nextCue = session.cues.state === 'recorded' ? findNextCue(session.cues.cues) : null
  return (
    <div className="show-night__now-next">
      <section className="panel show-night__now" aria-labelledby="show-night-now">
        <h3 id="show-night-now" className="t-subhead">Now</h3>
        <dl className="field-list">
          <dt>Session</dt>
          <dd>{session.id === '' ? 'none (no session has ever been created)' : session.id}</dd>
          <dt>State entered</dt>
          <dd>{formatAbsolute(session.stateEnteredAt)}</dd>
          <dt>Configuration</dt>
          <dd>{session.configObjectId === '' ? 'unavailable' : `${session.configObjectId} · revision ${session.configRevision}`}</dd>
        </dl>
      </section>
      <section className="panel show-night__next" aria-labelledby="show-night-next">
        <h3 id="show-night-next" className="t-subhead">Next transition</h3>
        {nextCue === null ? (
          <p className="text-muted">{session.cues.state === 'recorded' ? 'No pending Transition Step in the current cycle’s outbox.' : session.cues.reason}</p>
        ) : (
          <dl className="field-list">
            <dt>Step</dt>
            <dd>{nextCue.name}</dd>
            <dt>Phase</dt>
            <dd>{nextCue.phase}</dd>
            <dt>State</dt>
            <dd><NightCueStateBadge state={nextCue.state} /></dd>
          </dl>
        )}
      </section>
    </div>
  )
}

export function NightSession() {
  const model = useModelContext()
  const [state, reload] = useNightSessionState()
  const configReadGate = evaluateAnyScope(model.session, model.sessionFetchFailed, CONFIG_READ_SCOPES)
  const configUnavailableReason = configReadGate.allowed ? '' : configReadGate.reason
  const [configuredSteps, setConfiguredSteps] = useState<ConfiguredStepsState>({ kind: 'idle' })

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

  const session = state.kind === 'loaded' ? state.session : null
  const title = session !== null && session.cycle > 0 ? `Cycle ${session.cycle} of the night` : 'Show Night'

  return (
    <div className="operator-page show-night-page">
      <OperatorPageHeader
        eyebrow={session !== null && session.configObjectId !== '' ? `Show Night / ${session.configObjectId}` : 'Show Night'}
        title={title}
        lede="FPP owns the schedule, playlist selection, and progression. ShowMesh advances the transitions between shows and records what it observed."
        actions={
          <div className="dashboard-page__actions">
            {session !== null && session.configObjectId !== '' ? (
              <a className="button button--secondary" href={`/config/night.session/${encodeURIComponent(session.configObjectId)}`}>Edit definition</a>
            ) : (
              <span className="scoped-button">
                <button type="button" className="btn btn--secondary" disabled={true} aria-disabled="true" title="No configuration is pinned to this session yet.">Edit definition</button>
              </span>
            )}
          </div>
        }
      />

      <section aria-labelledby="show-night-lifecycle" className="dashboard-section">
        <div className="dashboard-section__heading"><div><h2 id="show-night-lifecycle">Lifecycle</h2><p className="text-muted">Tonight&rsquo;s cycles and the current cycle&rsquo;s phase. The bottom row repeats: rest, then show, then rest again, for as many cycles as the night allows.</p></div></div>
        <LifecycleTimeline loadState={state} />
      </section>

      {session !== null && <NowAndNext session={session} />}

      <OperatorSection title="Lifecycle commands" detail={<>Accepted, never confirmed here. Full transport in <Link to="/control">Live Control</Link>.</>} aria-labelledby="lifecycle-commands">
        <NightLifecycleCommands onApplied={() => reload()} />
      </OperatorSection>

      {session !== null && (
        <NightSessionDetail session={session} configuredSteps={configuredSteps} onReload={reload} />
      )}
    </div>
  )
}

function NightSessionDetail({ session, configuredSteps, onReload }: { session: NightSessionState; configuredSteps: ConfiguredStepsState; onReload: () => void }) {
  const currentCueIndex = session.cues.state === 'recorded'
    ? session.cues.cues.findIndex((cue) => cue.state === 'pending' || cue.state === 'dispatched' || cue.state === 'ambiguous' || cue.state === 'not_dispatched')
    : -1
  const readinessChecks = session.readiness.state === 'recorded' ? session.readiness.checks : []
  const healthyChecks = readinessChecks.filter((c) => c.state === 'healthy').length

  return (
    <div className="show-night__detail">
      <PanelErrorBoundary panelLabel="Lifecycle state">
        <section className="panel show-night__metadata">
          <dl className="field-list">
            <dt>Shutdown intent</dt>
            <dd>{session.shutdownIntent === '' ? 'none' : session.shutdownIntent}</dd>
            <dt>Armed show</dt>
            <dd>
              {session.armedShowId === '' ? 'none' : session.armedShowId}
              {session.showCommitted ? ' (committed)' : ''}
            </dd>
          </dl>
          <button type="button" onClick={onReload}>Reload</button>
        </section>
      </PanelErrorBoundary>

      <OperatorSection
        title="Configured Transition Steps"
        detail="Authored work pinned for this Show Night, including offsets. These are not runtime executions."
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
                <thead><tr><th>Phase</th><th>Transition Step</th><th>Offset</th><th>Target / action</th><th>Configured state</th></tr></thead>
                <tbody>
                  {[
                    ...configuredSteps.payload.enterShow.cues.map((step) => ({ phase: 'Enter show', step })),
                    ...configuredSteps.payload.enterResting.cues.map((step) => ({ phase: 'Enter resting', step })),
                  ].map(({ phase, step }, index) => (
                    <tr key={`${phase}-${step.name}-${index}`}>
                      <td>{phase}</td>
                      <td><strong>{step.name}</strong><small>{step.onFailure} on failure</small></td>
                      <td className="t-data">{step.offsetMs >= 0 ? '+' : ''}{step.offsetMs} ms</td>
                      <td><strong>{step.role}</strong><small>{step.action}</small></td>
                      <td>Configured</td>
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
      <p className="show-night__board-detail text-muted">
        Runtime executions from the current lifecycle. Armed / confirmed / refused is each row&rsquo;s own state and
        outcome; the runtime API carries no per-step offset, only the configured steps above do.
      </p>
      <PanelErrorBoundary panelLabel="Transition Step evidence">
        <section className="show-night__board">
          {session.cues.state !== 'recorded' && (
            <p className="text-muted" role="status">{session.cues.reason}</p>
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
                      <th>State</th>
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

      <h3 className="section-title">Final-cycle status</h3>
      <PanelErrorBoundary panelLabel="Final-cycle status">
        <section className="panel show-night__after-status">
          <dl className="field-list">
            <dt>Cycle</dt>
            <dd>{session.cycle}</dd>
            <dt>Final show requested</dt>
            <dd>{session.finalShowRequested ? 'yes' : 'no'}{session.finalShowRequestedAt !== null && `, at ${formatAbsolute(session.finalShowRequestedAt)}`}</dd>
            <dt>Admission closed</dt>
            <dd>{session.admissionClosed ? 'yes' : 'no'}{session.admissionClosedAt !== null && `, at ${formatAbsolute(session.admissionClosedAt)}`}</dd>
          </dl>
        </section>
      </PanelErrorBoundary>

      <h2 className="show-night__board-title">Evidence</h2>
      <p className="show-night__board-detail text-muted">Anything not observed says so.</p>

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

      <h3 className="section-title">Readiness</h3>
      <PanelErrorBoundary panelLabel="Readiness">
        <section className="panel show-night__evidence-card">
          {session.readiness.state !== 'recorded' ? (
            <p className="text-muted" role="status">{session.readiness.reason}</p>
          ) : (
            <>
              <dl className="field-list">
                <dt>Outcome</dt>
                <dd>{session.readiness.outcome ?? 'unknown'}</dd>
                <dt>Checks</dt>
                <dd>{readinessChecks.length === 0 ? 'none reported' : `${healthyChecks} of ${readinessChecks.length} healthy`}</dd>
                <dt>Completed</dt>
                <dd>{session.readiness.completedAt !== undefined ? formatAbsolute(session.readiness.completedAt) : '-'}</dd>
                <dt>Same epoch</dt>
                <dd>{session.readiness.sameEpoch ? 'yes' : 'no'}</dd>
                <dt>Fresh</dt>
                <dd>{session.readiness.fresh ? 'yes' : 'no'}</dd>
              </dl>
              <p className="t-small"><Link to="/monitor/readiness">See readiness detail →</Link></p>
            </>
          )}
        </section>
      </PanelErrorBoundary>

      <h3 className="section-title">Background audio</h3>
      <PanelErrorBoundary panelLabel="Background audio evidence">
        <section className="panel show-night__evidence-card">
          {session.backgroundAudio.state !== 'recorded' && (
            <p className="text-muted" role="status">{session.backgroundAudio.reason}</p>
          )}
          {session.backgroundAudio.state === 'recorded' &&
            (session.backgroundAudio.steps.length === 0 ? (
              <p className="text-muted">No background audio steps recorded for this cycle yet, or none is configured.</p>
            ) : (
              <div className="table-scroll show-night__table-scroll">
                <table className="config-table">
                  <thead>
                    <tr>
                      <th>Sequence</th><th>Transition Step</th><th>Kind</th><th>Pinned revision</th>
                      <th>State</th><th>Outcome</th><th>Reason</th><th>Dispatched</th><th>Resolved</th>
                    </tr>
                  </thead>
                  <tbody>
                    {session.backgroundAudio.steps.map((step: NightBackgroundAudioStep, i) => (
                      <tr key={`${step.sequence}-${step.cueName}-${step.kind}-${i}`}>
                        <td>{step.sequence}</td>
                        <td>{step.cueName}</td>
                        <td>{step.kind}</td>
                        <td>{step.actionRevision}</td>
                        <td><NightCueStateBadge state={step.state} /></td>
                        <td>{step.outcome === undefined ? '-' : <NightCueOutcomeBadge outcome={step.outcome} />}</td>
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
            <p className="text-muted" role="status">{session.authorization.reason ?? 'Nothing has been attributed yet.'}</p>
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
