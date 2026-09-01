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
  type ConfigNightSessionCue,
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
  Choice,
  ChoiceRow,
  DefinitionStrip,
  Field,
  FieldGrid,
  Input,
  NotWiredBanner,
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

  return (
    <>
      <div className="sm-page__head">
        <div>
          <p className="sm-eyebrow">Show Night · <span className="sm-data">{session.id}</span></p>
          <h1 className="sm-page__title">Cycle {session.cycle} of the night</h1>
          <p className="sm-page__lede">
            FPP owns the schedule, playlist selection and progression. ShowMesh advances the transitions between shows
            and records what it observed.
          </p>
        </div>
        <ButtonRow>
          <a className="sm-btn" href="#sn-definitions">Edit definition</a>
          <Button disabled={!gate.allowed} title={gate.allowed ? undefined : gate.reason} onClick={() => send('run-readiness')}>
            Run readiness
          </Button>
        </ButtonRow>
      </div>

      {tonight.some((step) => step.status === 'notWired') && (
        <NotWiredBanner
          what="The night timeline"
          missing={<code className="sm-data">GET /night/session/cycles</code>}
          detail="The rail is drawn to its final shape, but this session reports only the cycle it is in, so earlier cycles read as unreported rather than being reconstructed or invented."
        />
      )}

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
              <p className="sm-small sm-muted">
                {formatPosition(state.elapsedSeconds) ?? 'not reported'} / {formatPosition(state.totalSeconds) ?? 'not reported'}
              </p>
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
                  { term: 'Player state', value: state.playerState ?? 'not reported' },
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
              <p className="sm-small sm-muted">
                {armed} {armed === 1 ? 'step' : 'steps'} armed for the boundary.
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
        aside={<Link to="/control">Full transport in Live Control →</Link>}
      >
        <p className="sm-small sm-muted">Accepted, never confirmed here. Each one answers 202.</p>
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
            <div key={command}>
              <Button size="gloved" disabled={!gate.allowed} title={gate.allowed ? undefined : gate.reason} onClick={() => send(command)}>
                {label}
              </Button>
              <p className="sm-small sm-muted">{gate.allowed ? detail : gate.reason}</p>
              {command === 'start-night' && (
                <label className="sm-choice sm-choice--gloved">
                  <input
                    type="checkbox"
                    checked={skipEnterShowLead}
                    disabled={!gate.allowed}
                    onChange={(e) => setSkipEnterShowLead(e.target.checked)}
                  />
                  <span>Late start: skip the enter-show lead. An enter-show announcement cue still dispatches.</span>
                </label>
              )}
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
      <NightSessionDefinitions />

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
  timelineShow: string
  timelineSequence: string
  timelineTarget: string
  endOfNightPlaylist: string
  endOfNightRepeat: boolean
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
const blankBackgroundAudioItem = (): BackgroundAudioItemDraft => ({ itemId: '', show: '', sequence: '', target: '' })
const blankBackgroundAudio = (): BackgroundAudioDraft => ({ enabled: false, items: [], repeat: 'none', resume: 'resume', itemTransition: 'sequential', crossfadeMs: '', maxGainDb: '', fadeOutMs: '', fadeInMs: '' })
const blankPrerequisite = (): PrerequisiteDraft => ({ kind: 'action', action: '', requireConfirmation: false, delayMs: '' })
const blankSiteControl = (): SiteControlDraft => ({
  requestThermalProfile: '',
  powerOnEnabled: false, powerOnAction: '', powerOnDomain: 'presentation', powerOnProvenance: 'operator-declared',
  powerOffEnabled: false, powerOffAction: '', powerOffDomain: 'presentation', powerOffProvenance: 'operator-declared',
  removalPolicy: 'immediate', immediateSafeAttestation: false, prerequisites: [],
})
const blankInterlock = (): InterlockDraft => ({ name: '', phase: 'prepare-site', posture: 'observe', signal: '', freshnessSeconds: '', failureText: '', onUnavailable: 'block', overridePolicy: 'none' })
const blankDefinition = (): DefinitionDraft => ({
  id: '', show: '', label: '', showFpp: '', showPlaylist: '', restingFpp: '', restingPlaylist: '',
  timelineShow: '', timelineSequence: '', timelineTarget: '', endOfNightPlaylist: '', endOfNightRepeat: false,
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
    restingFpp: payload.resting.fppInstanceId, restingPlaylist: payload.resting.playlist,
    timelineShow: payload.resting.timelineAsset.show, timelineSequence: payload.resting.timelineAsset.sequence, timelineTarget: payload.resting.timelineAsset.target,
    endOfNightPlaylist: payload.resting.endOfNightPlaylist, endOfNightRepeat: payload.resting.endOfNightRepeat,
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

type AudioNodesState = { kind: 'loading' } | { kind: 'loaded'; nodes: ConfigObjectSummary[] } | { kind: 'failed'; reason: string }

function NightSessionDefinitions() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const [objects, setObjects] = useState<ConfigObjectSummary[] | null>(null)
  const [audioNodes, setAudioNodes] = useState<AudioNodesState>({ kind: 'loading' })
  const [selected, setSelected] = useState('')
  const [draft, setDraft] = useState<DefinitionDraft>(blankDefinition)
  const [loaded, setLoaded] = useState<NightSessionConfigResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [revision, setRevision] = useState<NightSessionConfigResponse | null>(null)
  const [reloadKey, setReloadKey] = useState(0)

  useEffect(() => {
    let cancelled = false
    listConfigObjects('night.session')
      .then((response) => { if (!cancelled) setObjects(response.objects) })
      .catch((err: unknown) => { if (!cancelled) setError(describeApiError(err)) })
    return () => { cancelled = true }
  }, [reloadKey])

  useEffect(() => {
    let cancelled = false
    listConfigObjects('audio.node')
      .then((response) => { if (!cancelled) setAudioNodes({ kind: 'loaded', nodes: response.objects }) })
      .catch((err: unknown) => { if (!cancelled) setAudioNodes({ kind: 'failed', reason: describeApiError(err) }) })
    return () => { cancelled = true }
  }, [])

  const selectDefinition = (id: string) => {
    setSelected(id); setError(null); setRevision(null)
    if (id === '') { setLoaded(null); setDraft(blankDefinition()); return }
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
            <Field label="Show">{(field) => <Input {...field} value={draft.show} onChange={(event) => setDraft((current) => ({ ...current, show: event.target.value, timelineShow: current.timelineShow === '' ? event.target.value : current.timelineShow }))} />}</Field>
            <Field label="Label">{(field) => <Input {...field} value={draft.label} onChange={(event) => setDraft((current) => ({ ...current, label: event.target.value }))} />}</Field>
            <Field label="Show playlist FPP instance">{(field) => <Input {...field} value={draft.showFpp} onChange={(event) => setDraft((current) => ({ ...current, showFpp: event.target.value }))} />}</Field>
            <Field label="Show playlist">{(field) => <Input {...field} value={draft.showPlaylist} onChange={(event) => setDraft((current) => ({ ...current, showPlaylist: event.target.value }))} />}</Field>
            <Field label="Resting FPP instance">{(field) => <Input {...field} value={draft.restingFpp} onChange={(event) => setDraft((current) => ({ ...current, restingFpp: event.target.value }))} />}</Field>
            <Field label="Resting playlist">{(field) => <Input {...field} value={draft.restingPlaylist} onChange={(event) => setDraft((current) => ({ ...current, restingPlaylist: event.target.value }))} />}</Field>
            <Field label="Resting timeline sequence">{(field) => <Input {...field} value={draft.timelineSequence} onChange={(event) => setDraft((current) => ({ ...current, timelineSequence: event.target.value }))} />}</Field>
            <Field label="Resting timeline target">{(field) => <Input {...field} value={draft.timelineTarget} onChange={(event) => setDraft((current) => ({ ...current, timelineTarget: event.target.value }))} />}</Field>
            <Field label="Announcement default policy" help="Applies to any announcement-role cue that does not name its own announcement policy.">
              {(field) => <Select {...field} value={draft.announcementDefaultPolicy} onChange={(event) => setDraft((current) => ({ ...current, announcementDefaultPolicy: event.target.value as DefinitionDraft['announcementDefaultPolicy'] }))}>{ANNOUNCEMENT_POLICIES.map((policy) => <option key={policy} value={policy}>{policy}</option>)}</Select>}
            </Field>
          </FieldGrid>

          <section className="sm-subsection" aria-label="Resting">
            <h3 className="sm-subsection__title">Resting</h3>
            <FieldGrid>
              <Field label="End-of-night playlist" help="Defaults to the resting playlist above when left blank.">
                {(field) => <Input {...field} value={draft.endOfNightPlaylist} onChange={(event) => setDraft((current) => ({ ...current, endOfNightPlaylist: event.target.value }))} />}
              </Field>
            </FieldGrid>
            <ChoiceRow><Choice type="checkbox" label="Repeat the end-of-night playlist" checked={draft.endOfNightRepeat} onChange={(event) => setDraft((current) => ({ ...current, endOfNightRepeat: event.target.checked }))} /></ChoiceRow>

            <section className="sm-subsection" aria-label="Background audio">
              <h3 className="sm-subsection__title">Background audio</h3>
              <ChoiceRow><Choice type="checkbox" label="Configure background audio for resting" checked={draft.backgroundAudio.enabled} onChange={(event) => updateBackgroundAudio({ enabled: event.target.checked })} /></ChoiceRow>
              {draft.backgroundAudio.enabled && (
                <>
                  <FieldGrid>
                    <Field label="Repeat">{(field) => <Select {...field} value={draft.backgroundAudio.repeat} onChange={(event) => updateBackgroundAudio({ repeat: event.target.value as BackgroundAudioDraft['repeat'] })}>{BACKGROUND_AUDIO_REPEATS.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}</Field>
                    <Field label="Resume policy" help="How the bed resumes after a show returns to resting.">
                      {(field) => <Select {...field} value={draft.backgroundAudio.resume} onChange={(event) => updateBackgroundAudio({ resume: event.target.value as BackgroundAudioDraft['resume'] })}>{BACKGROUND_AUDIO_RESUMES.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}
                    </Field>
                    <Field label="Item transition">
                      {(field) => <Select {...field} value={draft.backgroundAudio.itemTransition} onChange={(event) => updateBackgroundAudio({ itemTransition: event.target.value as BackgroundAudioDraft['itemTransition'] })}>{BACKGROUND_AUDIO_TRANSITIONS.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}
                    </Field>
                    {draft.backgroundAudio.itemTransition === 'crossfade' && (
                      <Field label="Crossfade (ms)" help="Required when item transition is crossfade.">
                        {(field) => <Input {...field} type="number" step="1" value={draft.backgroundAudio.crossfadeMs} onChange={(event) => updateBackgroundAudio({ crossfadeMs: event.target.value })} />}
                      </Field>
                    )}
                    <Field label="Max gain (dB)" help="Must be 0 dB or lower.">
                      {(field) => <Input {...field} type="number" step="0.1" value={draft.backgroundAudio.maxGainDb} onChange={(event) => updateBackgroundAudio({ maxGainDb: event.target.value })} />}
                    </Field>
                    <Field label="Fade-out (ms)" help="Fades the bed to silence before a show. Fade-out and fade-in must be set together, or both left blank for an instant cut.">
                      {(field) => <Input {...field} type="number" step="1" value={draft.backgroundAudio.fadeOutMs} onChange={(event) => updateBackgroundAudio({ fadeOutMs: event.target.value })} />}
                    </Field>
                    <Field label="Fade-in (ms)" help="Fades the bed back up to the max gain after resting returns.">
                      {(field) => <Input {...field} type="number" step="1" value={draft.backgroundAudio.fadeInMs} onChange={(event) => updateBackgroundAudio({ fadeInMs: event.target.value })} />}
                    </Field>
                  </FieldGrid>
                  {draft.backgroundAudio.items.map((item, index) => (
                    <div className="sm-field-grid sm-stack-2" key={`bg-item-${index}`}>
                      <Field label="Item id">{(field) => <Input {...field} value={item.itemId} onChange={(event) => updateBackgroundAudioItem(index, { itemId: event.target.value })} />}</Field>
                      <Field label="Asset show">{(field) => <Input {...field} value={item.show} onChange={(event) => updateBackgroundAudioItem(index, { show: event.target.value })} />}</Field>
                      <Field label="Asset sequence">{(field) => <Input {...field} value={item.sequence} onChange={(event) => updateBackgroundAudioItem(index, { sequence: event.target.value })} />}</Field>
                      <Field label="Target audio node">
                        {(field) =>
                          audioNodes.kind === 'loading' ? (
                            <RuledStrip absence="loading" label="Reading" fact="Fetching this deployment's declared audio nodes." />
                          ) : audioNodes.kind === 'failed' ? (
                            <RuledStrip absence="failed" label="Read failed" fact={audioNodes.reason} />
                          ) : (
                            <Select {...field} value={item.target} onChange={(event) => updateBackgroundAudioItem(index, { target: event.target.value })}>
                              <option value="">Choose a node…</option>
                              {audioNodes.nodes.map((node) => <option key={node.id} value={node.id}>{node.label} ({node.id})</option>)}
                            </Select>
                          )
                        }
                      </Field>
                      <Button variant="quiet" onClick={() => removeBackgroundAudioItem(index)}>Remove item</Button>
                    </div>
                  ))}
                  <Button variant="quiet" onClick={addBackgroundAudioItem}>Add background audio item</Button>
                </>
              )}
            </section>
          </section>

          <section className="sm-subsection" aria-label="Site control">
            <h3 className="sm-subsection__title">Site control</h3>
            <FieldGrid>
              <Field label="Request thermal profile" help="Names the show.action this deployment runs to request a thermal profile.">
                {(field) => <Input {...field} value={draft.siteControl.requestThermalProfile} onChange={(event) => updateSiteControl({ requestThermalProfile: event.target.value })} />}
              </Field>
            </FieldGrid>
            <section className="sm-subsection" aria-label="Presentation power on">
              <h3 className="sm-subsection__title">Presentation power-on</h3>
              <ChoiceRow><Choice type="checkbox" label="Configure a presentation power-on binding" checked={draft.siteControl.powerOnEnabled} onChange={(event) => updateSiteControl({ powerOnEnabled: event.target.checked })} /></ChoiceRow>
              {draft.siteControl.powerOnEnabled && (
                <FieldGrid>
                  <Field label="Action">{(field) => <Input {...field} value={draft.siteControl.powerOnAction} onChange={(event) => updateSiteControl({ powerOnAction: event.target.value })} />}</Field>
                  <Field label="Power domain">{(field) => <Select {...field} value={draft.siteControl.powerOnDomain} onChange={(event) => updateSiteControl({ powerOnDomain: event.target.value as SiteControlDraft['powerOnDomain'] })}>{POWER_DOMAINS.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}</Field>
                  <Field label="Domain provenance" help="The coordinator refuses provider for this binding: no control provider can authoritatively identify a power binding's physical targets, so operator-declared is the only accepted value.">
                    {(field) => <Select {...field} value={draft.siteControl.powerOnProvenance} onChange={(event) => updateSiteControl({ powerOnProvenance: event.target.value as SiteControlDraft['powerOnProvenance'] })}>{DOMAIN_PROVENANCES.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}
                  </Field>
                </FieldGrid>
              )}
            </section>
            <section className="sm-subsection" aria-label="Presentation power off">
              <h3 className="sm-subsection__title">Presentation power-off</h3>
              <ChoiceRow><Choice type="checkbox" label="Configure a presentation power-off binding" checked={draft.siteControl.powerOffEnabled} onChange={(event) => updateSiteControl({ powerOffEnabled: event.target.checked })} /></ChoiceRow>
              {draft.siteControl.powerOffEnabled && (
                <>
                  <FieldGrid>
                    <Field label="Action">{(field) => <Input {...field} value={draft.siteControl.powerOffAction} onChange={(event) => updateSiteControl({ powerOffAction: event.target.value })} />}</Field>
                    <Field label="Power domain">{(field) => <Select {...field} value={draft.siteControl.powerOffDomain} onChange={(event) => updateSiteControl({ powerOffDomain: event.target.value as SiteControlDraft['powerOffDomain'] })}>{POWER_DOMAINS.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}</Field>
                    <Field label="Domain provenance">{(field) => <Select {...field} value={draft.siteControl.powerOffProvenance} onChange={(event) => updateSiteControl({ powerOffProvenance: event.target.value as SiteControlDraft['powerOffProvenance'] })}>{DOMAIN_PROVENANCES.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}</Field>
                    <Field label="Removal policy" help="Immediate requires the safe-to-remove attestation and no prerequisites; after-actions requires at least one prerequisite and forbids the attestation.">
                      {(field) => <Select {...field} value={draft.siteControl.removalPolicy} onChange={(event) => updateSiteControl({ removalPolicy: event.target.value as SiteControlDraft['removalPolicy'] })}>{REMOVAL_POLICIES.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}
                    </Field>
                  </FieldGrid>
                  {draft.siteControl.removalPolicy === 'immediate' && (
                    <ChoiceRow><Choice type="checkbox" label="Attest this power domain is safe to remove immediately" checked={draft.siteControl.immediateSafeAttestation} onChange={(event) => updateSiteControl({ immediateSafeAttestation: event.target.checked })} /></ChoiceRow>
                  )}
                  {draft.siteControl.removalPolicy === 'after-actions' && (
                    <>
                      {draft.siteControl.prerequisites.map((p, index) => (
                        <div className="sm-field-grid sm-stack-2" key={`prereq-${index}`}>
                          <Field label="Kind">{(field) => <Select {...field} value={p.kind} onChange={(event) => updatePrerequisite(index, { kind: event.target.value as PrerequisiteDraft['kind'] })}>{PREREQUISITE_KINDS.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}</Field>
                          {p.kind === 'delay' ? (
                            <Field label="Delay (ms)">{(field) => <Input {...field} type="number" step="1" value={p.delayMs} onChange={(event) => updatePrerequisite(index, { delayMs: event.target.value })} />}</Field>
                          ) : (
                            <>
                              <Field label="Action">{(field) => <Input {...field} value={p.action} onChange={(event) => updatePrerequisite(index, { action: event.target.value })} />}</Field>
                              {p.kind === 'action' && (
                                <ChoiceRow><Choice type="checkbox" label="Require confirmation" checked={p.requireConfirmation} onChange={(event) => updatePrerequisite(index, { requireConfirmation: event.target.checked })} /></ChoiceRow>
                              )}
                            </>
                          )}
                          <Button variant="quiet" onClick={() => removePrerequisite(index)}>Remove prerequisite</Button>
                        </div>
                      ))}
                      <Button variant="quiet" onClick={addPrerequisite}>Add prerequisite</Button>
                    </>
                  )}
                </>
              )}
            </section>
          </section>

          <section className="sm-subsection" aria-label="Interlocks">
            <h3 className="sm-subsection__title">Interlocks</h3>
            {draft.interlocks.map((item, index) => (
              <div className="sm-field-grid sm-stack-2" key={`interlock-${index}`}>
                <Field label="Name">{(field) => <Input {...field} value={item.name} onChange={(event) => updateInterlock(index, { name: event.target.value })} />}</Field>
                <Field label="Phase">{(field) => <Select {...field} value={item.phase} onChange={(event) => updateInterlock(index, { phase: event.target.value as InterlockDraft['phase'] })}>{INTERLOCK_PHASES.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}</Field>
                <Field label="Posture" help="A disabled entry carries only name, phase, and posture. An observe entry must not set on-unavailable or override policy; a block entry requires both.">
                  {(field) => <Select {...field} value={item.posture} onChange={(event) => updateInterlock(index, { posture: event.target.value as InterlockDraft['posture'] })}>{INTERLOCK_POSTURES.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}
                </Field>
                {item.posture !== 'disabled' && (
                  <>
                    <Field label="Signal action" help="Must name a show.action that declares an mqtt target with a response expectation.">
                      {(field) => <Input {...field} value={item.signal} onChange={(event) => updateInterlock(index, { signal: event.target.value })} />}
                    </Field>
                    <Field label="Freshness (s)" help="Bounds how old the evidence this rule consults may be before it is treated as unavailable.">
                      {(field) => <Input {...field} type="number" step="1" value={item.freshnessSeconds} onChange={(event) => updateInterlock(index, { freshnessSeconds: event.target.value })} />}
                    </Field>
                    <Field label="Failure text">{(field) => <Input {...field} value={item.failureText} onChange={(event) => updateInterlock(index, { failureText: event.target.value })} />}</Field>
                    {item.posture === 'block' && (
                      <>
                        <Field label="On unavailable">{(field) => <Select {...field} value={item.onUnavailable} onChange={(event) => updateInterlock(index, { onUnavailable: event.target.value as InterlockDraft['onUnavailable'] })}>{ON_UNAVAILABLE_OPTIONS.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}</Field>
                        <Field label="Override policy">{(field) => <Select {...field} value={item.overridePolicy} onChange={(event) => updateInterlock(index, { overridePolicy: event.target.value as InterlockDraft['overridePolicy'] })}>{OVERRIDE_POLICIES.map((option) => <option key={option} value={option}>{option}</option>)}</Select>}</Field>
                      </>
                    )}
                  </>
                )}
                <Button variant="quiet" onClick={() => removeInterlock(index)}>Remove interlock</Button>
              </div>
            ))}
            <Button variant="quiet" onClick={addInterlock}>Add interlock</Button>
          </section>

          <FieldGrid>
            <Field label="Blackout hold (ms)" help="How long enter-show holds blackout before its cues fire.">
              {(field) => <Input {...field} type="number" step="1" value={draft.blackoutHoldMs} onChange={(event) => setDraft((current) => ({ ...current, blackoutHoldMs: event.target.value }))} />}
            </Field>
          </FieldGrid>
          <TransitionStepEditor title="Enter-show transition steps" steps={draft.enterShow} onChange={(index, patch) => updateCues('enterShow', index, patch)} onAdd={() => addCue('enterShow')} onRemove={(index) => setDraft((current) => ({ ...current, enterShow: current.enterShow.filter((_, itemIndex) => itemIndex !== index) }))} />
          <FieldGrid>
            <Field label="Blackout after show (ms)" help="How long enter-resting holds blackout after the show ends.">
              {(field) => <Input {...field} type="number" step="1" value={draft.blackoutAfterShowMs} onChange={(event) => setDraft((current) => ({ ...current, blackoutAfterShowMs: event.target.value }))} />}
            </Field>
          </FieldGrid>
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
  return (
    <section className="sm-subsection" aria-label={title}>
      <h3 className="sm-subsection__title">{title}</h3>
      {steps.map((step, index) => (
        <div className="sm-field-grid sm-stack-2" key={`${index}:${step.name}`}>
          <Field label="Name">{(field) => <Input {...field} value={step.name} onChange={(event) => onChange(index, { name: event.target.value })} />}</Field>
          <Field label="Role">
            {(field) => <Select {...field} value={step.role} onChange={(event) => onChange(index, { role: event.target.value as CueDraft['role'] })}>{CUE_ROLES.map((role) => <option key={role} value={role}>{role}</option>)}</Select>}
          </Field>
          <Field label="Action">{(field) => <Input {...field} value={step.action} onChange={(event) => onChange(index, { action: event.target.value })} />}</Field>
          <Field label="Offset (ms)">{(field) => <Input {...field} type="number" step="1" value={step.offsetMs} onChange={(event) => onChange(index, { offsetMs: event.target.value })} />}</Field>
          <Field label="Fade duration (ms)">{(field) => <Input {...field} type="number" step="1" value={step.fadeDurationMs} onChange={(event) => onChange(index, { fadeDurationMs: event.target.value })} />}</Field>
          <div className="sm-field">
            <ChoiceRow><Choice type="checkbox" label="Barrier" checked={step.barrier} onChange={(event) => onChange(index, { barrier: event.target.checked })} /></ChoiceRow>
            <span className="sm-field__help">A barrier step blocks later steps until it resolves.</span>
          </div>
          <Field label="On failure" help="Defaults to continue when absent.">
            {(field) => <Select {...field} value={step.onFailure} onChange={(event) => onChange(index, { onFailure: event.target.value as CueDraft['onFailure'] })}><option value="continue">continue</option><option value="abort">abort</option></Select>}
          </Field>
          {step.role === 'announcement' && (
            <Field label="Announcement policy" help="Absent means the session's own announcement default policy applies. interrupt uses background audio's resume policy on the way back.">
              {(field) => (
                <Select {...field} value={step.announcementPolicy} onChange={(event) => onChange(index, { announcementPolicy: event.target.value as CueDraft['announcementPolicy'] })}>
                  <option value="">(use session default)</option>
                  {ANNOUNCEMENT_POLICIES.map((policy) => <option key={policy} value={policy}>{policy}</option>)}
                </Select>
              )}
            </Field>
          )}
          <Button variant="quiet" onClick={() => onRemove(index)}>Remove step</Button>
        </div>
      ))}
      <Button variant="quiet" onClick={onAdd}>Add transition step</Button>
    </section>
  )
}
