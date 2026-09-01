import { useEffect, useState } from 'react'
import { Link, Outlet } from 'react-router-dom'
import {
  ChromeBar,
  ChromeProgress,
  ClockSkewStrip,
  ConnectionPill,
  Notice,
  Rail,
  RailGroup,
  RailLink,
  ShellBody,
  type Connection,
} from '../kit'
import { getShowModeConfig, type ConnectionState, type Model, type ShowModeConfigResponse } from '../api'
import { CLOCK_SKEW_WARNING_THRESHOLD_MS, formatDuration } from '../domain/time'
import { describeSignInState, type SignInState } from '../domain/session'
import { useModelContext } from './ModelContext'
import { BootstrapBand, ConnectingBand, SignedOutBand, SignOutControl } from './SessionBand'

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

function ShellMode() {
  const [response, setResponse] = useState<ShowModeConfigResponse | null>(null)

  useEffect(() => {
    let cancelled = false
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
  }, [])

  if (response === null) return null
  const mode = response.payload.mode
  const pin = response.cueActivationPin
  const firstSentence = response.resolumeWebSocketEffect.replace(/\.\s*$/, '')
  const title = `${firstSentence}. ${pin.effect}`
  return (
    <Link className={`sm-mode-badge sm-mode-badge--${mode}`} to="/settings#show-mode" title={title}>
      {mode}
      {pin.pinned && ' (edit staged)'}
      {pin.pinned && (
        <span role="status" className="sm-sr-only">
          Show mode: {mode}. A show.cue edit is staged and will not reach any node until the show is stopped and restarted.
        </span>
      )}
    </Link>
  )
}

function ShowPicker({ model }: { model: Model }) {
  const active = model.currentRuns?.activeShow
  if (active?.configured !== true || active.show === null) return null
  return (
    <Link className="sm-showpicker" to={`/shows/${encodeURIComponent(active.show)}`}>
      <span className="sm-showpicker__eyebrow">Show</span>
      <span className="sm-showpicker__value">{active.show}</span>
      <span className="sm-showpicker__chevron" aria-hidden="true">▾</span>
    </Link>
  )
}

export function Layout() {
  const model = useModelContext()
  const signIn = describeSignInState(model.session)
  const connection = connectionOf(model.connection)
  const principal = BLIND_PRINCIPAL[signIn.kind] ?? model.session?.principal?.name ?? 'Not signed in'

  return (
    <div className="sm-shell">
      <ChromeBar
        showPicker={<ShowPicker model={model} />}
        mode={<ShellMode />}
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
      {signIn.kind === 'signed_out' && <SignedOutBand />}
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
          <Outlet />
        </main>
      </ShellBody>
    </div>
  )
}
