import { useEffect, useRef, useState } from 'react'
import { Outlet } from 'react-router-dom'
import {
  Button,
  ChromeBar,
  ChromeProgress,
  ClockSkewStrip,
  ConnectionPill,
  FPPPluginReportsRefusedBanner,
  Notice,
  Popover,
  Rail,
  RailFooter,
  RailGroup,
  RailLink,
  RuledStrip,
  ShellBody,
  WeatherDelayBanner,
  WeatherDelayHeldBanner,
  WeatherDelayQuestionBanner,
  type Connection,
  type WeatherDelayGroupView,
} from '../kit'
import {
  ApiError,
  getCurrentNightSession,
  getServiceDescriptor,
  getShowActive,
  getShowModeConfig,
  listConfigObjects,
  putShowActive,
  putShowModeConfig,
  type ConfigObjectSummary,
  type ConfigShowModePayload,
  type ConnectionState,
  type Model,
  type NightSessionState,
  type ShowActiveConfigResponse,
  type ShowModeConfigResponse,
  type WeatherDelayPowerGroupStatus,
} from '../api'
import { ageMs, CLOCK_SKEW_WARNING_THRESHOLD_MS, effectiveServerTimeIso, formatClock, formatCountdown, formatDuration, parseIsoMs } from '../domain/time'
import { describeApiError, describeSignInState, evaluateScope, type SignInState } from '../domain/session'
import { guardedSave, type SaveOutcome } from '../domain/save'
import { StaleWriteStrip } from '../screens/StaleWrite'
import { liveCycle } from '../screens/settingsModel'
import { fppElapsedFraction } from '../screens/liveControlModel'
import { nowPlaying as fppNowPlaying } from '../screens/showNightModel'
import { useModelContext } from './ModelContext'
import { useWeatherDelay, WeatherDelayProvider } from './WeatherDelayContext'
import { StopHoldShellBanner } from './StopHoldShellBanner'
import { BootstrapBand, BootstrapPlate, ConnectingBand, SignedOutBand, SignedOutPlate, SignOutControl, useSignedOutBand } from './SessionBand'

const WEATHER_DELAY_KIND_LABEL: Record<'delay' | 'cancelNight', string> = {
  delay: 'Weather delay',
  cancelNight: 'Night cancelled for weather',
}

/** The three power-group member kinds' own operator-facing labels (ADR-053 decision 10), matching Live Control's own weather-delay target-kind labels. */
const WEATHER_DELAY_MEMBER_KIND_LABEL: Record<'fpp' | 'resolume' | 'render', string> = {
  fpp: 'FPP',
  resolume: 'Resolume',
  render: 'render surface',
}

/** Builds this banner's own presentation shape from a power group's raw status. A stale read never shows a group as confirmed dark. */
function weatherDelayGroupView(group: WeatherDelayPowerGroupStatus, nowIso: string | null, stale: boolean): WeatherDelayGroupView {
  const label = group.label !== undefined && group.label !== '' ? group.label : group.id
  if (group.confirmedDark && stale) return { id: group.id, label, unknownLabel: 'Dark not confirmed, could not refresh' }
  if (group.confirmedDark) {
    const sinceAge = group.since === undefined ? null : ageMs(group.since, nowIso)
    return {
      id: group.id,
      label,
      confirmedLabel: sinceAge === null ? 'Confirmed dark' : `Confirmed dark for ${formatDuration(Math.max(0, sinceAge))}`,
    }
  }
  return {
    id: group.id,
    label,
    notDarkMembers: group.members
      .filter((member) => !member.dark)
      .map((member) => ({
        label: `${WEATHER_DELAY_MEMBER_KIND_LABEL[member.kind]} ${member.id}`,
        reason: member.reason ?? 'Not reported dark.',
      })),
  }
}

const WEATHER_DELAY_CONSEQUENCE_LABEL: Record<'delay' | 'cancelNight', string> = {
  delay: 'A weather delay starts when this runs out.',
  cancelNight: 'The night is cancelled when this runs out.',
}

/**
 * ADR-053 decision 12: the pending automatic-trigger question, shown on
 * every screen alongside whatever the active/held banner below renders
 * (decision 1's own "a delay can be changed to a cancel while active" is
 * why Cancel night appears here even for a plain "delay" question once a
 * delay is already active). A failed read never hides a question that was
 * last known to be pending; it keeps showing the frozen question with a
 * note that the read failed, rather than silently dropping it.
 */
function WeatherDelayQuestionShellBanner({
  model,
  invokeGate,
  deliveringActive,
}: {
  model: Model
  invokeGate: ReturnType<typeof evaluateScope>
  deliveringActive: boolean
}) {
  const weatherDelay = useWeatherDelay()
  const shown = weatherDelay.state.kind === 'loaded' ? weatherDelay.state.response : weatherDelay.state.kind === 'failed' ? weatherDelay.state.lastKnown : null
  const readFailure = weatherDelay.state.kind === 'failed' ? weatherDelay.state.reason : null
  const pendingDecision = shown?.pendingDecision ?? null
  const lastPendingDecision = useRef(pendingDecision)
  if (pendingDecision !== null) lastPendingDecision.current = pendingDecision

  const outcome = weatherDelay.decisionOutcome
  const outcomeId = outcome.kind === 'idle' ? null : outcome.id
  const decisionMessage =
    outcome.kind === 'error'
      ? outcome.message
      : outcome.kind === 'result' && outcome.response.message !== undefined
        ? outcome.response.message
        : null

  // A message belongs to the question it was answered for, never to the next one.
  const messageForShown = decisionMessage !== null && outcomeId === (pendingDecision?.id ?? lastPendingDecision.current?.id) ? decisionMessage : null
  const decision = pendingDecision ?? (messageForShown !== null ? lastPendingDecision.current : null)
  const answerable = pendingDecision !== null

  const [, setTick] = useState(0)
  const deadline = decision?.deadline ?? null
  useEffect(() => {
    if (deadline === null || !answerable) return
    const id = setInterval(() => setTick((t) => t + 1), 1000)
    return () => clearInterval(id)
  }, [deadline, answerable])

  if (decision === null) return null

  const nowIso = effectiveServerTimeIso(model.serverTime, model.serverTimeReceivedAt, Date.now())
  const nowMs = parseIsoMs(nowIso) ?? Date.now()
  const deadlineMs = parseIsoMs(decision.deadline) ?? nowMs
  const askedLabel = formatClock(decision.askedAt)
  const expiresLabel = decision.expiresAt === undefined ? null : formatClock(decision.expiresAt)
  const showCancelNight = decision.question === 'delayOrCancel' || deliveringActive

  // The question is gone: keep what happened on screen, offer nothing that would act on it.
  if (!answerable) {
    return (
      <WeatherDelayQuestionBanner
        reason={decision.reason}
        {...(askedLabel === null ? {} : { askedLabel: `Asked at ${askedLabel}` })}
        dismiss={{ label: 'Dismiss', onClick: weatherDelay.dismissDecisionOutcome, disabled: false, busy: false }}
        {...(messageForShown === null ? {} : { error: messageForShown })}
      />
    )
  }

  return (
    <WeatherDelayQuestionBanner
      reason={decision.reason}
      countdownLabel={`${formatCountdown(deadlineMs - nowMs)} left`}
      consequenceLabel={WEATHER_DELAY_CONSEQUENCE_LABEL[decision.defaultAction]}
      {...(askedLabel === null ? {} : { askedLabel: `Asked at ${askedLabel}` })}
      {...(expiresLabel === null ? {} : { expiresLabel: `Warning ends at ${expiresLabel}` })}
      start={{
        label: 'Start weather delay',
        onClick: () => weatherDelay.answerDecision(decision.id, 'delay'),
        disabled: !invokeGate.allowed || weatherDelay.decisionBusy !== false,
        busy: weatherDelay.decisionBusy === 'delay',
        ...(invokeGate.allowed ? {} : { title: invokeGate.reason }),
      }}
      {...(showCancelNight
        ? {
            cancelNight: {
              label: 'Cancel night',
              onClick: () => weatherDelay.answerDecision(decision.id, 'cancelNight'),
              disabled: !invokeGate.allowed || weatherDelay.decisionBusy !== false,
              busy: weatherDelay.decisionBusy === 'cancelNight',
              ...(invokeGate.allowed ? {} : { title: invokeGate.reason }),
            },
          }
        : {})}
      dismiss={{
        label: 'Dismiss',
        onClick: () => weatherDelay.answerDecision(decision.id, 'dismiss'),
        disabled: !invokeGate.allowed || weatherDelay.decisionBusy !== false,
        busy: weatherDelay.decisionBusy === 'dismiss',
        ...(invokeGate.allowed ? {} : { title: invokeGate.reason }),
      }}
      {...(messageForShown !== null
        ? { error: messageForShown }
        : readFailure !== null
          ? { error: `Could not refresh this question, so it may be out of date. ${readFailure}` }
          : {})}
    />
  )
}

/**
 * ADR-053: the non-dismissible banner on every screen while a delay, a cancel-night
 * or held players are reported. A failed read never hides it or shows a group as dark.
 */
function WeatherDelayShellBanner({ model, authenticated }: { model: Model; authenticated: boolean }) {
  const weatherDelay = useWeatherDelay()
  const resumeGate = evaluateScope(model.session, model.sessionFetchFailed, 'show:weatherdelay:resume')
  const invokeGate = evaluateScope(model.session, model.sessionFetchFailed, 'show:weatherdelay:invoke')
  const [tick, setTick] = useState(0)
  const shown = weatherDelay.state.kind === 'loaded' ? weatherDelay.state.response : weatherDelay.state.kind === 'failed' ? weatherDelay.state.lastKnown : null
  const readFailure = weatherDelay.state.kind === 'failed' ? weatherDelay.state.reason : null
  useEffect(() => {
    if (shown === null || !shown.active) return
    const id = setInterval(() => setTick((t) => t + 1), 1000)
    return () => clearInterval(id)
  }, [shown])
  void tick

  const heldPlayers = shown?.active === false ? (shown.heldPlayers ?? []) : []

  if (!authenticated) return null

  const questionBanner = <WeatherDelayQuestionShellBanner model={model} invokeGate={invokeGate} deliveringActive={shown?.active === true} />

  if (readFailure !== null && (shown === null || (!shown.active && heldPlayers.length === 0))) {
    return (
      <>
        {questionBanner}
        <WeatherDelayBanner
          unknown
          kindLabel="Weather delay state unknown"
          elapsedLabel="Could not read whether a weather delay is active, and whether a trigger question is pending. Check the connection before running the display."
          error={readFailure}
        />
      </>
    )
  }
  if (shown === null || !shown.active) {
    if (heldPlayers.length === 0) return questionBanner
    const resumeErrorMessage = weatherDelay.outcome.kind === 'error' && weatherDelay.outcome.action === 'resume' ? weatherDelay.outcome.message : undefined
    const heldErrors = [
      resumeErrorMessage,
      readFailure === null ? undefined : `Could not refresh this banner, so it may be out of date. ${readFailure}`,
    ].filter((message): message is string => message !== undefined)
    return (
      <>
        {questionBanner}
        <WeatherDelayHeldBanner
          messages={heldPlayers.map((player) => player.message)}
          resume={{
            label: 'Resume',
            onClick: weatherDelay.resume,
            disabled: !resumeGate.allowed || weatherDelay.busy !== false,
            busy: weatherDelay.busy === 'resume',
            ...(resumeGate.allowed ? {} : { title: resumeGate.reason }),
          }}
          {...(heldErrors.length === 0 ? {} : { error: heldErrors.join(' ') })}
        />
      </>
    )
  }
  const response = shown
  const kind = response.kind ?? 'delay'
  const nowIso = effectiveServerTimeIso(model.serverTime, model.serverTimeReceivedAt, Date.now())
  const elapsed = response.startedAt === undefined ? null : ageMs(response.startedAt, nowIso)
  const elapsedLabel = elapsed === null ? 'Start time not reported' : `Started ${formatDuration(elapsed)} ago`
  const startedByLabel = response.startedBy === undefined || response.startedBy === '' ? 'Who started this is not reported' : `Started by ${response.startedBy}`
  // notSaved/notSavedMessage ride only the start/resume response, never
  // GET /weather-delay - a browser that did not make that call has no way
  // to know, so it says nothing rather than guess "saved".
  const savedLabel =
    weatherDelay.outcome.kind === 'result' && (weatherDelay.outcome.action === 'start' || weatherDelay.outcome.action === 'resume')
      ? weatherDelay.outcome.result.notSaved === true
        ? (weatherDelay.outcome.result.notSavedMessage ?? 'Not saved')
        : 'Saved'
      : undefined
  const resumeErrorMessage =
    weatherDelay.outcome.kind === 'error' && weatherDelay.outcome.action === 'resume' ? weatherDelay.outcome.message : undefined
  const cancelNightErrorMessage =
    weatherDelay.outcome.kind === 'error' && weatherDelay.outcome.action === 'cancelNight' ? weatherDelay.outcome.message : undefined

  const errors = [
    resumeErrorMessage ?? cancelNightErrorMessage,
    readFailure === null ? undefined : `Could not refresh this banner, so it may be out of date. ${readFailure}`,
  ].filter((message): message is string => message !== undefined)

  const cancelNightAction =
    kind === 'delay'
      ? {
          label: 'Cancel night',
          onClick: weatherDelay.cancelNight,
          disabled: !invokeGate.allowed || weatherDelay.busy !== false,
          busy: weatherDelay.busy === 'cancelNight',
          ...(invokeGate.allowed ? {} : { title: invokeGate.reason }),
        }
      : undefined
  const groups = (response.powerGroups ?? []).map((group) => weatherDelayGroupView(group, nowIso, readFailure !== null))

  return (
    <>
      {questionBanner}
      <WeatherDelayBanner
        kindLabel={WEATHER_DELAY_KIND_LABEL[kind]}
        elapsedLabel={elapsedLabel}
        startedByLabel={startedByLabel}
        {...(savedLabel === undefined ? {} : { savedLabel })}
        {...(groups.length === 0 ? {} : { groups })}
        resume={{
          label: kind === 'cancelNight' ? 'Clear cancellation' : 'Resume',
          onClick: weatherDelay.resume,
          disabled: !resumeGate.allowed || weatherDelay.busy !== false,
          busy: weatherDelay.busy === 'resume',
          ...(resumeGate.allowed ? {} : { title: resumeGate.reason }),
        }}
        {...(cancelNightAction === undefined ? {} : { cancelNight: cancelNightAction })}
        {...(errors.length > 0 ? { error: errors.join(' ') } : {})}
      />
    </>
  )
}

const CONNECTION_LABEL: Record<Connection, string> = {
  live: 'Live',
  degraded: 'Degraded',
  lost: 'Lost',
  unknown: 'Unknown',
}

function connectionOf(state: ConnectionState): Connection {
  switch (state.kind) {
    case 'live':
      return 'live'
    case 'connecting':
      return 'unknown'
    case 'reconnecting':
      return 'degraded'
    default:
      return 'lost'
  }
}

const BLIND_NOW: Partial<Record<SignInState['kind'], string>> = {
  signed_out: 'Nothing is being read on this device',
  bootstrap_required: 'Unclaimed coordinator, no administrator exists',
  loading: 'Reading the coordinator',
}

const BLIND_PRINCIPAL: Partial<Record<SignInState['kind'], string>> = {
  signed_out: 'Signed out',
  bootstrap_required: 'No principal',
  loading: 'not signed in yet',
}

/**
 * The now-playing group truncates and never wraps. Cycle and time to next
 * transition come from the night session, so they arrive with Show Night;
 * the show picker and mode badge arrive with Shows and Settings › Mode.
 */
function NowPlaying({ model, signInKind }: { model: Model; signInKind: SignInState['kind'] }) {
  // Without a credential this device cannot read the show, which is not the
  // same fact as the show being stopped. Never report the second for the first.
  const blind = BLIND_NOW[signInKind]
  if (blind !== undefined) {
    return (
      <>
        <span className="sm-meta sm-faint">Now</span>
        <span className="sm-small sm-faint sm-truncate">{blind}</span>
      </>
    )
  }
  const run = model.currentRuns?.runs[0]
  if (run === undefined) {
    return (
      <>
        <span className="sm-meta sm-faint">Now</span>
        <span className="sm-small sm-faint">Nothing playing</span>
      </>
    )
  }
  // An fpp run's human name comes from observation signals (Live Control and
  // Show Night read the same way); run.playback.media is empty for it.
  const name =
    run.runner === 'fpp' ? (fppNowPlaying(model).state?.media ?? null) : run.playback.media !== '' ? run.playback.media : null
  const item = name ?? run.playback.itemId
  return (
    <>
      <span className="sm-meta sm-faint">Now</span>
      <span className="sm-truncate">{item !== '' ? item : 'Item not named'}</span>
      <span className="sm-small sm-faint sm-truncate">{run.playback.state}</span>
    </>
  )
}

/**
 * D-020 (Eric, 2026-09-01): a night session, seeded once and kept live by
 * `nightSession.changed`, exactly as SettingsMode's identically named hook
 * does. Needed here only to word the mode-badge confirm's leaving-show-mode
 * warning the same way SettingsMode does.
 */
function useNightSessionSeed(model: Model): NightSessionState | null {
  const [seeded, setSeeded] = useState<NightSessionState | null>(null)

  useEffect(() => {
    let cancelled = false
    getCurrentNightSession()
      .then((response) => {
        if (!cancelled) setSeeded(response.session)
      })
      .catch(() => {
        // An unread session stays unread; it never claims a cycle either way.
      })
    return () => {
      cancelled = true
    }
  }, [])

  return model.nightSession ?? seeded
}

type CoordinatorBuildState =
  | { kind: 'loading' }
  | { kind: 'loaded'; version: string; commit: string }
  | { kind: 'failed' }

/** D-002 (re-ruled, 2026-09-01): the coordinator's version and commit live once, in the rail footer. */
function useCoordinatorBuild(): CoordinatorBuildState {
  const [state, setState] = useState<CoordinatorBuildState>({ kind: 'loading' })
  useEffect(() => {
    let cancelled = false
    getServiceDescriptor()
      .then((descriptor) => {
        if (!cancelled) {
          setState({ kind: 'loaded', version: descriptor.coordinator.version, commit: descriptor.coordinator.commit })
        }
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

/** The rail footer shows a short commit and titles the full one; empty while loading, never a guess. */
function RailBuild() {
  const build = useCoordinatorBuild()
  if (build.kind === 'loading') return null
  if (build.kind === 'failed') {
    return <span className="sm-small sm-faint">Coordinator build not reported</span>
  }
  return (
    <>
      <span className="sm-small sm-faint">{build.version}</span>
      <span className="sm-data sm-small sm-faint" title={build.commit}>
        {build.commit.slice(0, 7)}
      </span>
    </>
  )
}

/** No `show.active` object has ever existed: the 404 the store documents, translated so `guardedSave` never special-cases it. Mirrors Shows.tsx's identical helper. */
function emptyShowActive(): ShowActiveConfigResponse {
  return {
    serverTime: '',
    kind: 'show.active',
    id: 'show.active',
    revision: 0,
    payload: { show: '' },
    updatedAt: '',
    createdByPrincipalId: null,
    createdByPrincipalName: null,
    source: 'api',
  }
}

function readShowActiveOrEmpty(): Promise<ShowActiveConfigResponse> {
  return getShowActive().catch((err: unknown) => {
    if (err instanceof ApiError && err.status === 404) return emptyShowActive()
    throw err
  })
}

type ShowStale = Extract<SaveOutcome<ShowActiveConfigResponse>, { kind: 'stale' }>
type ModeStale = Extract<SaveOutcome<ShowModeConfigResponse>, { kind: 'stale' }>

/**
 * The show-pill popover's body: the show list, the current selection, and
 * the audited `show.active` write, gated by the anchor already being
 * disabled when `config:write` is refused (nothing here re-checks the gate).
 */
function ShowActivePicker({
  current,
  gate,
  onClose,
}: {
  current: string
  gate: ReturnType<typeof evaluateScope>
  onClose: () => void
}) {
  const [objects, setObjects] = useState<ConfigObjectSummary[] | null>(null)
  const [objectsError, setObjectsError] = useState<string | null>(null)
  const [baseline, setBaseline] = useState<ShowActiveConfigResponse | null>(null)
  const [baselineError, setBaselineError] = useState<string | null>(null)
  const [selected, setSelected] = useState(current)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [stale, setStale] = useState<ShowStale | null>(null)

  useEffect(() => {
    let cancelled = false
    listConfigObjects('show')
      .then((response) => {
        if (!cancelled) setObjects(response.objects)
      })
      .catch((err: unknown) => {
        if (!cancelled) setObjectsError(describeApiError(err))
      })
    readShowActiveOrEmpty()
      .then((response) => {
        if (!cancelled) setBaseline(response)
      })
      .catch((err: unknown) => {
        if (!cancelled) setBaselineError(describeApiError(err))
      })
    return () => {
      cancelled = true
    }
  }, [])

  const apply = () => {
    if (baseline === null || selected === '' || selected === current) return
    const was = current !== '' ? `"${current}"` : 'none'
    if (!window.confirm(`Activate show "${selected}"? This replaces the active show (currently ${was}).`)) return
    setSaving(true)
    setSaveError(null)
    setStale(null)
    guardedSave({
      loaded: baseline,
      read: readShowActiveOrEmpty,
      write: () => putShowActive({ show: selected }),
    })
      .then((outcome) => {
        if (outcome.kind === 'saved') {
          onClose()
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
    <>
      {objectsError !== null ? (
        <RuledStrip absence="failed" label="Read failed" fact={objectsError} />
      ) : objects === null ? (
        <p className="sm-small sm-faint">Reading shows.</p>
      ) : objects.length === 0 ? (
        <p className="sm-small sm-faint">No show is configured.</p>
      ) : (
        <div className="sm-popover__options">
          {objects.map((object) => (
            <button
              key={object.id}
              type="button"
              className={`sm-popover__option${selected === object.id ? ' sm-popover__option--selected' : ''}`}
              onClick={() => setSelected(object.id)}
            >
              <span>{object.label}</span>
              {current === object.id && <span className="sm-popover__option-current">Current</span>}
            </button>
          ))}
        </div>
      )}
      {baselineError !== null && <RuledStrip absence="failed" label="Read failed" fact={baselineError} />}
      {stale !== null && (
        <StaleWriteStrip
          stale={stale}
          onReload={() => {
            setStale(null)
            setBaseline(null)
            readShowActiveOrEmpty()
              .then(setBaseline)
              .catch((err: unknown) => setBaselineError(describeApiError(err)))
          }}
        />
      )}
      {!gate.allowed && <p className="sm-small sm-faint">{gate.reason}</p>}
      {saveError !== null && <RuledStrip absence="failed" label="Save failed" fact={saveError} />}
      <div className="sm-popover__actions">
        <Button
          variant="primary"
          size="compact"
          onClick={apply}
          disabled={!gate.allowed || saving || baseline === null || selected === '' || selected === current}
          title={gate.allowed ? undefined : gate.reason}
        >
          {saving ? 'Applying…' : 'Apply'}
        </Button>
        <Button variant="quiet" size="compact" onClick={onClose} disabled={saving}>
          Cancel
        </Button>
      </div>
    </>
  )
}

/**
 * The active show is a config object (`config/show.active`), which every
 * coordinator has; that read is this pill's source of truth for the current
 * show and its revision. `model.currentRuns.activeShow` is used only as
 * extra evidence (its generation) when a `current-runs` read has succeeded,
 * never as the primary source — a coordinator without `GET /current-runs`
 * must still open the picker. `Show not reported` is reserved for the one
 * case that actually is unreported: the `show.active` read itself failed.
 */
function ShowPicker({ model, signInKind }: { model: Model; signInKind: SignInState['kind'] }) {
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const anchorRef = useRef<HTMLButtonElement>(null)
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState<ShowActiveConfigResponse | null>(null)
  const [readError, setReadError] = useState<string | null>(null)
  const authenticated = signInKind === 'signed_in'
  const principalId = model.session?.principal?.id ?? null

  // Without a credential this device cannot read show.active either; the
  // read is never issued until signed in, and is re-issued whenever the
  // signed-in principal changes.
  useEffect(() => {
    if (!authenticated) return
    let cancelled = false
    setActive(null)
    setReadError(null)
    readShowActiveOrEmpty()
      .then((response) => {
        if (!cancelled) setActive(response)
      })
      .catch((err: unknown) => {
        if (!cancelled) setReadError(describeApiError(err))
      })
    return () => {
      cancelled = true
    }
  }, [authenticated, principalId])

  if (!authenticated) {
    return (
      <span className="sm-showpicker sm-showpicker--unavailable">
        <span className="sm-showpicker__eyebrow">Show</span>
      </span>
    )
  }

  if (readError !== null) {
    return (
      <span className="sm-showpicker sm-showpicker--unavailable">
        <span className="sm-showpicker__eyebrow">Show</span>
        <span className="sm-small sm-faint">Show not reported</span>
        <span className="sm-small sm-faint sm-truncate">{readError}</span>
      </span>
    )
  }

  // Not yet resolved: say nothing rather than invent a value.
  if (active === null) return null

  const current = active.payload.show
  const generation = model.currentRuns?.activeShow.generation ?? null

  return (
    <span className="sm-chrome__picker">
      <button
        ref={anchorRef}
        type="button"
        className="sm-showpicker"
        aria-haspopup="dialog"
        aria-expanded={open}
        title={generation !== null ? `Generation ${generation}` : undefined}
        onClick={() => setOpen((v) => !v)}
      >
        <span className="sm-showpicker__eyebrow">Show</span>
        <span className="sm-showpicker__value">{current !== '' ? current : 'None'}</span>
        <span className="sm-showpicker__chevron" aria-hidden="true">▾</span>
      </button>
      <Popover open={open} title="Choose show" anchorRef={anchorRef} onClose={() => setOpen(false)}>
        <ShowActivePicker current={current} gate={gate} onClose={() => setOpen(false)} />
      </Popover>
    </span>
  )
}

const MODE_CHOICES: readonly { value: ConfigShowModePayload['mode']; label: string }[] = [
  { value: 'show', label: 'Show mode' },
  { value: 'program', label: 'Program mode' },
]

/** The mode-badge popover's body: the show.mode schema's two values, the audited `show.mode` write. */
function ModePicker({
  response,
  nightSession,
  gate,
  onClose,
}: {
  response: ShowModeConfigResponse
  nightSession: NightSessionState | null
  gate: ReturnType<typeof evaluateScope>
  onClose: () => void
}) {
  const current = response.payload.mode
  const [selected, setSelected] = useState<ConfigShowModePayload['mode']>(current)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [stale, setStale] = useState<ModeStale | null>(null)

  const apply = () => {
    if (selected === current) return
    const chosen = MODE_CHOICES.find((option) => option.value === selected)?.label ?? selected
    const leavingShowLive = current === 'show' && selected === 'program' && liveCycle(nightSession) !== null
    const warning = leavingShowLive ? ' Switching to Program mode now is allowed, but it stops treating the audience as present.' : ''
    if (!window.confirm(`Switch mode to ${chosen}?${warning}`)) return
    setSaving(true)
    setSaveError(null)
    setStale(null)
    guardedSave({
      loaded: response,
      read: getShowModeConfig,
      write: () => putShowModeConfig({ mode: selected }),
    })
      .then((outcome) => {
        if (outcome.kind === 'saved') {
          onClose()
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
    <>
      <div className="sm-popover__options">
        {MODE_CHOICES.map((option) => (
          <button
            key={option.value}
            type="button"
            className={`sm-popover__option${selected === option.value ? ' sm-popover__option--selected' : ''}`}
            onClick={() => setSelected(option.value)}
          >
            <span>{option.label}</span>
            {current === option.value && <span className="sm-popover__option-current">Current</span>}
          </button>
        ))}
      </div>
      {stale !== null && (
        <StaleWriteStrip
          stale={stale}
          onReload={() => {
            setStale(null)
            onClose()
          }}
        />
      )}
      {!gate.allowed && <p className="sm-small sm-faint">{gate.reason}</p>}
      {saveError !== null && <RuledStrip absence="failed" label="Save failed" fact={saveError} />}
      <div className="sm-popover__actions">
        <Button
          variant="primary"
          size="compact"
          onClick={apply}
          disabled={!gate.allowed || saving || selected === current}
          title={gate.allowed ? undefined : gate.reason}
        >
          {saving ? 'Applying…' : 'Apply'}
        </Button>
        <Button variant="quiet" size="compact" onClick={onClose} disabled={saving}>
          Cancel
        </Button>
      </div>
    </>
  )
}

function ShellMode({ model, signInKind }: { model: Model; signInKind: SignInState['kind'] }) {
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const nightSession = useNightSessionSeed(model)
  const anchorRef = useRef<HTMLButtonElement>(null)
  const [open, setOpen] = useState(false)
  const [response, setResponse] = useState<ShowModeConfigResponse | null>(null)
  const authenticated = signInKind === 'signed_in'
  const principalId = model.session?.principal?.id ?? null

  // Same rule as ShowPicker: no show.mode read without a credential, and a
  // fresh read whenever the signed-in principal changes.
  useEffect(() => {
    if (!authenticated) return
    let cancelled = false
    setResponse(null)
    getShowModeConfig()
      .then((r) => {
        if (!cancelled) setResponse(r)
      })
      .catch(() => {
        // The mode is a fact only after the coordinator has reported it.
      })
    return () => {
      cancelled = true
    }
  }, [authenticated, principalId])

  if (!authenticated) {
    return (
      <span className="sm-showpicker sm-showpicker--unavailable">
        <span className="sm-showpicker__eyebrow">Mode</span>
      </span>
    )
  }

  if (response === null) return null
  const mode = response.payload.mode
  // A coordinator older than the pin field reports no pin; say nothing rather than invent one.
  const pin: typeof response.cueActivationPin | undefined = response.cueActivationPin
  const firstSentence = response.resolumeWebSocketEffect.replace(/\.\s*$/, '')
  const title = pin === undefined ? response.resolumeWebSocketEffect : `${firstSentence}. ${pin.effect}`
  return (
    <span className="sm-chrome__picker">
      <button
        ref={anchorRef}
        type="button"
        className={`sm-mode-badge sm-mode-badge--${mode}`}
        aria-haspopup="dialog"
        aria-expanded={open}
        title={title}
        onClick={() => setOpen((v) => !v)}
      >
        {mode}
        {pin?.pinned === true && ' (edit staged)'}
        {pin?.pinned === true && (
          <span role="status" className="sm-sr-only">
            Show mode: {mode}. A show.cue edit is staged and will not reach any node until the show is stopped and restarted.
          </span>
        )}
      </button>
      <Popover open={open} title="Choose mode" anchorRef={anchorRef} onClose={() => setOpen(false)}>
        <ModePicker response={response} nightSession={nightSession} gate={gate} onClose={() => setOpen(false)} />
      </Popover>
    </span>
  )
}

export function Layout() {
  const model = useModelContext()
  const signIn = describeSignInState(model.session)
  const connection = connectionOf(model.connection)
  const principal = BLIND_PRINCIPAL[signIn.kind] ?? model.session?.principal?.name ?? 'Not signed in'
  // Shared with SignedOutPlate below: one credential form, so the plate's
  // own "Sign in" CTA in `main` focuses the same field the band above does.
  const signedOutBand = useSignedOutBand()

  return (
    <WeatherDelayProvider>
    <div className="sm-shell">
      <ChromeBar
        showPicker={<ShowPicker model={model} signInKind={signIn.kind} />}
        mode={<ShellMode model={model} signInKind={signIn.kind} />}
        nowPlaying={<NowPlaying model={model} signInKind={signIn.kind} />}
        connection={<ConnectionPill state={connection} label={CONNECTION_LABEL[connection]} />}
        principal={
          <>
            <span className="sm-small sm-muted">{principal}</span>
            {signIn.kind === 'signed_in' && <SignOutControl />}
          </>
        }
      />
      <ChromeProgress value={fppElapsedFraction(model.fpp[0])} label="Position of the current item" />
      {model.clockSkewMs !== null && Math.abs(model.clockSkewMs) >= CLOCK_SKEW_WARNING_THRESHOLD_MS && (
        <ClockSkewStrip>
          This browser&rsquo;s clock is {model.clockSkewMs > 0 ? 'behind' : 'ahead of'} the coordinator&rsquo;s, the
          reference clock, by about {formatDuration(Math.abs(model.clockSkewMs))}. Every age and relative time shown
          here is off by roughly that much.
        </ClockSkewStrip>
      )}
      {signIn.kind === 'loading' && <ConnectingBand liveUpdatesConnected={model.connection.kind === 'live'} />}
      {signIn.kind === 'bootstrap_required' && <BootstrapBand />}
      {signIn.kind === 'signed_out' && <SignedOutBand state={signedOutBand} />}
      {model.auditStore?.state === 'unusable' && (
        <Notice
          tone="warn"
          live="status"
          headline="Audit attribution is degraded"
          explanation={model.auditStore.reason ?? 'Commands continue, but this coordinator cannot durably write their audit entries.'}
        />
      )}
      <FPPPluginReportsRefusedBanner
        instances={model.fpp
          .filter((instance) => instance.playlistObservationRefused !== null)
          .map((instance) => ({ instanceId: instance.instanceId }))}
      />
      <WeatherDelayShellBanner model={model} authenticated={signIn.kind === 'signed_in'} />
      <StopHoldShellBanner model={model} authenticated={signIn.kind === 'signed_in'} />
      <ShellBody>
        <Rail>
          <RailGroup>Operate</RailGroup>
          <RailLink to="/">Dashboard</RailLink>
          <RailLink to="/night">Show Night</RailLink>
          <RailLink to="/control">Live Control</RailLink>
          <RailLink to="/control/resolume" sub>Resolume</RailLink>
          <RailGroup>Author</RailGroup>
          <RailLink to="/shows">Shows</RailLink>
          <RailLink to="/assets">Assets</RailLink>
          <RailGroup>System</RailGroup>
          <RailLink to="/monitor">Monitor</RailLink>
          <RailLink to="/settings">Settings</RailLink>
          <RailFooter>
            <RailBuild />
          </RailFooter>
        </Rail>
        <main className="sm-main">
          {signIn.kind === 'signed_out' ? (
            <SignedOutPlate state={signedOutBand} />
          ) : signIn.kind === 'bootstrap_required' ? (
            <BootstrapPlate />
          ) : (
            <Outlet />
          )}
        </main>
      </ShellBody>
    </div>
    </WeatherDelayProvider>
  )
}
