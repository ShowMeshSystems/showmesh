import { useEffect, useRef, useState } from 'react'
import { Outlet } from 'react-router-dom'
import {
  Button,
  ChromeBar,
  ChromeProgress,
  ClockSkewStrip,
  ConnectionPill,
  Notice,
  Popover,
  Rail,
  RailGroup,
  RailLink,
  RuledStrip,
  ShellBody,
  type Connection,
} from '../kit'
import {
  ApiError,
  getCurrentNightSession,
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
} from '../api'
import { CLOCK_SKEW_WARNING_THRESHOLD_MS, formatDuration } from '../domain/time'
import { describeApiError, describeSignInState, evaluateScope, type SignInState } from '../domain/session'
import { guardedSave, type SaveOutcome } from '../domain/save'
import { StaleWriteStrip } from '../screens/StaleWrite'
import { liveCycle } from '../screens/settingsModel'
import { useModelContext } from './ModelContext'
import { BootstrapBand, BootstrapPlate, ConnectingBand, SignedOutBand, SignedOutPlate, SignOutControl, useSignedOutBand } from './SessionBand'

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
  const item = run.playback.media !== '' ? run.playback.media : run.playback.itemId
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
      <ChromeProgress value={null} label="Position of the current item" />
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
  )
}
