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
  listConfigObjects,
  putNightSessionActiveConfig,
  putNightSessionConfig,
  randomUUIDv4,
  type ConfigObjectSummary,
  type ConfigNightSessionWrite,
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
  DefinitionStrip,
  Field,
  FieldGrid,
  Input,
  RevisionHistory,
  RuledStrip,
  Section,
  Select,
  StatusPair,
  Table,
  TableWrap,
} from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { guardedSave, type SaveOutcome } from '../domain/save'
import { effectiveServerTimeIso, formatClock } from '../domain/time'
import { formatPosition, type CommandOutcome } from './liveControlModel'
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

  const send = useCallback(
    (command: NightCommandName, interlockOverrides?: readonly NightInterlockOverride[]) => {
      const idempotencyKey = command === 'prepare-site' ? prepareSiteKey : undefined
      dispatchNightCommand(command, idempotencyKey, interlockOverrides)
        .then((response) => {
          setWithheld(null)
          setOverrideRule('')
          setOverrideReason('')
          setOverrideInvalid(false)
          if (command === 'prepare-site') setPrepareSiteKey(randomUUIDv4())
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
    [prepareSiteKey],
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
          <p className="sm-page__lede">
            FPP owns the schedule, playlist selection, and progression. ShowMesh advances the transitions between shows
            and records what it observed.
          </p>
        </div>
        <ButtonRow>
          {session.armedShowId !== '' ? (
            <Link className="sm-btn" to={`/shows/${encodeURIComponent(session.armedShowId)}/night-session`}>Edit definition</Link>
          ) : (
            <Link className="sm-btn" to="/shows">Edit definition</Link>
          )}
          <Button disabled={!gate.allowed} title={gate.allowed ? undefined : gate.reason} onClick={() => send('run-readiness')}>
            Run readiness
          </Button>
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
        <div className="sm-grid sm-grid--auto sm-lifecycle-commands">
          {(
            [
              ['prepare-site', 'Prepare site', 'Opens a preparation epoch. Readiness and start-preshow both need one.'],
              ['start-preshow', 'Start preshow', 'Enters preshow from a prepared, ready session.'],
              ['start-night', 'Start night', 'Commits the armed show and starts the first cycle.'],
              ['request-final-show', 'Request final show', 'The next normally timed show becomes the last. Admission closes; this show finishes.'],
              ['fade-out-night', 'Fade out night', 'Arriving mid-show makes this show final and the fade waits for it to finish.'],
              ['power-down-presentation', 'Power down presentation', 'The terminal intent. An interlock can withhold it.'],
              ['end-session', 'End session', 'Abandons the session. Never withheld by an interlock.'],
            ] as const
          ).map(([command, label, detail]) => (
            <div key={command} className={`sm-lifecycle-command sm-lifecycle-command--${command}`}>
              <Button size="gloved" disabled={!gate.allowed} title={gate.allowed ? undefined : gate.reason} onClick={() => send(command)}>
                {label}
              </Button>
              <p className="sm-small sm-muted">{gate.allowed ? detail : gate.reason}</p>
            </div>
          ))}
        </div>
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
              <Table>
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
              response and reports that on every run, by design.
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
                      <Table>
                        <thead>
                          <tr>
                            <th scope="col">When</th>
                            <th scope="col">Sequence</th>
                            <th scope="col">Cue</th>
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

type CueDraft = { name: string; role: 'lighting' | 'projection' | 'audio' | 'announcement' | 'other'; action: string; offsetMs: string }
type DefinitionDraft = {
  id: string
  show: string
  label: string
  showFpp: string
  showPlaylist: string
  restingFpp: string
  restingPlaylist: string
  timelineShow: string
  timelineSequence: string
  timelineTarget: string
  enterShow: CueDraft[]
  enterResting: CueDraft[]
  base: ConfigNightSessionWrite | null
}

const blankCue = (): CueDraft => ({ name: '', role: 'lighting', action: '', offsetMs: '0' })
const blankDefinition = (show = ''): DefinitionDraft => ({ id: '', show, label: '', showFpp: '', showPlaylist: '', restingFpp: '', restingPlaylist: '', timelineShow: show, timelineSequence: '', timelineTarget: '', enterShow: [], enterResting: [], base: null })

function draftFromDefinition(response: NightSessionConfigResponse): DefinitionDraft {
  const { payload } = response
  const cue = (item: (typeof payload.enterShow.cues)[number]): CueDraft => ({ name: item.name, role: item.role, action: item.action, offsetMs: String(item.offsetMs) })
  return {
    id: response.id, show: payload.show, label: payload.label,
    showFpp: payload.showPlaylist.fppInstanceId, showPlaylist: payload.showPlaylist.playlist,
    restingFpp: payload.resting.fppInstanceId, restingPlaylist: payload.resting.playlist,
    timelineShow: payload.resting.timelineAsset.show, timelineSequence: payload.resting.timelineAsset.sequence, timelineTarget: payload.resting.timelineAsset.target,
    enterShow: payload.enterShow.cues.map(cue), enterResting: payload.enterResting.cues.map(cue), base: payload,
  }
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
      result.push({ name: item.name.trim(), role: item.role, action: item.action.trim(), offsetMs: offset })
    }
    return { ok: true, cues: result }
  }
  const enteringShow = buildCues(draft.enterShow, 'Enter-show')
  if (!enteringShow.ok) return { error: enteringShow.error }
  const enteringResting = buildCues(draft.enterResting, 'Enter-resting')
  if (!enteringResting.ok) return { error: enteringResting.error }
  const base = draft.base
  return {
    ...(base ?? {}), show: draft.show.trim(), label: draft.label.trim(),
    showPlaylist: { ...(base?.showPlaylist ?? {}), fppInstanceId: draft.showFpp.trim(), playlist: draft.showPlaylist.trim() },
    resting: {
      ...(base?.resting ?? {}), fppInstanceId: draft.restingFpp.trim(), playlist: draft.restingPlaylist.trim(),
      timelineAsset: { ...(base?.resting?.timelineAsset ?? {}), show: draft.timelineShow.trim(), sequence: draft.timelineSequence.trim(), target: draft.timelineTarget.trim() },
    },
    enterShow: { ...(base?.enterShow ?? {}), blackoutHoldMs: base?.enterShow?.blackoutHoldMs ?? 0, cues: enteringShow.cues },
    enterResting: { ...(base?.enterResting ?? {}), blackoutAfterShowMs: base?.enterResting?.blackoutAfterShowMs ?? 0, cues: enteringResting.cues },
  }
}

export function NightSessionDefinitions({ showId }: { showId?: string }) {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const [objects, setObjects] = useState<ConfigObjectSummary[] | null>(null)
  const [selected, setSelected] = useState('')
  const [draft, setDraft] = useState<DefinitionDraft>(() => blankDefinition(showId))
  const [loaded, setLoaded] = useState<NightSessionConfigResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [revision, setRevision] = useState<NightSessionConfigResponse | null>(null)
  const [reloadKey, setReloadKey] = useState(0)

  useEffect(() => {
    let cancelled = false
    listConfigObjects('night.session')
      .then((response) => { if (!cancelled) setObjects(showId === undefined ? response.objects : response.objects.filter((object) => object.show === showId)) })
      .catch((err: unknown) => { if (!cancelled) setError(describeApiError(err)) })
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

  return (
    <Section id="sn-definitions" title="Night session definitions" aside={<span className="sm-small sm-muted">Definitions change the next armed night, never the one running now</span>}>
      {objects === null ? <RuledStrip absence="loading" label="Reading" fact="Reading night-session definitions." /> : (
        <>
          <FieldGrid>
            <Field label="Definition">
              {(field) => <Select {...field} value={selected} onChange={(event) => selectDefinition(event.target.value)}><option value="">New definition</option>{objects.map((object) => <option key={object.id} value={object.id}>Edit {object.id}</option>)}</Select>}
            </Field>
            <Field label="Definition id" help={loaded === null ? 'Used to create this new definition.' : 'Definition ids are stable; editing this field creates a separate definition.'}>
              {(field) => <Input {...field} value={draft.id} disabled={loaded !== null} onChange={(event) => setDraft((current) => ({ ...current, id: event.target.value }))} />}
            </Field>
            <Field label="Show">{(field) => <Input {...field} value={draft.show} disabled={showId !== undefined} onChange={(event) => setDraft((current) => ({ ...current, show: event.target.value, timelineShow: current.timelineShow === '' ? event.target.value : current.timelineShow }))} />}</Field>
            <Field label="Label">{(field) => <Input {...field} value={draft.label} onChange={(event) => setDraft((current) => ({ ...current, label: event.target.value }))} />}</Field>
            <Field label="Show playlist FPP instance">{(field) => <Input {...field} value={draft.showFpp} onChange={(event) => setDraft((current) => ({ ...current, showFpp: event.target.value }))} />}</Field>
            <Field label="Show playlist">{(field) => <Input {...field} value={draft.showPlaylist} onChange={(event) => setDraft((current) => ({ ...current, showPlaylist: event.target.value }))} />}</Field>
            <Field label="Resting FPP instance">{(field) => <Input {...field} value={draft.restingFpp} onChange={(event) => setDraft((current) => ({ ...current, restingFpp: event.target.value }))} />}</Field>
            <Field label="Resting playlist">{(field) => <Input {...field} value={draft.restingPlaylist} onChange={(event) => setDraft((current) => ({ ...current, restingPlaylist: event.target.value }))} />}</Field>
            <Field label="Resting timeline sequence">{(field) => <Input {...field} value={draft.timelineSequence} onChange={(event) => setDraft((current) => ({ ...current, timelineSequence: event.target.value }))} />}</Field>
            <Field label="Resting timeline target">{(field) => <Input {...field} value={draft.timelineTarget} onChange={(event) => setDraft((current) => ({ ...current, timelineTarget: event.target.value }))} />}</Field>
          </FieldGrid>
          <TransitionStepEditor title="Enter-show transition steps" steps={draft.enterShow} onChange={(index, patch) => updateCues('enterShow', index, patch)} onAdd={() => addCue('enterShow')} onRemove={(index) => setDraft((current) => ({ ...current, enterShow: current.enterShow.filter((_, itemIndex) => itemIndex !== index) }))} />
          <TransitionStepEditor title="Enter-resting transition steps" steps={draft.enterResting} onChange={(index, patch) => updateCues('enterResting', index, patch)} onAdd={() => addCue('enterResting')} onRemove={(index) => setDraft((current) => ({ ...current, enterResting: current.enterResting.filter((_, itemIndex) => itemIndex !== index) }))} />
          <ButtonRow><Button variant="primary" disabled={!gate.allowed || saving} title={gate.allowed ? undefined : gate.reason} onClick={save}>{saving ? 'Saving…' : loaded === null ? 'Create definition' : 'Save definition'}</Button></ButtonRow>
          {loaded !== null && <RevisionHistory mode="list" id="sn-definition-revisions" fetch={() => getNightSessionConfigRevisions(loaded.id)} reloadKey={reloadKey} onSelect={(item) => getNightSessionConfigRevision(loaded.id, item.revision).then(setRevision).catch((err: unknown) => setError(describeApiError(err)))} />}
          {revision !== null && <DefinitionStrip items={[{ term: 'Viewing revision', value: <span className="sm-data">{revision.revision}</span> }, { term: 'Label', value: revision.payload.label }, { term: 'Show playlist', value: <span className="sm-data">{revision.payload.showPlaylist.playlist}</span> }]} />}
        </>
      )}
      {error !== null && <RuledStrip absence="failed" label="Definition failed" fact={error} />}
    </Section>
  )
}

function TransitionStepEditor({ title, steps, onChange, onAdd, onRemove }: { title: string; steps: CueDraft[]; onChange: (index: number, patch: Partial<CueDraft>) => void; onAdd: () => void; onRemove: (index: number) => void }) {
  return <section className="sm-subsection" aria-label={title}><h3 className="sm-subsection__title">{title}</h3>{steps.map((step, index) => <div className="sm-field-grid sm-stack-2" key={`${index}:${step.name}`}><Field label="Name">{(field) => <Input {...field} value={step.name} onChange={(event) => onChange(index, { name: event.target.value })} />}</Field><Field label="Role">{(field) => <Select {...field} value={step.role} onChange={(event) => onChange(index, { role: event.target.value as CueDraft['role'] })}>{['lighting', 'projection', 'audio', 'announcement', 'other'].map((role) => <option key={role} value={role}>{role}</option>)}</Select>}</Field><Field label="Action">{(field) => <Input {...field} value={step.action} onChange={(event) => onChange(index, { action: event.target.value })} />}</Field><Field label="Offset (ms)">{(field) => <Input {...field} type="number" step="1" value={step.offsetMs} onChange={(event) => onChange(index, { offsetMs: event.target.value })} />}</Field><Button variant="quiet" onClick={() => onRemove(index)}>Remove step</Button></div>)}<Button variant="quiet" onClick={onAdd}>Add transition step</Button></section>
}
