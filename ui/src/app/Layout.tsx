import { Outlet } from 'react-router-dom'
import {
  ChromeBar,
  ChromeProgress,
  ConnectionPill,
  Rail,
  RailGroup,
  RailLink,
  ShellBody,
  type Connection,
} from '../kit'
import type { ConnectionState, Model } from '../api'
import { describeSignInState } from '../domain/session'
import { useModelContext } from './ModelContext'
import { BootstrapBand, SignedOutBand } from './SessionBand'

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

/**
 * The now-playing group truncates and never wraps. Cycle and time to next
 * transition come from the night session, so they arrive with Show Night;
 * the show picker and mode badge arrive with Shows and Settings › Mode.
 */
function NowPlaying({ model }: { model: Model }) {
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

export function Layout() {
  const model = useModelContext()
  const signIn = describeSignInState(model.session)
  const connection = connectionOf(model.connection)
  const principal = model.session?.principal?.name ?? 'Not signed in'

  return (
    <div className="sm-shell">
      <ChromeBar
        showPicker={null}
        mode={null}
        nowPlaying={<NowPlaying model={model} />}
        connection={<ConnectionPill state={connection} label={CONNECTION_LABEL[connection]} />}
        principal={<span className="sm-small sm-muted">{principal}</span>}
      />
      <ChromeProgress value={null} label="Position of the current item" />
      {signIn.kind === 'bootstrap_required' && <BootstrapBand />}
      {signIn.kind === 'signed_out' && <SignedOutBand />}
      <ShellBody>
        <Rail>
          <RailGroup>Operate</RailGroup>
          <RailLink to="/">Dashboard</RailLink>
          <RailLink to="/night">Show Night</RailLink>
          <RailLink to="/control">Live Control</RailLink>
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
