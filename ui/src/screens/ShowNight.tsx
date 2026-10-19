import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  ApiError,
  PROBLEM_TYPE,
  deleteNightSessionConfig,
  dispatchNightCommand,
  getCurrentNightSession,
  getNightSessionConfig,
  getNightSessionConfigRevision,
  getNightSessionConfigRevisions,
  getNightSessionActiveConfig,
  getNightSessionActiveConfigRevisions,
  getShowAction,
  listAssets,
  listConfigObjects,
  listFPPPlaylistDefinitions,
  putNightSessionActiveConfig,
  putNightSessionConfig,
  randomUUIDv4,
  readBackgroundAudioExcludeNodes,
  type ConfigObjectSummary,
  type ConfigNightSessionBackgroundAudioInlineWrite,
  type ConfigNightSessionBackgroundAudioInlineWriteWithExclude,
  type ConfigNightSessionBackgroundAudioReferenceWriteWithExclude,
  type ConfigNightSessionCue,
  type ConfigNightSessionWrite,
  type ShowActionConfigResponse,
  type Asset,
  type AudioNodeSummary,
  type FPPPlaylistDefinitionMetadata,
  type NightCommandName,
  type NightInterlockOverride,
  type NightSessionActiveConfigResponse,
  type NightSessionConfigResponse,
  type NightSessionState,
} from '../api'
import {
  BlankingPlate,
  Button,
  ButtonRow,
  Choice,
  ConfirmDialog,
  DefinitionStrip,
  DeletePanel,
  Drawer,
  Field,
  FieldGrid,
  Input,
  LifecycleCommands,
  Notice,
  Panes,
  RadioCardList,
  RevisionHistory,
  RuledStrip,
  Section,
  Select,
  SelectableRow,
  StatusPair,
  Table,
  TableWrap,
  Workbench,
} from '../kit'
import { useModelContext } from '../app/ModelContext'
import { RESUME_CONFIRM_TITLE, useResumeShow } from '../app/stopHold'
import { describeApiError, evaluateScope } from '../domain/session'
import { guardedSave, type SaveOutcome } from '../domain/save'
import { effectiveServerTimeIso, formatClock } from '../domain/time'
import { formatPosition, nightLifecycleGroups } from './liveControlModel'
import { AudioAssetPicker } from './audioAssetPicker'
import { audioAssetOptions, audioItemResolves, excludeNodesError, explicitListExcludeConflictError, type AudioAssetOption } from './showsModel'
import { AudioNodesResolutionField, useShowAudioNodes } from './audioNodesField'
import { StaleWriteStrip } from './StaleWrite'
import {
  CUES_STATE_LABEL,
  DEGRADED_WITHOUT_REASON,
  backgroundAudioSteps,
  inWords,
  plannedBackgroundAudio,
  cycleRail,
  evidenceReadouts,
  nextTransition,
  nightCommandLabel,
  nightRail,
  nowPlaying,
  pinnedCeilingFact,
  readinessChecks,
  readinessOutcome,
  runOfShow,
  sentOutcome,
  type NightCommandOutcome,
  type RailStep,
} from './showNightModel'
import { usePrepareSiteConfirm } from './usePrepareSiteConfirm'

function useNightSession(): { session: NightSessionState | null; error: string | null } {
  const model = useModelContext()
  const [seeded, setSeeded] = useState<NightSessionState | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    getCurrentNightSession()
      .then((response) => {
        if (!cancelled) setSeeded(response.session)
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(describeApiError(err))
      })
    return () => {
      cancelled = true
    }
  }, [])

  return { session: model.nightSession ?? seeded, error }
}

function Rail({ title, steps }: { title: string; steps: readonly RailStep[] }) {
  return (
    <div className="sm-rail-strip">
      <p className="sm-rail-strip__title">{title}</p>
      {steps.map((step) => (
        <div key={step.key} className={`sm-rail-strip__step sm-rail-strip__step--${step.status}`}>
          <p className="sm-rail-strip__label">
            <span aria-hidden="true">
              {step.status === 'done'
                ? '✓ '
                : step.status === 'now'
                  ? '● '
                  : step.status === 'notWired' || step.status === 'stopped'
                    ? '⚠ '
                    : step.status === 'error'
                      ? '✕ '
                      : step.status === 'unknown'
                        ? '? '
                        : ''}
            </span>
            {step.label}
          </p>
          <p className="sm-rail-strip__detail" title={step.title}>
            {step.detail}
          </p>
        </div>
      ))}
    </div>
  )
}

/** Whichever lifecycle command a `night-not-ready` refusal named. */
type Withheld = { command: NightCommandName }

/** How a refused command is labelled, beside the coordinator's own message. */
const COMMAND_REFUSAL_WORDS: Record<string, string> = {
  [PROBLEM_TYPE.nightNotReady]: 'withheld',
  [PROBLEM_TYPE.nightStateRejected]: 'refused',
  [PROBLEM_TYPE.nightAmbiguous]: 'refused',
  [PROBLEM_TYPE.nightCommandRefusedAuditUnavailable]: 'not sent',
}

export function ShowNight() {
  const model = useModelContext()
  const { session, error } = useNightSession()
  const nowIso = effectiveServerTimeIso(model.serverTime, model.serverTimeReceivedAt, Date.now())
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'night:command')
  const overrideGate = evaluateScope(model.session, model.sessionFetchFailed, 'night:override')
  const resumeShow = useResumeShow(model, session)
  const [outcome, setOutcome] = useState<NightCommandOutcome | null>(null)
  const [withheld, setWithheld] = useState<Withheld | null>(null)
  const [overrideRule, setOverrideRule] = useState('')
  const [overrideReason, setOverrideReason] = useState('')
  const [overrideInvalid, setOverrideInvalid] = useState(false)
  // prepare-site is the one command the coordinator honors idempotencyKey
  // for (ADR-038): reused across a double-press so the second press is a
  // no-op rather than a second dispatch, then rolled once the epoch it
  // named actually opens so a LATER, genuinely new prepare-site is not
  // silently folded into the epoch this key already named.
  const [prepareSiteKey, setPrepareSiteKey] = useState(() => randomUUIDv4())
  const [skipEnterShowLead, setSkipEnterShowLead] = useState(false)
  const [stopFppPlayback, setStopFppPlayback] = useState(false)
  const [startNightConfirmOpen, setStartNightConfirmOpen] = useState(false)
  const [activationOpen, setActivationOpen] = useState(false)

  const send = useCallback(
    (command: NightCommandName, interlockOverrides?: readonly NightInterlockOverride[], stopOverride?: boolean) => {
      const idempotencyKey = command === 'prepare-site' ? prepareSiteKey : undefined
      setOutcome(sentOutcome(command))
      dispatchNightCommand(
        command,
        idempotencyKey,
        interlockOverrides,
        command === 'start-night' ? skipEnterShowLead : undefined,
        command === 'prepare-site' ? (stopOverride ?? stopFppPlayback) : undefined,
      )
        .then((response) => {
          setWithheld(null)
          setOverrideRule('')
          setOverrideReason('')
          setOverrideInvalid(false)
          if (command === 'prepare-site') setPrepareSiteKey(randomUUIDv4())
          if (command === 'start-night') setSkipEnterShowLead(false)
          const applied = response.command.outcome === 'applied'
          if (command === 'run-readiness' && applied) {
            setOutcome(readinessOutcome(response.session.readiness))
            return
          }
          const name = nightCommandLabel(command)
          const lines = [applied ? `${name} was accepted.` : `${name} changed nothing. The night was already there.`]
          if (response.command.reason !== undefined && response.command.reason !== '') lines.push(response.command.reason)
          if (response.command.attributionDegraded) lines.push('This ran with no user recorded.')
          setOutcome({ tone: 'warn', label: applied ? 'Accepted' : 'No change', lines, blocked: [] })
        })
        .catch((err: unknown) => {
          if (command === 'prepare-site') setPrepareSiteKey(randomUUIDv4())
          const label = `${nightCommandLabel(command)} ${COMMAND_REFUSAL_WORDS[err instanceof ApiError ? (err.problemType ?? '') : ''] ?? 'refused'}`
          const blocked = err instanceof ApiError ? err.blockedChecks : []
          setWithheld(err instanceof ApiError && err.problemType === PROBLEM_TYPE.nightNotReady ? { command } : null)
          const tone = err instanceof ApiError && err.problemType === PROBLEM_TYPE.nightCommandRefusedAuditUnavailable ? 'warn' : 'bad'
          setOutcome({ tone, label, lines: [describeApiError(err)], blocked })
        })
    },
    [prepareSiteKey, skipEnterShowLead, stopFppPlayback],
  )

  const prepareSite = usePrepareSiteConfirm((stop) => {
    setStopFppPlayback(stop)
    send('prepare-site', undefined, stop)
  })

  const submitOverride = useCallback(() => {
    if (withheld === null) return
    if (overrideRule.trim() === '' || overrideReason.trim() === '') {
      setOverrideInvalid(true)
      return
    }
    send(withheld.command, [{ rule: overrideRule.trim(), reason: overrideReason.trim() }])
  }, [withheld, overrideRule, overrideReason, send])

  const tonight = session === null ? [] : nightRail(session)

  if (session === null) {
    return (
      <>
        <h1 className="sm-page__title">Show Night</h1>
        <BlankingPlate
          absence={error === null ? 'loading' : 'failed'}
          stamp={error === null ? 'Wait' : 'Fail'}
          eyebrow="Night session · not reported"
          title={error === null ? 'The night session has not reported yet' : 'The night session could not be read'}
          detail={
            error ??
            'This page fills in when the session next reports.'
          }
        />
      </>
    )
  }

  const { instance, run, state } = nowPlaying(model)
  const next = nextTransition(model)
  const steps = runOfShow(session)
  const armed = steps.filter((step) => step.when === 'Waiting').length
  const elapsedSeconds = state?.elapsedSeconds ?? null
  const totalSeconds = state?.totalSeconds ?? null
  const playbackPercent =
    elapsedSeconds !== null && totalSeconds !== null && totalSeconds > 0
      ? Math.min(100, Math.max(0, (elapsedSeconds / totalSeconds) * 100))
      : null

  const statusSection = (
      <Section id="sn-evidence" title="Status">
        {evidenceReadouts(session, nowIso).map((readout) => (
          <div key={readout.key} className="sm-readout">
            <StatusPair tone={readout.tone} label={readout.label} />
            <div>
              {readout.facts.map((fact, index) => (
                <p key={`${index}:${fact}`} className="sm-readout__fact">
                  {fact}
                  {readout.key === 'readiness' && readout.tone !== 'good' && (
                    <>
                      {' '}
                      <button
                        type="button"
                        className="sm-linkbutton"
                        disabled={!gate.allowed}
                        title={gate.allowed ? undefined : gate.reason}
                        onClick={() => send('run-readiness')}
                      >
                        Run readiness again
                      </button>
                    </>
                  )}
                </p>
              ))}
              {readout.key === 'readiness' &&
                (session.readiness.checks.length === 0 ? (
                  <p className="sm-small sm-faint">No individual checks were recorded with this result.</p>
                ) : (
                  <div className="sm-readout__checks">
                    {readinessChecks(session.readiness.checks).map((check) => (
                      <div key={check.key} className="sm-readout">
                        <StatusPair tone={check.tone} label={check.label} />
                        {check.facts.length > 0 && (
                          <ul className="sm-plain-list sm-readout__fact" aria-label={check.label}>
                            {check.facts.map((fact, index) => (
                              <li key={`${index}:${fact}`}>{fact}</li>
                            ))}
                          </ul>
                        )}
                      </div>
                    ))}
                  </div>
                ))}
              {readout.key === 'audio' && <p className="sm-small sm-muted">{pinnedCeilingFact(session.backgroundAudio)}</p>}
            </div>
          </div>
        ))}
      </Section>
  )
  const bgAudioAll = backgroundAudioSteps(session.backgroundAudio)
  const bgPlan = plannedBackgroundAudio(session.backgroundAudio)
  const bgAudioMax = 25
  const bgAudioShown = bgAudioAll.slice(-bgAudioMax)
  const bgAudioSection = (
      <Section id="sn-bg-audio" title="Background Audio Record" aside={<span className="sm-small sm-muted">This cycle</span>}>
        {session.backgroundAudio.nodesNotPlaying.map((node) => (
          <RuledStrip key={node.nodeId} absence="stale" label={node.nodeId} fact="Not playing background audio" detail={node.reason} />
        ))}
        <h3 id="sn-bg-plan" className="sm-subsection__title">Planned</h3>
        {bgPlan.kind === 'configured' ? (
          <>
            <p className="sm-small sm-muted">
              {bgPlan.source}. {bgPlan.settings.map((setting) => `${setting.label}: ${setting.value}`).join(' · ')}. Plays on <span className="sm-data">{bgPlan.nodes === '' ? 'no node' : bgPlan.nodes}</span>.
            </p>
            <TableWrap label="Planned background audio items, scrollable">
              <Table minWidth={500}>
                <thead>
                  <tr>
                    <th scope="col">Order</th>
                    <th scope="col">Sequence</th>
                    <th scope="col">File on</th>
                  </tr>
                </thead>
                <tbody>
                  {bgPlan.items.map((item) => (
                    <tr key={item.key}>
                      <td className="sm-data">{item.position}</td>
                      <td>{item.sequence}</td>
                      <td className="sm-data">{item.target}</td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            </TableWrap>
          </>
        ) : bgPlan.kind === 'not_configured' ? (
          <RuledStrip absence="empty" label="Not configured" fact={bgPlan.fact} />
        ) : (
          <RuledStrip absence="unavailable" label="Unavailable" fact={bgPlan.fact} />
        )}
        <h3 id="sn-bg-steps" className="sm-subsection__title">Recorded</h3>
        {bgAudioAll.length > 0 ? (
          <>
          <TableWrap label="Background audio steps this cycle, scrollable">
            <Table minWidth={700}>
              <thead>
                <tr>
                  <th scope="col">When</th>
                  <th scope="col">Sequence</th>
                  <th scope="col">Cue</th>
                  <th scope="col">Node</th>
                  <th scope="col">Kind</th>
                  <th scope="col">State</th>
                </tr>
              </thead>
              <tbody>
                {bgAudioShown.map((step) => (
                  <tr key={step.key}>
                    <td className="sm-data">{step.when}</td>
                    <td>{step.sequence}</td>
                    <td>
                      {step.cueName}
                      <br />
                      <span className="sm-small sm-faint">{step.detail}</span>
                    </td>
                    <td className="sm-data">{step.nodeId}</td>
                    <td>{step.kind}</td>
                    <td>
                      <StatusPair tone={step.tone} label={step.state} />
                      {step.resolved !== null && (
                        <>
                          <br />
                          <span className="sm-small sm-faint">{step.resolved}</span>
                        </>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </Table>
          </TableWrap>
          {bgAudioAll.length > bgAudioMax && (
            <p className="sm-section__footnote">Showing the most recent {bgAudioMax} of {bgAudioAll.length} events this cycle.</p>
          )}
          </>
        ) : (
          <RuledStrip
            absence="empty"
            label={bgPlan.kind === 'configured' ? 'Not started' : 'None recorded'}
            fact={bgPlan.kind === 'configured' ? 'Background audio has not started this cycle.' : 'No background audio steps are recorded for this cycle.'}
          />
        )}
      </Section>
  )
  return (
    <>
      <div className="sm-page__head">
        <div>
          <p className="sm-eyebrow">Show Night</p>
          <h1 className="sm-page__title">Cycle {session.cycle} of the night</h1>
        </div>
        <ButtonRow>
          <Button onClick={() => setActivationOpen(true)}>Activate definition</Button>
          {session.armedShowId !== '' ? (
            <Link className="sm-btn" to={`/shows/${encodeURIComponent(session.armedShowId)}/night-session`}>Edit definition</Link>
          ) : (
            <Link className="sm-btn" to="/shows">Edit definition</Link>
          )}
        </ButtonRow>
      </div>

      <div role="group" aria-label="Night lifecycle" className="sm-rails">
        <Rail title="Tonight" steps={tonight} />
        <Rail title={`Cycle ${session.cycle}`} steps={cycleRail(session, nowIso)} />
      </div>

      <div className="sm-nownext">
        <section aria-labelledby="sn-now">
          <h3 id="sn-now" className="sm-subsection__title">
            Now playing · reported by FPP
          </h3>
          {state === null || instance === undefined ? (
            <RuledStrip
              absence="unobserved"
              label="Unobserved"
              fact="No FPP is reporting playback."
            />
          ) : (
            <>
              <p className="sm-nownext__title">{state.media ?? run?.playback.media ?? 'No item reported'}</p>
              <div className="sm-nownext__position">
                <span>{formatPosition(elapsedSeconds) ?? 'not reported'}</span>
                <span className="sm-nownext__track" aria-hidden="true">
                  {playbackPercent !== null && <span style={{ width: `${playbackPercent}%` }} />}
                </span>
                <span>{formatPosition(totalSeconds) ?? 'not reported'}</span>
              </div>
              <DefinitionStrip
                items={[
                  { term: 'Playlist', value: <span className="sm-data">{state.playlist ?? 'not reported'}</span> },
                  {
                    term: 'Entry',
                    value: (
                      <span className="sm-data">
                        {state.itemIndex === null ? 'not reported' : `${state.itemIndex}${state.itemCount === null ? '' : ` / ${state.itemCount}`}`}
                      </span>
                    ),
                  },
                  {
                    term: 'Confirmed by',
                    value: (
                      <span className={run?.freshness.state === 'current' ? 'sm-nownext__evidence--good' : 'sm-muted'}>
                        {run === undefined
                          ? state.playerState ?? 'not reported'
                          : `${run.runner.toUpperCase()} · ${run.freshness.state.replace('_', ' ')}`}
                      </span>
                    ),
                  },
                ]}
              />
            </>
          )}
        </section>
        <section aria-labelledby="sn-next">
          <h3 id="sn-next" className="sm-subsection__title">
            Next transition
          </h3>
          {next.known ? (
            <p className="sm-nownext__title">
              {formatPosition(next.remainingSeconds)}
              <span className="sm-small sm-muted"> until the next transition</span>
            </p>
          ) : (
            <RuledStrip absence="unavailable" label="Unknown" fact={next.reason} />
          )}
          <div className="sm-nownext__boundary">
            {armed > 0 && <p className="sm-small sm-muted">{`${armed} ${armed === 1 ? 'cue' : 'cues'} waiting this cycle`}</p>}
          </div>
        </section>
      </div>

      <Section
        id="sn-commands"
        title="Lifecycle commands"
        aside={
          <span className="sm-small sm-muted">
            <Link to="/control">Full transport in Live Control →</Link>
          </span>
        }
      >
        {session.fallbackHold !== undefined && (
          <Notice
            tone="warn"
            live="status"
            headline={<>The night is waiting because FPP <span className="sm-data">{session.fallbackHold.fppInstanceId}</span> is held.</>}
            explanation={session.fallbackHold.message}
          />
        )}
        <ButtonRow>
          <Button variant="primary" size="gloved" disabled={resumeShow.disabled} title={resumeShow.title} onClick={resumeShow.onClick}>
            {resumeShow.busy ? 'Resuming…' : 'Resume'}
          </Button>
        </ButtonRow>
        {resumeShow.error !== null && <Notice tone="bad" headline="Resume was refused." explanation={resumeShow.error} />}
        <ConfirmDialog
          open={resumeShow.confirmOpen}
          title={RESUME_CONFIRM_TITLE}
          detail={null}
          confirmLabel="Resume"
          onConfirm={resumeShow.onConfirm}
          onCancel={resumeShow.onCancel}
        />
        <LifecycleCommands
          dense
          groups={[
            {
              id: 'sn-lifecycle',
              commands: nightLifecycleGroups(gate, (command) => {
                if (command === 'start-night') setStartNightConfirmOpen(true)
                else if (command === 'prepare-site') prepareSite.start()
                else send(command)
              }).flatMap((group) => group.commands),
            },
          ]}
        />
        {prepareSite.dialog}
        <ConfirmDialog
          open={startNightConfirmOpen}
          title="Start the night and its first cycle?"
          detail={
            <>
              <p className="sm-body">This starts the show.</p>
              <Choice
                type="checkbox"
                checked={skipEnterShowLead}
                onChange={(event) => setSkipEnterShowLead(event.target.checked)}
                label="Start the show now, without the lead-in. An announcement cue still plays."
              />
            </>
          }
          confirmLabel="Start night"
          onConfirm={() => {
            setStartNightConfirmOpen(false)
            send('start-night')
          }}
          onCancel={() => setStartNightConfirmOpen(false)}
        />
        {outcome !== null && (
          <div className="sm-outcome">
            <StatusPair tone={outcome.tone} label={outcome.label} />
            {outcome.lines.map((line, index) => (
              <p key={`${index}:${line}`} className="sm-outcome__detail">{line}</p>
            ))}
            {outcome.blocked.length > 0 && (
              <ul className="sm-plain-list sm-small" aria-label="Safety checks that blocked this">
                {outcome.blocked.map((check) => (
                  <li key={check.name}>
                    <span className="sm-data">{check.name}</span> · {check.reason}
                  </li>
                ))}
              </ul>
            )}
            {withheld !== null &&
              (overrideGate.allowed ? (
                <div className="sm-outcome__override">
                  <FieldGrid>
                    <Field
                      label="Safety check"
                      help="The safety check's name, as listed above."
                      error={overrideInvalid && overrideRule.trim() === '' ? 'Name the safety check to override.' : undefined}
                    >
                      {(props) => <Input {...props} value={overrideRule} onChange={(e) => setOverrideRule(e.target.value)} />}
                    </Field>
                    <Field
                      label="Reason"
                      error={overrideInvalid && overrideReason.trim() === '' ? 'A reason is required to override a safety check.' : undefined}
                    >
                      {(props) => <Input {...props} value={overrideReason} onChange={(e) => setOverrideReason(e.target.value)} />}
                    </Field>
                  </FieldGrid>
                  <ButtonRow>
                    <Button size="gloved" disabled={!gate.allowed} onClick={submitOverride}>
                      Retry {nightCommandLabel(withheld.command)} with override
                    </Button>
                  </ButtonRow>
                </div>
              ) : (
                <p className="sm-small sm-muted">{overrideGate.reason}</p>
              ))}
          </div>
        )}
      </Section>
      <Workbench
        wide="side"
        sideLabel="Run of show and background audio"
        main={statusSection}
        side={<>

      <Section
        id="sn-run"
        title="Run of Show"
        aside={
          <span className="sm-small sm-muted">
            Cycle {session.cycle} · {steps.length} {steps.length === 1 ? 'step' : 'steps'}
          </span>
        }
      >
        {session.cues.state !== 'recorded' ? (
          <RuledStrip
            absence={session.cues.state === 'not_configured' ? 'empty' : 'unobserved'}
            label={CUES_STATE_LABEL[session.cues.state] ?? 'Unknown'}
            fact={session.cues.reason !== '' ? session.cues.reason : 'No transition steps are recorded for this cycle.'}
          />
        ) : (
          <>
            <TableWrap label="Run of show steps, scrollable">
              <Table minWidth={720}>
                <thead>
                  <tr>
                    <th scope="col">When</th>
                    <th scope="col">Step</th>
                    <th scope="col">Phase</th>
                    <th scope="col">Target</th>
                    <th scope="col">State</th>
                  </tr>
                </thead>
                <tbody>
                  {steps.map((step) => (
                    <tr key={step.key}>
                      <td className="sm-data">{step.when}</td>
                      <td>
                        {step.name}
                        <br />
                        <span className="sm-small sm-faint">{step.detail}</span>
                      </td>
                      <td>{step.phase}</td>
                      <td>
                        {step.target}
                        <br />
                        <span className="sm-data sm-small">{step.action}</span>
                      </td>
                      <td>
                        <StatusPair tone={step.tone} label={step.state} />
                        {step.resolved !== null && (
                          <>
                            <br />
                            <span className="sm-small sm-faint">{step.resolved}</span>
                          </>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            </TableWrap>
            <p className="sm-section__footnote">
              {steps.length} {steps.length === 1 ? 'step' : 'steps'} this cycle · {armed} waiting.
            </p>
          </>
        )}
      </Section>

      {bgAudioSection}
      </>}
      />

      <NightSessionActivation open={activationOpen} onClose={() => setActivationOpen(false)} />

      {session.degraded && (
        <BlankingPlate
          absence="stale"
          stamp="Degr"
          eyebrow="Night session · degraded"
          title="This session is degraded"
          detail={session.degradedReason ?? DEGRADED_WITHOUT_REASON}
        />
      )}

    </>
  )
}

/** No `night.session.active` object has ever existed: the 404 the store documents, translated into the same shape every other read produces so `guardedSave` never special-cases it. */
function emptyActiveConfig(): NightSessionActiveConfigResponse {
  return {
    serverTime: '',
    kind: 'night.session.active',
    id: 'night.session.active',
    revision: 0,
    payload: { session: '' },
    updatedAt: '',
    createdByPrincipalId: null,
    createdByPrincipalName: null,
    source: 'api',
  }
}

function readActiveConfigOrEmpty(): Promise<NightSessionActiveConfigResponse> {
  return getNightSessionActiveConfig().catch((err: unknown) => {
    if (err instanceof ApiError && err.status === 404) return emptyActiveConfig()
    throw err
  })
}

type ActiveLoadState =
  | { kind: 'loading' }
  | { kind: 'loaded'; response: NightSessionActiveConfigResponse; objects: ConfigObjectSummary[] }
  | { kind: 'failed'; reason: string }

/**
 * `/config/night.session.active` (ADR-039 rule 4): which `night.session`
 * definition is armed for the next `start-night`, and the control to point
 * it at a different one. Definition authoring lives directly below it so the
 * active pointer and the immutable object it names stay visibly distinct.
 */
function NightSessionActivation({ open, onClose }: { open: boolean; onClose: () => void }) {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<ActiveLoadState>({ kind: 'loading' })
  const [selected, setSelected] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [stale, setStale] = useState<Extract<SaveOutcome<NightSessionActiveConfigResponse>, { kind: 'stale' }> | null>(null)
  const [clearConfirmText, setClearConfirmText] = useState('')

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    Promise.all([readActiveConfigOrEmpty(), listConfigObjects('night.session')])
      .then(([active, list]) => {
        if (cancelled) return
        setState({ kind: 'loaded', response: active, objects: list.objects })
        setSelected(active.payload.session)
        setClearConfirmText('')
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [attempt])

  const activate = (session: string) => {
    if (state.kind !== 'loaded') return
    setSaving(true)
    setSaveError(null)
    setStale(null)
    guardedSave({
      loaded: state.response,
      read: readActiveConfigOrEmpty,
      write: () => putNightSessionActiveConfig({ session }),
    })
      .then((outcome) => {
        if (outcome.kind === 'saved') {
          setState({ kind: 'loaded', response: outcome.response, objects: state.objects })
          setSelected(outcome.response.payload.session)
          setClearConfirmText('')
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
    <Drawer open={open} onClose={onClose} labelledBy="sn-active" width="content">
    <Section
      id="sn-active"
      title="Night session activation"
      aside={<span className="sm-small sm-muted">The definition Start night uses</span>}
    >
      {state.kind === 'loading' ? (
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator which night session definition is active." />
      ) : state.kind === 'failed' ? (
        <RuledStrip absence="failed" label="Read failed" fact={state.reason} />
      ) : (
        <>
          <DefinitionStrip
            items={[
              {
                term: 'Active definition',
                value:
                  state.response.payload.session === '' ? (
                    <span className="sm-muted">None. No definition is active.</span>
                  ) : (
                    <span className="sm-data">{state.response.payload.session}</span>
                  ),
              },
              {
                term: 'Version',
                value: state.response.revision === 0 ? 'never activated' : <span className="sm-data">{state.response.revision}</span>,
              },
              {
                term: 'Updated',
                value:
                  state.response.revision === 0
                    ? 'never'
                    : `${formatClock(state.response.updatedAt) ?? 'at an unrecorded time'} by ${state.response.createdByPrincipalName ?? 'an unknown user'}`,
              },
            ]}
          />

          <FieldGrid>
            <Field label="Activate a definition" help="Start night uses the definition chosen here.">
              {(props) => (
                <Select {...props} value={selected} onChange={(e) => setSelected(e.target.value)}>
                  <option value="">Choose a definition…</option>
                  {state.objects.map((object) => (
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
              disabled={!gate.allowed || saving || selected === '' || selected === state.response.payload.session}
              title={gate.allowed ? undefined : gate.reason}
            >
              {saving ? 'Activating…' : 'Activate'}
            </Button>
          </ButtonRow>

          {state.response.payload.session !== '' && (
            <div className="sm-outcome__override">
              <Field
                label={`Type ${state.response.payload.session} to confirm clearing the active definition`}
                help="After clearing, Start night has no definition until one is activated."
              >
                {(props) => <Input {...props} value={clearConfirmText} onChange={(e) => setClearConfirmText(e.target.value)} />}
              </Field>
              <ButtonRow>
                <Button
                  variant="quiet"
                  disabled={!gate.allowed || saving || clearConfirmText !== state.response.payload.session}
                  title={gate.allowed ? undefined : gate.reason}
                  onClick={() => activate('')}
                >
                  Clear active definition
                </Button>
              </ButtonRow>
            </div>
          )}

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
      <RevisionHistory fetch={getNightSessionActiveConfigRevisions} reloadKey={attempt} />
    </Section>
    </Drawer>
  )
}

type CueDraft = {
  name: string
  role: 'lighting' | 'projection' | 'audio' | 'announcement' | 'other'
  action: string
  offsetMs: string
  barrier: boolean
  onFailure: 'continue' | 'abort'
  fadeDurationMs: string
  announcementPolicy: '' | 'duck' | 'mix' | 'interrupt'
  base: ConfigNightSessionCue | null
}

type BackgroundAudioItemDraft = { itemId: string; show: string; sequence: string; target: string }
type BackgroundAudioMode = 'none' | 'inline' | 'playlist'
type BackgroundAudioDraft = {
  // Only the chosen mode is saved; the other mode's fields stay in the draft so switching back loses nothing.
  mode: BackgroundAudioMode
  mediaPlaylist: string
  items: BackgroundAudioItemDraft[]
  repeat: 'none' | 'item' | 'playlist'
  resume: 'resume' | 'restart'
  itemTransition: 'sequential' | 'gapless' | 'crossfade'
  crossfadeMs: string
  maxGainDb: string
  fadeOutMs: string
  fadeInMs: string
  targets: string[]
  excludeNodes: string[]
}

type PrerequisiteDraft = { kind: 'action' | 'delay' | 'evidence'; action: string; requireConfirmation: boolean; delayMs: string }
type SiteControlDraft = {
  requestThermalProfile: string
  powerOnEnabled: boolean
  powerOnAction: string
  powerOnDomain: 'presentation' | 'environmental' | 'mixed' | 'unknown'
  powerOnProvenance: 'provider' | 'operator-declared'
  powerOffEnabled: boolean
  powerOffAction: string
  powerOffDomain: 'presentation' | 'environmental' | 'mixed' | 'unknown'
  powerOffProvenance: 'provider' | 'operator-declared'
  removalPolicy: 'immediate' | 'after-actions'
  immediateSafeAttestation: boolean
  prerequisites: PrerequisiteDraft[]
}

type InterlockDraft = {
  name: string
  phase:
    | 'prepare-site'
    | 'presentation-power-on'
    | 'projector-strike'
    | 'run-readiness'
    | 'start-preshow'
    | 'start-night'
    | 'enter-resting'
    | 'fade-out-night'
    | 'power-down-presentation'
  posture: 'observe' | 'block' | 'disabled'
  signal: string
  freshnessSeconds: string
  failureText: string
  onUnavailable: 'block' | 'allow'
  overridePolicy: 'none' | 'authorized-operator'
}

type DefinitionDraft = {
  id: string
  show: string
  label: string
  showFpp: string
  showPlaylist: string
  restingFpp: string
  restingPlaylist: string
  endOfNightPlaylist: string
  endOfNightRepeat: boolean
  timelineShow: string
  timelineSequence: string
  timelineTarget: string
  announcementDefaultPolicy: 'duck' | 'mix' | 'interrupt'
  backgroundAudio: BackgroundAudioDraft
  siteControl: SiteControlDraft
  interlocks: InterlockDraft[]
  blackoutHoldMs: string
  blackoutAfterShowMs: string
  lightsFadeOutMs: string
  lightsFadeInMs: string
  enterShow: CueDraft[]
  enterResting: CueDraft[]
  endOfNightMode: EndOfNightMode
  endOfNightBlackoutMs: string
  enterEndOfNight: CueDraft[]
  base: ConfigNightSessionWrite | null
}

type EndOfNightMode = 'resting' | 'none' | 'custom'

const CUE_ROLES = ['lighting', 'projection', 'audio', 'announcement', 'other'] as const
const ANNOUNCEMENT_POLICIES = ['duck', 'mix', 'interrupt'] as const
const BACKGROUND_AUDIO_REPEATS = ['none', 'item', 'playlist'] as const
const BACKGROUND_AUDIO_RESUMES = ['resume', 'restart'] as const
const BACKGROUND_AUDIO_TRANSITIONS = ['sequential', 'gapless', 'crossfade'] as const
const POWER_DOMAINS = ['presentation', 'environmental', 'mixed', 'unknown'] as const
const DOMAIN_PROVENANCES = ['provider', 'operator-declared'] as const
const REMOVAL_POLICIES = ['immediate', 'after-actions'] as const
const PREREQUISITE_KINDS = ['action', 'delay', 'evidence'] as const
const INTERLOCK_PHASES = [
  'prepare-site', 'presentation-power-on', 'projector-strike', 'run-readiness',
  'start-preshow', 'start-night', 'enter-resting', 'fade-out-night', 'power-down-presentation',
] as const
const INTERLOCK_POSTURES = ['observe', 'block', 'disabled'] as const
const ON_UNAVAILABLE_OPTIONS = ['block', 'allow'] as const
const OVERRIDE_POLICIES = ['none', 'authorized-operator'] as const

/** A stored option value in the words its select shows. */
const OPTION_LABEL: Record<string, string> = {
  none: 'None',
  item: 'Repeat the current item',
  playlist: 'Repeat the whole list',
  resume: 'Resume where it left off',
  restart: 'Restart from the first item',
  sequential: 'One after another',
  'authorized-operator': 'A user allowed to override',
  'operator-declared': 'User declared',
}

function optionLabel(value: string): string {
  return OPTION_LABEL[value] ?? inWords(value)
}

const blankCue = (): CueDraft => ({ name: '', role: 'lighting', action: '', offsetMs: '0', barrier: false, onFailure: 'continue', fadeDurationMs: '', announcementPolicy: '', base: null })
const blankBackgroundAudioItem = (show = ''): BackgroundAudioItemDraft => ({ itemId: '', show, sequence: '', target: '' })
const blankBackgroundAudio = (): BackgroundAudioDraft => ({
  mode: 'none', mediaPlaylist: '', items: [], repeat: 'none', resume: 'resume', itemTransition: 'sequential',
  crossfadeMs: '', maxGainDb: '', fadeOutMs: '', fadeInMs: '', targets: [], excludeNodes: [],
})

/** The reference picker's options: the show's reported media.playlist objects, plus the retained id if a loaded session names one this fetch did not report (never dropped silently). */
function mediaPlaylistOptions(reported: readonly ConfigObjectSummary[], retained: string): { id: string; label: string }[] {
  const options = reported.map((playlist) => ({ id: playlist.id, label: playlist.label }))
  if (retained !== '' && !options.some((option) => option.id === retained)) options.push({ id: retained, label: retained })
  return options
}
const blankPrerequisite = (): PrerequisiteDraft => ({ kind: 'action', action: '', requireConfirmation: false, delayMs: '' })
const blankSiteControl = (): SiteControlDraft => ({
  requestThermalProfile: '',
  powerOnEnabled: false, powerOnAction: '', powerOnDomain: 'presentation', powerOnProvenance: 'operator-declared',
  powerOffEnabled: false, powerOffAction: '', powerOffDomain: 'presentation', powerOffProvenance: 'operator-declared',
  removalPolicy: 'immediate', immediateSafeAttestation: false, prerequisites: [],
})
const blankInterlock = (): InterlockDraft => ({ name: '', phase: 'prepare-site', posture: 'observe', signal: '', freshnessSeconds: '', failureText: '', onUnavailable: 'block', overridePolicy: 'none' })
const blankDefinition = (show = ''): DefinitionDraft => ({
  id: '', show, label: '', showFpp: '', showPlaylist: '', restingFpp: '', restingPlaylist: '',
  timelineShow: show, timelineSequence: '', timelineTarget: '', endOfNightPlaylist: '', endOfNightRepeat: false,
  announcementDefaultPolicy: 'duck', backgroundAudio: blankBackgroundAudio(), siteControl: blankSiteControl(), interlocks: [],
  blackoutHoldMs: '0', blackoutAfterShowMs: '0', lightsFadeOutMs: '', lightsFadeInMs: '',
  enterShow: [], enterResting: [], endOfNightMode: 'resting', endOfNightBlackoutMs: '', enterEndOfNight: [], base: null,
})

function draftFromDefinition(response: NightSessionConfigResponse): DefinitionDraft {
  const { payload } = response
  const cue = (item: (typeof payload.enterShow.cues)[number]): CueDraft => ({
    name: item.name, role: item.role, action: item.action, offsetMs: String(item.offsetMs),
    barrier: item.barrier, onFailure: item.onFailure,
    fadeDurationMs: item.fadeDurationMs === undefined ? '' : String(item.fadeDurationMs),
    announcementPolicy: item.announcementPolicy ?? '', base: item,
  })
  const bg = payload.resting.backgroundAudio
  const backgroundAudio: BackgroundAudioDraft =
    bg === undefined
      ? blankBackgroundAudio()
      : 'mediaPlaylist' in bg
        ? { ...blankBackgroundAudio(), mode: 'playlist', mediaPlaylist: bg.mediaPlaylist, targets: bg.targets ?? [], excludeNodes: readBackgroundAudioExcludeNodes(bg) }
        : {
            mode: 'inline',
            mediaPlaylist: '',
            items: bg.items.map((item) => ({ itemId: item.itemId, show: item.show, sequence: item.sequence, target: item.target ?? '' })),
            repeat: bg.repeat, resume: bg.resume, itemTransition: bg.itemTransition,
            crossfadeMs: bg.crossfadeMs === undefined ? '' : String(bg.crossfadeMs),
            maxGainDb: String(bg.maxGainDb),
            fadeOutMs: bg.fadeOutMs === undefined ? '' : String(bg.fadeOutMs),
            fadeInMs: bg.fadeInMs === undefined ? '' : String(bg.fadeInMs),
            targets: bg.targets ?? [],
            excludeNodes: readBackgroundAudioExcludeNodes(bg),
          }
  const powerOn = payload.siteControl?.presentationPowerOn
  const powerOff = payload.siteControl?.presentationPowerOff
  const siteControl: SiteControlDraft = {
    requestThermalProfile: payload.siteControl?.requestThermalProfile ?? '',
    powerOnEnabled: powerOn !== undefined,
    powerOnAction: powerOn?.action ?? '',
    powerOnDomain: powerOn?.powerDomain ?? 'presentation',
    powerOnProvenance: powerOn?.domainProvenance ?? 'operator-declared',
    powerOffEnabled: powerOff !== undefined,
    powerOffAction: powerOff?.action ?? '',
    powerOffDomain: powerOff?.powerDomain ?? 'presentation',
    powerOffProvenance: powerOff?.domainProvenance ?? 'operator-declared',
    removalPolicy: powerOff?.removalPolicy ?? 'immediate',
    immediateSafeAttestation: powerOff?.immediateSafeAttestation ?? false,
    prerequisites: (powerOff?.prerequisites ?? []).map((p) => ({
      kind: p.kind, action: p.action ?? '', requireConfirmation: p.requireConfirmation ?? false,
      delayMs: p.delayMs === undefined ? '' : String(p.delayMs),
    })),
  }
  const interlocks: InterlockDraft[] = (payload.interlocks ?? []).map((i) => ({
    name: i.name, phase: i.phase, posture: i.posture, signal: i.signal ?? '',
    freshnessSeconds: i.freshnessSeconds === undefined ? '' : String(i.freshnessSeconds),
    failureText: i.failureText ?? '', onUnavailable: i.onUnavailable ?? 'block', overridePolicy: i.overridePolicy ?? 'none',
  }))
  return {
    id: response.id, show: payload.show, label: payload.label,
    showFpp: payload.showPlaylist.fppInstanceId, showPlaylist: payload.showPlaylist.playlist,
    restingFpp: payload.resting.fppInstanceId, restingPlaylist: payload.resting.playlist, endOfNightPlaylist: payload.resting.endOfNightPlaylist, endOfNightRepeat: payload.resting.endOfNightRepeat,
    timelineShow: payload.resting.timelineAsset.show, timelineSequence: payload.resting.timelineAsset.sequence, timelineTarget: payload.resting.timelineAsset.target,
    announcementDefaultPolicy: payload.announcementDefaultPolicy, backgroundAudio, siteControl, interlocks,
    blackoutHoldMs: String(payload.enterShow.blackoutHoldMs), blackoutAfterShowMs: String(payload.enterResting.blackoutAfterShowMs),
    lightsFadeOutMs: payload.lightsFadeOutMs === undefined ? '' : String(payload.lightsFadeOutMs),
    lightsFadeInMs: payload.lightsFadeInMs === undefined ? '' : String(payload.lightsFadeInMs),
    enterShow: payload.enterShow.cues.map(cue), enterResting: payload.enterResting.cues.map(cue),
    endOfNightMode: payload.enterEndOfNight.mode,
    endOfNightBlackoutMs: payload.enterEndOfNight.blackoutAfterShowMs === undefined ? '' : String(payload.enterEndOfNight.blackoutAfterShowMs),
    enterEndOfNight: payload.enterEndOfNight.cues.map(cue), base: payload,
  }
}

type BackgroundAudioWrite = ConfigNightSessionBackgroundAudioInlineWriteWithExclude | ConfigNightSessionBackgroundAudioReferenceWriteWithExclude
type SiteControlWrite = NonNullable<ConfigNightSessionWrite['siteControl']>
type InterlockWrite = NonNullable<ConfigNightSessionWrite['interlocks']>[number]

function buildBackgroundAudio(
  audio: BackgroundAudioDraft,
  audioAssets: readonly AudioAssetOption[],
  showAudioNodes: readonly string[],
): { ok: true; value: BackgroundAudioWrite | undefined } | { ok: false; error: string } {
  if (audio.mode === 'none') return { ok: true, value: undefined }
  const conflict = explicitListExcludeConflictError(audio.targets, audio.excludeNodes)
  if (conflict !== null) return { ok: false, error: conflict }
  if (audio.targets.length === 0) {
    const excludeError = excludeNodesError(showAudioNodes, audio.excludeNodes)
    if (excludeError !== null) return { ok: false, error: excludeError }
  }
  const targetsField = audio.targets.length > 0 ? { targets: audio.targets } : audio.excludeNodes.length > 0 ? { excludeNodes: audio.excludeNodes } : {}
  if (audio.mode === 'playlist') {
    if (audio.mediaPlaylist === '') return { ok: false, error: 'Pick a media playlist for the background audio, or choose None.' }
    return { ok: true, value: { mediaPlaylist: audio.mediaPlaylist, ...targetsField } }
  }
  if (audio.items.length === 0) return { ok: false, error: 'Background audio needs at least one item, or choose None.' }
  const items: ConfigNightSessionBackgroundAudioInlineWrite['items'] = []
  for (const [index, item] of audio.items.entries()) {
    if (item.itemId.trim() === '' || item.show.trim() === '' || item.sequence.trim() === '') {
      return { ok: false, error: `Background audio item ${index + 1} needs an item id and an audio track.` }
    }
    const sequence = item.sequence.trim()
    const target = item.target.trim()
    // A stored (sequence, target) that no longer matches a current asset must not be persisted silently.
    if (!audioItemResolves(audioAssets, sequence, target)) {
      return { ok: false, error: `Background audio item ${index + 1} names a missing asset. Pick another or remove it.` }
    }
    items.push({ itemId: item.itemId.trim(), show: item.show.trim(), sequence, ...(target === '' ? {} : { target }) })
  }
  const maxGainDb = Number(audio.maxGainDb)
  if (audio.maxGainDb.trim() === '' || Number.isNaN(maxGainDb) || maxGainDb > 0) {
    return { ok: false, error: 'Background audio ceiling (max gain) must be a number no greater than 0 dB.' }
  }
  const fadeOutText = audio.fadeOutMs.trim()
  const fadeInText = audio.fadeInMs.trim()
  if ((fadeOutText === '') !== (fadeInText === '')) {
    return { ok: false, error: 'Set background audio fade-out and fade-in together, or leave both empty.' }
  }
  if (audio.itemTransition === 'crossfade' && audio.crossfadeMs.trim() === '') {
    return { ok: false, error: 'Crossfade duration is required when item transition is crossfade.' }
  }
  return {
    ok: true,
    value: {
      items, repeat: audio.repeat, resume: audio.resume, itemTransition: audio.itemTransition, maxGainDb,
      ...(audio.itemTransition === 'crossfade' ? { crossfadeMs: Number(audio.crossfadeMs) } : {}),
      ...(fadeOutText === '' ? {} : { fadeOutMs: Number(fadeOutText), fadeInMs: Number(fadeInText) }),
      ...targetsField,
    },
  }
}

function buildSiteControl(sc: SiteControlDraft): { ok: true; value: SiteControlWrite | undefined } | { ok: false; error: string } {
  const built: SiteControlWrite = {}
  if (sc.requestThermalProfile.trim() !== '') built.requestThermalProfile = sc.requestThermalProfile.trim()
  if (sc.powerOnEnabled) {
    if (sc.powerOnAction.trim() === '') return { ok: false, error: 'Presentation power-on needs an action.' }
    built.presentationPowerOn = { action: sc.powerOnAction.trim(), powerDomain: sc.powerOnDomain, domainProvenance: sc.powerOnProvenance }
  }
  if (sc.powerOffEnabled) {
    if (sc.powerOffAction.trim() === '') return { ok: false, error: 'Presentation power down needs an action.' }
    if (sc.removalPolicy === 'immediate' && !sc.immediateSafeAttestation) {
      return { ok: false, error: 'Immediate removal needs the safe-to-remove box ticked.' }
    }
    if (sc.removalPolicy === 'after-actions' && sc.prerequisites.length === 0) {
      return { ok: false, error: 'After-actions removal requires at least one prerequisite.' }
    }
    const prerequisites: NonNullable<SiteControlWrite['presentationPowerOff']>['prerequisites'] = []
    for (const [index, p] of sc.prerequisites.entries()) {
      if (p.kind === 'delay') {
        const delayMs = Number(p.delayMs)
        if (p.delayMs.trim() === '' || !Number.isInteger(delayMs) || delayMs < 0) {
          return { ok: false, error: `Prerequisite ${index + 1} needs a whole, non-negative delay in milliseconds.` }
        }
        prerequisites.push({ kind: 'delay', delayMs })
      } else {
        if (p.action.trim() === '') return { ok: false, error: `Prerequisite ${index + 1} needs an action.` }
        prerequisites.push({ kind: p.kind, action: p.action.trim(), ...(p.kind === 'action' ? { requireConfirmation: p.requireConfirmation } : {}) })
      }
    }
    built.presentationPowerOff = {
      action: sc.powerOffAction.trim(), powerDomain: sc.powerOffDomain, domainProvenance: sc.powerOffProvenance, removalPolicy: sc.removalPolicy,
      ...(sc.removalPolicy === 'immediate' ? { immediateSafeAttestation: true } : {}),
      ...(sc.removalPolicy === 'after-actions' ? { prerequisites } : {}),
    }
  }
  return { ok: true, value: Object.keys(built).length === 0 ? undefined : built }
}

function buildInterlocks(items: InterlockDraft[]): { ok: true; value: InterlockWrite[] | undefined } | { ok: false; error: string } {
  if (items.length === 0) return { ok: true, value: undefined }
  const built: InterlockWrite[] = []
  for (const [index, item] of items.entries()) {
    if (item.name.trim() === '') return { ok: false, error: `Safety check ${index + 1} needs a name.` }
    if (item.posture === 'disabled') {
      built.push({ name: item.name.trim(), phase: item.phase, posture: 'disabled' })
      continue
    }
    if (item.signal.trim() === '') return { ok: false, error: `Safety check ${index + 1} needs a signal action.` }
    const entry: InterlockWrite = { name: item.name.trim(), phase: item.phase, posture: item.posture, signal: item.signal.trim() }
    if (item.freshnessSeconds.trim() !== '') {
      const freshness = Number(item.freshnessSeconds)
      if (!Number.isInteger(freshness) || freshness < 0) {
        return { ok: false, error: `Safety check ${index + 1}'s freshness must be a whole, non-negative number of seconds.` }
      }
      entry.freshnessSeconds = freshness
    }
    if (item.failureText.trim() !== '') entry.failureText = item.failureText.trim()
    if (item.posture === 'block') {
      entry.onUnavailable = item.onUnavailable
      entry.overridePolicy = item.overridePolicy
    }
    built.push(entry)
  }
  return { ok: true, value: built }
}

function definitionPayload(
  draft: DefinitionDraft,
  audioAssets: readonly AudioAssetOption[],
  showAudioNodes: readonly string[],
): ConfigNightSessionWrite | { error: string } {
  const required: readonly [string, string][] = [
    ['Definition id', draft.id], ['Show', draft.show], ['Label', draft.label], ['Show playlist FPP', draft.showFpp], ['Show playlist', draft.showPlaylist],
    ['Resting FPP', draft.restingFpp], ['Resting playlist', draft.restingPlaylist], ['Resting timeline show', draft.timelineShow], ['Resting timeline sequence', draft.timelineSequence], ['Resting timeline target', draft.timelineTarget],
  ]
  const missing = required.find(([, value]) => value.trim() === '')
  if (missing !== undefined) return { error: `${missing[0]} is required.` }
  const buildCues = (items: CueDraft[], phase: string): { ok: true; cues: ConfigNightSessionWrite['enterShow']['cues'] } | { ok: false; error: string } => {
    const result: ConfigNightSessionWrite['enterShow']['cues'] = []
    for (const [index, item] of items.entries()) {
      const offset = Number(item.offsetMs)
      if (item.name.trim() === '' || item.action.trim() === '' || !Number.isInteger(offset)) return { ok: false, error: `${phase} transition step ${index + 1} needs a name, action, and whole-millisecond offset.` }
      const fadeText = item.fadeDurationMs.trim()
      const fade = fadeText === '' ? null : Number(fadeText)
      if (fade !== null && (!Number.isInteger(fade) || fade < 0)) {
        return { ok: false, error: `${phase} step ${index + 1} fade must be a whole number of milliseconds, 0 or more.` }
      }
      result.push({
        name: item.name.trim(), role: item.role, action: item.action.trim(), offsetMs: offset,
        barrier: item.barrier, onFailure: item.onFailure,
        ...(fade === null ? {} : { fadeDurationMs: fade }),
        ...(item.role === 'announcement' && item.announcementPolicy !== '' ? { announcementPolicy: item.announcementPolicy } : {}),
      })
    }
    return { ok: true, cues: result }
  }
  const enteringShow = buildCues(draft.enterShow, 'Enter-show')
  if (!enteringShow.ok) return { error: enteringShow.error }
  const enteringResting = buildCues(draft.enterResting, 'Enter-resting')
  if (!enteringResting.ok) return { error: enteringResting.error }
  const enteringEndOfNight = buildCues(draft.endOfNightMode === 'custom' ? draft.enterEndOfNight : [], 'End-of-night')
  if (!enteringEndOfNight.ok) return { error: enteringEndOfNight.error }
  if (draft.endOfNightMode === 'custom' && enteringEndOfNight.cues.length === 0) return { error: 'Custom end-of-night cues need at least one cue. Add a cue, or choose Run no cues.' }
  const backgroundAudio = buildBackgroundAudio(draft.backgroundAudio, audioAssets, showAudioNodes)
  if (!backgroundAudio.ok) return { error: backgroundAudio.error }
  const siteControl = buildSiteControl(draft.siteControl)
  if (!siteControl.ok) return { error: siteControl.error }
  const interlocks = buildInterlocks(draft.interlocks)
  if (!interlocks.ok) return { error: interlocks.error }
  const blackoutHoldMs = Number(draft.blackoutHoldMs)
  if (!Number.isInteger(blackoutHoldMs) || blackoutHoldMs < 0) return { error: 'Blackout hold must be a whole number of milliseconds, 0 or more.' }
  const blackoutAfterShowMs = Number(draft.blackoutAfterShowMs)
  if (!Number.isInteger(blackoutAfterShowMs) || blackoutAfterShowMs < 0) return { error: 'Blackout after show must be a whole number of milliseconds, 0 or more.' }
  const endOfNightBlackoutMs = draft.endOfNightBlackoutMs.trim() === '' ? null : Number(draft.endOfNightBlackoutMs)
  if (endOfNightBlackoutMs !== null && (!Number.isInteger(endOfNightBlackoutMs) || endOfNightBlackoutMs < 0)) return { error: 'Blackout after the last show must be a whole number of milliseconds, 0 or more, or empty.' }

  const lightsFadeOutMs = parseLightsFadeMs(draft.lightsFadeOutMs, 'Lights fade-out')
  if (typeof lightsFadeOutMs === 'string') return { error: lightsFadeOutMs }
  const lightsFadeInMs = parseLightsFadeMs(draft.lightsFadeInMs, 'Lights fade-in')
  if (typeof lightsFadeInMs === 'string') return { error: lightsFadeInMs }

  const base = draft.base
  const resting: ConfigNightSessionWrite['resting'] = {
    ...(base?.resting ?? {}), fppInstanceId: draft.restingFpp.trim(), playlist: draft.restingPlaylist.trim(),
    timelineAsset: { ...(base?.resting?.timelineAsset ?? {}), show: draft.timelineShow.trim(), sequence: draft.timelineSequence.trim(), target: draft.timelineTarget.trim() },
    endOfNightRepeat: draft.endOfNightRepeat,
  }
  if (draft.endOfNightPlaylist.trim() === '') delete resting.endOfNightPlaylist
  else resting.endOfNightPlaylist = draft.endOfNightPlaylist.trim()
  if (backgroundAudio.value === undefined) delete resting.backgroundAudio
  else resting.backgroundAudio = backgroundAudio.value

  const result: ConfigNightSessionWrite = {
    ...(base ?? {}), show: draft.show.trim(), label: draft.label.trim(),
    showPlaylist: { ...(base?.showPlaylist ?? {}), fppInstanceId: draft.showFpp.trim(), playlist: draft.showPlaylist.trim() },
    resting,
    enterShow: { ...(base?.enterShow ?? {}), blackoutHoldMs, cues: enteringShow.cues },
    enterResting: { ...(base?.enterResting ?? {}), blackoutAfterShowMs, cues: enteringResting.cues },
    announcementDefaultPolicy: draft.announcementDefaultPolicy,
  }
  result.enterEndOfNight = {
    mode: draft.endOfNightMode, cues: enteringEndOfNight.cues,
    ...(endOfNightBlackoutMs === null ? {} : { blackoutAfterShowMs: endOfNightBlackoutMs }),
  }
  if (lightsFadeOutMs === null) delete result.lightsFadeOutMs
  else result.lightsFadeOutMs = lightsFadeOutMs
  if (lightsFadeInMs === null) delete result.lightsFadeInMs
  else result.lightsFadeInMs = lightsFadeInMs
  if (siteControl.value === undefined) delete result.siteControl
  else result.siteControl = siteControl.value
  if (interlocks.value === undefined) delete result.interlocks
  else result.interlocks = interlocks.value
  return result
}

/** Empty means no fade; otherwise a whole number of milliseconds the lights can fade over. Returns the problem as a string. */
function parseLightsFadeMs(text: string, label: string): number | null | string {
  const trimmed = text.trim()
  if (trimmed === '') return null
  const ms = Number(trimmed)
  if (!Number.isInteger(ms) || ms < 1000 || ms > 86_400_000) return `${label} must be a whole number from 1000 to 86400000 ms, or empty.`
  return ms
}

type AudioNodesState = { kind: 'loading' } | { kind: 'loaded'; nodes: AudioNodeSummary[] } | { kind: 'failed'; reason: string }

/** Same `listConfigObjects('audio.node')` source ShowsCues.tsx and LiveControl.tsx each read for their own node pickers. */
function useAudioNodes(): AudioNodesState {
  const [state, setState] = useState<AudioNodesState>({ kind: 'loading' })
  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    listConfigObjects('audio.node')
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', nodes: response.objects })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [])
  return state
}

export function NightSessionDefinitions({ showId }: { showId?: string }) {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const [objects, setObjects] = useState<NightSessionConfigResponse[] | null>(null)
  const [activeId, setActiveId] = useState('')
  const [actions, setActions] = useState<ShowActionConfigResponse[]>([])
  const [playlistDefinitions, setPlaylistDefinitions] = useState<FPPPlaylistDefinitionMetadata[]>([])
  const [assets, setAssets] = useState<Asset[]>([])
  const [mediaPlaylists, setMediaPlaylists] = useState<ConfigObjectSummary[]>([])
  const [selected, setSelected] = useState('')
  const [draft, setDraft] = useState<DefinitionDraft>(() => blankDefinition(showId))
  const [loaded, setLoaded] = useState<NightSessionConfigResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [backgroundAudioTargetsError, setBackgroundAudioTargetsError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [reloadKey, setReloadKey] = useState(0)
  const [deleting, setDeleting] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)
  const audioNodesState = useAudioNodes()
  const showAudioNodesState = useShowAudioNodes(draft.show)

  useEffect(() => {
    let cancelled = false
    Promise.all([
      listConfigObjects('night.session'),
      showId === undefined ? Promise.resolve({ objects: [] as ConfigObjectSummary[], serverTime: '' }) : listConfigObjects('show.action', showId),
      listFPPPlaylistDefinitions().catch(() => ({ definitions: [], serverTime: '' })),
      showId === undefined ? Promise.resolve({ assets: [], serverTime: '' }) : listAssets({ show: showId }).catch(() => ({ assets: [], serverTime: '' })),
      showId === undefined ? Promise.resolve({ objects: [] as ConfigObjectSummary[], serverTime: '', kind: 'media.playlist' as const }) : listConfigObjects('media.playlist', showId),
    ])
      .then(async ([response, actionObjects, definitions, assetResponse, mediaPlaylistObjects]) => {
        if (cancelled) return
        const summaries = showId === undefined ? response.objects : response.objects.filter((object) => object.show === showId)
        const [full, fullActions] = await Promise.all([Promise.all(summaries.map((object) => getNightSessionConfig(object.id))), Promise.all(actionObjects.objects.map((object) => getShowAction(object.id)))])
        if (cancelled) return
        setObjects(full)
        setActions(fullActions)
        setPlaylistDefinitions(definitions.definitions)
        setAssets(assetResponse.assets)
        setMediaPlaylists(mediaPlaylistObjects.objects)
      })
      .catch((err: unknown) => { if (!cancelled) setError(describeApiError(err)) })
    getNightSessionActiveConfig().then((active) => { if (!cancelled) setActiveId(active.payload.session) }).catch(() => undefined)
    return () => { cancelled = true }
  }, [reloadKey, showId])

  const selectDefinition = (id: string) => {
    setSelected(id); setError(null); setBackgroundAudioTargetsError(null); setDeleteError(null)
    if (id === '') { setLoaded(null); setDraft(blankDefinition(showId)); return }
    getNightSessionConfig(id)
      .then((response) => { setLoaded(response); setDraft(draftFromDefinition(response)) })
      .catch((err: unknown) => setError(describeApiError(err)))
  }
  const removeDefinition = () => {
    if (loaded === null) return
    setDeleting(true); setDeleteError(null)
    deleteNightSessionConfig(loaded.id)
      .then(() => { setObjects((current) => current?.filter((o) => o.id !== loaded.id) ?? current); closeInspector() })
      .catch((err: unknown) => setDeleteError(describeApiError(err)))
      .finally(() => setDeleting(false))
  }
  const updateCues = (which: 'enterShow' | 'enterResting' | 'enterEndOfNight', index: number, patch: Partial<CueDraft>) => setDraft((current) => ({ ...current, [which]: current[which].map((item, itemIndex) => itemIndex === index ? { ...item, ...patch } : item) }))
  const save = () => {
    const showAudioNodes = showAudioNodesState.kind === 'loaded' ? showAudioNodesState.audioNodes : []
    const payload = definitionPayload(draft, audioAssets, showAudioNodes)
    if ('error' in payload) { setError(payload.error); return }
    setSaving(true); setError(null); setBackgroundAudioTargetsError(null)
    putNightSessionConfig(draft.id.trim(), payload)
      .then((response) => { setLoaded(response); setDraft(draftFromDefinition(response)); setSelected(response.id); setReloadKey((n) => n + 1) })
      .catch((err: unknown) => {
        const message = describeApiError(err)
        // resting.backgroundAudio.targets[i]/excludeNodes[i] (a coordinator
        // write-validation field path, api/openapi.yaml's Problem.detail)
        // surfaces next to the checklist it names, not in the inspector's
        // generic failure strip.
        if (message.includes('resting.backgroundAudio.targets') || message.includes('resting.backgroundAudio.excludeNodes')) setBackgroundAudioTargetsError(message)
        else setError(message)
      })
      .finally(() => setSaving(false))
  }
  const addCue = (which: 'enterShow' | 'enterResting' | 'enterEndOfNight') => setDraft((current) => ({ ...current, [which]: [...current[which], blankCue()] }))

  const updateBackgroundAudio = (patch: Partial<BackgroundAudioDraft>) => setDraft((current) => ({ ...current, backgroundAudio: { ...current.backgroundAudio, ...patch } }))
  const updateBackgroundAudioItem = (index: number, patch: Partial<BackgroundAudioItemDraft>) =>
    setDraft((current) => ({ ...current, backgroundAudio: { ...current.backgroundAudio, items: current.backgroundAudio.items.map((item, itemIndex) => itemIndex === index ? { ...item, ...patch } : item) } }))
  const addBackgroundAudioItem = () => setDraft((current) => ({ ...current, backgroundAudio: { ...current.backgroundAudio, items: [...current.backgroundAudio.items, blankBackgroundAudioItem()] } }))
  const removeBackgroundAudioItem = (index: number) =>
    setDraft((current) => ({ ...current, backgroundAudio: { ...current.backgroundAudio, items: current.backgroundAudio.items.filter((_, itemIndex) => itemIndex !== index) } }))

  const updateSiteControl = (patch: Partial<SiteControlDraft>) => setDraft((current) => ({ ...current, siteControl: { ...current.siteControl, ...patch } }))
  const updatePrerequisite = (index: number, patch: Partial<PrerequisiteDraft>) =>
    setDraft((current) => ({ ...current, siteControl: { ...current.siteControl, prerequisites: current.siteControl.prerequisites.map((p, itemIndex) => itemIndex === index ? { ...p, ...patch } : p) } }))
  const addPrerequisite = () => setDraft((current) => ({ ...current, siteControl: { ...current.siteControl, prerequisites: [...current.siteControl.prerequisites, blankPrerequisite()] } }))
  const removePrerequisite = (index: number) =>
    setDraft((current) => ({ ...current, siteControl: { ...current.siteControl, prerequisites: current.siteControl.prerequisites.filter((_, itemIndex) => itemIndex !== index) } }))

  const updateInterlock = (index: number, patch: Partial<InterlockDraft>) => setDraft((current) => ({ ...current, interlocks: current.interlocks.map((item, itemIndex) => itemIndex === index ? { ...item, ...patch } : item) }))
  const addInterlock = () => setDraft((current) => ({ ...current, interlocks: [...current.interlocks, blankInterlock()] }))
  const removeInterlock = (index: number) => setDraft((current) => ({ ...current, interlocks: current.interlocks.filter((_, itemIndex) => itemIndex !== index) }))

  const fppInstances = model.fpp
  const playlistNames = (instanceId: string, retained: string) => {
    const uuid = fppInstances.find((instance) => instance.instanceId === instanceId)?.instanceUuid
    const reported = uuid === null || uuid === undefined
      ? []
      : playlistDefinitions.filter((definition) => definition.instanceUuid === uuid).map((definition) => definition.playlistName)
    return Array.from(new Set([...(retained === '' ? [] : [retained]), ...reported])).sort()
  }
  const timelineAssets = assets.filter((asset) => asset.current && asset.mediaType === 'fseq' && asset.targetKind === 'node')
  const audioAssets = audioAssetOptions(assets)
  // A non-blank stored (sequence, target) that matches no current asset is a dead reference, not "unset".
  const deadAudioAssetError = (item: BackgroundAudioItemDraft): string | undefined =>
    audioItemResolves(audioAssets, item.sequence, item.target)
      ? undefined
      : 'This audio asset no longer exists. Pick another or remove the item.'
  const timelineSequences = Array.from(new Set([...(draft.timelineSequence === '' ? [] : [draft.timelineSequence]), ...timelineAssets.map((asset) => asset.sequence)])).sort()
  const timelineTargets = Array.from(new Set([
    ...(draft.timelineTarget === '' ? [] : [draft.timelineTarget]),
    ...timelineAssets.filter((asset) => draft.timelineSequence === '' || asset.sequence === draft.timelineSequence).map((asset) => asset.target),
  ])).sort()
  const fppIds = (retained: string) => Array.from(new Set([...(retained === '' ? [] : [retained]), ...fppInstances.map((instance) => instance.instanceId)])).sort()

  const closeInspector = () => { setSelected(''); setLoaded(null); setDraft(blankDefinition(showId)); setError(null); setBackgroundAudioTargetsError(null); setDeleteError(null) }
  return <div id="sn-definitions" className="sm-night-session-workspace">
    <div className="sm-night-session-heading">
      <div><h2 className="sm-section__title">Night session definitions</h2><p className="sm-small sm-muted">How the night enters the show and returns to resting.</p></div>
      <Button variant="primary" onClick={() => { setSelected('__new__'); setLoaded(null); setDraft(blankDefinition(showId)); setError(null); setBackgroundAudioTargetsError(null); setDeleteError(null) }}>New definition</Button>
    </div>
    <Panes
      inspectorOpen={selected !== ''}
      onInspectorClose={closeInspector}
      inspectorLabelledBy="sn-definition-inspector-heading"
      inspectorWidth="wide"
    >
      <div className="sm-night-session-list">
        <p className="sm-eyebrow">Definitions · {objects?.length ?? 0}</p>
        {objects === null ? <RuledStrip absence="loading" label="Reading" fact="Reading night-session definitions." /> : objects.length === 0 ? <RuledStrip absence="empty" label="No definitions" fact="No definitions yet." /> : <TableWrap label="Night session definitions"><Table minWidth={820}><thead><tr><th>Definition</th><th>Show playlist</th><th>Resting playlist</th><th>Version</th><th>State</th></tr></thead><tbody>{objects.map((object) => <SelectableRow key={object.id} selected={selected === object.id} onActivate={() => selectDefinition(object.id)} ariaLabel={`Edit ${object.payload.label}`}><td><strong>{object.payload.label}</strong><br /><span className="sm-data sm-small">{object.id}</span></td><td><span className="sm-data">{object.payload.showPlaylist.fppInstanceId}</span><br />{object.payload.showPlaylist.playlist}</td><td><span className="sm-data">{object.payload.resting.fppInstanceId}</span><br />{object.payload.resting.playlist}</td><td className="sm-data">{object.revision}</td><td><span className={activeId === object.id ? 'sm-meta-status sm-meta-status--live' : 'sm-meta-status'}>{activeId === object.id ? 'Active' : 'Inactive'}</span></td></SelectableRow>)}</tbody></Table></TableWrap>}
        <p className="sm-small sm-muted sm-night-session-list__note">Activate a definition on Show Night.</p>
      </div>
      <aside>
        {selected === '' ? <div className="sm-inspector sm-night-session-empty"><p className="sm-eyebrow">Definition inspector</p><h3 className="sm-inspector__title">Select a definition</h3><p className="sm-small sm-muted">Choose a row to edit it, or create a new definition.</p></div> : <div className="sm-inspector sm-night-session-inspector">
          <p className="sm-eyebrow sm-eyebrow--accent">{loaded === null ? 'Draft · new definition' : `Editing · version ${loaded.revision}`}</p>
          <h3 className="sm-inspector__title" id="sn-definition-inspector-heading">{loaded === null ? 'New night session' : draft.label}</h3>
          <div className="sm-inspector__group"><Field label="Label">{(p) => <Input {...p} value={draft.label} onChange={(e) => setDraft((d) => ({ ...d, label: e.target.value }))} />}</Field><Field label="Definition id" help={loaded === null ? 'Stable after creation.' : 'Definition ids do not change.'}>{(p) => <Input {...p} className="sm-data" disabled={loaded !== null} value={draft.id} onChange={(e) => setDraft((d) => ({ ...d, id: e.target.value }))} />}</Field></div>
          <div className="sm-inspector__group"><h4 className="sm-subsection__title">Show playback</h4><Field label="FPP">{(p) => <Select {...p} value={draft.showFpp} onChange={(e) => setDraft((d) => ({ ...d, showFpp: e.target.value, showPlaylist: '' }))}><option value="">Select an instance…</option>{fppIds(draft.showFpp).map((id) => <option key={id}>{id}</option>)}</Select>}</Field><Field label="Show playlist">{(p) => <Select {...p} value={draft.showPlaylist} onChange={(e) => setDraft((d) => ({ ...d, showPlaylist: e.target.value }))}><option value="">Select a playlist…</option>{playlistNames(draft.showFpp, draft.showPlaylist).map((name) => <option key={name}>{name}</option>)}</Select>}</Field></div>
          <div className="sm-inspector__group"><h4 className="sm-subsection__title">Resting playback</h4><Field label="FPP">{(p) => <Select {...p} value={draft.restingFpp} onChange={(e) => setDraft((d) => ({ ...d, restingFpp: e.target.value, restingPlaylist: '', endOfNightPlaylist: '' }))}><option value="">Select an instance…</option>{fppIds(draft.restingFpp).map((id) => <option key={id}>{id}</option>)}</Select>}</Field><Field label="Resting playlist">{(p) => <Select {...p} value={draft.restingPlaylist} onChange={(e) => setDraft((d) => ({ ...d, restingPlaylist: e.target.value }))}><option value="">Select a playlist…</option>{playlistNames(draft.restingFpp, draft.restingPlaylist).map((name) => <option key={name}>{name}</option>)}</Select>}</Field><Field label="End-of-night playlist">{(p) => <Select {...p} value={draft.endOfNightPlaylist} onChange={(e) => setDraft((d) => ({ ...d, endOfNightPlaylist: e.target.value }))}><option value="">Same as resting playlist</option>{playlistNames(draft.restingFpp, draft.endOfNightPlaylist).map((name) => <option key={name}>{name}</option>)}</Select>}</Field><Choice type="checkbox" checked={draft.endOfNightRepeat} onChange={(e) => setDraft((d) => ({ ...d, endOfNightRepeat: e.target.checked }))} label="Repeat the end-of-night playlist" /><Field label="Resting sequence" help="Its length sets how long resting runs. Any render node that has this sequence draws it during resting.">{(p) => <Select {...p} value={draft.timelineSequence} onChange={(e) => setDraft((d) => ({ ...d, timelineSequence: e.target.value, timelineTarget: '' }))}><option value="">Select a sequence…</option>{timelineSequences.map((v) => <option key={v}>{v}</option>)}</Select>}</Field><Field label="Read the length from" help="The node whose copy of the resting sequence is measured, usually the FPP player.">{(p) => <Select {...p} value={draft.timelineTarget} onChange={(e) => setDraft((d) => ({ ...d, timelineTarget: e.target.value }))}><option value="">Select a node…</option>{timelineTargets.map((v) => <option key={v}>{v}</option>)}</Select>}</Field></div>
          <div className="sm-inspector__group"><h4 className="sm-subsection__title">Transition timing</h4><Field label="Blackout hold (ms)">{(p) => <Input {...p} type="number" min="0" step="1" value={draft.blackoutHoldMs} onChange={(e) => setDraft((d) => ({ ...d, blackoutHoldMs: e.target.value }))} />}</Field><Field label="Blackout after show (ms)">{(p) => <Input {...p} type="number" min="0" step="1" value={draft.blackoutAfterShowMs} onChange={(e) => setDraft((d) => ({ ...d, blackoutAfterShowMs: e.target.value }))} />}</Field><Field label="Lights fade-out (ms)" help="Finishes as the resting sequence ends before a show. Leave empty for no fade.">{(p) => <Input {...p} type="number" min="1000" step="1" value={draft.lightsFadeOutMs} onChange={(e) => setDraft((d) => ({ ...d, lightsFadeOutMs: e.target.value }))} />}</Field><Field label="Lights fade-in (ms)" help="Starts when the resting playlist starts after a show. Leave empty for no fade.">{(p) => <Input {...p} type="number" min="1000" step="1" value={draft.lightsFadeInMs} onChange={(e) => setDraft((d) => ({ ...d, lightsFadeInMs: e.target.value }))} />}</Field><Field label="Default announcement policy">{(p) => <Select {...p} value={draft.announcementDefaultPolicy} onChange={(e) => setDraft((d) => ({ ...d, announcementDefaultPolicy: e.target.value as DefinitionDraft['announcementDefaultPolicy'] }))}>{ANNOUNCEMENT_POLICIES.map((v) => <option key={v} value={v}>{optionLabel(v)}</option>)}</Select>}</Field></div>
          <TransitionStepEditor title="Enter show" offsetHelp="Milliseconds from the end of the resting sequence. Negative is before." steps={draft.enterShow} actions={actions} onChange={(i, p) => updateCues('enterShow', i, p)} onAdd={() => addCue('enterShow')} onRemove={(i) => setDraft((d) => ({ ...d, enterShow: d.enterShow.filter((_, n) => n !== i) }))} />
          <TransitionStepEditor title="Enter resting" offsetHelp="Milliseconds from the end of the show. Negative is before." steps={draft.enterResting} actions={actions} onChange={(i, p) => updateCues('enterResting', i, p)} onAdd={() => addCue('enterResting')} onRemove={(i) => setDraft((d) => ({ ...d, enterResting: d.enterResting.filter((_, n) => n !== i) }))} />
          <div className="sm-inspector__group">
            <h4 className="sm-subsection__title">After the last show</h4>
            <RadioCardList
              label="Cues after the last show"
              help="Runs in place of the enter resting cues once the last show ends. The end-of-night playlist starts either way."
              value={draft.endOfNightMode}
              onChange={(value) => setDraft((d) => ({ ...d, endOfNightMode: value as EndOfNightMode }))}
              options={[
                { value: 'resting', title: 'Run the enter resting cues', desc: 'The same cues as after every other show. This is the default.' },
                { value: 'none', title: 'Run no cues', desc: 'Only the end-of-night playlist starts.' },
                { value: 'custom', title: 'Run end-of-night cues', desc: 'A separate list, such as a different Resolume resting look.' },
              ]}
            />
            <Field label="Blackout after the last show (ms)" help="Leave empty to use Blackout after show.">{(p) => <Input {...p} type="number" min="0" step="1" value={draft.endOfNightBlackoutMs} onChange={(e) => setDraft((d) => ({ ...d, endOfNightBlackoutMs: e.target.value }))} />}</Field>
          </div>
          {draft.endOfNightMode === 'custom' && <TransitionStepEditor title="Enter end of night" offsetHelp="Milliseconds from the end of the last show. Negative is before." steps={draft.enterEndOfNight} actions={actions} onChange={(i, p) => updateCues('enterEndOfNight', i, p)} onAdd={() => addCue('enterEndOfNight')} onRemove={(i) => setDraft((d) => ({ ...d, enterEndOfNight: d.enterEndOfNight.filter((_, n) => n !== i) }))} />}
          <div className="sm-inspector__group">
            <h4 className="sm-subsection__title">Background audio</h4>
            <RadioCardList
              label="Background audio while resting"
              value={draft.backgroundAudio.mode}
              onChange={(value) => {
                const mode = value as BackgroundAudioMode
                setDraft((current) => ({
                  ...current,
                  backgroundAudio: {
                    ...current.backgroundAudio,
                    mode,
                    items: mode === 'inline' && current.backgroundAudio.items.length === 0 ? [blankBackgroundAudioItem(current.show)] : current.backgroundAudio.items,
                  },
                }))
              }}
              options={[
                { value: 'none', title: 'None' },
                { value: 'inline', title: 'Inline items' },
                { value: 'playlist', title: 'Media playlist' },
              ]}
            />
            {draft.backgroundAudio.mode === 'playlist' && (
              <Field label="Media playlist" help="Its own items, repeat, resume and gain apply.">
                {(p) => (
                  <Select
                    {...p}
                    value={draft.backgroundAudio.mediaPlaylist}
                    onChange={(e) => updateBackgroundAudio({ mediaPlaylist: e.target.value })}
                  >
                    <option value="">Select a media playlist…</option>
                    {mediaPlaylistOptions(mediaPlaylists, draft.backgroundAudio.mediaPlaylist).map((playlist) => (
                      <option key={playlist.id} value={playlist.id}>
                        {playlist.label}
                      </option>
                    ))}
                  </Select>
                )}
              </Field>
            )}
            {draft.backgroundAudio.mode === 'inline' && (
              <>
                <div className="sm-night-session-compact-grid">
                  <Field label="Repeat">{(p) => <Select {...p} value={draft.backgroundAudio.repeat} onChange={(e) => updateBackgroundAudio({ repeat: e.target.value as BackgroundAudioDraft['repeat'] })}>{BACKGROUND_AUDIO_REPEATS.map((option) => <option key={option} value={option}>{optionLabel(option)}</option>)}</Select>}</Field>
                  <Field label="Resume" help="How background audio resumes after a show returns to resting.">{(p) => <Select {...p} value={draft.backgroundAudio.resume} onChange={(e) => updateBackgroundAudio({ resume: e.target.value as BackgroundAudioDraft['resume'] })}>{BACKGROUND_AUDIO_RESUMES.map((option) => <option key={option} value={option}>{optionLabel(option)}</option>)}</Select>}</Field>
                  <Field label="Between items">{(p) => <Select {...p} value={draft.backgroundAudio.itemTransition} onChange={(e) => updateBackgroundAudio({ itemTransition: e.target.value as BackgroundAudioDraft['itemTransition'] })}>{BACKGROUND_AUDIO_TRANSITIONS.map((option) => <option key={option} value={option}>{optionLabel(option)}</option>)}</Select>}</Field>
                  {draft.backgroundAudio.itemTransition === 'crossfade' && (
                    <Field label="Crossfade (ms)" help="Required when item transition is crossfade.">{(p) => <Input {...p} type="number" min="0" step="1" value={draft.backgroundAudio.crossfadeMs} onChange={(e) => updateBackgroundAudio({ crossfadeMs: e.target.value })} />}</Field>
                  )}
                  <Field label="Maximum gain (dB)" help="Must be 0 dB or lower.">{(p) => <Input {...p} type="number" max="0" step="0.1" value={draft.backgroundAudio.maxGainDb} onChange={(e) => updateBackgroundAudio({ maxGainDb: e.target.value })} />}</Field>
                  <Field label="Fade-out (ms)" help="Set together with fade-in, or leave both blank for an instant cut.">{(p) => <Input {...p} type="number" min="0" step="1" value={draft.backgroundAudio.fadeOutMs} onChange={(e) => updateBackgroundAudio({ fadeOutMs: e.target.value })} />}</Field>
                  <Field label="Fade-in (ms)">{(p) => <Input {...p} type="number" min="0" step="1" value={draft.backgroundAudio.fadeInMs} onChange={(e) => updateBackgroundAudio({ fadeInMs: e.target.value })} />}</Field>
                </div>
                {draft.backgroundAudio.items.map((item, index) => (
                  <div className="sm-night-session-item" key={`bg-item-${index}`}>
                    <Field label="Item id">{(p) => <Input {...p} value={item.itemId} onChange={(e) => updateBackgroundAudioItem(index, { itemId: e.target.value })} />}</Field>
                    <Field label="Audio asset" help="Plays on the nodes chosen below." error={deadAudioAssetError(item)}>
                      {(p) => (
                        <AudioAssetPicker
                          {...p}
                          assets={audioAssets}
                          sequence={item.sequence}
                          target={item.target}
                          onPick={(asset) =>
                            updateBackgroundAudioItem(index, { show: draft.show, sequence: asset?.sequence ?? '', target: asset?.target ?? '', itemId: item.itemId || (asset?.sequence ?? '') })
                          }
                        />
                      )}
                    </Field>
                    <Button variant="quiet" onClick={() => removeBackgroundAudioItem(index)}>Remove</Button>
                  </div>
                ))}
                <Button onClick={addBackgroundAudioItem}>Add audio item</Button>
              </>
            )}
            {draft.backgroundAudio.mode !== 'none' && (
              <AudioNodesResolutionField
                label="Play on these nodes"
                ownLabel="this background audio"
                explicitValue={draft.backgroundAudio.targets}
                onExplicitChange={(targets) => updateBackgroundAudio({ targets })}
                excludeValue={draft.backgroundAudio.excludeNodes}
                onExcludeChange={(excludeNodes) => updateBackgroundAudio({ excludeNodes })}
                excludeError={backgroundAudioTargetsError ?? undefined}
                showAudioNodesState={showAudioNodesState}
                nodesState={audioNodesState}
              />
            )}
          </div>
          <div className="sm-inspector__group">
            <h4 className="sm-subsection__title">Site control</h4>
            <Field label="Request thermal profile" help="The action this site runs.">
              {(p) => <Input {...p} value={draft.siteControl.requestThermalProfile} onChange={(e) => updateSiteControl({ requestThermalProfile: e.target.value })} />}
            </Field>
            <div className="sm-night-session-group-head"><h4 className="sm-subsection__title">Presentation power-on</h4></div>
            <Choice type="checkbox" checked={draft.siteControl.powerOnEnabled} onChange={(e) => updateSiteControl({ powerOnEnabled: e.target.checked })} label="Set a presentation power on action" />
            {draft.siteControl.powerOnEnabled && (
              <div className="sm-night-session-compact-grid">
                <Field label="Action">{(p) => <Input {...p} value={draft.siteControl.powerOnAction} onChange={(e) => updateSiteControl({ powerOnAction: e.target.value })} />}</Field>
                <Field label="Power domain">{(p) => <Select {...p} value={draft.siteControl.powerOnDomain} onChange={(e) => updateSiteControl({ powerOnDomain: e.target.value as SiteControlDraft['powerOnDomain'] })}>{POWER_DOMAINS.map((option) => <option key={option} value={option}>{optionLabel(option)}</option>)}</Select>}</Field>
                <Field label="Power kind set by" help="Only User declared is accepted here.">
                  {(p) => <Select {...p} value={draft.siteControl.powerOnProvenance} onChange={(e) => updateSiteControl({ powerOnProvenance: e.target.value as SiteControlDraft['powerOnProvenance'] })}>{DOMAIN_PROVENANCES.map((option) => <option key={option} value={option}>{optionLabel(option)}</option>)}</Select>}
                </Field>
              </div>
            )}
            <div className="sm-night-session-group-head"><h4 className="sm-subsection__title">Presentation power down</h4></div>
            <Choice type="checkbox" checked={draft.siteControl.powerOffEnabled} onChange={(e) => updateSiteControl({ powerOffEnabled: e.target.checked })} label="Set a presentation power down action" />
            {draft.siteControl.powerOffEnabled && (
              <>
                <div className="sm-night-session-compact-grid">
                  <Field label="Action">{(p) => <Input {...p} value={draft.siteControl.powerOffAction} onChange={(e) => updateSiteControl({ powerOffAction: e.target.value })} />}</Field>
                  <Field label="Power domain">{(p) => <Select {...p} value={draft.siteControl.powerOffDomain} onChange={(e) => updateSiteControl({ powerOffDomain: e.target.value as SiteControlDraft['powerOffDomain'] })}>{POWER_DOMAINS.map((option) => <option key={option} value={option}>{optionLabel(option)}</option>)}</Select>}</Field>
                  <Field label="Power kind set by">{(p) => <Select {...p} value={draft.siteControl.powerOffProvenance} onChange={(e) => updateSiteControl({ powerOffProvenance: e.target.value as SiteControlDraft['powerOffProvenance'] })}>{DOMAIN_PROVENANCES.map((option) => <option key={option} value={option}>{optionLabel(option)}</option>)}</Select>}</Field>
                  <Field label="Removal policy" help="Immediate needs the confirmation below. After-actions needs a prerequisite.">
                    {(p) => <Select {...p} value={draft.siteControl.removalPolicy} onChange={(e) => updateSiteControl({ removalPolicy: e.target.value as SiteControlDraft['removalPolicy'] })}>{REMOVAL_POLICIES.map((option) => <option key={option} value={option}>{optionLabel(option)}</option>)}</Select>}
                  </Field>
                </div>
                {draft.siteControl.removalPolicy === 'immediate' && (
                  <Choice type="checkbox" checked={draft.siteControl.immediateSafeAttestation} onChange={(e) => updateSiteControl({ immediateSafeAttestation: e.target.checked })} label="This power domain is safe to remove immediately" />
                )}
                {draft.siteControl.removalPolicy === 'after-actions' && (
                  <>
                    {draft.siteControl.prerequisites.map((p, index) => (
                      <div className="sm-night-session-item" key={`prereq-${index}`}>
                        <Field label="Kind">{(field) => <Select {...field} value={p.kind} onChange={(e) => updatePrerequisite(index, { kind: e.target.value as PrerequisiteDraft['kind'] })}>{PREREQUISITE_KINDS.map((option) => <option key={option} value={option}>{option === 'evidence' ? 'Signal check' : option}</option>)}</Select>}</Field>
                        {p.kind === 'delay' ? (
                          <Field label="Delay (ms)">{(field) => <Input {...field} type="number" min="0" step="1" value={p.delayMs} onChange={(e) => updatePrerequisite(index, { delayMs: e.target.value })} />}</Field>
                        ) : (
                          <>
                            <Field label="Action">{(field) => <Input {...field} value={p.action} onChange={(e) => updatePrerequisite(index, { action: e.target.value })} />}</Field>
                            {p.kind === 'action' && (
                              <Choice type="checkbox" checked={p.requireConfirmation} onChange={(e) => updatePrerequisite(index, { requireConfirmation: e.target.checked })} label="Require confirmation" />
                            )}
                          </>
                        )}
                        <Button variant="quiet" onClick={() => removePrerequisite(index)}>Remove prerequisite</Button>
                      </div>
                    ))}
                    <Button onClick={addPrerequisite}>Add prerequisite</Button>
                  </>
                )}
              </>
            )}
          </div>
          <div className="sm-inspector__group">
            <div className="sm-night-session-group-head"><h4 className="sm-subsection__title">Safety checks</h4><Button onClick={addInterlock}>Add safety check</Button></div>
            {draft.interlocks.map((item, index) => (
              <div className="sm-night-session-item" key={`interlock-${index}`}>
                <Field label="Name">{(p) => <Input {...p} value={item.name} onChange={(e) => updateInterlock(index, { name: e.target.value })} />}</Field>
                <div className="sm-night-session-compact-grid">
                  <Field label="Phase">{(p) => <Select {...p} value={item.phase} onChange={(e) => updateInterlock(index, { phase: e.target.value as InterlockDraft['phase'] })}>{INTERLOCK_PHASES.map((option) => <option key={option} value={option}>{optionLabel(option)}</option>)}</Select>}</Field>
                  <Field label="Mode" help="Block needs the two settings below.">
                    {(p) => <Select {...p} value={item.posture} onChange={(e) => updateInterlock(index, { posture: e.target.value as InterlockDraft['posture'] })}>{INTERLOCK_POSTURES.map((option) => <option key={option} value={option}>{optionLabel(option)}</option>)}</Select>}
                  </Field>
                  {item.posture !== 'disabled' && (
                    <>
                      <Field label="Signal action" help="Pick an MQTT action that waits for a reply.">
                        {(p) => <Input {...p} value={item.signal} onChange={(e) => updateInterlock(index, { signal: e.target.value })} />}
                      </Field>
                      <Field label="Freshness (s)" help="A reading older than this counts as unavailable.">
                        {(p) => <Input {...p} type="number" min="0" step="1" value={item.freshnessSeconds} onChange={(e) => updateInterlock(index, { freshnessSeconds: e.target.value })} />}
                      </Field>
                      <Field label="Failure text">{(p) => <Input {...p} value={item.failureText} onChange={(e) => updateInterlock(index, { failureText: e.target.value })} />}</Field>
                      {item.posture === 'block' && (
                        <>
                          <Field label="When the source does not answer">{(p) => <Select {...p} value={item.onUnavailable} onChange={(e) => updateInterlock(index, { onUnavailable: e.target.value as InterlockDraft['onUnavailable'] })}>{ON_UNAVAILABLE_OPTIONS.map((option) => <option key={option} value={option}>{optionLabel(option)}</option>)}</Select>}</Field>
                          <Field label="Who may override">{(p) => <Select {...p} value={item.overridePolicy} onChange={(e) => updateInterlock(index, { overridePolicy: e.target.value as InterlockDraft['overridePolicy'] })}>{OVERRIDE_POLICIES.map((option) => <option key={option} value={option}>{optionLabel(option)}</option>)}</Select>}</Field>
                        </>
                      )}
                    </>
                  )}
                </div>
                <Button variant="quiet" onClick={() => removeInterlock(index)}>Remove safety check</Button>
              </div>
            ))}
          </div>
          {error !== null && <RuledStrip absence="failed" label="Definition failed" fact={error} />}
          {loaded !== null && <RevisionHistory mode="list" id="sn-definition-revisions" fetch={() => getNightSessionConfigRevisions(loaded.id)} reloadKey={reloadKey} fetchRevision={(n) => getNightSessionConfigRevision(loaded.id, n)} />}
          <div className="sm-inspector__actions"><span className="sm-small sm-muted">{loaded === null ? 'Creates version 1' : `Creates version ${loaded.revision + 1}`}</span><div className="sm-btn-row"><Button variant="quiet" onClick={closeInspector}>Cancel</Button><Button variant="primary" disabled={!gate.allowed || saving} title={gate.allowed ? undefined : gate.reason} onClick={save}>{saving ? 'Saving…' : loaded === null ? 'Create definition' : 'Save definition'}</Button></div></div>
          {loaded !== null && (
            <DeletePanel
              key={loaded.id}
              title="Delete this definition"
              confirmNoun="the definition's own label"
              confirmValue={loaded.payload.label}
              actionLabel="Delete definition"
              deleting={deleting}
              error={deleteError}
              allowed={gate.allowed}
              disallowedReason={gate.allowed ? undefined : gate.reason}
              onDelete={removeDefinition}
            >
              <p className="sm-small sm-muted">A definition cannot be deleted while its night is running; stop the night first.</p>
            </DeletePanel>
          )}
        </div>}
      </aside>
    </Panes>
  </div>
}

function TransitionStepEditor({ title, offsetHelp, steps, actions, onChange, onAdd, onRemove }: { title: string; offsetHelp: string; steps: CueDraft[]; actions: ShowActionConfigResponse[]; onChange: (index: number, patch: Partial<CueDraft>) => void; onAdd: () => void; onRemove: (index: number) => void }) {
  return <div className="sm-inspector__group"><div className="sm-night-session-group-head"><div><h4 className="sm-subsection__title">{title}</h4><p className="sm-small sm-muted">{steps.length} {steps.length === 1 ? 'step' : 'steps'}</p></div><Button onClick={onAdd}>Add step</Button></div>{steps.map((step, index) => <div className="sm-night-session-item" key={index}><Field label="Name">{(p) => <Input {...p} value={step.name} onChange={(e) => onChange(index, { name: e.target.value })} />}</Field><div className="sm-night-session-compact-grid"><Field label="Role">{(p) => <Select {...p} value={step.role} onChange={(e) => onChange(index, { role: e.target.value as CueDraft['role'] })}>{CUE_ROLES.map((v) => <option key={v} value={v}>{optionLabel(v)}</option>)}</Select>}</Field><Field label="Action">{(p) => <Select {...p} value={step.action} onChange={(e) => onChange(index, { action: e.target.value })}><option value="">Select an action…</option>{actions.map((action) => <option key={action.id} value={action.id}>{action.payload.label} · {action.id}</option>)}</Select>}</Field><Field label="Offset (ms)" help={offsetHelp}>{(p) => <Input {...p} type="number" step="1" value={step.offsetMs} onChange={(e) => onChange(index, { offsetMs: e.target.value })} />}</Field><Field label="Fade (ms)">{(p) => <Input {...p} type="number" min="0" step="1" value={step.fadeDurationMs} onChange={(e) => onChange(index, { fadeDurationMs: e.target.value })} />}</Field><Field label="On failure">{(p) => <Select {...p} value={step.onFailure} onChange={(e) => onChange(index, { onFailure: e.target.value as CueDraft['onFailure'] })}><option value="continue">Continue</option><option value="abort">Abort the rest</option></Select>}</Field>{step.role === 'announcement' && <Field label="Announcement policy">{(p) => <Select {...p} value={step.announcementPolicy} onChange={(e) => onChange(index, { announcementPolicy: e.target.value as CueDraft['announcementPolicy'] })}><option value="">Use default</option><option value="duck">Duck</option><option value="mix">Mix</option><option value="interrupt">Interrupt</option></Select>}</Field>}</div><Choice type="checkbox" checked={step.barrier} onChange={(e) => onChange(index, { barrier: e.target.checked })} label="Wait for this action before continuing" /><Button variant="quiet" onClick={() => onRemove(index)}>Remove step</Button></div>)}</div>
}
