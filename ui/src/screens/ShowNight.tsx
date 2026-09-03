import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  ApiError,
  PROBLEM_TYPE,
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
  type ConfigObjectSummary,
  type ConfigNightSessionCue,
  type ConfigNightSessionWrite,
  type ShowActionConfigResponse,
  type Asset,
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
  DefinitionStrip,
  Field,
  FieldGrid,
  Input,
  LifecycleCommands,
  Panes,
  RevisionHistory,
  RuledStrip,
  Section,
  Select,
  SelectableRow,
  StatusPair,
  Table,
  TableWrap,
} from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { guardedSave, type SaveOutcome } from '../domain/save'
import { effectiveServerTimeIso, formatClock } from '../domain/time'
import { formatPosition, nightLifecycleGroups, type CommandOutcome } from './liveControlModel'
import { StaleWriteStrip } from './StaleWrite'
import {
  backgroundAudioSteps,
  cycleRail,
  evidenceReadouts,
  nextTransition,
  nightRail,
  nowPlaying,
  pinnedCeilingFact,
  readinessChecks,
  runOfShow,
  type RailStep,
} from './showNightModel'

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
              {step.status === 'done' ? '✓ ' : step.status === 'now' ? '● ' : step.status === 'notWired' ? '⚠ ' : ''}
            </span>
            {step.label}
          </p>
          <p className="sm-rail-strip__detail">{step.detail}</p>
        </div>
      ))}
    </div>
  )
}

/** Whichever lifecycle command a `night-not-ready` refusal named, and its server-supplied detail (verbatim; no structured rule field exists). */
type Withheld = { command: NightCommandName; detail: string }

export function ShowNight() {
  const model = useModelContext()
  const { session, error } = useNightSession()
  const nowIso = effectiveServerTimeIso(model.serverTime, model.serverTimeReceivedAt, Date.now())
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'night:command')
  const overrideGate = evaluateScope(model.session, model.sessionFetchFailed, 'night:override')
  const [outcome, setOutcome] = useState<CommandOutcome | null>(null)
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

  const send = useCallback(
    (command: NightCommandName, interlockOverrides?: readonly NightInterlockOverride[]) => {
      const idempotencyKey = command === 'prepare-site' ? prepareSiteKey : undefined
      dispatchNightCommand(command, idempotencyKey, interlockOverrides, command === 'start-night' ? skipEnterShowLead : undefined)
        .then((response) => {
          setWithheld(null)
          setOverrideRule('')
          setOverrideReason('')
          setOverrideInvalid(false)
          if (command === 'prepare-site') setPrepareSiteKey(randomUUIDv4())
          if (command === 'start-night') setSkipEnterShowLead(false)
          const applied = response.command.outcome === 'applied'
          const parts = [
            applied
              ? `${command} was accepted. What the session then does is what this page reports.`
              : `${command} reports idempotent_no_op: this repeat matched the session's own state and changed nothing.`,
          ]
          if (response.command.reason !== undefined && response.command.reason !== '') parts.push(response.command.reason)
          if (response.command.attributionDegraded) {
            parts.push('This applied with no recorded audit attribution (attributionDegraded); that flag never clears for this session.')
          }
          setOutcome({ tone: 'warn', label: applied ? 'Accepted' : 'No-op', detail: parts.join(' ') })
        })
        .catch((err: unknown) => {
          if (err instanceof ApiError && err.problemType === PROBLEM_TYPE.nightNotReady) {
            setWithheld({ command, detail: err.message })
            setOutcome({ tone: 'bad', label: 'Withheld', detail: err.message })
            return
          }
          setWithheld(null)
          if (err instanceof ApiError && err.problemType === PROBLEM_TYPE.nightStateRejected) {
            setOutcome({
              tone: 'bad',
              label: 'Refused',
              detail: `${command} is not valid from the session's current state. ${err.message}`,
            })
            return
          }
          if (err instanceof ApiError && err.problemType === PROBLEM_TYPE.nightAmbiguous) {
            setOutcome({
              tone: 'bad',
              label: 'Degraded',
              detail: `${err.message} Recover with end-session, then prepare-site.`,
            })
            return
          }
          if (err instanceof ApiError && err.problemType === PROBLEM_TYPE.nightCommandRefusedAuditUnavailable) {
            setOutcome({
              tone: 'warn',
              label: 'Not dispatched',
              detail: `${command} was not dispatched and nothing was recorded, so this is not a failed command. ${err.message}`,
            })
            return
          }
          setOutcome({ tone: 'bad', label: 'Refused', detail: `${command} was refused: ${describeApiError(err)}` })
        })
    },
    [prepareSiteKey, skipEnterShowLead],
  )

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
            'The change stream announces a change, not a state, so this page stays blank until the session reports or a read succeeds.'
          }
        />
      </>
    )
  }

  const { instance, run, state } = nowPlaying(model)
  const next = nextTransition(model)
  const steps = runOfShow(session)
  const armed = steps.filter((step) => step.when === 'Armed').length
  const nextArmedStep = steps.find((step) => step.when === 'Armed')
  const elapsedSeconds = state?.elapsedSeconds ?? null
  const totalSeconds = state?.totalSeconds ?? null
  const playbackPercent =
    elapsedSeconds !== null && totalSeconds !== null && totalSeconds > 0
      ? Math.min(100, Math.max(0, (elapsedSeconds / totalSeconds) * 100))
      : null

  return (
    <>
      <div className="sm-page__head">
        <div>
          <p className="sm-eyebrow">Show Night · <span className="sm-data">{session.id}</span></p>
          <h1 className="sm-page__title">Cycle {session.cycle} of the night</h1>
        </div>
        <ButtonRow>
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
        <p className="sm-section__footnote">
          The bottom row repeats: rest, then show, then rest again, for as many cycles as the night allows.{' '}
          <strong>Request final show</strong> closes admission and sends the last cycle to end-of-night instead of back
          to resting.
        </p>
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
              fact="No FPP instance is reporting playback."
              detail="Nothing here is inferred from the schedule."
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
                    term: 'Evidence',
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
            <>
              <p className="sm-nownext__title">{formatPosition(next.remainingSeconds)}</p>
              <p className="sm-small sm-muted">until the sequence ends and the boundary begins.</p>
              <div className="sm-nownext__boundary">
                <p>{nextArmedStep?.name ?? 'No Transition Step is armed'}</p>
                <p className="sm-small sm-muted">
                  {nextArmedStep === undefined ? 'The boundary has no recorded next step.' : `${nextArmedStep.detail} · ${armed} ${armed === 1 ? 'step' : 'steps'} armed`}
                </p>
              </div>
              <p className="sm-small sm-faint sm-nownext__derivation">
                Derived from observed playback, not a clock. If the position goes stale the boundary becomes unknown rather than assumed.
              </p>
            </>
          ) : (
            <RuledStrip absence="unavailable" label="Unknown" fact={next.reason} detail="Derived from observed playback, not a clock." />
          )}
        </section>
      </div>

      <Section
        id="sn-commands"
        title="Lifecycle commands"
        aside={
          <span className="sm-small sm-muted">
            Accepted, never confirmed here. <Link to="/control">Full transport in Live Control →</Link>
          </span>
        }
      >
        <LifecycleCommands
          groups={nightLifecycleGroups(
            gate,
            send,
            <label className="sm-choice sm-choice--gloved">
              <input
                type="checkbox"
                checked={skipEnterShowLead}
                disabled={!gate.allowed}
                onChange={(e) => setSkipEnterShowLead(e.target.checked)}
              />
              <span>Skip the enter-show lead. An enter-show announcement cue still dispatches.</span>
            </label>,
          )}
        />
        {outcome !== null && (
          <div className="sm-outcome">
            <StatusPair tone={outcome.tone} label={outcome.label} />
            <p className="sm-outcome__detail">{outcome.detail}</p>
            {withheld !== null &&
              (overrideGate.allowed ? (
                <div className="sm-outcome__override">
                  <FieldGrid>
                    <Field
                      label="Interlock rule"
                      help="Name it exactly as configured; the refusal above names it only in its own detail text."
                      error={overrideInvalid && overrideRule.trim() === '' ? 'Name the rule to override.' : undefined}
                    >
                      {(props) => <Input {...props} value={overrideRule} onChange={(e) => setOverrideRule(e.target.value)} />}
                    </Field>
                    <Field
                      label="Reason"
                      error={overrideInvalid && overrideReason.trim() === '' ? 'A reason is required to override an interlock.' : undefined}
                    >
                      {(props) => <Input {...props} value={overrideReason} onChange={(e) => setOverrideReason(e.target.value)} />}
                    </Field>
                  </FieldGrid>
                  <ButtonRow>
                    <Button size="gloved" disabled={!gate.allowed} onClick={submitOverride}>
                      Retry {withheld.command} with override
                    </Button>
                  </ButtonRow>
                </div>
              ) : (
                <p className="sm-small sm-muted">{overrideGate.reason}</p>
              ))}
          </div>
        )}
      </Section>

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
            label={session.cues.state.replace('_', ' ')}
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
              {steps.length} steps this cycle · {armed} armed for the boundary. A step marked unconfirmable expects no
              response and reports that on every run.
            </p>
          </>
        )}
      </Section>

      <Section id="sn-evidence" title="Evidence" aside={<span className="sm-small sm-muted">Anything not observed says so</span>}>
        {evidenceReadouts(session, nowIso).map((readout) => (
          <div key={readout.key} className="sm-readout">
            <StatusPair tone={readout.tone} label={readout.label} />
            <div>
              <p className="sm-readout__fact">
                {readout.fact}
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
              {readout.key === 'readiness' &&
                (session.readiness.checks.length === 0 ? (
                  <p className="sm-small sm-faint">No individual checks were recorded with this result.</p>
                ) : (
                  <div className="sm-readout__checks">
                    {readinessChecks(session.readiness.checks).map((check) => (
                      <div key={check.key} className="sm-readout">
                        <StatusPair tone={check.tone} label={check.label} />
                        <p className="sm-readout__fact">{check.fact}</p>
                      </div>
                    ))}
                  </div>
                ))}
              {readout.key === 'audio' && (
                <>
                  <p className="sm-small sm-muted">{pinnedCeilingFact(session.backgroundAudio)}</p>
                  {session.backgroundAudio.steps.length > 0 && (
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
                          {backgroundAudioSteps(session.backgroundAudio).map((step) => (
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
                  )}
                </>
              )}
            </div>
          </div>
        ))}
      </Section>

      <NightSessionActivation />

      {session.degraded && (
        <BlankingPlate
          absence="stale"
          stamp="Degr"
          eyebrow="Night session · degraded"
          title="This session is degraded"
          detail={session.degradedReason ?? 'The controller reported degraded without a reason.'}
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
function NightSessionActivation() {
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
    <Section
      id="sn-active"
      title="Night session activation"
      aside={<span className="sm-small sm-muted">Which definition start-night arms next</span>}
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
                    <span className="sm-muted">none - the pointer is cleared</span>
                  ) : (
                    <span className="sm-data">{state.response.payload.session}</span>
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

          <FieldGrid>
            <Field label="Activate a definition" help="Points start-night at a different night.session object.">
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
                label={`Type ${state.response.payload.session} to confirm clearing the pointer`}
                help="Clearing is a sharp control: start-night then has no armed definition until another is activated."
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
type BackgroundAudioDraft = {
  enabled: boolean
  items: BackgroundAudioItemDraft[]
  repeat: 'none' | 'item' | 'playlist'
  resume: 'resume' | 'restart'
  itemTransition: 'sequential' | 'gapless' | 'crossfade'
  crossfadeMs: string
  maxGainDb: string
  fadeOutMs: string
  fadeInMs: string
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
  enterShow: CueDraft[]
  enterResting: CueDraft[]
  base: ConfigNightSessionWrite | null
}

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

const blankCue = (): CueDraft => ({ name: '', role: 'lighting', action: '', offsetMs: '0', barrier: false, onFailure: 'continue', fadeDurationMs: '', announcementPolicy: '', base: null })
const blankBackgroundAudioItem = (show = ''): BackgroundAudioItemDraft => ({ itemId: '', show, sequence: '', target: '' })
const blankBackgroundAudio = (): BackgroundAudioDraft => ({ enabled: false, items: [], repeat: 'none', resume: 'resume', itemTransition: 'sequential', crossfadeMs: '', maxGainDb: '', fadeOutMs: '', fadeInMs: '' })
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
  blackoutHoldMs: '0', blackoutAfterShowMs: '0',
  enterShow: [], enterResting: [], base: null,
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
      : {
          enabled: true,
          items: bg.items.map((item) => ({ itemId: item.itemId, show: item.show, sequence: item.sequence, target: item.target })),
          repeat: bg.repeat, resume: bg.resume, itemTransition: bg.itemTransition,
          crossfadeMs: bg.crossfadeMs === undefined ? '' : String(bg.crossfadeMs),
          maxGainDb: String(bg.maxGainDb),
          fadeOutMs: bg.fadeOutMs === undefined ? '' : String(bg.fadeOutMs),
          fadeInMs: bg.fadeInMs === undefined ? '' : String(bg.fadeInMs),
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
    enterShow: payload.enterShow.cues.map(cue), enterResting: payload.enterResting.cues.map(cue), base: payload,
  }
}

type BackgroundAudioWrite = NonNullable<ConfigNightSessionWrite['resting']['backgroundAudio']>
type SiteControlWrite = NonNullable<ConfigNightSessionWrite['siteControl']>
type InterlockWrite = NonNullable<ConfigNightSessionWrite['interlocks']>[number]

function buildBackgroundAudio(audio: BackgroundAudioDraft): { ok: true; value: BackgroundAudioWrite | undefined } | { ok: false; error: string } {
  if (!audio.enabled) return { ok: true, value: undefined }
  if (audio.items.length === 0) return { ok: false, error: 'Background audio needs at least one item, or disable it.' }
  const items: BackgroundAudioWrite['items'] = []
  for (const [index, item] of audio.items.entries()) {
    if (item.itemId.trim() === '' || item.show.trim() === '' || item.sequence.trim() === '' || item.target.trim() === '') {
      return { ok: false, error: `Background audio item ${index + 1} needs an item id, show, sequence, and target node.` }
    }
    items.push({ itemId: item.itemId.trim(), show: item.show.trim(), sequence: item.sequence.trim(), target: item.target.trim() })
  }
  const maxGainDb = Number(audio.maxGainDb)
  if (audio.maxGainDb.trim() === '' || Number.isNaN(maxGainDb) || maxGainDb > 0) {
    return { ok: false, error: 'Background audio ceiling (max gain) must be a number no greater than 0 dB.' }
  }
  const fadeOutText = audio.fadeOutMs.trim()
  const fadeInText = audio.fadeInMs.trim()
  if ((fadeOutText === '') !== (fadeInText === '')) {
    return { ok: false, error: 'Background audio fade-out and fade-in must be configured together, or both left empty for an instant cut.' }
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
    if (sc.powerOffAction.trim() === '') return { ok: false, error: 'Presentation power-off needs an action.' }
    if (sc.removalPolicy === 'immediate' && !sc.immediateSafeAttestation) {
      return { ok: false, error: 'Immediate removal requires the safe-to-remove attestation.' }
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
    if (item.name.trim() === '') return { ok: false, error: `Interlock ${index + 1} needs a name.` }
    if (item.posture === 'disabled') {
      built.push({ name: item.name.trim(), phase: item.phase, posture: 'disabled' })
      continue
    }
    if (item.signal.trim() === '') return { ok: false, error: `Interlock ${index + 1} needs a signal action.` }
    const entry: InterlockWrite = { name: item.name.trim(), phase: item.phase, posture: item.posture, signal: item.signal.trim() }
    if (item.freshnessSeconds.trim() !== '') {
      const freshness = Number(item.freshnessSeconds)
      if (!Number.isInteger(freshness) || freshness < 0) {
        return { ok: false, error: `Interlock ${index + 1}'s freshness must be a whole, non-negative number of seconds.` }
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

function definitionPayload(draft: DefinitionDraft): ConfigNightSessionWrite | { error: string } {
  const required: readonly [string, string][] = [
    ['Definition id', draft.id], ['Show', draft.show], ['Label', draft.label], ['Show playlist FPP instance', draft.showFpp], ['Show playlist', draft.showPlaylist],
    ['Resting FPP instance', draft.restingFpp], ['Resting playlist', draft.restingPlaylist], ['Resting timeline show', draft.timelineShow], ['Resting timeline sequence', draft.timelineSequence], ['Resting timeline target', draft.timelineTarget],
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
        return { ok: false, error: `${phase} transition step ${index + 1}'s fade duration must be a whole, non-negative number of milliseconds.` }
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
  const backgroundAudio = buildBackgroundAudio(draft.backgroundAudio)
  if (!backgroundAudio.ok) return { error: backgroundAudio.error }
  const siteControl = buildSiteControl(draft.siteControl)
  if (!siteControl.ok) return { error: siteControl.error }
  const interlocks = buildInterlocks(draft.interlocks)
  if (!interlocks.ok) return { error: interlocks.error }
  const blackoutHoldMs = Number(draft.blackoutHoldMs)
  if (!Number.isInteger(blackoutHoldMs) || blackoutHoldMs < 0) return { error: 'Enter-show blackout hold must be a whole, non-negative number of milliseconds.' }
  const blackoutAfterShowMs = Number(draft.blackoutAfterShowMs)
  if (!Number.isInteger(blackoutAfterShowMs) || blackoutAfterShowMs < 0) return { error: 'Enter-resting blackout-after-show must be a whole, non-negative number of milliseconds.' }

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
  if (siteControl.value === undefined) delete result.siteControl
  else result.siteControl = siteControl.value
  if (interlocks.value === undefined) delete result.interlocks
  else result.interlocks = interlocks.value
  return result
}

export function NightSessionDefinitions({ showId }: { showId?: string }) {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const [objects, setObjects] = useState<NightSessionConfigResponse[] | null>(null)
  const [activeId, setActiveId] = useState('')
  const [actions, setActions] = useState<ShowActionConfigResponse[]>([])
  const [playlistDefinitions, setPlaylistDefinitions] = useState<FPPPlaylistDefinitionMetadata[]>([])
  const [assets, setAssets] = useState<Asset[]>([])
  const [selected, setSelected] = useState('')
  const [draft, setDraft] = useState<DefinitionDraft>(() => blankDefinition(showId))
  const [loaded, setLoaded] = useState<NightSessionConfigResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [revision, setRevision] = useState<NightSessionConfigResponse | null>(null)
  const [reloadKey, setReloadKey] = useState(0)

  useEffect(() => {
    let cancelled = false
    Promise.all([
      listConfigObjects('night.session'),
      showId === undefined ? Promise.resolve({ objects: [] as ConfigObjectSummary[], serverTime: '' }) : listConfigObjects('show.action', showId),
      listFPPPlaylistDefinitions().catch(() => ({ definitions: [], serverTime: '' })),
      showId === undefined ? Promise.resolve({ assets: [], serverTime: '' }) : listAssets({ show: showId }).catch(() => ({ assets: [], serverTime: '' })),
    ])
      .then(async ([response, actionObjects, definitions, assetResponse]) => {
        if (cancelled) return
        const summaries = showId === undefined ? response.objects : response.objects.filter((object) => object.show === showId)
        const [full, fullActions] = await Promise.all([Promise.all(summaries.map((object) => getNightSessionConfig(object.id))), Promise.all(actionObjects.objects.map((object) => getShowAction(object.id)))])
        if (cancelled) return
        setObjects(full)
        setActions(fullActions)
        setPlaylistDefinitions(definitions.definitions)
        setAssets(assetResponse.assets)
      })
      .catch((err: unknown) => { if (!cancelled) setError(describeApiError(err)) })
    getNightSessionActiveConfig().then((active) => { if (!cancelled) setActiveId(active.payload.session) }).catch(() => undefined)
    return () => { cancelled = true }
  }, [reloadKey, showId])

  const selectDefinition = (id: string) => {
    setSelected(id); setError(null); setRevision(null)
    if (id === '') { setLoaded(null); setDraft(blankDefinition(showId)); return }
    getNightSessionConfig(id)
      .then((response) => { setLoaded(response); setDraft(draftFromDefinition(response)) })
      .catch((err: unknown) => setError(describeApiError(err)))
  }
  const updateCues = (which: 'enterShow' | 'enterResting', index: number, patch: Partial<CueDraft>) => setDraft((current) => ({ ...current, [which]: current[which].map((item, itemIndex) => itemIndex === index ? { ...item, ...patch } : item) }))
  const save = () => {
    const payload = definitionPayload(draft)
    if ('error' in payload) { setError(payload.error); return }
    setSaving(true); setError(null)
    putNightSessionConfig(draft.id.trim(), payload)
      .then((response) => { setLoaded(response); setDraft(draftFromDefinition(response)); setSelected(response.id); setReloadKey((n) => n + 1) })
      .catch((err: unknown) => setError(describeApiError(err)))
      .finally(() => setSaving(false))
  }
  const addCue = (which: 'enterShow' | 'enterResting') => setDraft((current) => ({ ...current, [which]: [...current[which], blankCue()] }))

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
  const audioAssets = assets.filter((asset) => asset.current && asset.mediaType === 'audio' && asset.targetKind === 'node')
  const timelineSequences = Array.from(new Set([...(draft.timelineSequence === '' ? [] : [draft.timelineSequence]), ...timelineAssets.map((asset) => asset.sequence)])).sort()
  const timelineTargets = Array.from(new Set([
    ...(draft.timelineTarget === '' ? [] : [draft.timelineTarget]),
    ...timelineAssets.filter((asset) => draft.timelineSequence === '' || asset.sequence === draft.timelineSequence).map((asset) => asset.target),
  ])).sort()
  const fppIds = (retained: string) => Array.from(new Set([...(retained === '' ? [] : [retained]), ...fppInstances.map((instance) => instance.instanceId)])).sort()

  const closeInspector = () => { setSelected(''); setLoaded(null); setDraft(blankDefinition(showId)); setRevision(null); setError(null) }
  return <div id="sn-definitions" className="sm-night-session-workspace">
    <div className="sm-night-session-heading">
      <div><h2 className="sm-section__title">Night session definitions</h2><p className="sm-small sm-muted">A definition says how the night enters the show and returns to resting. Editing creates a new revision; a running night is unchanged.</p></div>
      <Button variant="primary" onClick={() => { setSelected('__new__'); setLoaded(null); setDraft(blankDefinition(showId)); setError(null) }}>New definition</Button>
    </div>
    <Panes
      inspectorOpen={selected !== ''}
      onInspectorClose={closeInspector}
      inspectorLabelledBy="sn-definition-inspector-heading"
      inspectorWidth="wide"
    >
      <div className="sm-night-session-list">
        <p className="sm-eyebrow">Definitions · {objects?.length ?? 0}</p>
        {objects === null ? <RuledStrip absence="loading" label="Reading" fact="Reading night-session definitions." /> : objects.length === 0 ? <RuledStrip absence="empty" label="No definitions" fact="Create the first night-session definition for this Show." /> : <TableWrap label="Night session definitions"><Table minWidth={820}><thead><tr><th>Definition</th><th>Show playlist</th><th>Resting playlist</th><th>Revision</th><th>State</th></tr></thead><tbody>{objects.map((object) => <SelectableRow key={object.id} selected={selected === object.id} onActivate={() => selectDefinition(object.id)} ariaLabel={`Edit ${object.payload.label}`}><td><strong>{object.payload.label}</strong><br /><span className="sm-data sm-small">{object.id}</span></td><td><span className="sm-data">{object.payload.showPlaylist.fppInstanceId}</span><br />{object.payload.showPlaylist.playlist}</td><td><span className="sm-data">{object.payload.resting.fppInstanceId}</span><br />{object.payload.resting.playlist}</td><td className="sm-data">{object.revision}</td><td><span className={activeId === object.id ? 'sm-meta-status sm-meta-status--live' : 'sm-meta-status'}>{activeId === object.id ? 'Active' : 'Inactive'}</span></td></SelectableRow>)}</tbody></Table></TableWrap>}
        <p className="sm-small sm-muted sm-night-session-list__note">Definitions belong to this Show. Activation remains an operational action on Show Night.</p>
      </div>
      <aside>
        {selected === '' ? <div className="sm-inspector sm-night-session-empty"><p className="sm-eyebrow">Definition inspector</p><h3 className="sm-inspector__title">Select a definition</h3><p className="sm-small sm-muted">Choose a row to edit it, or create a new definition.</p></div> : <div className="sm-inspector sm-night-session-inspector">
          <p className="sm-eyebrow sm-eyebrow--accent">{loaded === null ? 'Draft · new definition' : `Editing · revision ${loaded.revision}`}</p>
          <h3 className="sm-inspector__title" id="sn-definition-inspector-heading">{loaded === null ? 'New night session' : draft.label}</h3>
          <div className="sm-inspector__group"><Field label="Label">{(p) => <Input {...p} value={draft.label} onChange={(e) => setDraft((d) => ({ ...d, label: e.target.value }))} />}</Field><Field label="Definition id" help={loaded === null ? 'Stable after creation.' : 'Definition ids do not change.'}>{(p) => <Input {...p} className="sm-data" disabled={loaded !== null} value={draft.id} onChange={(e) => setDraft((d) => ({ ...d, id: e.target.value }))} />}</Field></div>
          <div className="sm-inspector__group"><h4 className="sm-subsection__title">Show playback</h4><Field label="FPP instance">{(p) => <Select {...p} value={draft.showFpp} onChange={(e) => setDraft((d) => ({ ...d, showFpp: e.target.value, showPlaylist: '' }))}><option value="">Select an instance…</option>{fppIds(draft.showFpp).map((id) => <option key={id}>{id}</option>)}</Select>}</Field><Field label="Show playlist">{(p) => <Select {...p} value={draft.showPlaylist} onChange={(e) => setDraft((d) => ({ ...d, showPlaylist: e.target.value }))}><option value="">Select a playlist…</option>{playlistNames(draft.showFpp, draft.showPlaylist).map((name) => <option key={name}>{name}</option>)}</Select>}</Field></div>
          <div className="sm-inspector__group"><h4 className="sm-subsection__title">Resting playback</h4><Field label="FPP instance">{(p) => <Select {...p} value={draft.restingFpp} onChange={(e) => setDraft((d) => ({ ...d, restingFpp: e.target.value, restingPlaylist: '', endOfNightPlaylist: '' }))}><option value="">Select an instance…</option>{fppIds(draft.restingFpp).map((id) => <option key={id}>{id}</option>)}</Select>}</Field><Field label="Resting playlist">{(p) => <Select {...p} value={draft.restingPlaylist} onChange={(e) => setDraft((d) => ({ ...d, restingPlaylist: e.target.value }))}><option value="">Select a playlist…</option>{playlistNames(draft.restingFpp, draft.restingPlaylist).map((name) => <option key={name}>{name}</option>)}</Select>}</Field><Field label="End-of-night playlist">{(p) => <Select {...p} value={draft.endOfNightPlaylist} onChange={(e) => setDraft((d) => ({ ...d, endOfNightPlaylist: e.target.value }))}><option value="">Same as resting playlist</option>{playlistNames(draft.restingFpp, draft.endOfNightPlaylist).map((name) => <option key={name}>{name}</option>)}</Select>}</Field><Choice type="checkbox" checked={draft.endOfNightRepeat} onChange={(e) => setDraft((d) => ({ ...d, endOfNightRepeat: e.target.checked }))} label="Repeat the end-of-night playlist" /><Field label="Resting sequence">{(p) => <Select {...p} value={draft.timelineSequence} onChange={(e) => setDraft((d) => ({ ...d, timelineSequence: e.target.value, timelineTarget: '' }))}><option value="">Select a sequence…</option>{timelineSequences.map((v) => <option key={v}>{v}</option>)}</Select>}</Field><Field label="Resting target">{(p) => <Select {...p} value={draft.timelineTarget} onChange={(e) => setDraft((d) => ({ ...d, timelineTarget: e.target.value }))}><option value="">Select a target…</option>{timelineTargets.map((v) => <option key={v}>{v}</option>)}</Select>}</Field></div>
          <div className="sm-inspector__group"><h4 className="sm-subsection__title">Transition timing</h4><Field label="Blackout hold (ms)">{(p) => <Input {...p} type="number" min="0" step="1" value={draft.blackoutHoldMs} onChange={(e) => setDraft((d) => ({ ...d, blackoutHoldMs: e.target.value }))} />}</Field><Field label="Blackout after show (ms)">{(p) => <Input {...p} type="number" min="0" step="1" value={draft.blackoutAfterShowMs} onChange={(e) => setDraft((d) => ({ ...d, blackoutAfterShowMs: e.target.value }))} />}</Field><Field label="Default announcement policy">{(p) => <Select {...p} value={draft.announcementDefaultPolicy} onChange={(e) => setDraft((d) => ({ ...d, announcementDefaultPolicy: e.target.value as DefinitionDraft['announcementDefaultPolicy'] }))}>{ANNOUNCEMENT_POLICIES.map((v) => <option key={v}>{v}</option>)}</Select>}</Field></div>
          <TransitionStepEditor title="Enter show" steps={draft.enterShow} actions={actions} onChange={(i, p) => updateCues('enterShow', i, p)} onAdd={() => addCue('enterShow')} onRemove={(i) => setDraft((d) => ({ ...d, enterShow: d.enterShow.filter((_, n) => n !== i) }))} />
          <TransitionStepEditor title="Enter resting" steps={draft.enterResting} actions={actions} onChange={(i, p) => updateCues('enterResting', i, p)} onAdd={() => addCue('enterResting')} onRemove={(i) => setDraft((d) => ({ ...d, enterResting: d.enterResting.filter((_, n) => n !== i) }))} />
          <div className="sm-inspector__group">
            <h4 className="sm-subsection__title">Background audio</h4>
            <Choice
              type="checkbox"
              checked={draft.backgroundAudio.enabled}
              onChange={(e) => {
                const enabled = e.target.checked
                setDraft((current) => ({
                  ...current,
                  backgroundAudio: {
                    ...current.backgroundAudio,
                    enabled,
                    items: enabled && current.backgroundAudio.items.length === 0 ? [blankBackgroundAudioItem(current.show)] : current.backgroundAudio.items,
                  },
                }))
              }}
              label="Enable background audio while resting"
            />
            {draft.backgroundAudio.enabled && (
              <>
                <div className="sm-night-session-compact-grid">
                  <Field label="Repeat">{(p) => <Select {...p} value={draft.backgroundAudio.repeat} onChange={(e) => updateBackgroundAudio({ repeat: e.target.value as BackgroundAudioDraft['repeat'] })}>{BACKGROUND_AUDIO_REPEATS.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}</Field>
                  <Field label="Resume" help="How the bed resumes after a show returns to resting.">{(p) => <Select {...p} value={draft.backgroundAudio.resume} onChange={(e) => updateBackgroundAudio({ resume: e.target.value as BackgroundAudioDraft['resume'] })}>{BACKGROUND_AUDIO_RESUMES.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}</Field>
                  <Field label="Between items">{(p) => <Select {...p} value={draft.backgroundAudio.itemTransition} onChange={(e) => updateBackgroundAudio({ itemTransition: e.target.value as BackgroundAudioDraft['itemTransition'] })}>{BACKGROUND_AUDIO_TRANSITIONS.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}</Field>
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
                    <Field label="Audio asset">
                      {(p) => (
                        <Select
                          {...p}
                          value={audioAssets.find((asset) => asset.sequence === item.sequence && asset.target === item.target)?.id ?? ''}
                          onChange={(e) => {
                            const selectedAsset = audioAssets.find((asset) => asset.id === e.target.value)
                            updateBackgroundAudioItem(index, { show: draft.show, sequence: selectedAsset?.sequence ?? '', target: selectedAsset?.target ?? '', itemId: item.itemId || (selectedAsset?.sequence ?? '') })
                          }}
                        >
                          <option value="">Select an audio asset…</option>
                          {audioAssets.map((asset) => <option key={asset.id} value={asset.id}>{asset.sequence} · {asset.target}</option>)}
                        </Select>
                      )}
                    </Field>
                    <Button variant="quiet" onClick={() => removeBackgroundAudioItem(index)}>Remove</Button>
                  </div>
                ))}
                <Button onClick={addBackgroundAudioItem}>Add audio item</Button>
              </>
            )}
          </div>
          <div className="sm-inspector__group">
            <h4 className="sm-subsection__title">Site control</h4>
            <Field label="Request thermal profile" help="The show.action this deployment runs.">
              {(p) => <Input {...p} value={draft.siteControl.requestThermalProfile} onChange={(e) => updateSiteControl({ requestThermalProfile: e.target.value })} />}
            </Field>
            <div className="sm-night-session-group-head"><h4 className="sm-subsection__title">Presentation power-on</h4></div>
            <Choice type="checkbox" checked={draft.siteControl.powerOnEnabled} onChange={(e) => updateSiteControl({ powerOnEnabled: e.target.checked })} label="Configure a presentation power-on binding" />
            {draft.siteControl.powerOnEnabled && (
              <div className="sm-night-session-compact-grid">
                <Field label="Action">{(p) => <Input {...p} value={draft.siteControl.powerOnAction} onChange={(e) => updateSiteControl({ powerOnAction: e.target.value })} />}</Field>
                <Field label="Power domain">{(p) => <Select {...p} value={draft.siteControl.powerOnDomain} onChange={(e) => updateSiteControl({ powerOnDomain: e.target.value as SiteControlDraft['powerOnDomain'] })}>{POWER_DOMAINS.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}</Field>
                <Field label="Domain provenance" help="Only operator-declared is accepted for this binding.">
                  {(p) => <Select {...p} value={draft.siteControl.powerOnProvenance} onChange={(e) => updateSiteControl({ powerOnProvenance: e.target.value as SiteControlDraft['powerOnProvenance'] })}>{DOMAIN_PROVENANCES.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}
                </Field>
              </div>
            )}
            <div className="sm-night-session-group-head"><h4 className="sm-subsection__title">Presentation power-off</h4></div>
            <Choice type="checkbox" checked={draft.siteControl.powerOffEnabled} onChange={(e) => updateSiteControl({ powerOffEnabled: e.target.checked })} label="Configure a presentation power-off binding" />
            {draft.siteControl.powerOffEnabled && (
              <>
                <div className="sm-night-session-compact-grid">
                  <Field label="Action">{(p) => <Input {...p} value={draft.siteControl.powerOffAction} onChange={(e) => updateSiteControl({ powerOffAction: e.target.value })} />}</Field>
                  <Field label="Power domain">{(p) => <Select {...p} value={draft.siteControl.powerOffDomain} onChange={(e) => updateSiteControl({ powerOffDomain: e.target.value as SiteControlDraft['powerOffDomain'] })}>{POWER_DOMAINS.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}</Field>
                  <Field label="Domain provenance">{(p) => <Select {...p} value={draft.siteControl.powerOffProvenance} onChange={(e) => updateSiteControl({ powerOffProvenance: e.target.value as SiteControlDraft['powerOffProvenance'] })}>{DOMAIN_PROVENANCES.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}</Field>
                  <Field label="Removal policy" help="Immediate needs the attestation and no prerequisites; after-actions needs a prerequisite and no attestation.">
                    {(p) => <Select {...p} value={draft.siteControl.removalPolicy} onChange={(e) => updateSiteControl({ removalPolicy: e.target.value as SiteControlDraft['removalPolicy'] })}>{REMOVAL_POLICIES.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}
                  </Field>
                </div>
                {draft.siteControl.removalPolicy === 'immediate' && (
                  <Choice type="checkbox" checked={draft.siteControl.immediateSafeAttestation} onChange={(e) => updateSiteControl({ immediateSafeAttestation: e.target.checked })} label="Attest this power domain is safe to remove immediately" />
                )}
                {draft.siteControl.removalPolicy === 'after-actions' && (
                  <>
                    {draft.siteControl.prerequisites.map((p, index) => (
                      <div className="sm-night-session-item" key={`prereq-${index}`}>
                        <Field label="Kind">{(field) => <Select {...field} value={p.kind} onChange={(e) => updatePrerequisite(index, { kind: e.target.value as PrerequisiteDraft['kind'] })}>{PREREQUISITE_KINDS.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}</Field>
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
            <div className="sm-night-session-group-head"><h4 className="sm-subsection__title">Interlocks</h4><Button onClick={addInterlock}>Add interlock</Button></div>
            {draft.interlocks.map((item, index) => (
              <div className="sm-night-session-item" key={`interlock-${index}`}>
                <Field label="Name">{(p) => <Input {...p} value={item.name} onChange={(e) => updateInterlock(index, { name: e.target.value })} />}</Field>
                <div className="sm-night-session-compact-grid">
                  <Field label="Phase">{(p) => <Select {...p} value={item.phase} onChange={(e) => updateInterlock(index, { phase: e.target.value as InterlockDraft['phase'] })}>{INTERLOCK_PHASES.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}</Field>
                  <Field label="Posture" help="Block requires on-unavailable and override policy.">
                    {(p) => <Select {...p} value={item.posture} onChange={(e) => updateInterlock(index, { posture: e.target.value as InterlockDraft['posture'] })}>{INTERLOCK_POSTURES.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}
                  </Field>
                  {item.posture !== 'disabled' && (
                    <>
                      <Field label="Signal action" help="Must name a show.action with an mqtt target and a response expectation.">
                        {(p) => <Input {...p} value={item.signal} onChange={(e) => updateInterlock(index, { signal: e.target.value })} />}
                      </Field>
                      <Field label="Freshness (s)" help="Evidence older than this is treated as unavailable.">
                        {(p) => <Input {...p} type="number" min="0" step="1" value={item.freshnessSeconds} onChange={(e) => updateInterlock(index, { freshnessSeconds: e.target.value })} />}
                      </Field>
                      <Field label="Failure text">{(p) => <Input {...p} value={item.failureText} onChange={(e) => updateInterlock(index, { failureText: e.target.value })} />}</Field>
                      {item.posture === 'block' && (
                        <>
                          <Field label="On unavailable">{(p) => <Select {...p} value={item.onUnavailable} onChange={(e) => updateInterlock(index, { onUnavailable: e.target.value as InterlockDraft['onUnavailable'] })}>{ON_UNAVAILABLE_OPTIONS.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}</Field>
                          <Field label="Override policy">{(p) => <Select {...p} value={item.overridePolicy} onChange={(e) => updateInterlock(index, { overridePolicy: e.target.value as InterlockDraft['overridePolicy'] })}>{OVERRIDE_POLICIES.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}</Field>
                        </>
                      )}
                    </>
                  )}
                </div>
                <Button variant="quiet" onClick={() => removeInterlock(index)}>Remove interlock</Button>
              </div>
            ))}
          </div>
          {error !== null && <RuledStrip absence="failed" label="Definition failed" fact={error} />}
          {loaded !== null && <RevisionHistory mode="list" id="sn-definition-revisions" fetch={() => getNightSessionConfigRevisions(loaded.id)} reloadKey={reloadKey} onSelect={(item) => getNightSessionConfigRevision(loaded.id, item.revision).then(setRevision).catch((err: unknown) => setError(describeApiError(err)))} />}
          {revision !== null && <DefinitionStrip items={[{ term: 'Viewing revision', value: <span className="sm-data">{revision.revision}</span> }, { term: 'Label', value: revision.payload.label }]} />}
          <div className="sm-inspector__actions"><span className="sm-small sm-muted">{loaded === null ? 'Creates revision 1' : `Creates revision ${loaded.revision + 1}`}</span><div className="sm-btn-row"><Button variant="quiet" onClick={closeInspector}>Cancel</Button><Button variant="primary" disabled={!gate.allowed || saving} title={gate.allowed ? undefined : gate.reason} onClick={save}>{saving ? 'Saving…' : loaded === null ? 'Create definition' : 'Save definition'}</Button></div></div>
        </div>}
      </aside>
    </Panes>
  </div>
}

function TransitionStepEditor({ title, steps, actions, onChange, onAdd, onRemove }: { title: string; steps: CueDraft[]; actions: ShowActionConfigResponse[]; onChange: (index: number, patch: Partial<CueDraft>) => void; onAdd: () => void; onRemove: (index: number) => void }) {
  return <div className="sm-inspector__group"><div className="sm-night-session-group-head"><div><h4 className="sm-subsection__title">{title}</h4><p className="sm-small sm-muted">{steps.length} {steps.length === 1 ? 'step' : 'steps'}</p></div><Button onClick={onAdd}>Add step</Button></div>{steps.map((step, index) => <div className="sm-night-session-item" key={index}><Field label="Name">{(p) => <Input {...p} value={step.name} onChange={(e) => onChange(index, { name: e.target.value })} />}</Field><div className="sm-night-session-compact-grid"><Field label="Role">{(p) => <Select {...p} value={step.role} onChange={(e) => onChange(index, { role: e.target.value as CueDraft['role'] })}>{CUE_ROLES.map((v) => <option key={v}>{v}</option>)}</Select>}</Field><Field label="Action">{(p) => <Select {...p} value={step.action} onChange={(e) => onChange(index, { action: e.target.value })}><option value="">Select an action…</option>{actions.map((action) => <option key={action.id} value={action.id}>{action.payload.label} · {action.id}</option>)}</Select>}</Field><Field label="Offset (ms)">{(p) => <Input {...p} type="number" step="1" value={step.offsetMs} onChange={(e) => onChange(index, { offsetMs: e.target.value })} />}</Field><Field label="Fade (ms)">{(p) => <Input {...p} type="number" min="0" step="1" value={step.fadeDurationMs} onChange={(e) => onChange(index, { fadeDurationMs: e.target.value })} />}</Field><Field label="On failure">{(p) => <Select {...p} value={step.onFailure} onChange={(e) => onChange(index, { onFailure: e.target.value as CueDraft['onFailure'] })}><option>continue</option><option>abort</option></Select>}</Field>{step.role === 'announcement' && <Field label="Announcement policy">{(p) => <Select {...p} value={step.announcementPolicy} onChange={(e) => onChange(index, { announcementPolicy: e.target.value as CueDraft['announcementPolicy'] })}><option value="">Use default</option><option>duck</option><option>mix</option><option>interrupt</option></Select>}</Field>}</div><Choice type="checkbox" checked={step.barrier} onChange={(e) => onChange(index, { barrier: e.target.checked })} label="Wait for this action before continuing" /><Button variant="quiet" onClick={() => onRemove(index)}>Remove step</Button></div>)}</div>
}
