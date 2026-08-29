import { Link } from 'react-router-dom'
import type { ConnectionState, CurrentRun } from '../app/types'
import { describeSignInState } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { useShowList } from './useShowList'
import { useShowModeState } from './ShowModeIndicator'

/**
 * The 49px global chrome bar (UI-DESIGN-GUIDE.md section 2 / README.md
 * "Global chrome"), present on every screen including every session
 * state -- App.tsx mounts it once, in Layout.tsx, above `ConnectionBanner`
 * / `TokenPrompt` / `SessionPanel` and the `blockContent` gate, so it
 * keeps rendering even before the first snapshot has ever arrived.
 *
 * Left to right: brand mark, show picker, mode badge, a divider,
 * now-playing (flexes; everything else is `flex: 0 0 auto` so the bar
 * never wraps), then connection state and principal.
 *
 * NOT built here, and reported rather than invented: `CurrentRun` has no
 * item title (only `playback.media`, a filename-shaped string, and
 * `itemId`), no total-duration field to pair with `positionMs` (so no
 * "1:42 / 2:48" fraction and no percentage the progress bar could show),
 * no cycle counter, and `next` carries no time-to-transition -- see this
 * file's `NowPlaying` and the progress bar below for how each absence is
 * rendered instead of fabricated.
 */
export function ChromeBar() {
  const model = useModelContext()
  const activeRun = pickActiveRun(model.currentRuns?.runs ?? null)

  return (
    <div className="chrome">
      <div className="chrome__bar">
        <Link to="/" className="chrome__brand t-meta" aria-label="ShowMesh Operator home">
          SM
        </Link>
        <ShowPicker />
        <ModeBadge />
        <span className="chrome__divider" aria-hidden="true" />
        <NowPlaying model={model} activeRun={activeRun} />
        <div className="chrome__right">
          <ConnectionIndicator connection={model.connection} />
          <span className="chrome__divider" aria-hidden="true" />
          <PrincipalName />
        </div>
      </div>
      <PlaybackProgress />
    </div>
  )
}

// ------------------------------------------------------------- show picker

function ShowPicker() {
  const model = useModelContext()
  const showList = useShowList()
  const activeShow = model.currentRuns?.activeShow ?? null

  let display: string
  if (model.currentRunsFetchFailed) {
    display = 'unavailable'
  } else if (model.currentRuns === null) {
    display = 'loading…'
  } else if (!activeShow?.configured || activeShow.show === null) {
    // Genuinely no show selected: state that plainly rather than render a
    // picker with nothing in it.
    display = 'none selected'
  } else {
    const known = showList.kind === 'loaded' ? showList.shows.find((s) => s.id === activeShow.show) : undefined
    display = known?.label ?? activeShow.show
  }

  // Never fires an activation itself -- activating a different show is "the
  // sharp control" (ShowActive.tsx's own header comment): it changes what
  // every declared node is expected to hold, so it stays a two-step,
  // explicitly-confirmed action on its own screen. This is a link to that
  // screen, exactly like the mode badge below is a link to /config/mode.
  return (
    <Link to="/shows" className="chrome__show-picker">
      <span className="chrome__show-picker-eyebrow t-meta">Show</span>
      <span>{display}</span>
      <span className="chrome__show-picker-chevron" aria-hidden="true">
        ▾
      </span>
    </Link>
  )
}

// -------------------------------------------------------------- mode badge

function ModeBadge() {
  const state = useShowModeState()

  if (state.kind === 'loading') {
    return (
      <Link to="/config/mode" className="chrome__mode-badge t-meta" aria-label="Show mode">
        Mode: loading…
      </Link>
    )
  }
  if (state.kind === 'error') {
    return (
      <Link to="/config/mode" className="chrome__mode-badge t-meta" aria-label="Show mode" title={state.message}>
        Mode: cannot be read
      </Link>
    )
  }

  const mode = state.config.payload.mode
  const modeLabel = mode === 'show' ? 'Show mode' : 'Program mode'
  return (
    <Link
      to="/config/mode"
      className="chrome__mode-badge t-meta"
      aria-label="Show mode"
      title={state.config.resolumeWebSocketEffect}
    >
      {modeLabel}
    </Link>
  )
}

// ------------------------------------------------------------ now playing

/**
 * `status === 'playing'` is this codebase's existing convention for "this
 * is the run actually playing right now" (Dashboard.tsx's own
 * `currentRunStatusTone`/icon logic). Concurrent runs can be reported
 * (fpp + showmesh-audio); the bar has room for exactly one, so this picks
 * the first one actually playing rather than the first one reported.
 */
function pickActiveRun(runs: CurrentRun[] | null): CurrentRun | null {
  if (runs === null) return null
  return runs.find((run) => run.status === 'playing') ?? null
}

function formatPosition(positionMs: number | null): string | null {
  if (positionMs === null || positionMs < 0) return null
  const totalSeconds = Math.floor(positionMs / 1000)
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  return `${minutes}:${String(seconds).padStart(2, '0')}`
}

interface NowPlayingProps {
  model: ReturnType<typeof useModelContext>
  activeRun: CurrentRun | null
}

function NowPlaying({ model, activeRun }: NowPlayingProps) {
  // Four absences (UI-DESIGN-GUIDE.md section 4), not one collapsed "no
  // data" state: a failed fetch is unavailable, a response that has never
  // arrived this session is unobserved-so-far, and a response that
  // reports no playing run is a settled, empty state -- each says a
  // different true thing and none may borrow another's wording.
  if (model.currentRunsFetchFailed) {
    return (
      <div className="chrome__now">
        <NowEyebrow />
        <span className="chrome__now-detail">Playback status unavailable</span>
      </div>
    )
  }
  if (model.currentRunsReceivedAt === null) {
    return (
      <div className="chrome__now">
        <NowEyebrow />
        <span className="chrome__now-detail">Checking for an active run…</span>
      </div>
    )
  }
  if (activeRun === null) {
    return (
      <div className="chrome__now">
        <NowEyebrow />
        <span className="chrome__now-detail">Nothing currently playing</span>
      </div>
    )
  }

  const position = formatPosition(activeRun.playback.positionMs)
  // `CurrentRunNext` carries the next item's id/media/source, never a
  // countdown -- there is no field to compute "next in 1:06" from, so this
  // states the next item instead of inventing a duration.
  const nextDetail =
    activeRun.next === null ? 'no reported next item' : `next: ${activeRun.next.media || activeRun.next.itemId}`

  return (
    <div className="chrome__now">
      <NowEyebrow />
      <span className="chrome__now-title">{activeRun.playback.media || activeRun.playback.itemId}</span>
      {position !== null && <span className="chrome__now-position t-data">{position}</span>}
      <span className="chrome__now-detail t-data">{nextDetail}</span>
    </div>
  )
}

function NowEyebrow() {
  return (
    <span className="chrome__now-eyebrow t-meta">
      <span className="chrome__dot" aria-hidden="true" />
      Now
    </span>
  )
}

/**
 * `CurrentRun` has no total-duration field, so there is no valid
 * `aria-valuemax` to pair with `positionMs` -- asserting one (0-100%
 * against an invented ceiling, or `positionMs` itself as the max) would be
 * exactly the fabrication section 4 forbids. Until that field exists on
 * the wire, this always renders the plain strip, matching the treatment
 * the guide specifies for a session with no playback at all.
 */
function PlaybackProgress() {
  return <div className="chrome__progress-strip" />
}

// -------------------------------------------------------- connection + principal

const CONNECTION_LABEL: Record<ConnectionState['kind'], string> = {
  connecting: 'Connecting',
  live: 'Live',
  reconnecting: 'Reconnecting',
  unauthorized: 'Signed out',
  incompatible: 'Incompatible',
  failed: 'Offline',
}

function connectionTone(kind: ConnectionState['kind']): 'good' | 'warn' | 'unknown' {
  if (kind === 'live') return 'good'
  if (kind === 'connecting') return 'unknown'
  return 'warn'
}

function ConnectionIndicator({ connection }: { connection: ConnectionState }) {
  const tone = connectionTone(connection.kind)
  return (
    <span className={`chrome__connection chrome__connection--${tone} t-meta`}>
      <span className="chrome__dot" aria-hidden="true" />
      {CONNECTION_LABEL[connection.kind]}
    </span>
  )
}

function PrincipalName() {
  const model = useModelContext()
  const state = describeSignInState(model.session)
  const name = state.kind === 'signed_in' ? (state.session.principal?.name ?? 'unknown principal') : 'Signed out'
  return <span className="chrome__principal">{name}</span>
}
