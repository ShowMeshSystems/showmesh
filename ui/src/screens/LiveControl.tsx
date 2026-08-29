import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  dispatchNightCommand,
  invokeAction,
  getShowCue,
  listConfigObjects,
  nextFPPPlaylistItem,
  pauseFPPPlaylist,
  prevFPPPlaylistItem,
  resumeFPPPlaylist,
  setFPPVolume,
  stopFPPPlaylist,
  stopFPPPlaylistGracefully,
  submitMacroRun,
  type ConfigObjectSummary,
  type FPPCommandResult,
  type NightCommandName,
} from '../api'
import {
  Button,
  ButtonRow,
  ButtonRule,
  Callout,
  Field,
  Input,
  RuledStrip,
  Section,
  Segmented,
  StatusPair,
  Table,
  TableWrap,
} from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { effectiveServerTimeIso } from '../domain/time'
import {
  audioRows,
  describeFPPOutcome,
  formatPosition,
  outputRows,
  transportState,
  type CommandOutcome,
} from './liveControlModel'

function useActiveShow(): string | null {
  const model = useModelContext()
  const show = model.currentRuns?.activeShow
  return show?.configured === true ? show.show : null
}

function useConfigList(kind: 'show.macro' | 'show.action' | 'show.cue', show: string | null) {
  const [items, setItems] = useState<ConfigObjectSummary[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (show === null) {
      setItems([])
      return
    }
    let cancelled = false
    listConfigObjects(kind, show)
      .then((response) => {
        if (!cancelled) setItems(response.objects)
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(describeApiError(err))
      })
    return () => {
      cancelled = true
    }
  }, [kind, show])

  return { items, error }
}

function Outcome({ outcome }: { outcome: CommandOutcome | null }) {
  if (outcome === null) return null
  return (
    <div className="sm-outcome">
      <StatusPair tone={outcome.tone} label={outcome.label} />
      <p className="sm-outcome__detail">{outcome.detail}</p>
    </div>
  )
}

export function LiveControl() {
  const model = useModelContext()
  const nowIso = effectiveServerTimeIso(model.serverTime, model.serverTimeReceivedAt, Date.now())
  const show = useActiveShow()
  const commandGate = evaluateScope(model.session, model.sessionFetchFailed, 'fpp:command')
  const nightGate = evaluateScope(model.session, model.sessionFetchFailed, 'night:command')
  const configGate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')

  const [selected, setSelected] = useState<string | null>(null)
  const instance = model.fpp.find((entry) => entry.instanceId === selected) ?? model.fpp[0]
  const [outcome, setOutcome] = useState<CommandOutcome | null>(null)
  const [nightOutcome, setNightOutcome] = useState<CommandOutcome | null>(null)
  const [volume, setVolume] = useState('')

  const macros = useConfigList('show.macro', show)
  const actions = useConfigList('show.action', show)

  const run = useCallback((action: string, call: () => Promise<FPPCommandResult>) => {
    call()
      .then((result) => setOutcome(describeFPPOutcome(result, action)))
      .catch((err: unknown) => setOutcome({ tone: 'bad', label: 'Refused', detail: `${action}: ${describeApiError(err)}` }))
  }, [])

  const night = useCallback((command: NightCommandName) => {
    dispatchNightCommand(command)
      .then(() =>
        setNightOutcome({
          tone: 'warn',
          label: 'Accepted',
          detail: `${command} was accepted. The coordinator answers 202 and reports nothing further here; Show Night carries the session's own state.`,
        }),
      )
      .catch((err: unknown) => setNightOutcome({ tone: 'bad', label: 'Refused', detail: `${command}: ${describeApiError(err)}` }))
  }, [])

  const state = instance === undefined ? null : transportState(instance)
  const rows = [...outputRows(model, nowIso), ...audioRows(model, nowIso)]
  const confirmed = rows.filter((row) => row.confirmed).length

  return (
    <>
      <PageHeader />

      <Section
        id="lc-transport"
        title="Transport"
        aside={
          model.fpp.length > 1 && instance !== undefined ? (
            <Segmented
              label="FPP instance"
              value={instance.instanceId}
              options={model.fpp.map((entry) => ({ value: entry.instanceId, label: entry.instanceId }))}
              onChange={setSelected}
            />
          ) : undefined
        }
      >
        {instance === undefined || state === null ? (
          <RuledStrip
            absence="empty"
            label="None configured"
            fact="No FPP instance is configured on this coordinator."
            detail="Settings › Connections is where an endpoint is added."
          />
        ) : (
          <>
            <p className="sm-transport__now">
              <span className="sm-data">{state.playlist ?? 'No playlist reported'}</span>
              {state.itemIndex !== null && (
                <>
                  {' · '}
                  <span className="sm-data">
                    {state.itemIndex}
                    {state.itemCount === null ? '' : ` / ${state.itemCount}`}
                  </span>
                </>
              )}
            </p>
            <p className="sm-small sm-muted">
              {state.playerState ?? 'Player state not reported'}
              {state.elapsedSeconds !== null && ` · ${formatPosition(state.elapsedSeconds) ?? ''}`}
              {state.totalSeconds !== null && ` / ${formatPosition(state.totalSeconds) ?? ''}`}
            </p>
            <ButtonRow>
              <Button size="gloved" disabled={!commandGate.allowed} title={commandGate.allowed ? undefined : commandGate.reason} onClick={() => run('Previous item', () => prevFPPPlaylistItem(instance.instanceId))}>
                <span aria-hidden="true">⏮ </span>Previous item
              </Button>
              {state.playerState === 'paused' ? (
                <Button size="gloved" disabled={!commandGate.allowed} title={commandGate.allowed ? undefined : commandGate.reason} onClick={() => run('Resume', () => resumeFPPPlaylist(instance.instanceId))}>
                  <span aria-hidden="true">▶ </span>Resume
                </Button>
              ) : (
                <Button size="gloved" disabled={!commandGate.allowed} title={commandGate.allowed ? undefined : commandGate.reason} onClick={() => run('Pause', () => pauseFPPPlaylist(instance.instanceId))}>
                  <span aria-hidden="true">⏸ </span>Pause
                </Button>
              )}
              <Button size="gloved" disabled={!commandGate.allowed} title={commandGate.allowed ? undefined : commandGate.reason} onClick={() => run('Next item', () => nextFPPPlaylistItem(instance.instanceId))}>
                <span aria-hidden="true">⏭ </span>Next item
              </Button>
              <ButtonRule />
              <Button size="gloved" disabled={!commandGate.allowed} title={commandGate.allowed ? undefined : commandGate.reason} onClick={() => run('Stop after this item', () => stopFPPPlaylistGracefully(instance.instanceId, false))}>
                Stop after this item
              </Button>
              <Button variant="danger" size="gloved" disabled={!commandGate.allowed} title={commandGate.allowed ? undefined : commandGate.reason} onClick={() => run('Stop now', () => stopFPPPlaylist(instance.instanceId))}>
                <span aria-hidden="true">■ </span>Stop now
              </Button>
            </ButtonRow>
            <div className="sm-volume">
              <Field label="Volume" {...(state.volume === null ? { help: 'This instance does not report its volume.' } : {})}>
                {(field) => (
                  <Input
                    {...field}
                    type="number"
                    min={0}
                    max={100}
                    value={volume}
                    placeholder={state.volume === null ? '' : String(state.volume)}
                    onChange={(event) => setVolume(event.target.value)}
                  />
                )}
              </Field>
              <Button
                disabled={!commandGate.allowed || volume.trim() === ''}
                title={commandGate.allowed ? undefined : commandGate.reason}
                onClick={() => run('Set volume', () => setFPPVolume(instance.instanceId, Number(volume)))}
              >
                Apply
              </Button>
            </div>
            <Outcome outcome={outcome} />
          </>
        )}
        <Callout>
          This coordinator advertises no installation-wide emergency stop, so there is no control for one here.
          <strong> Stop now</strong> halts this player only; projection and audio hold their last state until their own
          cues run.
        </Callout>
      </Section>

      <Section
        id="lc-outputs"
        title="What each output is doing"
        aside={<span className="sm-small sm-muted">As each output last reported it</span>}
      >
        {rows.length === 0 ? (
          <RuledStrip
            absence="unobserved"
            label="Unobserved"
            fact="No output has reported what it is doing."
            detail="No render or audio observation has reached this coordinator. That is not the same as nothing running."
          />
        ) : (
          <>
            <TableWrap label="What each output is doing">
              <Table>
                <thead>
                  <tr>
                    <th scope="col">Output</th>
                    <th scope="col">Doing what</th>
                    <th scope="col">Evidence</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((row) => (
                    <tr key={row.key}>
                      <td>
                        <span className="sm-data">{row.name}</span>
                        <br />
                        <span className="sm-small sm-faint">{row.where}</span>
                      </td>
                      <td>
                        {row.doing}
                        {row.content !== null && (
                          <>
                            {' '}
                            <span className="sm-data">{row.content}</span>
                          </>
                        )}
                      </td>
                      <td>
                        <StatusPair tone={row.tone} label={row.evidence} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            </TableWrap>
            <p className="sm-section__footnote">
              {confirmed} of {rows.length} outputs confirm what they are doing.
              {confirmed < rows.length && ` ${rows.length - confirmed} cannot be verified right now.`}
            </p>
          </>
        )}
      </Section>

      <Section id="lc-lifecycle" title="Night lifecycle" aside={<Link to="/night">Show Night →</Link>}>
        <p className="sm-small sm-muted">
          Every command here answers 202. The UI reports that it was accepted, never that it is done; Show Night carries
          what the session then reports.
        </p>
        <NightGroup
          id="lc-prep"
          title="Prepare"
          commands={[
            ['prepare-site', 'Prepare site', 'Opens a preparation epoch. Readiness and start-preshow both need one.'],
            ['run-readiness', 'Run readiness', 'Re-runs every readiness check against this epoch.'],
          ]}
          gate={nightGate}
          onRun={night}
        />
        <NightGroup
          id="lc-start"
          title="Start"
          commands={[
            ['start-preshow', 'Start preshow', 'Enters preshow from a prepared, ready session.'],
            ['start-night', 'Start night', 'Commits the armed show and starts the first cycle.'],
          ]}
          gate={nightGate}
          onRun={night}
        />
        <NightGroup
          id="lc-end"
          title="End the night"
          commands={[
            ['request-final-show', 'Request final show', 'Closes admission. The next normally timed show becomes the last.'],
            ['fade-out-night', 'Fade out night', 'Arriving mid-show makes this show final and the fade waits for it to finish.'],
            ['power-down-presentation', 'Power down presentation', 'The terminal intent. An interlock can withhold it.'],
            ['end-session', 'End session', 'Abandons the session. Never withheld by an interlock; prepare-site then starts a fresh one.'],
          ]}
          gate={nightGate}
          onRun={night}
        />
        <Outcome outcome={nightOutcome} />
      </Section>

      <RunList
        id="lc-macros"
        title="Macros"
        kindLabel="show.macro"
        show={show}
        list={macros}
        gate={configGate}
        detail="Each step confirms separately. A macro is accepted, then its steps report their own outcomes."
        onRun={(id) => submitMacroRun(id)}
      />

      <Announcements show={show} />

      <RunList
        id="lc-actions"
        title="Actions"
        kindLabel="show.action"
        show={show}
        list={actions}
        gate={configGate}
        detail="One integration command each. Macros are built from these; these are here for when you need just the one step."
        onRun={(id) => invokeAction(id)}
      />

      <Callout>
        Brightness ceiling, site control and interlock authoring are not advertised by this coordinator, so they have no
        controls here. All lists above are scoped to the active show.
      </Callout>
    </>
  )
}

/**
 * Cues that declare an announcement output. This coordinator advertises no
 * way to fire one outside a Show Night transition (no POST /cues/{id}/fire
 * or equivalent in api/openapi.yaml), so this states that rather than
 * offering a control that cannot work.
 */
function Announcements({ show }: { show: string | null }) {
  const [cues, setCues] = useState<{ id: string; policy: string; duckGainDb?: number; fadeMillis: number }[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (show === null) {
      setCues([])
      return
    }
    let cancelled = false
    listConfigObjects('show.cue', show)
      .then(async (response) => {
        const loaded = await Promise.all(response.objects.map((object) => getShowCue(object.id)))
        if (cancelled) return
        setCues(
          loaded
            .filter((cue) => cue.payload.outputs.announcement !== undefined)
            .map((cue) => ({
              id: cue.id,
              policy: cue.payload.outputs.announcement?.policy ?? 'unknown',
              ...(cue.payload.outputs.announcement?.duckGainDb === undefined
                ? {}
                : { duckGainDb: cue.payload.outputs.announcement.duckGainDb }),
              fadeMillis: cue.payload.outputs.announcement?.fadeMillis ?? 0,
            })),
        )
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(describeApiError(err))
      })
    return () => {
      cancelled = true
    }
  }, [show])

  return (
    <Section
      id="lc-annc"
      title="Announcements"
      aside={
        cues === null ? undefined : (
          <span className="sm-small sm-muted">Cues with an announcement output · {cues.length}</span>
        )
      }
    >
      {error !== null ? (
        <RuledStrip absence="failed" label="Read failed" fact={error} detail="Nothing below is current." />
      ) : cues === null ? (
        <RuledStrip absence="loading" label="Reading" fact="Reading cues with an announcement output." />
      ) : cues.length === 0 ? (
        <RuledStrip
          absence="empty"
          label="None"
          fact={show === null ? 'No show is active.' : `No cue in ${show} declares an announcement output.`}
          detail="Shows › Cues is where an announcement output is added to a cue."
        />
      ) : (
        <ul className="sm-plain-list">
          {cues.map((cue) => (
            <li key={cue.id}>
              <span className="sm-data">{cue.id}</span>{' '}
              <span className="sm-small sm-muted">
                {cue.policy === 'duck' && cue.duckGainDb !== undefined
                  ? `Ducks the bed to ${cue.duckGainDb} dB`
                  : cue.policy === 'interrupt'
                    ? 'Interrupts the bed'
                    : `Mixes with the bed`}
                {` · ${(cue.fadeMillis / 1000).toFixed(1)} s fade`}
              </span>
            </li>
          ))}
        </ul>
      )}
      <Callout>
        This coordinator advertises no way to fire an announcement directly: the API has no endpoint for firing a cue
        outside a Show Night transition. These run when their transition runs.
      </Callout>
    </Section>
  )
}

function PageHeader() {
  return (
    <>
      <h1 className="sm-page__title">Live Control</h1>
      <p className="sm-page__lede">
        Acting on the show that is running now. A command is not successful because it was sent: each one reports the
        evidence that it took effect, or why it did not.
      </p>
    </>
  )
}

type Gate = { allowed: true } | { allowed: false; reason: string }

function NightGroup({
  id,
  title,
  commands,
  gate,
  onRun,
}: {
  id: string
  title: string
  commands: readonly (readonly [NightCommandName, string, string])[]
  gate: Gate
  onRun: (command: NightCommandName) => void
}) {
  return (
    <section aria-labelledby={id} className="sm-subsection">
      <h3 id={id} className="sm-subsection__title">
        {title}
      </h3>
      <div className="sm-grid sm-grid--auto">
        {commands.map(([command, label, detail]) => (
          <div key={command}>
            <Button
              size="gloved"
              disabled={!gate.allowed}
              title={gate.allowed ? undefined : gate.reason}
              onClick={() => onRun(command)}
            >
              {label}
            </Button>
            <p className="sm-small sm-muted">{gate.allowed ? detail : gate.reason}</p>
          </div>
        ))}
      </div>
    </section>
  )
}

function RunList({
  id,
  title,
  kindLabel,
  show,
  list,
  gate,
  detail,
  onRun,
}: {
  id: string
  title: string
  kindLabel: string
  show: string | null
  list: { items: ConfigObjectSummary[] | null; error: string | null }
  gate: Gate
  detail: string
  onRun: (id: string) => Promise<unknown>
}) {
  const [outcome, setOutcome] = useState<CommandOutcome | null>(null)

  return (
    <Section
      id={id}
      title={title}
      aside={
        list.items === null ? undefined : (
          <span className="sm-small sm-muted">
            <span className="sm-data">{kindLabel}</span> · {list.items.length}
          </span>
        )
      }
    >
      <p className="sm-small sm-muted">{detail}</p>
      {show === null ? (
        <RuledStrip
          absence="empty"
          label="No active show"
          fact={`${title} are scoped to the active show, and none is active.`}
          detail="Shows is where one is activated."
        />
      ) : list.error !== null ? (
        <RuledStrip absence="failed" label="Read failed" fact={list.error} detail="Nothing below is current." />
      ) : list.items === null ? (
        <RuledStrip absence="loading" label="Reading" fact={`Reading ${kindLabel} objects for ${show}.`} />
      ) : list.items.length === 0 ? (
        <RuledStrip
          absence="empty"
          label="None"
          fact={`${show} has no ${kindLabel} objects.`}
          detail="Shows › Automation is where they are authored."
        />
      ) : (
        <div className="sm-grid sm-grid--auto">
          {list.items.map((item) => (
            <div key={item.id}>
              <Button
                size="gloved"
                disabled={!gate.allowed}
                title={gate.allowed ? undefined : gate.reason}
                onClick={() => {
                  onRun(item.id)
                    .then(() =>
                      setOutcome({
                        tone: 'warn',
                        label: 'Accepted',
                        detail: `${item.id} was accepted. Each step reports its own outcome; this is not a report that it finished.`,
                      }),
                    )
                    .catch((err: unknown) =>
                      setOutcome({ tone: 'bad', label: 'Refused', detail: `${item.id}: ${describeApiError(err)}` }),
                    )
                }}
              >
                {item.label !== '' ? item.label : item.id}
              </Button>
              <p className="sm-small sm-muted">
                <span className="sm-data">{item.id}</span> · rev {item.currentRevision}
              </p>
            </div>
          ))}
        </div>
      )}
      <Outcome outcome={outcome} />
    </Section>
  )
}
