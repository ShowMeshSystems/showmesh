import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { SitePowerTesting } from './SitePowerTesting'
import {
  ApiError,
  activateCue,
  advanceAudioSession,
  alignedStartAudioSession,
  applyAudioSession,
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
  listFPPPlaylistDefinitions,
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
  type AudioNodeSummary,
  type AudioSessionCommandResult,
  type ConfigObjectSummary,
  type CueActionOutcome,
  type CueActivationNodeOutcome,
  type FPPCommandResult,
  type FPPPlaylistDefinitionMetadata,
  type NightCommandName,
  type ObservationEntry,
  type ResolumeActionResult,
  type WeatherDelayTargetOutcome,
  randomUUIDv4,
} from '../api'
import {
  Button,
  ButtonRow,
  ButtonRule,
  Callout,
  Choice,
  ConfirmDialog,
  DefinitionStrip,
  Drawer,
  Field,
  Input,
  LifecycleCommands,
  Notice,
  RuledStrip,
  Section,
  Segmented,
  Select,
  StatusPair,
  Table,
  TableWrap,
  Textarea,
  Workbench,
} from '../kit'
import {
  instanceOutcomeTone,
  STOP_CONFIRM_DETAIL,
  STOP_CONFIRM_TITLE,
  STOP_POWER_DOWN_CONFIRM_DETAIL,
  STOP_POWER_DOWN_CONFIRM_TITLE,
  useEmergencyStop,
} from '../app/emergencyStop'
import { EmergencyStopOutcome } from '../app/EmergencyStopOutcome'
import { useModelContext } from '../app/ModelContext'
import { audioNodeIdentityText, useFleetNodes } from './audioRouteModel'
import { RESUME_CONFIRM_TITLE, useCurrentNightSession, useResumeShow } from '../app/stopHold'
import { useWeatherDelay } from '../app/WeatherDelayContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { effectiveServerTimeIso, millisToTimecode, timecodeToMillis } from '../domain/time'
import {
  audioRows,
  audioSessionOptions,
  audioSessionSummaries,
  classifyStartPlaylistConflict,
  currentRunsAbsence,
  deriveAudioSessionRevision,
  describeAudioSessionOutcome,
  describeFPPOutcome,
  formatPosition,
  fppPlaylistNames,
  nightLifecycleGroups,
  outputRows,
  parseExactRevisionInput,
  parseScheduledAtNsInput,
  audioTimelineRows,
  describeAlignedStart,
  reportedPlaylistName,
  transportState,
  type CommandOutcome,
  type StartPlaylistConflictReason,
} from './liveControlModel'
import { usePrepareSiteConfirm } from './usePrepareSiteConfirm'

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

type PlaylistDefinitionsState =
  | { kind: 'loading' }
  | { kind: 'loaded'; definitions: FPPPlaylistDefinitionMetadata[] }
  | { kind: 'failed' }

/** FPP's imported playlist catalog. Failure degrades to the typed fallback silently; this is a convenience list, not a gate. */
function useFPPPlaylistDefinitions(): PlaylistDefinitionsState {
  const [state, setState] = useState<PlaylistDefinitionsState>({ kind: 'loading' })
  useEffect(() => {
    let cancelled = false
    listFPPPlaylistDefinitions()
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', definitions: response.definitions })
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

function Outcome({ outcome }: { outcome: CommandOutcome | null }) {
  if (outcome === null) return null
  return (
    <div className="sm-outcome">
      <StatusPair tone={outcome.tone} label={outcome.label} />
      <p className="sm-outcome__detail">{outcome.detail}</p>
    </div>
  )
}

type WeatherDelayActionName = 'start' | 'cancelNight' | 'resume'

const WEATHER_DELAY_ACTION_LABEL: Record<WeatherDelayActionName, string> = {
  start: 'Weather delay',
  cancelNight: 'Cancel night',
  resume: 'Resume',
}

/** The five target-kind groups a weather-delay action's own targets fall into, in display order - mirrors [STOP_OUTCOME_TARGET_KIND_GROUPS] plus render surfaces and the weatherdelay.start/resume node command itself (ADR-053 decision 8). */
const WEATHER_DELAY_TARGET_KIND_GROUPS: Array<{ kind: string; label: string; column: string }> = [
  { kind: 'fpp', label: 'FPP', column: 'FPP instance' },
  { kind: 'node', label: 'audio node', column: 'Audio node' },
  { kind: 'resolume', label: 'Resolume', column: 'Resolume instance' },
  { kind: 'render', label: 'render surface', column: 'Render surface' },
  { kind: 'node-command', label: 'node command', column: 'Target' },
]

function groupWeatherDelayTargetsByKind(
  targets: WeatherDelayTargetOutcome[],
): Array<{ kind: string; label: string; column: string; targets: WeatherDelayTargetOutcome[] }> {
  return WEATHER_DELAY_TARGET_KIND_GROUPS.map((g) => ({ ...g, targets: targets.filter((row) => row.targetKind === g.kind) }))
}

/**
 * Reuses [instanceOutcomeTone] and the same grouped-table shape
 * [EmergencyStopOutcome] renders: what each target's own outcome was, per
 * kind, with no combined pass/fail rollup - a node reached by only one of
 * its two delivery paths (mqtt/http) still shows as its own row, via
 * `deliveredVia`. [WeatherDelayActionResult.notSavedMessage] is the
 * coordinator's own operator sentence, rendered verbatim.
 */
function WeatherDelayOutcomePanel() {
  const weatherDelay = useWeatherDelay()
  const { outcome } = weatherDelay
  if (outcome.kind === 'idle') return null
  if (outcome.kind === 'error') {
    const headline =
      outcome.status !== undefined
        ? `${WEATHER_DELAY_ACTION_LABEL[outcome.action]} was refused. ${outcome.message}`
        : `${WEATHER_DELAY_ACTION_LABEL[outcome.action]}: the coordinator reported no outcome. ${outcome.message}`
    return <Notice tone="bad" headline={headline} />
  }
  const { action, result } = outcome
  const groups = groupWeatherDelayTargetsByKind(result.targets)
  return (
    <div className="sm-lc-emergency__outcome">
      <p className="sm-small">
        <strong>{WEATHER_DELAY_ACTION_LABEL[action]}</strong> was dispatched.
      </p>
      {result.notSaved === true && result.notSavedMessage !== undefined && (
        <Notice tone="warn" live="status" headline={result.notSavedMessage} />
      )}
      {groups.map(
        (group) =>
          group.targets.length > 0 && (
            <TableWrap key={group.kind} label={`${WEATHER_DELAY_ACTION_LABEL[action]}, per ${group.label} target`}>
              <Table minWidth={480}>
                <thead>
                  <tr>
                    <th scope="col">{group.column}</th>
                    <th scope="col">Outcome</th>
                  </tr>
                </thead>
                <tbody>
                  {group.targets.map((row) => (
                    <tr key={`${row.targetKind}:${row.instanceId}`}>
                      <td>
                        <span className="sm-data">{row.instanceId}</span>
                      </td>
                      <td>
                        <StatusPair tone={instanceOutcomeTone(row.outcome)} label={row.outcome} />
                        <p className="sm-small sm-muted">{row.outcomeReason}</p>
                        {row.deliveredVia !== undefined && (
                          <p className="sm-small sm-faint">Delivered via {row.deliveredVia}.</p>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            </TableWrap>
          ),
      )}
    </div>
  )
}

/**
 * ADR-053 decision 7's own alert asset, per plan node, from GET
 * /weather-delay's `assets` field. Power-group "confirmed dark" renders in
 * the shell's own weather-delay banner (every screen), not here.
 */
function WeatherDelayReadiness() {
  const weatherDelay = useWeatherDelay()
  if (weatherDelay.state.kind !== 'loaded') return null
  const assets = weatherDelay.state.response.assets ?? []
  if (assets.length === 0) return null
  return (
    <TableWrap label="Weather delay, alert readiness per node">
      <Table minWidth={480}>
        <thead>
          <tr>
            <th scope="col">Node</th>
            <th scope="col">Delay alert</th>
            <th scope="col">Cancel-night alert</th>
          </tr>
        </thead>
        <tbody>
          {assets.map((row) => (
            <tr key={row.nodeId}>
              <td>
                <span className="sm-data">{row.nodeId}</span>
              </td>
              <td>
                {row.delayAsset === undefined ? (
                  <span className="sm-small sm-faint">Not configured</span>
                ) : (
                  <StatusPair
                    tone={row.delayAsset.present ? 'good' : 'bad'}
                    label={row.delayAsset.present ? 'On node' : 'Missing on node'}
                  />
                )}
                {row.delayAsset?.filename !== undefined && <p className="sm-small sm-muted">{row.delayAsset.filename}</p>}
              </td>
              <td>
                {row.cancelNightAsset === undefined ? (
                  <span className="sm-small sm-faint">Not configured</span>
                ) : (
                  <StatusPair
                    tone={row.cancelNightAsset.present ? 'good' : 'bad'}
                    label={row.cancelNightAsset.present ? 'On node' : 'Missing on node'}
                  />
                )}
                {row.cancelNightAsset?.filename !== undefined && (
                  <p className="sm-small sm-muted">{row.cancelNightAsset.filename}</p>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </Table>
    </TableWrap>
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
  const {
    gate: emergencyGate,
    outcome: emergencyOutcome,
    busy: emergencyBusy,
    hardStopArm,
    hardStopArmBusy,
    hardStopArmError,
    armRemainingMs,
    armExpired,
    stop: runStop,
    stopPowerDown: runStopPowerDown,
    armHardStop,
    fireHardStop,
  } = useEmergencyStop(model)
  const weatherDelayGate = evaluateScope(model.session, model.sessionFetchFailed, 'show:weatherdelay:invoke')
  const weatherDelay = useWeatherDelay()
  const resumeShow = useResumeShow(model, useCurrentNightSession(model))

  const [selected, setSelected] = useState<string | null>(null)
  const instance = model.fpp.find((entry) => entry.instanceId === selected) ?? model.fpp[0]
  const [outcome, setOutcome] = useState<CommandOutcome | null>(null)
  const [nightOutcome, setNightOutcome] = useState<CommandOutcome | null>(null)
  const [volume, setVolume] = useState('')
  const [startPlaylistName, setStartPlaylistName] = useState('')
  const [startRepeat, setStartRepeat] = useState(false)
  const [startState, setStartState] = useState<StartPlaylistState>({ kind: 'idle' })
  const [skipEnterShowLead, setSkipEnterShowLead] = useState(false)
  const [prepareSiteKey, setPrepareSiteKey] = useState(() => randomUUIDv4())
  const [startNightConfirmOpen, setStartNightConfirmOpen] = useState(false)
  const [stopConfirmOpen, setStopConfirmOpen] = useState(false)
  const [powerDownConfirmOpen, setPowerDownConfirmOpen] = useState(false)
  const [resolumeBlackoutOutcome, setResolumeBlackoutOutcome] = useState<ResolumeActionResult | null>(null)
  const [resolumeBlackoutError, setResolumeBlackoutError] = useState<string | null>(null)
  const runResolumeBlackout = useCallback(() => {
    setResolumeBlackoutError(null)
    blackoutResolume()
      .then(setResolumeBlackoutOutcome)
      .catch((err: unknown) => setResolumeBlackoutError(describeApiError(err)))
  }, [])

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

  const night = useCallback(
    (command: NightCommandName, stopFppPlayback?: boolean) => {
      dispatchNightCommand(
        command,
        command === 'prepare-site' ? prepareSiteKey : undefined,
        undefined,
        command === 'start-night' ? skipEnterShowLead : undefined,
        command === 'prepare-site' ? stopFppPlayback : undefined,
      )
        .then((response) => {
          if (command === 'start-night') setSkipEnterShowLead(false)
          if (command === 'prepare-site') setPrepareSiteKey(randomUUIDv4())
          const reason = response.command.reason
          setNightOutcome({
            tone: 'warn',
            label: 'Accepted',
            detail: `${command} was accepted. Show Night shows what the session does.${reason === undefined || reason === '' ? '' : ` ${reason}`}`,
          })
        })
        .catch((err: unknown) => {
          if (command === 'prepare-site') setPrepareSiteKey(randomUUIDv4())
          setNightOutcome({ tone: 'bad', label: 'Refused', detail: `${command}: ${describeApiError(err)}` })
        })
    },
    [skipEnterShowLead, prepareSiteKey],
  )

  const prepareSite = usePrepareSiteConfirm((stop) => night('prepare-site', stop))

  const state = instance === undefined ? null : transportState(instance)
  const rows = [...outputRows(model, nowIso), ...audioRows(model, nowIso)]
  const confirmed = rows.filter((row) => row.confirmed).length
  const runsAbsence = currentRunsAbsence(model)

  const playlistDefinitions = useFPPPlaylistDefinitions()
  const playlistNames =
    instance?.instanceUuid == null || playlistDefinitions.kind !== 'loaded'
      ? []
      : fppPlaylistNames(playlistDefinitions.definitions, instance.instanceUuid)
  const reportedPlaylistNow = state === null ? null : reportedPlaylistName(state)
  const reportedPlaylistMissing = reportedPlaylistNow !== null && !playlistNames.includes(reportedPlaylistNow)
  // The select must always be able to show what state holds: when FPP reports a
  // playlist the coordinator never imported, add it rather than leave the select
  // blank while the (non-empty) state still enables Start playlist.
  const selectablePlaylistNames = reportedPlaylistMissing ? [reportedPlaylistNow, ...playlistNames] : playlistNames
  useEffect(() => {
    if (reportedPlaylistNow !== null) setStartPlaylistName((current) => (current === '' ? reportedPlaylistNow : current))
  }, [reportedPlaylistNow])

  const outputsSection = (
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
                ? 'This coordinator does not report what program audio is playing.'
                : 'Reading current-run state for program audio.'
            }
          />
        )}
        {rows.length === 0 ? (
          <RuledStrip
            absence="unobserved"
            label="Unobserved"
            fact="No output has reported what it is doing."
            detail="Outputs may still be running."
          />
        ) : (
          <>
            <TableWrap label="Outputs, scrollable">
              <Table minWidth={600}>
                <thead>
                  <tr>
                    <th scope="col">Output</th>
                    <th scope="col">Doing what</th>
                    <th scope="col" className="sm-lc-output-when">Last confirmed</th>
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
                      <td className="sm-lc-output-when">
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
        <div className="sm-lc-output-audio">
          <AudioSessionsBlock gate={audioGate} show={show} nowIso={nowIso} />
        </div>
      </Section>
  )
  const emergencySection = (
      <Section id="lc-emergency" title="Emergency stop">
        <ConfirmDialog
          open={stopConfirmOpen}
          title={STOP_CONFIRM_TITLE}
          detail={STOP_CONFIRM_DETAIL}
          confirmLabel="Stop"
          onConfirm={() => {
            setStopConfirmOpen(false)
            runStop()
          }}
          onCancel={() => setStopConfirmOpen(false)}
        />
        <ConfirmDialog
          open={powerDownConfirmOpen}
          title={STOP_POWER_DOWN_CONFIRM_TITLE}
          detail={STOP_POWER_DOWN_CONFIRM_DETAIL}
          confirmLabel="Stop and power down"
          onConfirm={() => {
            setPowerDownConfirmOpen(false)
            runStopPowerDown()
          }}
          onCancel={() => setPowerDownConfirmOpen(false)}
        />
        <div className="sm-lc-emergency">
          <ButtonRow>
            <Button
              variant="danger"
              size="gloved"
              disabled={!emergencyGate.allowed || emergencyBusy !== false}
              title={emergencyGate.allowed ? 'Stops every FPP instance and runs the site stop actions.' : emergencyGate.reason}
              onClick={() => setStopConfirmOpen(true)}
            >
              Stop
            </Button>
            <Button variant="primary" size="gloved" disabled={resumeShow.disabled} title={resumeShow.title} onClick={resumeShow.onClick}>
              {resumeShow.busy ? 'Resuming…' : 'Resume'}
            </Button>
            <Button
              variant="danger"
              size="gloved"
              disabled={!emergencyGate.allowed || emergencyBusy !== false}
              title={emergencyGate.allowed ? 'Stops everything, then powers the night session down.' : emergencyGate.reason}
              onClick={() => setPowerDownConfirmOpen(true)}
            >
              Stop and power down
            </Button>
            <Button
              variant="danger"
              size="gloved"
              disabled={!resolumeGate.allowed}
              title={resolumeGate.allowed ? 'Blacks out Resolume immediately, without stopping FPP.' : resolumeGate.reason}
              onClick={runResolumeBlackout}
            >
              Resolume blackout
            </Button>
              <Button
                size="gloved"
                disabled={!emergencyGate.allowed || hardStopArmBusy}
                title={emergencyGate.allowed ? 'Stops everything and abandons the night session now. Arm first, then Fire.' : emergencyGate.reason}
                onClick={armHardStop}
              >
                Arm hard stop
              </Button>
              <Button
                variant="danger"
                size="gloved"
                disabled={!emergencyGate.allowed || hardStopArm === null || armExpired}
                title={
                  !emergencyGate.allowed
                    ? emergencyGate.reason
                    : hardStopArm === null
                      ? 'Arm hard stop first.'
                      : armExpired
                        ? 'The arm token expired. Arm again, then fire promptly.'
                        : undefined
                }
                onClick={fireHardStop}
              >
                Fire hard stop
              </Button>
            <Button
              variant="danger"
              size="gloved"
              disabled={!weatherDelayGate.allowed || weatherDelay.busy !== false}
              title={
                weatherDelayGate.allowed
                  ? 'Stops the display and starts the weather alert. Dark until you resume.'
                  : weatherDelayGate.reason
              }
              onClick={weatherDelay.start}
            >
              {weatherDelay.busy === 'start' ? 'Starting…' : 'Weather delay'}
            </Button>
            <Button
              variant="danger"
              size="gloved"
              disabled={!weatherDelayGate.allowed || weatherDelay.busy !== false}
              title={
                weatherDelayGate.allowed
                  ? 'Stops the display, starts the cancel alert and powers down. No resume tonight.'
                  : weatherDelayGate.reason
              }
              onClick={weatherDelay.cancelNight}
            >
              {weatherDelay.busy === 'cancelNight' ? 'Cancelling night…' : 'Cancel night'}
            </Button>
          </ButtonRow>
          {resumeShow.error !== null && <Notice tone="bad" headline={`Resume was refused: ${resumeShow.error}`} />}
          <ConfirmDialog
            open={resumeShow.confirmOpen}
            title={RESUME_CONFIRM_TITLE}
            detail={null}
            confirmLabel="Resume"
            onConfirm={resumeShow.onConfirm}
            onCancel={resumeShow.onCancel}
          />
          {hardStopArmError !== null && <Notice tone="bad" headline={`Arm was refused: ${hardStopArmError}`} />}
          {hardStopArm !== null && armRemainingMs !== null && (
            <Notice
              tone="warn"
              live="status"
              headline={
                armExpired
                  ? 'The arm token expired. Arm again, then fire promptly.'
                  : `Armed. Fire within ${Math.max(0, Math.ceil(armRemainingMs / 1000))}s, or arm again to reset the window.`
              }
            />
          )}
          {resolumeBlackoutOutcome !== null && <ResolumeOutcome result={resolumeBlackoutOutcome} />}
          {resolumeBlackoutError !== null && <RuledStrip absence="failed" label="Dispatch failed" fact={resolumeBlackoutError} />}

          <EmergencyStopOutcome outcome={emergencyOutcome} />
          <WeatherDelayOutcomePanel />
          <WeatherDelayReadiness />
        </div>
      </Section>
  )
  return (
    <>
      <PageHeader />

      <Workbench
        sideLabel="Emergency stop and output status"
        side={<>{emergencySection}{outputsSection}</>}
        main={<>

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
          <div className="sm-panel sm-lc-transport">
            <div className="sm-lc-transport__head">
              <p>
                <span className="sm-data">{reportedPlaylistName(state) ?? 'No playlist reported'}</span>
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
              <p>
                <span className="sm-data">{state.playerState ?? 'Player state not reported'}</span>
                {state.elapsedSeconds !== null && (
                  <>
                    {' · '}
                    <span className="sm-data">{formatPosition(state.elapsedSeconds) ?? ''}</span>
                  </>
                )}
                {state.totalSeconds !== null && (
                  <>
                    {' / '}
                    <span className="sm-data">{formatPosition(state.totalSeconds) ?? ''}</span>
                  </>
                )}
              </p>
            </div>
            <div className="sm-lc-transport__body">
              <div className="sm-lc-transport__playlist-row">
                {selectablePlaylistNames.length > 0 ? (
                  <Field label="Playlist">
                    {(field) => (
                      <Select {...field} value={startPlaylistName} onChange={(event) => setStartPlaylistName(event.target.value)}>
                        <option value="">Choose a playlist</option>
                        {selectablePlaylistNames.map((name) => (
                          <option key={name} value={name}>
                            {reportedPlaylistMissing && name === reportedPlaylistNow ? `${name} (reported by FPP)` : name}
                          </option>
                        ))}
                      </Select>
                    )}
                  </Field>
                ) : (
                  <Field
                    label="Playlist name"
                    help="Type the exact playlist name."
                  >
                    {(field) => (
                      <Input
                        {...field}
                        value={startPlaylistName}
                        onChange={(event) => setStartPlaylistName(event.target.value)}
                        placeholder="e.g. Standard Show"
                      />
                    )}
                  </Field>
                )}
                <Choice
                  type="checkbox"
                  checked={startRepeat}
                  onChange={(event) => setStartRepeat(event.target.checked)}
                  label="Repeat"
                />
                <Button
                  variant="primary"
                  size="gloved"
                  className="sm-lc-transport__nowrap"
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
              <div className="sm-lc-transport__controls">
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
                  <ButtonRule />
                  <Input
                    aria-label="Volume, 0 to 100"
                    className="sm-lc-volume-input"
                    type="number"
                    min={0}
                    max={100}
                    value={volume}
                    placeholder={state.volume === null ? 'Vol' : String(state.volume)}
                    title={state.volume === null ? 'This instance does not report its volume.' : 'Volume, 0 to 100'}
                    onChange={(event) => setVolume(event.target.value)}
                  />
                  <Button
                    size="gloved"
                    disabled={!commandGate.allowed || volume.trim() === ''}
                    title={commandGate.allowed ? undefined : commandGate.reason}
                    onClick={() => run('Set volume', () => setFPPVolume(instance.instanceId, Number(volume)))}
                  >
                    Set volume
                  </Button>
                </ButtonRow>
              </div>
              <Outcome outcome={outcome} />
            </div>
          </div>
        )}
      </Section>


      <Section id="lc-lifecycle" title="Night lifecycle" aside={<Link to="/night">Show Night →</Link>}>
        <LifecycleCommands
          groups={nightLifecycleGroups(nightGate, (command) =>
            command === 'start-night' ? setStartNightConfirmOpen(true) : command === 'prepare-site' ? prepareSite.start() : night(command),
          )}
        />
        {prepareSite.dialog}
        <SitePowerTesting />
        <ConfirmDialog
          open={startNightConfirmOpen}
          title="Start the night and its first cycle?"
          detail={
            <>
              <p className="sm-body">This starts the show. The session reports what it does.</p>
              <Choice
                type="checkbox"
                checked={skipEnterShowLead}
                onChange={(event) => setSkipEnterShowLead(event.target.checked)}
                label="Skip the enter-show lead. An enter-show announcement cue still dispatches."
              />
            </>
          }
          confirmLabel="Start night"
          onConfirm={() => {
            setStartNightConfirmOpen(false)
            night('start-night')
          }}
          onCancel={() => setStartNightConfirmOpen(false)}
        />
        <Outcome outcome={nightOutcome} />
      </Section>

      <Announcements show={show} />

      <RunList
        id="lc-macros"
        title="Macros"
        kindLabel="show.macro"
        show={show}
        list={macros}
        gate={configGate}
        onRun={(id) => submitMacroRun(id)}
      />

      <RunList
        id="lc-actions"
        title="Actions"
        kindLabel="show.action"
        show={show}
        list={actions}
        gate={configGate}
        onRun={(id) => invokeAction(id)}
      />

      <Callout>Lists above cover the active show only.</Callout>
      </>}
      />
    </>
  )
}

/**
 * Cues that declare an announcement output. Fire calls POST
 * /cues/{id}/activate: accepted, then each node's own outcome,
 * never a claimed success.
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

/** One cue row's own Fire attempt: in flight, or its own reported per-node outcomes, or a refusal before any node was ever reached. */
type AnnouncementFireState =
  | { kind: 'firing' }
  | { kind: 'reported'; nodes: CueActivationNodeOutcome[]; actions: CueActionOutcome[]; aligned: boolean; unalignedReason: string; scheduledAtNs: number | null }
  | { kind: 'refused'; message: string }

function AnnouncementFireOutcome({ state }: { state: AnnouncementFireState | undefined }) {
  if (state === undefined || state.kind === 'firing') return null
  if (state.kind === 'refused') {
    return (
      <div className="sm-outcome">
        <StatusPair tone="bad" label="Refused" />
        <p className="sm-outcome__detail">{state.message}</p>
      </div>
    )
  }
  return (
    <div className="sm-outcome">
      <StatusPair tone="warn" label="Accepted" />
      <p className="sm-outcome__detail">Accepted, not finished. Each node reports its own outcome below.</p>
      {state.aligned && state.scheduledAtNs !== null && (
        <p className="sm-small">
          <StatusPair tone="good" label="Aligned" />
          {' Every node started at the same shared time.'}
        </p>
      )}
      {!state.aligned && (
        <p className="sm-small">
          <StatusPair tone="warn" label="Unaligned" />
          {state.unalignedReason !== '' ? ` ${state.unalignedReason}` : ' Started on arrival, not to a shared time.'}
        </p>
      )}
      {state.nodes.map((n) => (
        <p key={n.nodeId} className="sm-small">
          <StatusPair tone={instanceOutcomeTone(n.outcome)} label={`${n.nodeId}: ${n.outcome}`} />
          {n.outcomeReason !== undefined && n.outcomeReason !== '' ? ` ${n.outcomeReason}` : ''}
          {describeStartTrigger(n) !== null ? ` ${describeStartTrigger(n)}` : ''}
        </p>
      ))}
      {state.actions.map((a) => (
        <p key={a.actionId} className="sm-small">
          <StatusPair tone={instanceOutcomeTone(a.outcome)} label={`${a.label ?? a.actionId}: ${a.outcome}`} />
          {a.outcomeReason !== undefined && a.outcomeReason !== '' ? ` ${a.outcomeReason}` : ''}
        </p>
      ))}
    </div>
  )
}

/**
 * ADR-051 decision 6's own operator copy for one node's start: fact
 * first, then the action, matching the copy standard. Null when the node
 * carries no startTrigger at all (a render-only cue, or one that used the
 * shared start instant instead).
 */
function describeStartTrigger(n: CueActivationNodeOutcome): string | null {
  if (n.startTrigger === 'multisync') {
    return `Started by MultiSync, ${n.startLeadMs ?? 0} ms lead.`
  }
  if (n.startTrigger === 'coordinator') {
    return 'Started by coordinator (fallback: no MultiSync packet).'
  }
  return null
}

function Announcements({ show }: { show: string | null }) {
  const [cues, setCues] = useState<AnnouncementCue[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [fireState, setFireState] = useState<Record<string, AnnouncementFireState>>({})

  const fireCue = useCallback((cueId: string) => {
    setFireState((s) => ({ ...s, [cueId]: { kind: 'firing' } }))
    activateCue(cueId)
      .then((resp) => {
        setFireState((s) => ({
          ...s,
          [cueId]: {
            kind: 'reported',
            nodes: resp.nodes,
            // A coordinator older than cue actions omits the field.
            actions: resp.actions ?? [],
            aligned: resp.aligned,
            unalignedReason: resp.unalignedReason ?? '',
            scheduledAtNs: resp.scheduledAtNs ?? null,
          },
        }))
      })
      .catch((err: unknown) => {
        setFireState((s) => ({ ...s, [cueId]: { kind: 'refused', message: describeApiError(err) } }))
      })
  }, [])

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
                      ? 'This cue has no audio output.'
                      : 'Its audio asset has not been uploaded.'}{' '}
                    <Link to="/assets">Show Assets</Link>
                  </p>
                )}
                <AnnouncementFireOutcome state={fireState[cue.id]} />
              </div>
              {cue.uploaded ? (
                <Button
                  variant="primary"
                  size="gloved"
                  disabled={fireState[cue.id]?.kind === 'firing'}
                  onClick={() => fireCue(cue.id)}
                >
                  {fireState[cue.id]?.kind === 'firing' ? 'Firing…' : 'Fire'}
                </Button>
              ) : (
                <Button variant="primary" size="gloved" disabled title="Its audio asset has not been uploaded.">
                  Fire
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
    </Section>
  )
}

type AudioNodesState =
  | { kind: 'loading' }
  | { kind: 'loaded'; nodes: AudioNodeSummary[] }
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

/**
 * The selected node's own `node.audio.*` observations, which is where the
 * six node.audio.timeline.* signals live: they describe the NODE's
 * playback timeline, not one session's state, so they come from a
 * separate read against resourceKind `node` rather than from the
 * audio_session list above.
 */
function useNodeAudioObservations(nodeId: string, reloadKey: number): ObservationsState {
  const [state, setState] = useState<ObservationsState>({ kind: 'loading' })
  useEffect(() => {
    if (nodeId === '') {
      setState({ kind: 'loaded', observations: [] })
      return
    }
    let cancelled = false
    setState({ kind: 'loading' })
    listObservations('node', nodeId)
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', observations: response.observations })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [nodeId, reloadKey])
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
 * Parses the Apply session textarea. Empty input maps to `undefined`, mirroring
 * `applyAudioSession`'s own "omitted entirely, not sent as {}" contract and
 * `showmeshctl audio session apply`'s optional positional argument. A parse
 * or shape failure never reaches the network: it is reported locally.
 */
function parseAudioSessionApplyParams(raw: string): { params: Record<string, unknown> | undefined } | { error: string } {
  const trimmed = raw.trim()
  if (trimmed === '') return { params: undefined }
  let parsed: unknown
  try {
    parsed = JSON.parse(trimmed)
  } catch {
    return { error: 'Not sent. The text is not valid JSON.' }
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    const kind = parsed === null ? 'null' : Array.isArray(parsed) ? 'an array' : typeof parsed
    return {
      error: `Not sent. The payload must be a JSON object, not ${kind}.`,
    }
  }
  return { params: parsed as Record<string, unknown> }
}

/**
 * Collapsed per 2026-09-01 owner feedback into a target picker plus a floating inspector.
 * `apply` was excluded here 2026-09-01 as authoring rather than live control, then restored
 * 2026-09-02 by Eric's later owner placement ruling that this method belongs on Live Control.
 */
function AudioSessionsBlock({ gate, show, nowIso }: { gate: Gate; show: string | null; nowIso: string | null }) {
  const fleet = useFleetNodes()
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
  const [scheduledAtNs, setScheduledAtNs] = useState('')
  const [alignedNodeIds, setAlignedNodeIds] = useState('')
  const [clearConfirm, setClearConfirm] = useState('')
  const [applyPayload, setApplyPayload] = useState('')
  const [applyConfirm, setApplyConfirm] = useState('')
  const [outcome, setOutcome] = useState<CommandOutcome | null>(null)
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [sessionOpen, setSessionOpen] = useState(false)

  useEffect(() => {
    if (selectedNodeId === null && nodesState.kind === 'loaded' && nodesState.nodes[0] !== undefined) {
      setSelectedNodeId(nodesState.nodes[0].id)
    }
  }, [nodesState, selectedNodeId])

  const nodeObservationsState = useNodeAudioObservations(selectedNodeId ?? '', reloadKey)

  const observations = observationsState.kind === 'loaded' ? observationsState.observations : []
  const nodeObservations = nodeObservationsState.kind === 'loaded' ? nodeObservationsState.observations : []
  const actions = actionsState.kind === 'loaded' ? actionsState.actions : []
  const options = audioSessionOptions(observations, actions)
  const summaries = audioSessionSummaries(observations, actions, nowIso)
  const trimmedSessionId = sessionId.trim()
  const revisionInfo = trimmedSessionId === '' ? null : deriveAudioSessionRevision(observations, trimmedSessionId)
  const overrideValue = revisionOverride === null ? null : parseExactRevisionInput(revisionOverride)
  const overrideError =
    revisionOverride !== null && revisionOverride.trim() !== '' && overrideValue === null
      ? 'Not a valid version. Type a whole number, 0 or greater.'
      : null
  const effectiveRevision = overrideValue ?? revisionInfo?.next ?? 1n
  const nodeId = selectedNodeId ?? ''
  const canDispatch = gate.allowed && nodeId !== '' && trimmedSessionId !== '' && overrideError === null
  const dispatchTitle = !gate.allowed
    ? gate.reason
    : nodeId === '' || trimmedSessionId === ''
      ? 'Choose a node and a session id first.'
      : overrideError !== null
        ? overrideError
        : undefined

  const positionMs =
    position.trim() === '' ? null : timecodeToMillis(position, frameRateState.kind === 'loaded' ? frameRateState.fps : null)

  // An empty box means "start on arrival", which is what Start has always
  // done; only a non-empty box can be a bad instant.
  const scheduledAtNsValue = scheduledAtNs.trim() === '' ? null : parseScheduledAtNsInput(scheduledAtNs)
  const scheduledAtNsError =
    scheduledAtNs.trim() !== '' && scheduledAtNsValue === null
      ? 'Not a valid instant. Type whole nanoseconds, 0 or greater, as digits.'
      : null
  const canStart = canDispatch && scheduledAtNsError === null
  const startTitle = scheduledAtNsError ?? dispatchTitle

  // The aligned group is the selected node plus any others typed in, in
  // that order. Duplicates are dropped here rather than sent: the
  // coordinator refuses a repeated node id outright, and the selected
  // node being retyped is the obvious way to hit that.
  const alignedGroup = [nodeId, ...alignedNodeIds.split(',').map((id) => id.trim())].filter(
    (id, index, all) => id !== '' && all.indexOf(id) === index,
  )
  const canAlignedStart = canDispatch && alignedGroup.length > 1
  const alignedTitle = !canDispatch
    ? dispatchTitle
    : alignedGroup.length > 1
      ? undefined
      : 'Name at least one more node to align with.'

  const run = useCallback((action: string, call: () => Promise<AudioSessionCommandResult>) => {
    call()
      .then((result) => {
        setOutcome(describeAudioSessionOutcome(result, action))
        setReloadKey((n) => n + 1)
      })
      .catch((err: unknown) => setOutcome({ tone: 'bad', label: 'Refused', detail: `${action}: ${describeApiError(err)}` }))
  }, [])

  const timelineRows = audioTimelineRows(nodeObservations, nodeId)

  // Its own runner, not `run`: an aligned start answers with a selection
  // and two result lists rather than one command result, and collapsing
  // that into a single outcome would lose the aligned/not-aligned
  // distinction this control exists to show.
  const runAligned = useCallback(
    (nodeIds: readonly string[]) => {
      alignedStartAudioSession(trimmedSessionId, effectiveRevision, nodeIds)
        .then((result) => {
          setOutcome(describeAlignedStart(result))
          setReloadKey((n) => n + 1)
        })
        .catch((err: unknown) =>
          setOutcome({ tone: 'bad', label: 'Refused', detail: `Aligned start: ${describeApiError(err)}` }),
        )
    },
    [trimmedSessionId, effectiveRevision],
  )

  const stateEntry = trimmedSessionId === '' ? undefined : audioSessionSignal(observations, trimmedSessionId, 'audio_session.state')
  const positionEntry = trimmedSessionId === '' ? undefined : audioSessionSignal(observations, trimmedSessionId, 'audio_session.position_ms')
  const gainEntry = trimmedSessionId === '' ? undefined : audioSessionSignal(observations, trimmedSessionId, 'audio_session.gain.effective')

  const audioSummary =
    options.length === 0 ? 'No sessions known.' : `${options.length} known session${options.length === 1 ? '' : 's'}.`

  return (
    <>
      <button
        type="button"
        className="sm-linkbutton"
        disabled={nodesState.kind !== 'loaded' || nodesState.nodes.length === 0}
        title={
          nodesState.kind === 'loading'
            ? "Reading this deployment's declared audio nodes."
            : nodesState.kind === 'failed'
              ? nodesState.reason
              : nodesState.kind === 'loaded' && nodesState.nodes.length === 0
                ? 'No audio node is declared. Add one in Settings › Node routing.'
                : audioSummary
        }
        onClick={() => setDrawerOpen(true)}
      >
        Audio sessions →
      </button>

      <Drawer
        open={drawerOpen}
        onClose={() => {
          setDrawerOpen(false)
          setSessionOpen(false)
        }}
        labelledBy="lc-audio-controls"
        width="wide"
      >
        <Section
          id="lc-audio-controls"
          title={trimmedSessionId === '' ? 'Audio session controls' : `Audio session controls · ${trimmedSessionId}`}
        >
          <div className="sm-panel">
            <h3 className="sm-subsection__title">Target</h3>
            <Field label="Node">
              {(props) => (
                <Select {...props} value={nodeId} onChange={(event) => setSelectedNodeId(event.target.value)}>
                  {nodesState.kind === 'loaded' &&
                    nodesState.nodes.map((node) => (
                      <option key={node.id} value={node.id} title={node.label}>
                        {audioNodeIdentityText(node, fleet)}
                      </option>
                    ))}
                </Select>
              )}
            </Field>
            {options.length > 0 && (
              <Field label="Known sessions" help="Fills the session id below.">
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
            {summaries.length > 0 && (
              <TableWrap label="Known sessions' observed state, scrollable">
                <Table minWidth={420}>
                  <thead>
                    <tr>
                      <th scope="col">Session</th>
                      <th scope="col">State</th>
                      <th scope="col">Position</th>
                    </tr>
                  </thead>
                  <tbody>
                    {summaries.map((summary) => (
                      <tr key={summary.sessionId}>
                        <td>
                          <span className="sm-data">{summary.sessionId}</span>
                          <br />
                          <span className="sm-small sm-faint">{summary.origin}</span>
                        </td>
                        <td>
                          <StatusPair tone={summary.tone} label={summary.stateLabel} />
                        </td>
                        <td>
                          {summary.positionLabel === null ? (
                            <span className="sm-small sm-faint">Not reported</span>
                          ) : (
                            <span className="sm-data">{summary.positionLabel}</span>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </Table>
              </TableWrap>
            )}
            {options.length === 0 && (
              <RuledStrip
                absence="unobserved"
                label="No sessions observed"
                fact="No audio session has reported. Type the session id."
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
                  ? 'Never observed. Sending version 1.'
                  : `Version ${revisionInfo.observed} → ${revisionInfo.next}`}
              </p>
            )}
            {revisionOverride === null ? (
              <ButtonRow>
                <Button size="compact" variant="quiet" onClick={() => setRevisionOverride(String(effectiveRevision))}>
                  Set version
                </Button>
              </ButtonRow>
            ) : (
              <Field
                label="Version override"
                help="Digits only. Use when the automatic value above is stuck."
                error={overrideError ?? undefined}
              >
                {(props) => (
                  <Input
                    {...props}
                    type="text"
                    inputMode="numeric"
                    pattern="[0-9]+"
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
            <ButtonRow>
              <Button
                variant="primary"
                disabled={nodeId === '' || trimmedSessionId === ''}
                title={nodeId === '' || trimmedSessionId === '' ? 'Choose a node and a session id first.' : undefined}
                onClick={() => setSessionOpen(true)}
              >
                Open
              </Button>
            </ButtonRow>
          </div>

          {sessionOpen && trimmedSessionId !== '' && (
            <>
              <div className="sm-panel">
                <h3 className="sm-subsection__title">Transport</h3>
                <ButtonRow>
                  <Button size="gloved" disabled={!canDispatch} title={dispatchTitle} onClick={() => run('Prepare', () => prepareAudioSession(nodeId, trimmedSessionId, effectiveRevision))}>
                    Prepare
                  </Button>
                  <Button
                    size="gloved"
                    disabled={!canStart}
                    title={startTitle}
                    onClick={() =>
                      run('Start', () => startAudioSession(nodeId, trimmedSessionId, effectiveRevision, scheduledAtNsValue ?? undefined))
                    }
                  >
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
                <Field
                  label="Start at"
                  help="Nanoseconds. Empty starts on arrival. A time already past is refused."
                  error={scheduledAtNsError ?? undefined}
                >
                  {(props) => (
                    <Input
                      {...props}
                      type="text"
                      inputMode="numeric"
                      pattern="[0-9]+"
                      value={scheduledAtNs}
                      onChange={(event) => setScheduledAtNs(event.target.value)}
                      placeholder="1789012345678901234"
                    />
                  )}
                </Field>
                <div className="sm-volume">
                  <Field
                    label="Align with"
                    help="Other node ids, comma separated. All start at the same instant."
                  >
                    {(props) => (
                      <Input
                        {...props}
                        value={alignedNodeIds}
                        onChange={(event) => setAlignedNodeIds(event.target.value)}
                        placeholder="node-b, node-c"
                      />
                    )}
                  </Field>
                  <Button
                    disabled={!canAlignedStart}
                    title={alignedTitle}
                    onClick={() => runAligned(alignedGroup)}
                  >
                    Aligned start
                  </Button>
                </div>
              </div>

              <div className="sm-panel">
                <h3 className="sm-subsection__title">Timeline</h3>
                <p className="sm-small sm-muted">This node's scheduled-playback timeline.</p>
                <DefinitionStrip
                  items={timelineRows.map((row) => ({
                    term: row.label,
                    value: row.value !== null ? <span className="sm-data">{row.value}</span> : 'Not collected',
                    detail: row.value !== null ? undefined : (row.reason ?? 'No reason reported.'),
                  }))}
                />
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

              <div className="sm-panel">
                <h3 className="sm-subsection__title">Apply session</h3>
                <p className="sm-small sm-muted">
                  Replaces this session&rsquo;s definition on the node. Same JSON as
                  <code className="sm-data"> showmeshctl audio session apply</code>.
                </p>
                <Field label="Params (JSON)" help="A JSON object. Empty applies with no params.">
                  {(props) => (
                    <Textarea
                      {...props}
                      className="sm-data"
                      rows={4}
                      value={applyPayload}
                      onChange={(event) => setApplyPayload(event.target.value)}
                      placeholder='{"sourceRole": "primary"}'
                    />
                  )}
                </Field>
                <Field label={trimmedSessionId === '' ? 'Type the session id to confirm applying' : `Type ${trimmedSessionId} to confirm applying`}>
                  {(props) => <Input {...props} value={applyConfirm} onChange={(event) => setApplyConfirm(event.target.value)} />}
                </Field>
                <ButtonRow>
                  <Button
                    variant="danger"
                    disabled={!canDispatch || applyConfirm !== trimmedSessionId}
                    title={!canDispatch ? dispatchTitle : applyConfirm !== trimmedSessionId ? 'Type the session id exactly to enable this.' : undefined}
                    onClick={() => {
                      const parsed = parseAudioSessionApplyParams(applyPayload)
                      if ('error' in parsed) {
                        setOutcome({ tone: 'bad', label: 'Invalid JSON', detail: parsed.error })
                        return
                      }
                      run('Apply', () => applyAudioSession(nodeId, trimmedSessionId, effectiveRevision, parsed.params))
                    }}
                  >
                    Apply
                  </Button>
                </ButtonRow>
              </div>
            </>
          )}

          <Outcome outcome={outcome} />
        </Section>
      </Drawer>
    </>
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
      <p className="sm-outcome__detail">{result.outcomeReason || 'The earlier command is still running.'}{result.replay ? ' This repeats an earlier command.' : ''}{result.attributionDegraded ? ' The audit record could not be written.' : ''}</p>
    </div>
  )
}

function PageHeader() {
  return (
    <>
      <h1 className="sm-page__title">Live Control</h1>
    </>
  )
}

type Gate = { allowed: true } | { allowed: false; reason: string }

function RunList({
  id,
  title,
  kindLabel,
  show,
  list,
  gate,
  onRun,
}: {
  id: string
  title: string
  kindLabel: string
  show: string | null
  list: { items: ConfigObjectSummary[] | null; error: string | null }
  gate: Gate
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
        <ul className="sm-plain-list">
          {list.items.map((item) => (
            <li key={item.id} className="sm-annc">
              <div>
                <p>
                  <span className="sm-data">{item.label !== '' ? item.label : item.id}</span>
                </p>
                <p className="sm-small sm-muted">
                  <span className="sm-data">{item.id}</span> · rev {item.currentRevision}
                </p>
              </div>
              <Button
                variant="primary"
                size="gloved"
                disabled={!gate.allowed}
                title={gate.allowed ? undefined : gate.reason}
                onClick={() => {
                  onRun(item.id)
                    .then(() =>
                      setOutcome({
                        tone: 'warn',
                        label: 'Accepted',
                        detail: `${item.id} was accepted, not finished. Each step reports its own outcome.`,
                      }),
                    )
                    .catch((err: unknown) =>
                      setOutcome({ tone: 'bad', label: 'Refused', detail: `${item.id}: ${describeApiError(err)}` }),
                    )
                }}
              >
                Run
              </Button>
            </li>
          ))}
        </ul>
      )}
      <Outcome outcome={outcome} />
    </Section>
  )
}
