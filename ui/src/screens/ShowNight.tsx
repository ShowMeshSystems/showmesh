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
  Panes,
  RevisionHistory,
  RuledStrip,
  Section,
  Select,
  SelectableRow,
  StatusPair,
  Table,
  TableWrap,
  Textarea,
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

type CueDraft = {
  name: string
  role: 'lighting' | 'projection' | 'audio' | 'announcement' | 'other'
  action: string
  offsetMs: string
  fadeDurationMs: string
  barrier: boolean
  onFailure: 'continue' | 'abort'
  announcementPolicy: '' | 'duck' | 'mix' | 'interrupt'
}
type BackgroundItemDraft = { itemId: string; show: string; sequence: string; target: string }
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
  enterShow: CueDraft[]
  enterResting: CueDraft[]
  blackoutHoldMs: string
  blackoutAfterShowMs: string
  announcementDefaultPolicy: 'duck' | 'mix' | 'interrupt'
  backgroundEnabled: boolean
  backgroundItems: BackgroundItemDraft[]
  backgroundRepeat: 'none' | 'item' | 'playlist'
  backgroundResume: 'resume' | 'restart'
  backgroundTransition: 'sequential' | 'gapless' | 'crossfade'
  backgroundCrossfadeMs: string
  backgroundMaxGainDb: string
  siteControlJson: string
  interlocksJson: string
  base: ConfigNightSessionWrite | null
}

const blankCue = (): CueDraft => ({ name: '', role: 'lighting', action: '', offsetMs: '0', fadeDurationMs: '', barrier: false, onFailure: 'continue', announcementPolicy: '' })
const blankBackgroundItem = (show = ''): BackgroundItemDraft => ({ itemId: '', show, sequence: '', target: '' })
const blankDefinition = (show = ''): DefinitionDraft => ({
  id: '', show, label: '', showFpp: '', showPlaylist: '', restingFpp: '', restingPlaylist: '', endOfNightPlaylist: '', endOfNightRepeat: false,
  timelineShow: show, timelineSequence: '', timelineTarget: '', enterShow: [], enterResting: [], blackoutHoldMs: '0', blackoutAfterShowMs: '0',
  announcementDefaultPolicy: 'duck', backgroundEnabled: false, backgroundItems: [], backgroundRepeat: 'none', backgroundResume: 'resume',
  backgroundTransition: 'sequential', backgroundCrossfadeMs: '', backgroundMaxGainDb: '0', siteControlJson: '', interlocksJson: '', base: null,
})

function draftFromDefinition(response: NightSessionConfigResponse): DefinitionDraft {
  const { payload } = response
  const cue = (item: (typeof payload.enterShow.cues)[number]): CueDraft => ({
    name: item.name, role: item.role, action: item.action, offsetMs: String(item.offsetMs), fadeDurationMs: item.fadeDurationMs === undefined ? '' : String(item.fadeDurationMs),
    barrier: item.barrier, onFailure: item.onFailure, announcementPolicy: item.announcementPolicy ?? '',
  })
  const background = payload.resting.backgroundAudio
  return {
    id: response.id, show: payload.show, label: payload.label,
    showFpp: payload.showPlaylist.fppInstanceId, showPlaylist: payload.showPlaylist.playlist,
    restingFpp: payload.resting.fppInstanceId, restingPlaylist: payload.resting.playlist, endOfNightPlaylist: payload.resting.endOfNightPlaylist, endOfNightRepeat: payload.resting.endOfNightRepeat,
    timelineShow: payload.resting.timelineAsset.show, timelineSequence: payload.resting.timelineAsset.sequence, timelineTarget: payload.resting.timelineAsset.target,
    enterShow: payload.enterShow.cues.map(cue), enterResting: payload.enterResting.cues.map(cue), blackoutHoldMs: String(payload.enterShow.blackoutHoldMs), blackoutAfterShowMs: String(payload.enterResting.blackoutAfterShowMs),
    announcementDefaultPolicy: payload.announcementDefaultPolicy, backgroundEnabled: background !== undefined, backgroundItems: background?.items.map((item) => ({ ...item })) ?? [],
    backgroundRepeat: background?.repeat ?? 'none', backgroundResume: background?.resume ?? 'resume', backgroundTransition: background?.itemTransition ?? 'sequential',
    backgroundCrossfadeMs: background?.crossfadeMs === undefined ? '' : String(background.crossfadeMs), backgroundMaxGainDb: String(background?.maxGainDb ?? 0),
    siteControlJson: payload.siteControl === undefined ? '' : JSON.stringify(payload.siteControl, null, 2), interlocksJson: payload.interlocks === undefined ? '' : JSON.stringify(payload.interlocks, null, 2), base: payload,
  }
}

function integer(value: string, label: string, minimum?: number): number | { error: string } {
  const parsed = Number(value)
  if (!Number.isInteger(parsed) || (minimum !== undefined && parsed < minimum)) return { error: `${label} must be a whole number${minimum === undefined ? '' : ` of at least ${minimum}`}.` }
  return parsed
}

function optionalJson<T>(value: string, label: string): T | undefined | { error: string } {
  if (value.trim() === '') return undefined
  try { return JSON.parse(value) as T } catch { return { error: `${label} must be valid JSON.` } }
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
      const fade = item.fadeDurationMs === '' ? undefined : Number(item.fadeDurationMs)
      if (item.name.trim() === '' || item.action.trim() === '' || !Number.isInteger(offset)) return { ok: false, error: `${phase} transition step ${index + 1} needs a name, action, and whole-millisecond offset.` }
      if (fade !== undefined && (!Number.isInteger(fade) || fade < 0)) return { ok: false, error: `${phase} transition step ${index + 1} fade must be a non-negative whole number.` }
      result.push({ name: item.name.trim(), role: item.role, action: item.action.trim(), offsetMs: offset, ...(fade === undefined ? {} : { fadeDurationMs: fade }), barrier: item.barrier, onFailure: item.onFailure, ...(item.role === 'announcement' && item.announcementPolicy !== '' ? { announcementPolicy: item.announcementPolicy } : {}) })
    }
    return { ok: true, cues: result }
  }
  const enteringShow = buildCues(draft.enterShow, 'Enter-show')
  if (!enteringShow.ok) return { error: enteringShow.error }
  const enteringResting = buildCues(draft.enterResting, 'Enter-resting')
  if (!enteringResting.ok) return { error: enteringResting.error }
  const blackoutHoldMs = integer(draft.blackoutHoldMs, 'Blackout hold', 0); if (typeof blackoutHoldMs !== 'number') return blackoutHoldMs
  const blackoutAfterShowMs = integer(draft.blackoutAfterShowMs, 'Blackout after show', 0); if (typeof blackoutAfterShowMs !== 'number') return blackoutAfterShowMs
  const siteControl = optionalJson<NonNullable<ConfigNightSessionWrite['siteControl']>>(draft.siteControlJson, 'Site control'); if (siteControl !== undefined && 'error' in siteControl) return siteControl
  const interlocks = optionalJson<NonNullable<ConfigNightSessionWrite['interlocks']>>(draft.interlocksJson, 'Interlocks'); if (interlocks !== undefined && 'error' in interlocks) return interlocks
  let backgroundAudio: ConfigNightSessionWrite['resting']['backgroundAudio'] | undefined
  if (draft.backgroundEnabled) {
    const maxGainDb = Number(draft.backgroundMaxGainDb)
    if (!Number.isFinite(maxGainDb) || maxGainDb > 0) return { error: 'Background maximum gain must be a number at or below 0 dB.' }
    if (draft.backgroundItems.length === 0) return { error: 'Background audio needs at least one item.' }
    if (draft.backgroundItems.some((item) => item.itemId.trim() === '' || item.sequence.trim() === '' || item.target.trim() === '')) return { error: 'Every background audio item needs an id, sequence, and target.' }
    const ids = draft.backgroundItems.map((item) => item.itemId.trim())
    if (new Set(ids).size !== ids.length) return { error: 'Background audio item ids must be unique.' }
    const crossfadeMs = draft.backgroundTransition === 'crossfade' ? integer(draft.backgroundCrossfadeMs, 'Background crossfade', 0) : undefined
    if (crossfadeMs !== undefined && typeof crossfadeMs !== 'number') return crossfadeMs
    backgroundAudio = { items: draft.backgroundItems.map((item) => ({ itemId: item.itemId.trim(), show: item.show.trim(), sequence: item.sequence.trim(), target: item.target.trim() })), repeat: draft.backgroundRepeat, resume: draft.backgroundResume, itemTransition: draft.backgroundTransition, ...(typeof crossfadeMs === 'number' ? { crossfadeMs } : {}), maxGainDb }
  }
  const base = draft.base
  const { backgroundAudio: _previousBackgroundAudio, ...baseResting } = base?.resting ?? {}
  const { siteControl: _previousSiteControl, interlocks: _previousInterlocks, ...basePayload } = base ?? {}
  return {
    ...basePayload, show: draft.show.trim(), label: draft.label.trim(),
    showPlaylist: { ...(base?.showPlaylist ?? {}), fppInstanceId: draft.showFpp.trim(), playlist: draft.showPlaylist.trim() },
    resting: {
      ...baseResting, fppInstanceId: draft.restingFpp.trim(), playlist: draft.restingPlaylist.trim(), endOfNightPlaylist: draft.endOfNightPlaylist.trim() || draft.restingPlaylist.trim(), endOfNightRepeat: draft.endOfNightRepeat,
      timelineAsset: { ...(base?.resting?.timelineAsset ?? {}), show: draft.timelineShow.trim(), sequence: draft.timelineSequence.trim(), target: draft.timelineTarget.trim() },
      ...(backgroundAudio === undefined ? {} : { backgroundAudio }),
    },
    enterShow: { ...(base?.enterShow ?? {}), blackoutHoldMs, cues: enteringShow.cues },
    enterResting: { ...(base?.enterResting ?? {}), blackoutAfterShowMs, cues: enteringResting.cues }, announcementDefaultPolicy: draft.announcementDefaultPolicy,
    ...(siteControl === undefined ? {} : { siteControl }), ...(interlocks === undefined ? {} : { interlocks }),
  }
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
  const updateBackgroundItem = (index: number, patch: Partial<BackgroundItemDraft>) => setDraft((current) => ({ ...current, backgroundItems: current.backgroundItems.map((item, itemIndex) => itemIndex === index ? { ...item, ...patch } : item) }))

  return <div id="sn-definitions" className="sm-night-session-workspace">
    <div className="sm-night-session-heading">
      <div><h2 className="sm-section__title">Night session definitions</h2><p className="sm-small sm-muted">A definition says how the night enters the show and returns to resting. Editing creates a new revision; a running night is unchanged.</p></div>
      <Button variant="primary" onClick={() => { setSelected('__new__'); setLoaded(null); setDraft(blankDefinition(showId)); setError(null) }}>New definition</Button>
    </div>
    <Panes>
      <div className="sm-night-session-list">
        <p className="sm-eyebrow">Definitions · {objects?.length ?? 0}</p>
        {objects === null ? <RuledStrip absence="loading" label="Reading" fact="Reading night-session definitions." /> : objects.length === 0 ? <RuledStrip absence="empty" label="No definitions" fact="Create the first night-session definition for this Show." /> : <TableWrap label="Night session definitions"><Table><thead><tr><th>Definition</th><th>Show playlist</th><th>Resting playlist</th><th>Revision</th><th>State</th></tr></thead><tbody>{objects.map((object) => <SelectableRow key={object.id} selected={selected === object.id} onActivate={() => selectDefinition(object.id)} ariaLabel={`Edit ${object.payload.label}`}><td><strong>{object.payload.label}</strong><br /><span className="sm-data sm-small">{object.id}</span></td><td><span className="sm-data">{object.payload.showPlaylist.fppInstanceId}</span><br />{object.payload.showPlaylist.playlist}</td><td><span className="sm-data">{object.payload.resting.fppInstanceId}</span><br />{object.payload.resting.playlist}</td><td className="sm-data">{object.revision}</td><td><span className={activeId === object.id ? 'sm-meta-status sm-meta-status--live' : 'sm-meta-status'}>{activeId === object.id ? 'Active' : 'Inactive'}</span></td></SelectableRow>)}</tbody></Table></TableWrap>}
        <p className="sm-small sm-muted sm-night-session-list__note">Definitions belong to this Show. Activation remains an operational action on Show Night.</p>
      </div>
      <aside>
        {selected === '' ? <div className="sm-inspector sm-night-session-empty"><p className="sm-eyebrow">Definition inspector</p><h3 className="sm-inspector__title">Select a definition</h3><p className="sm-small sm-muted">Choose a row to edit it, or create a new definition.</p></div> : <div className="sm-inspector sm-night-session-inspector">
          <p className="sm-eyebrow sm-eyebrow--accent">{loaded === null ? 'Draft · new definition' : `Editing · revision ${loaded.revision}`}</p>
          <h3 className="sm-inspector__title">{loaded === null ? 'New night session' : draft.label}</h3>
          <div className="sm-inspector__group"><Field label="Label">{(p) => <Input {...p} value={draft.label} onChange={(e) => setDraft((d) => ({ ...d, label: e.target.value }))} />}</Field><Field label="Definition id" help={loaded === null ? 'Stable after creation.' : 'Definition ids do not change.'}>{(p) => <Input {...p} className="sm-data" disabled={loaded !== null} value={draft.id} onChange={(e) => setDraft((d) => ({ ...d, id: e.target.value }))} />}</Field></div>
          <div className="sm-inspector__group"><h4 className="sm-subsection__title">Show playback</h4><Field label="FPP instance">{(p) => <Select {...p} value={draft.showFpp} onChange={(e) => setDraft((d) => ({ ...d, showFpp: e.target.value, showPlaylist: '' }))}><option value="">Select an instance…</option>{fppIds(draft.showFpp).map((id) => <option key={id}>{id}</option>)}</Select>}</Field><Field label="Show playlist">{(p) => <Select {...p} value={draft.showPlaylist} onChange={(e) => setDraft((d) => ({ ...d, showPlaylist: e.target.value }))}><option value="">Select a playlist…</option>{playlistNames(draft.showFpp, draft.showPlaylist).map((name) => <option key={name}>{name}</option>)}</Select>}</Field></div>
          <div className="sm-inspector__group"><h4 className="sm-subsection__title">Resting playback</h4><Field label="FPP instance">{(p) => <Select {...p} value={draft.restingFpp} onChange={(e) => setDraft((d) => ({ ...d, restingFpp: e.target.value, restingPlaylist: '', endOfNightPlaylist: '' }))}><option value="">Select an instance…</option>{fppIds(draft.restingFpp).map((id) => <option key={id}>{id}</option>)}</Select>}</Field><Field label="Resting playlist">{(p) => <Select {...p} value={draft.restingPlaylist} onChange={(e) => setDraft((d) => ({ ...d, restingPlaylist: e.target.value }))}><option value="">Select a playlist…</option>{playlistNames(draft.restingFpp, draft.restingPlaylist).map((name) => <option key={name}>{name}</option>)}</Select>}</Field><Field label="End-of-night playlist">{(p) => <Select {...p} value={draft.endOfNightPlaylist} onChange={(e) => setDraft((d) => ({ ...d, endOfNightPlaylist: e.target.value }))}><option value="">Same as resting playlist</option>{playlistNames(draft.restingFpp, draft.endOfNightPlaylist).map((name) => <option key={name}>{name}</option>)}</Select>}</Field><Choice type="checkbox" checked={draft.endOfNightRepeat} onChange={(e) => setDraft((d) => ({ ...d, endOfNightRepeat: e.target.checked }))} label="Repeat the end-of-night playlist" /><Field label="Resting sequence">{(p) => <Select {...p} value={draft.timelineSequence} onChange={(e) => setDraft((d) => ({ ...d, timelineSequence: e.target.value, timelineTarget: '' }))}><option value="">Select a sequence…</option>{timelineSequences.map((v) => <option key={v}>{v}</option>)}</Select>}</Field><Field label="Resting target">{(p) => <Select {...p} value={draft.timelineTarget} onChange={(e) => setDraft((d) => ({ ...d, timelineTarget: e.target.value }))}><option value="">Select a target…</option>{timelineTargets.map((v) => <option key={v}>{v}</option>)}</Select>}</Field></div>
          <div className="sm-inspector__group"><h4 className="sm-subsection__title">Transition timing</h4><Field label="Blackout hold (ms)">{(p) => <Input {...p} type="number" min="0" step="1" value={draft.blackoutHoldMs} onChange={(e) => setDraft((d) => ({ ...d, blackoutHoldMs: e.target.value }))} />}</Field><Field label="Blackout after show (ms)">{(p) => <Input {...p} type="number" min="0" step="1" value={draft.blackoutAfterShowMs} onChange={(e) => setDraft((d) => ({ ...d, blackoutAfterShowMs: e.target.value }))} />}</Field><Field label="Default announcement policy">{(p) => <Select {...p} value={draft.announcementDefaultPolicy} onChange={(e) => setDraft((d) => ({ ...d, announcementDefaultPolicy: e.target.value as DefinitionDraft['announcementDefaultPolicy'] }))}>{['duck', 'mix', 'interrupt'].map((v) => <option key={v}>{v}</option>)}</Select>}</Field></div>
          <TransitionStepEditor title="Enter show" steps={draft.enterShow} actions={actions} onChange={(i, p) => updateCues('enterShow', i, p)} onAdd={() => addCue('enterShow')} onRemove={(i) => setDraft((d) => ({ ...d, enterShow: d.enterShow.filter((_, n) => n !== i) }))} />
          <TransitionStepEditor title="Enter resting" steps={draft.enterResting} actions={actions} onChange={(i, p) => updateCues('enterResting', i, p)} onAdd={() => addCue('enterResting')} onRemove={(i) => setDraft((d) => ({ ...d, enterResting: d.enterResting.filter((_, n) => n !== i) }))} />
          <div className="sm-inspector__group"><h4 className="sm-subsection__title">Background audio</h4><Choice type="checkbox" checked={draft.backgroundEnabled} onChange={(e) => setDraft((d) => ({ ...d, backgroundEnabled: e.target.checked, backgroundItems: e.target.checked && d.backgroundItems.length === 0 ? [blankBackgroundItem(d.show)] : d.backgroundItems }))} label="Enable background audio while resting" />{draft.backgroundEnabled && <><div className="sm-night-session-compact-grid"><Field label="Repeat">{(p) => <Select {...p} value={draft.backgroundRepeat} onChange={(e) => setDraft((d) => ({ ...d, backgroundRepeat: e.target.value as DefinitionDraft['backgroundRepeat'] }))}>{['none', 'item', 'playlist'].map((v) => <option key={v}>{v}</option>)}</Select>}</Field><Field label="Resume">{(p) => <Select {...p} value={draft.backgroundResume} onChange={(e) => setDraft((d) => ({ ...d, backgroundResume: e.target.value as DefinitionDraft['backgroundResume'] }))}><option>resume</option><option>restart</option></Select>}</Field><Field label="Between items">{(p) => <Select {...p} value={draft.backgroundTransition} onChange={(e) => setDraft((d) => ({ ...d, backgroundTransition: e.target.value as DefinitionDraft['backgroundTransition'] }))}>{['sequential', 'gapless', 'crossfade'].map((v) => <option key={v}>{v}</option>)}</Select>}</Field>{draft.backgroundTransition === 'crossfade' && <Field label="Crossfade (ms)">{(p) => <Input {...p} type="number" min="0" value={draft.backgroundCrossfadeMs} onChange={(e) => setDraft((d) => ({ ...d, backgroundCrossfadeMs: e.target.value }))} />}</Field>}<Field label="Maximum gain (dB)">{(p) => <Input {...p} type="number" max="0" step="0.1" value={draft.backgroundMaxGainDb} onChange={(e) => setDraft((d) => ({ ...d, backgroundMaxGainDb: e.target.value }))} />}</Field></div>{draft.backgroundItems.map((item, i) => <div className="sm-night-session-item" key={`${i}:${item.itemId}`}><Field label="Item id">{(p) => <Input {...p} value={item.itemId} onChange={(e) => updateBackgroundItem(i, { itemId: e.target.value })} />}</Field><Field label="Audio asset">{(p) => <Select {...p} value={`${item.sequence}\u0000${item.target}`} onChange={(e) => { const [selectedSequence = '', selectedTarget = ''] = e.target.value.split('\u0000'); updateBackgroundItem(i, { show: draft.show, sequence: selectedSequence, target: selectedTarget, itemId: item.itemId || selectedSequence }) }}><option value="\u0000">Select an audio asset…</option>{audioAssets.map((asset) => <option key={asset.id} value={`${asset.sequence}\u0000${asset.target}`}>{asset.sequence} · {asset.target}</option>)}</Select>}</Field><Button variant="quiet" onClick={() => setDraft((d) => ({ ...d, backgroundItems: d.backgroundItems.filter((_, n) => n !== i) }))}>Remove</Button></div>)}<Button onClick={() => setDraft((d) => ({ ...d, backgroundItems: [...d.backgroundItems, blankBackgroundItem(d.show)] }))}>Add audio item</Button></>}</div>
          <div className="sm-inspector__group"><h4 className="sm-subsection__title">Site control and interlocks</h4><p className="sm-small sm-muted">Optional advanced configuration. These objects use the coordinator schema exactly; blank means not configured.</p><Field label="Site control (JSON)">{(p) => <Textarea {...p} rows={5} className="sm-data" value={draft.siteControlJson} onChange={(e) => setDraft((d) => ({ ...d, siteControlJson: e.target.value }))} />}</Field><Field label="Interlocks (JSON array)">{(p) => <Textarea {...p} rows={5} className="sm-data" value={draft.interlocksJson} onChange={(e) => setDraft((d) => ({ ...d, interlocksJson: e.target.value }))} />}</Field></div>
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
  return <div className="sm-inspector__group"><div className="sm-night-session-group-head"><div><h4 className="sm-subsection__title">{title}</h4><p className="sm-small sm-muted">{steps.length} {steps.length === 1 ? 'step' : 'steps'}</p></div><Button onClick={onAdd}>Add step</Button></div>{steps.map((step, index) => <div className="sm-night-session-item" key={index}><Field label="Name">{(p) => <Input {...p} value={step.name} onChange={(e) => onChange(index, { name: e.target.value })} />}</Field><div className="sm-night-session-compact-grid"><Field label="Role">{(p) => <Select {...p} value={step.role} onChange={(e) => onChange(index, { role: e.target.value as CueDraft['role'] })}>{['lighting', 'projection', 'audio', 'announcement', 'other'].map((v) => <option key={v}>{v}</option>)}</Select>}</Field><Field label="Action">{(p) => <Select {...p} value={step.action} onChange={(e) => onChange(index, { action: e.target.value })}><option value="">Select an action…</option>{actions.map((action) => <option key={action.id} value={action.id}>{action.payload.label} · {action.id}</option>)}</Select>}</Field><Field label="Offset (ms)">{(p) => <Input {...p} type="number" step="1" value={step.offsetMs} onChange={(e) => onChange(index, { offsetMs: e.target.value })} />}</Field><Field label="Fade (ms)">{(p) => <Input {...p} type="number" min="0" step="1" value={step.fadeDurationMs} onChange={(e) => onChange(index, { fadeDurationMs: e.target.value })} />}</Field><Field label="On failure">{(p) => <Select {...p} value={step.onFailure} onChange={(e) => onChange(index, { onFailure: e.target.value as CueDraft['onFailure'] })}><option>continue</option><option>abort</option></Select>}</Field>{step.role === 'announcement' && <Field label="Announcement policy">{(p) => <Select {...p} value={step.announcementPolicy} onChange={(e) => onChange(index, { announcementPolicy: e.target.value as CueDraft['announcementPolicy'] })}><option value="">Use default</option><option>duck</option><option>mix</option><option>interrupt</option></Select>}</Field>}</div><Choice type="checkbox" checked={step.barrier} onChange={(e) => onChange(index, { barrier: e.target.checked })} label="Wait for this action before continuing" /><Button variant="quiet" onClick={() => onRemove(index)}>Remove step</Button></div>)}</div>
}
