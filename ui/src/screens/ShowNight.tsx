import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { dispatchNightCommand, getCurrentNightSession, type NightCommandName, type NightSessionState } from '../api'
import {
  BlankingPlate,
  Button,
  ButtonRow,
  DefinitionStrip,
  NotWiredBanner,
  RuledStrip,
  Section,
  StatusPair,
  Table,
  TableWrap,
} from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { effectiveServerTimeIso } from '../domain/time'
import { formatPosition } from './liveControlModel'
import {
  cycleRail,
  evidenceReadouts,
  nextTransition,
  nightRail,
  nowPlaying,
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

export function ShowNight() {
  const model = useModelContext()
  const { session, error } = useNightSession()
  const nowIso = effectiveServerTimeIso(model.serverTime, model.serverTimeReceivedAt, Date.now())
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'night:command')
  const [outcome, setOutcome] = useState<string | null>(null)

  const send = useCallback((command: NightCommandName) => {
    dispatchNightCommand(command)
      .then(() => setOutcome(`${command} was accepted. What the session then does is what this page reports.`))
      .catch((err: unknown) => setOutcome(`${command} was refused: ${describeApiError(err)}`))
  }, [])

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
          <Link className="sm-btn" to={`/shows/${session.configObjectId}`}>
            Edit definition
          </Link>
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
        <div className="sm-grid sm-grid--auto">
          {(
            [
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
            </div>
          ))}
        </div>
        {outcome !== null && (
          <div className="sm-outcome">
            <StatusPair tone="warn" label="Accepted" />
            <p className="sm-outcome__detail">{outcome}</p>
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
            <p className="sm-readout__fact">{readout.fact}</p>
          </div>
        ))}
      </Section>

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
