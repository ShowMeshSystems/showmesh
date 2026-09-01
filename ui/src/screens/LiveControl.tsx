import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  ApiError,
  advanceAudioSession,
  blackoutResolume,
  clearAudioSession,
  dispatchNightCommand,
  fadeAudioSessionGain,
  getAudioSettingsConfig,
  getShowAction,
  getShowCue,
  invokeAction,
  listAssets,
  listConfigObjects,
  listObservations,
  muteAudioSessionOutput,
  nextFPPPlaylistItem,
  pauseAudioSession,
  pauseFPPPlaylist,
  prepareAudioSession,
  prevFPPPlaylistItem,
  resumeAudioSession,
  resumeFPPPlaylist,
  seekAudioSession,
  setAudioSessionGain,
  setFPPVolume,
  startAudioSession,
  startFPPPlaylist,
  stopAudioSession,
  stopFPPPlaylist,
  stopFPPPlaylistGracefully,
  submitMacroRun,
  unmuteAudioSessionOutput,
  type AudioSessionCommandResult,
  type ConfigObjectSummary,
  type FPPCommandResult,
  type NightCommandName,
  type ObservationEntry,
  type ResolumeActionResult,
} from '../api'
import {
  Button,
  ButtonRow,
  ButtonRule,
  Callout,
  Choice,
  Field,
  Input,
  NotWired,
  NotWiredBanner,
  RuledStrip,
  Section,
  Segmented,
  Select,
  StatusPair,
  Table,
  TableWrap,
} from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { effectiveServerTimeIso, millisToTimecode, timecodeToMillis } from '../domain/time'
import {
  audioRows,
  audioSessionOptions,
  classifyStartPlaylistConflict,
  currentRunsAbsence,
  deriveAudioSessionRevision,
  describeAudioSessionOutcome,
  describeFPPOutcome,
  formatPosition,
  outputRows,
  transportState,
  type CommandOutcome,
  type StartPlaylistConflictReason,
} from './liveControlModel'

/** Kept apart from the generic Refused outcome: the busy guard needs a distinct render and its own "start anyway" CTA. */
type StartPlaylistState =
  | { kind: 'idle' }
  | { kind: 'busy'; message: string; reason: Exclude<StartPlaylistConflictReason, 'unknown'>; playlist: string; repeat: boolean }

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
  const resolumeGate = evaluateScope(model.session, model.sessionFetchFailed, 'resolume:action')
  const audioGate = evaluateScope(model.session, model.sessionFetchFailed, 'audio:command')

  const [selected, setSelected] = useState<string | null>(null)
  const instance = model.fpp.find((entry) => entry.instanceId === selected) ?? model.fpp[0]
  const [outcome, setOutcome] = useState<CommandOutcome | null>(null)
  const [nightOutcome, setNightOutcome] = useState<CommandOutcome | null>(null)
  const [volume, setVolume] = useState('')
  const [startPlaylistName, setStartPlaylistName] = useState('')
  const [startRepeat, setStartRepeat] = useState(false)
  const [startState, setStartState] = useState<StartPlaylistState>({ kind: 'idle' })

  const macros = useConfigList('show.macro', show)
  const actions = useConfigList('show.action', show)

  const run = useCallback((action: string, call: () => Promise<FPPCommandResult>) => {
    call()
      .then((result) => setOutcome(describeFPPOutcome(result, action)))
      .catch((err: unknown) => setOutcome({ tone: 'bad', label: 'Refused', detail: `${action}: ${describeApiError(err)}` }))
  }, [])

  // The ifBusy="refuse" guard's two conflict types get their own state and
  // CTA, distinguishable from a generic Refused outcome; every other 409 and
  // every non-409 failure falls through to the same Refused path `run` uses.
  const dispatchStartPlaylist = useCallback(
    (instanceId: string, playlist: string, repeat: boolean, ifBusy: 'refuse' | 'replace') => {
      startFPPPlaylist(instanceId, playlist, repeat, ifBusy)
        .then((result) => {
          setStartState({ kind: 'idle' })
          setOutcome(describeFPPOutcome(result, 'Start playlist'))
        })
        .catch((err: unknown) => {
          if (err instanceof ApiError && err.status === 409) {
            const reason = classifyStartPlaylistConflict(err.problemType)
            if (reason !== 'unknown') {
              setStartState({ kind: 'busy', message: describeApiError(err), reason, playlist, repeat })
              return
            }
          }
          setStartState({ kind: 'idle' })
          setOutcome({ tone: 'bad', label: 'Refused', detail: `Start playlist: ${describeApiError(err)}` })
        })
    },
    [],
  )

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
  const runsAbsence = currentRunsAbsence(model)

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
          <div className="sm-transport-surface">
            <div className="sm-transport-surface__head">
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
            </div>
            <div className="sm-transport-surface__body">
              <div className="sm-volume sm-transport__start">
                <Field label="Playlist name" help="FPP's own catalog is not exposed here; type the exact name.">
                  {(field) => (
                    <Input
                      {...field}
                      value={startPlaylistName}
                      onChange={(event) => setStartPlaylistName(event.target.value)}
                      placeholder="e.g. Standard Show"
                    />
                  )}
                </Field>
                <Choice
                  type="checkbox"
                  checked={startRepeat}
                  onChange={(event) => setStartRepeat(event.target.checked)}
                  label="Repeat"
                />
                <Button
                  variant="primary"
                  disabled={!commandGate.allowed || startPlaylistName.trim() === ''}
                  title={commandGate.allowed ? undefined : commandGate.reason}
                  onClick={() => dispatchStartPlaylist(instance.instanceId, startPlaylistName.trim(), startRepeat, 'refuse')}
                >
                  Start playlist
                </Button>
              </div>
              {startState.kind === 'busy' && (
                <div className="sm-outcome" role="alert">
                  <StatusPair tone="warn" label="Busy" />
                  <p className="sm-outcome__detail">{startState.message}</p>
                  <ButtonRow>
                    <Button
                      variant="danger"
                      disabled={!commandGate.allowed}
                      title={commandGate.allowed ? undefined : commandGate.reason}
                      onClick={() => dispatchStartPlaylist(instance.instanceId, startState.playlist, startState.repeat, 'replace')}
                    >
                      {startState.reason === 'differentPlaylistPlaying'
                        ? 'Start anyway (replace what is currently playing)'
                        : 'Start anyway (override the busy check)'}
                    </Button>
                  </ButtonRow>
                </div>
              )}
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
                <Button size="gloved" disabled={!commandGate.allowed} title={commandGate.allowed ? undefined : commandGate.reason} onClick={() => run('Stop after this loop', () => stopFPPPlaylistGracefully(instance.instanceId, true))}>
                  Stop after this loop
                </Button>
                <Button variant="danger" size="gloved" disabled={!commandGate.allowed} title={commandGate.allowed ? undefined : commandGate.reason} onClick={() => run('Stop now', () => stopFPPPlaylist(instance.instanceId))}>
                  <span aria-hidden="true">■ </span>Stop now
                </Button>
              </ButtonRow>
              <div className="sm-volume sm-transport__volume">
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
            </div>
            <Outcome outcome={outcome} />
          </div>
        )}
        <Callout>
          <strong> Stop now</strong> halts this player only; projection and audio hold their last state until their own
          cues run. Installation-wide emergency stop is now a separate, deliberately gated API workflow; it is not
          represented by this transport control.
        </Callout>
      </Section>

      <Section
        id="lc-outputs"
        title="What each output is doing"
        aside={<span className="sm-small sm-muted">As each output last reported it</span>}
      >
        {runsAbsence !== null && (
          <RuledStrip
            absence={runsAbsence}
            label={runsAbsence === 'unavailable' ? 'Now playing not reported' : 'Reading'}
            fact={
              runsAbsence === 'unavailable'
                ? 'This coordinator does not serve current-run state, so program audio has no row here.'
                : 'Reading current-run state for program audio.'
            }
          />
        )}
        {rows.length === 0 ? (
          <RuledStrip
            absence="unobserved"
            label="Unobserved"
            fact="No output has reported what it is doing."
            detail="No render or audio observation has reached this coordinator. That is not the same as nothing running."
          />
        ) : (
          <>
            <TableWrap label="Outputs, scrollable">
              <Table minWidth={600}>
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

      <ResolumeQuickStrip gate={resolumeGate} />

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

      <AudioSessionsBlock gate={audioGate} show={show} />

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
 * Cues that declare an announcement output. The mock's Fire button is drawn
 * here to its final shape and is inert: the API has no POST /cues/{id}/fire
 * or equivalent. Ruled 2026-08-29, D-008.
 */
type AnnouncementCue = {
  id: string
  policy: string
  duckGainDb?: number
  fadeMillis: number
  /** The logical sequence the cue's audio resolves through, or null when it declares none. */
  sequence: string | null
  /** False only when the lookup ran and found no current asset for that sequence. */
  uploaded: boolean
}

function Announcements({ show }: { show: string | null }) {
  const [cues, setCues] = useState<AnnouncementCue[] | null>(null)
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
        const announcing = loaded.filter((cue) => cue.payload.outputs.announcement !== undefined)
        // A cue's audio asset resolves by sequence id, never by asset id
        // (assetsync/cuecatalog.go resolveAssetFor). Uploaded means a current
        // asset exists for that sequence in this show.
        const assets = await listAssets({ show })
        if (cancelled) return
        const currentSequences = new Set(
          assets.assets.filter((asset) => asset.current).map((asset) => asset.sequence),
        )
        setCues(
          announcing.map((cue) => {
            const sequence = cue.payload.outputs.audio?.asset ?? null
            return {
              id: cue.id,
              policy: cue.payload.outputs.announcement?.policy ?? 'unknown',
              ...(cue.payload.outputs.announcement?.duckGainDb === undefined
                ? {}
                : { duckGainDb: cue.payload.outputs.announcement.duckGainDb }),
              fadeMillis: cue.payload.outputs.announcement?.fadeMillis ?? 0,
              sequence,
              uploaded: sequence !== null && currentSequences.has(sequence),
            }
          }),
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
        <>
          <NotWiredBanner
            what="Firing an announcement"
            missing={<code className="sm-data">POST /cues/{'{id}'}/fire</code>}
            detail="Until it exists, an announcement runs only when its Show Night transition runs."
          />
          <ul className="sm-plain-list">
            {cues.map((cue) => (
              <li key={cue.id} className="sm-annc">
                <div>
                  <p>
                    <span className="sm-data">{cue.id}</span>
                  </p>
                  <p className="sm-small sm-muted">
                    {cue.policy === 'duck' && cue.duckGainDb !== undefined
                      ? `Ducks the bed to ${cue.duckGainDb} dB`
                      : cue.policy === 'interrupt'
                        ? 'Interrupts the bed'
                        : `Mixes with the bed`}
                    {` · ${(cue.fadeMillis / 1000).toFixed(1)} s fade`}
                  </p>
                  {!cue.uploaded && (
                    <p className="sm-small sm-muted">
                      {cue.sequence === null
                        ? 'This cue declares no audio output, so there is nothing to play.'
                        : 'Its audio asset has not been uploaded.'}{' '}
                      <Link to="/assets">Show Assets</Link>
                    </p>
                  )}
                </div>
                {cue.uploaded ? (
                  <NotWired>
                    <Button variant="primary">Fire</Button>
                  </NotWired>
                ) : (
                  <Button variant="primary" disabled title="Its audio asset has not been uploaded.">
                    Fire
                  </Button>
                )}
              </li>
            ))}
          </ul>
        </>
      )}
    </Section>
  )
}

type AudioNodesState =
  | { kind: 'loading' }
  | { kind: 'loaded'; nodes: ConfigObjectSummary[] }
  | { kind: 'failed'; reason: string }

function useAudioNodes(): AudioNodesState {
  const [state, setState] = useState<AudioNodesState>({ kind: 'loading' })
  useEffect(() => {
    let cancelled = false
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

/** A show.action target the coordinator can use to run this session: audio-integration, with a session id named on it. */
type AudioActionSessionRef = { id: string; label: string; audioSessionId: string }

type AudioActionSessionsState =
  | { kind: 'loading' }
  | { kind: 'loaded'; actions: AudioActionSessionRef[] }
  | { kind: 'failed'; reason: string }

function useAudioActionSessions(show: string | null): AudioActionSessionsState {
  const [state, setState] = useState<AudioActionSessionsState>({ kind: 'loading' })
  useEffect(() => {
    if (show === null) {
      setState({ kind: 'loaded', actions: [] })
      return
    }
    let cancelled = false
    setState({ kind: 'loading' })
    listConfigObjects('show.action', show)
      .then(async (response) => {
        const loaded = await Promise.all(response.objects.map((object) => getShowAction(object.id)))
        if (cancelled) return
        const actions = loaded
          .filter((action) => action.payload.target.integration === 'audio' && action.payload.target.audioSessionId !== undefined)
          .map((action) => ({
            id: action.id,
            label: action.payload.label,
            audioSessionId: action.payload.target.audioSessionId as string,
          }))
        setState({ kind: 'loaded', actions })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [show])
  return state
}

type ObservationsState =
  | { kind: 'loading' }
  | { kind: 'loaded'; observations: ObservationEntry[] }
  | { kind: 'failed'; reason: string }

/** `reloadKey` forces a re-read after a dispatched command, per the design's "re-read the session's observations" rule. */
function useAudioSessionObservations(reloadKey: number): ObservationsState {
  const [state, setState] = useState<ObservationsState>({ kind: 'loading' })
  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    listObservations('audio_session')
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', observations: response.observations })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [reloadKey])
  return state
}

type LtcFrameRateState = { kind: 'loading' } | { kind: 'loaded'; fps: number } | { kind: 'failed' }

function useLtcFrameRate(): LtcFrameRateState {
  const [state, setState] = useState<LtcFrameRateState>({ kind: 'loading' })
  useEffect(() => {
    let cancelled = false
    getAudioSettingsConfig()
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', fps: Number(response.payload.ltcFrameRate) })
      })
      .catch(() => {
        if (!cancelled) setState({ kind: 'failed' })
      })
    return () => {
      cancelled = true
    }
  }, [])
  return state
}

function audioSessionSignal(observations: ObservationEntry[], sessionId: string, signal: string): ObservationEntry | undefined {
  return observations.find(
    (entry) => entry.resource.kind === 'audio_session' && entry.resource.id === sessionId && entry.signal === signal,
  )
}

/**
 * SM-269 design section 3: target picker (node, then a real session id),
 * a gloved transport row, seek, gain set/fade, mute/unmute, and a typed
 * clear confirmation. `apply` is deliberately excluded — loading media
 * into a session is authoring, not live control.
 */
function AudioSessionsBlock({ gate, show }: { gate: Gate; show: string | null }) {
  const nodesState = useAudioNodes()
  const actionsState = useAudioActionSessions(show)
  const [reloadKey, setReloadKey] = useState(0)
  const observationsState = useAudioSessionObservations(reloadKey)
  const frameRateState = useLtcFrameRate()

  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null)
  const [sessionId, setSessionId] = useState('')
  const [revisionOverride, setRevisionOverride] = useState<string | null>(null)
  const [position, setPosition] = useState('')
  const [gainDb, setGainDb] = useState('')
  const [fadeTargetDb, setFadeTargetDb] = useState('')
  const [fadeDurationMs, setFadeDurationMs] = useState('')
  const [clearConfirm, setClearConfirm] = useState('')
  const [outcome, setOutcome] = useState<CommandOutcome | null>(null)

  useEffect(() => {
    if (selectedNodeId === null && nodesState.kind === 'loaded' && nodesState.nodes[0] !== undefined) {
      setSelectedNodeId(nodesState.nodes[0].id)
    }
  }, [nodesState, selectedNodeId])

  const observations = observationsState.kind === 'loaded' ? observationsState.observations : []
  const actions = actionsState.kind === 'loaded' ? actionsState.actions : []
  const options = audioSessionOptions(observations, actions)
  const trimmedSessionId = sessionId.trim()
  const revisionInfo = trimmedSessionId === '' ? null : deriveAudioSessionRevision(observations, trimmedSessionId)
  const overrideValue =
    revisionOverride !== null && revisionOverride.trim() !== '' && !Number.isNaN(Number(revisionOverride))
      ? Number(revisionOverride)
      : null
  const effectiveRevision = overrideValue ?? revisionInfo?.next ?? 1
  const nodeId = selectedNodeId ?? ''
  const canDispatch = gate.allowed && nodeId !== '' && trimmedSessionId !== ''
  const dispatchTitle = !gate.allowed ? gate.reason : nodeId === '' || trimmedSessionId === '' ? 'Choose a node and a session id first.' : undefined

  const positionMs =
    position.trim() === '' ? null : timecodeToMillis(position, frameRateState.kind === 'loaded' ? frameRateState.fps : null)

  const run = useCallback((action: string, call: () => Promise<AudioSessionCommandResult>) => {
    call()
      .then((result) => {
        setOutcome(describeAudioSessionOutcome(result, action))
        setReloadKey((n) => n + 1)
      })
      .catch((err: unknown) => setOutcome({ tone: 'bad', label: 'Refused', detail: `${action}: ${describeApiError(err)}` }))
  }, [])

  const stateEntry = trimmedSessionId === '' ? undefined : audioSessionSignal(observations, trimmedSessionId, 'audio_session.state')
  const positionEntry = trimmedSessionId === '' ? undefined : audioSessionSignal(observations, trimmedSessionId, 'audio_session.position_ms')
  const gainEntry = trimmedSessionId === '' ? undefined : audioSessionSignal(observations, trimmedSessionId, 'audio_session.gain.effective')

  return (
    <Section id="lc-audio" title="Audio sessions" aside={<span className="sm-small sm-muted">Dispatched to one node's session</span>}>
      {nodesState.kind === 'loading' ? (
        <RuledStrip absence="loading" label="Reading" fact="Reading this deployment's declared audio nodes." />
      ) : nodesState.kind === 'failed' ? (
        <RuledStrip absence="failed" label="Read failed" fact={nodesState.reason} />
      ) : nodesState.nodes.length === 0 ? (
        <RuledStrip
          absence="empty"
          label="No audio nodes"
          fact="No node advertises an audio engine."
          detail="Settings › Node routing is where an audio.node object is declared."
        />
      ) : (
        <>
          <div className="sm-panel">
            <h3 className="sm-subsection__title">Target</h3>
            <Field label="Node">
              {(props) => (
                <Select {...props} value={nodeId} onChange={(event) => setSelectedNodeId(event.target.value)}>
                  {nodesState.nodes.map((node) => (
                    <option key={node.id} value={node.id}>
                      {node.label}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
            {options.length > 0 && (
              <Field label="Known sessions" help="Fills the session id below. A session neither source knows can still be typed there.">
                {(props) => (
                  <Select
                    {...props}
                    value=""
                    onChange={(event) => {
                      if (event.target.value !== '') setSessionId(event.target.value)
                    }}
                  >
                    <option value="">Choose a known session</option>
                    {options.map((option) => (
                      <option key={option.sessionId} value={option.sessionId}>
                        {option.sessionId} ({option.origin})
                      </option>
                    ))}
                  </Select>
                )}
              </Field>
            )}
            {options.length === 0 && (
              <RuledStrip
                absence="unobserved"
                label="No sessions observed"
                fact="No audio session has ever reported to this coordinator. Type the session id the show action or night session uses."
              />
            )}
            <Field label="Session id">
              {(props) => (
                <Input {...props} value={sessionId} onChange={(event) => setSessionId(event.target.value)} placeholder="e.g. bg-holiday-01" />
              )}
            </Field>
            {trimmedSessionId !== '' && (
              <p className="sm-small sm-data">
                {revisionInfo === null || revisionInfo.observed === null
                  ? 'Never observed. Sending revision 1.'
                  : `Revision ${revisionInfo.observed} → ${revisionInfo.next}`}
              </p>
            )}
            {revisionOverride === null ? (
              <ButtonRow>
                <Button size="compact" variant="quiet" onClick={() => setRevisionOverride(String(effectiveRevision))}>
                  Set revision
                </Button>
              </ButtonRow>
            ) : (
              <Field label="Revision override" help="For a wedged ledger. Overrides the value above.">
                {(props) => (
                  <Input
                    {...props}
                    type="number"
                    min={0}
                    value={revisionOverride}
                    onChange={(event) => setRevisionOverride(event.target.value)}
                  />
                )}
              </Field>
            )}
            {trimmedSessionId !== '' && (stateEntry !== undefined || positionEntry !== undefined || gainEntry !== undefined) && (
              <p className="sm-small sm-muted">
                {stateEntry !== undefined && (
                  <>
                    State: <span className="sm-data">{String(stateEntry.value)}</span>{' '}
                  </>
                )}
                {positionEntry !== undefined && typeof positionEntry.value === 'number' && (
                  <>
                    Position:{' '}
                    <span className="sm-data">
                      {millisToTimecode(positionEntry.value, frameRateState.kind === 'loaded' ? frameRateState.fps : null)}
                    </span>{' '}
                  </>
                )}
                {gainEntry !== undefined && typeof gainEntry.value === 'number' && (
                  <>
                    Gain: <span className="sm-data">{gainEntry.value} dB</span>
                  </>
                )}
              </p>
            )}
          </div>

          <div className="sm-panel">
            <h3 className="sm-subsection__title">Transport</h3>
            <ButtonRow>
              <Button size="gloved" disabled={!canDispatch} title={dispatchTitle} onClick={() => run('Prepare', () => prepareAudioSession(nodeId, trimmedSessionId, effectiveRevision))}>
                Prepare
              </Button>
              <Button size="gloved" disabled={!canDispatch} title={dispatchTitle} onClick={() => run('Start', () => startAudioSession(nodeId, trimmedSessionId, effectiveRevision))}>
                Start
              </Button>
              <Button size="gloved" disabled={!canDispatch} title={dispatchTitle} onClick={() => run('Pause', () => pauseAudioSession(nodeId, trimmedSessionId, effectiveRevision))}>
                Pause
              </Button>
              <Button size="gloved" disabled={!canDispatch} title={dispatchTitle} onClick={() => run('Resume', () => resumeAudioSession(nodeId, trimmedSessionId, effectiveRevision))}>
                Resume
              </Button>
              <Button size="gloved" disabled={!canDispatch} title={dispatchTitle} onClick={() => run('Advance', () => advanceAudioSession(nodeId, trimmedSessionId, effectiveRevision))}>
                Advance
              </Button>
              <Button variant="danger" size="gloved" disabled={!canDispatch} title={dispatchTitle} onClick={() => run('Stop', () => stopAudioSession(nodeId, trimmedSessionId, effectiveRevision))}>
                Stop
              </Button>
            </ButtonRow>
            <div className="sm-volume">
              <Field
                label="Position"
                help={
                  frameRateState.kind === 'loaded'
                    ? `hh:mm:ss.ff at ${frameRateState.fps} fps, or a bare millisecond count.`
                    : 'hh:mm:ss, or a bare millisecond count. Frame rate unavailable.'
                }
              >
                {(props) => <Input {...props} value={position} onChange={(event) => setPosition(event.target.value)} placeholder="00:01:30" />}
              </Field>
              <Button
                disabled={!canDispatch || positionMs === null}
                title={!canDispatch ? dispatchTitle : positionMs === null ? 'Not a recognized timecode.' : undefined}
                onClick={() => positionMs !== null && run('Seek', () => seekAudioSession(nodeId, trimmedSessionId, effectiveRevision, positionMs))}
              >
                Seek
              </Button>
            </div>
          </div>

          <div className="sm-panel">
            <h3 className="sm-subsection__title">Gain</h3>
            <div className="sm-volume">
              <Field label="Gain" help="Decibels. 0 dB is unity; +12 dB is the refused ceiling.">
                {(props) => <Input {...props} type="number" max={12} value={gainDb} onChange={(event) => setGainDb(event.target.value)} />}
              </Field>
              <Button
                disabled={!canDispatch || gainDb.trim() === ''}
                title={dispatchTitle}
                onClick={() => run('Set gain', () => setAudioSessionGain(nodeId, trimmedSessionId, effectiveRevision, Number(gainDb)))}
              >
                Set
              </Button>
            </div>
            <div className="sm-volume">
              <Field label="Fade to" help="Decibels.">
                {(props) => (
                  <Input {...props} type="number" max={12} value={fadeTargetDb} onChange={(event) => setFadeTargetDb(event.target.value)} />
                )}
              </Field>
              <Field label="Over" help="Milliseconds. Leave empty for this node's own fade duration.">
                {(props) => (
                  <Input {...props} type="number" min={1} value={fadeDurationMs} onChange={(event) => setFadeDurationMs(event.target.value)} />
                )}
              </Field>
              <Button
                disabled={!canDispatch || fadeTargetDb.trim() === ''}
                title={dispatchTitle}
                onClick={() =>
                  run('Fade gain', () =>
                    fadeAudioSessionGain(
                      nodeId,
                      trimmedSessionId,
                      effectiveRevision,
                      Number(fadeTargetDb),
                      fadeDurationMs.trim() === '' ? undefined : Number(fadeDurationMs),
                    ),
                  )
                }
              >
                Fade
              </Button>
            </div>
            <p className="sm-small sm-faint">Curve: linear. The only curve this build ships.</p>
            <ButtonRow>
              <Button size="gloved" disabled={!canDispatch} title={dispatchTitle} onClick={() => run('Mute', () => muteAudioSessionOutput(nodeId, trimmedSessionId, effectiveRevision))}>
                Mute
              </Button>
              <Button size="gloved" disabled={!canDispatch} title={dispatchTitle} onClick={() => run('Unmute', () => unmuteAudioSessionOutput(nodeId, trimmedSessionId, effectiveRevision))}>
                Unmute
              </Button>
            </ButtonRow>
          </div>

          <div className="sm-panel">
            <h3 className="sm-subsection__title">Clear this session</h3>
            <p className="sm-small sm-muted">Releases the session entirely on the node. Discards a loaded session mid-show.</p>
            <Field label={trimmedSessionId === '' ? 'Type the session id to confirm' : `Type ${trimmedSessionId} to confirm`}>
              {(props) => <Input {...props} value={clearConfirm} onChange={(event) => setClearConfirm(event.target.value)} />}
            </Field>
            <ButtonRow>
              <Button
                variant="danger"
                disabled={!canDispatch || clearConfirm !== trimmedSessionId}
                title={!canDispatch ? dispatchTitle : clearConfirm !== trimmedSessionId ? 'Type the session id exactly to enable this.' : undefined}
                onClick={() => run('Clear', () => clearAudioSession(nodeId, trimmedSessionId, effectiveRevision))}
              >
                Clear
              </Button>
            </ButtonRow>
          </div>

          <p className="sm-section__footnote">
            Loading media into a session is authoring, not live control. A session gets its content from its cue, its playlist, or an audio
            action.
          </p>

          <Outcome outcome={outcome} />
        </>
      )}
    </Section>
  )
}

function ResolumeQuickStrip({ gate }: { gate: Gate }) {
  const [outcome, setOutcome] = useState<ResolumeActionResult | null>(null)
  const [error, setError] = useState<string | null>(null)

  const blackout = () => {
    setError(null)
    blackoutResolume().then(setOutcome).catch((err: unknown) => setError(describeApiError(err)))
  }

  return (
    <Section id="lc-resolume" title="Resolume" aside={<Link to="/control/resolume">Open Resolume control →</Link>}>
      <p className="sm-small sm-muted">The clip grid and layer controls have their own wide workspace. Blackout remains here for immediate recovery.</p>
      <ButtonRow>
        <Button variant="danger" size="gloved" disabled={!gate.allowed} title={gate.allowed ? undefined : gate.reason} onClick={blackout}>Blackout</Button>
      </ButtonRow>
      {outcome !== null && <ResolumeOutcome result={outcome} />}
      {error !== null && <RuledStrip absence="failed" label="Dispatch failed" fact={error} />}
    </Section>
  )
}

function resolumeTone(result: ResolumeActionResult): 'good' | 'warn' | 'bad' | 'unknown' {
  if (result.outcome === 'confirmed') return 'good'
  if (result.outcome === 'unconfirmed' || result.outcome === 'unconfirmable') return 'warn'
  if (result.outcome === 'refused' || result.outcome === 'failed') return 'bad'
  return 'unknown'
}

function resolumeLabel(result: ResolumeActionResult): string {
  if (result.outcome === '') return result.replay ? 'Replay pending' : 'Outcome pending'
  return result.outcome.charAt(0).toUpperCase() + result.outcome.slice(1)
}

function ResolumeOutcome({ result }: { result: ResolumeActionResult }) {
  return (
    <div className="sm-outcome">
      <StatusPair tone={resolumeTone(result)} label={resolumeLabel(result)} />
      <p className="sm-outcome__detail">{result.outcomeReason || 'The prior dispatch is still resolving.'}{result.replay ? ' This response reuses the original dispatch.' : ''}{result.attributionDegraded ? ' Attribution is degraded because the audit record could not be written.' : ''}</p>
    </div>
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
      <div className="sm-grid sm-grid--auto sm-control-grid">
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
        <div className="sm-grid sm-grid--auto sm-control-grid">
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
