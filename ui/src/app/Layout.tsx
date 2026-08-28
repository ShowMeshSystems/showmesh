import { useEffect, useState } from 'react'
import { NavLink, Outlet, useLocation } from 'react-router-dom'
import { getServiceDescriptor, type ServiceDescriptor } from '../api'
import { ConnectionBanner } from '../components/ConnectionBanner'
import { TokenPrompt } from '../components/TokenPrompt'
import { SessionPanel, SessionIdentity } from '../components/SessionPanel'
import { ShowModeIndicator } from '../components/ShowModeIndicator'
import { describeApiError } from './session'
import { useHighContrast } from './useHighContrast'
import { useNavGroupOpenState } from './useNavGroupState'
import { useModelContext } from './ModelContext'

export interface LayoutProps {
  /** Wired to seam B's token submission (spec section 5.6); App.tsx supplies it. */
  onSubmitToken: (token: string) => void
}

/**
 * Navigation is grouped by the OPERATOR-UI section 8 information
 * architecture: Show night, Monitor, Diagnostics, Control, Configure.
 *
 * Show night exists because those three screens are what an operator
 * actually uses while the installation is running at night: the
 * night-session controller, the active show, and the dashboard overview.
 * Grouping them together keeps the run-time path out of the diagnostic
 * and archival screens an operator does not need mid-show.
 *
 * Configure now exists: Step 7 seam A ships this application's first
 * configuration write surface (RES-008 D1). Control now exists too, as
 * of Step 9: "moving between operational states through show macros" is
 * OPERATOR-UI section 8's own example of what belongs here, and a macro
 * run is the first thing that fits it. Both groups were deliberately
 * NOT rendered empty or disabled before the behaviour behind them
 * existed — this is the same rule the dashboard follows for subsystems
 * the coordinator does not model: a visible-but-empty group asserts that
 * the section exists and currently has nothing in it, which was a false
 * statement right up until this step. A future group (e.g. controlled
 * devices) stays absent from this list until its own behaviour ships,
 * the same way these two did.
 */
type NavItem = { to: string; label: string; end: boolean }
type NavGroup = { heading: string; primary: NavItem[]; secondary: NavItem[] }

// One rail nav owns both the seven primary destinations and the compact
// legacy groups. Keeping each route in one place prevents the former primary
// and "All destinations" trees from drifting into duplicate current links.
const NAV_GROUPS: NavGroup[] = [
  {
    heading: 'Operate',
    primary: [
      { to: '/', label: 'Dashboard', end: true },
      { to: '/night', label: 'Show Night', end: true },
      { to: '/control', label: 'Live Control', end: true },
    ],
    secondary: [
      { to: '/config/show.active', label: 'Active show', end: false },
      { to: '/playlists/readiness', label: 'Playlist readiness', end: false },
    ],
  },
  {
    heading: 'Author',
    primary: [
      { to: '/config/show', label: 'Shows', end: true },
      { to: '/assets', label: 'Assets', end: true },
    ],
    secondary: [
      { to: '/config/show.surface', label: 'Surfaces', end: false },
      { to: '/config/show.cue', label: 'Cues', end: false },
      { to: '/config/show.playlist', label: 'Playlists', end: false },
      { to: '/config/fpp-playlist-definitions', label: 'FPP playlist definitions', end: false },
      { to: '/config/night.session', label: 'Night sessions', end: false },
      { to: '/config/night.session.active', label: 'Active night session', end: false },
    ],
  },
  {
    heading: 'System',
    primary: [
      { to: '/monitor', label: 'Monitor', end: true },
      { to: '/config', label: 'Settings', end: true },
    ],
    secondary: [
      { to: '/nodes', label: 'Nodes', end: false },
      { to: '/fpp', label: 'FPP', end: false },
      { to: '/resolume', label: 'Resolume', end: false },
      { to: '/events', label: 'Events', end: false },
      { to: '/capabilities', label: 'Capabilities', end: false },
      { to: '/assets/manifest', label: 'Asset manifest', end: false },
      { to: '/audit', label: 'Audit log', end: false },
      { to: '/actions', label: 'Show actions', end: false },
      { to: '/access', label: 'Access', end: false },
      { to: '/config/audio.settings', label: 'Audio settings', end: false },
      { to: '/config/audio.node', label: 'Audio nodes', end: false },
    ],
  },
]

// The state hook only needs the flattened route set to keep a top-level group
// open for its active destination. Rendering still uses the nested structure
// above so the rail stays compact and each legacy destination has one owner.
const NAV_STATE_GROUPS = NAV_GROUPS.map((group) => ({
  heading: group.heading,
  items: [...group.primary, ...group.secondary],
}))

// A stable DOM id for each group's link list (aria-controls target), not
// used for anything else -- lowercased/hyphenated so it stays a valid id
// across every current and future heading in NAV_GROUPS.
function slugifyHeading(heading: string): string {
  return heading.toLowerCase().replace(/[^a-z0-9]+/g, '-')
}

export function Layout({ onSubmitToken }: LayoutProps) {
  const model = useModelContext()
  const [highContrast, toggleHighContrast] = useHighContrast()
  const location = useLocation()
  const { isOpen, toggle } = useNavGroupOpenState(NAV_STATE_GROUPS, location.pathname)

  // Acceptance criterion 5 (spec section 7 / OPERATOR-UI section 5.1): an
  // incompatible coordinator "produces the explicit error, not a partial
  // render." Rendering the normal views underneath the banner in that
  // state would show an empty dashboard ("0 nodes") that looks like real
  // inventory rather than like a connection this UI has refused to trust.
  // The same reasoning covers any state before the first snapshot has
  // ever been applied (model.snapshotReceivedAt === null) -- an empty
  // node list before any data has arrived is not evidence there are no
  // nodes, so it must not render as if it were. Once a snapshot has been
  // applied at least once, spec section 5.5's "keep the last good model
  // across a disconnection" takes over instead: the views render
  // normally and each one's DataFreshnessNotice carries the staleness.
  const blockContent = model.connection.kind === 'incompatible' || model.snapshotReceivedAt === null

  return (
    <div className="app-shell">
      <aside className="app-sidebar" aria-label="ShowMesh Operator">
        <NavLink to="/" className="app-brand" aria-label="ShowMesh Operator home">
          <span className="app-brand__mark" aria-hidden="true">SM</span>
          <span className="app-brand__name">ShowMesh</span>
          <span className="app-brand__product">Operator</span>
        </NavLink>
        <nav className="app-nav" aria-label="Operator navigation">
          {NAV_GROUPS.map((group) => {
            const open = isOpen(group.heading)
            const linksId = `app-nav__group-links-${slugifyHeading(group.heading)}`
            return (
              <section key={group.heading} className="app-nav__group" data-open={open}>
                <button
                  type="button"
                  className="app-nav__group-heading"
                  aria-expanded={open}
                  aria-controls={linksId}
                  onClick={() => toggle(group.heading)}
                >
                  <span>{group.heading}</span>
                  {!open && (
                    <span className="app-nav__group-count">
                      {group.primary.length + group.secondary.length}
                    </span>
                  )}
                </button>
                <div id={linksId} className="app-nav__group-links">
                  <div className="app-nav__primary-links">
                    {group.primary.map((item) => (
                      <NavLink key={item.to} to={item.to} end={item.end} className="app-nav__primary-link">
                        {item.label}
                      </NavLink>
                    ))}
                  </div>
                  <div className="app-nav__secondary-links">
                    {group.secondary.map((item) => (
                      <NavLink key={item.to} to={item.to} end={item.end} className="app-nav__secondary-link">
                        {item.label}
                      </NavLink>
                    ))}
                  </div>
                </div>
              </section>
            )
          })}
        </nav>
        <footer className="app-sidebar__footer" aria-label="Operator status and controls">
          <ShowModeIndicator />
          <CoordinatorBuildNotice />
          <SessionIdentity />
          <button
            type="button"
            className="icon-button"
            aria-pressed={highContrast}
            onClick={toggleHighContrast}
          >
            {highContrast ? 'High contrast: on' : 'High contrast: off'}
          </button>
        </footer>
      </aside>
      <div className="app-content">
        <ConnectionBanner connection={model.connection} />
        {model.connection.kind === 'unauthorized' && (
          <TokenPrompt reason={model.connection.reason} onSubmit={onSubmitToken} />
        )}
        {/* ADR-024: independent of `connection` above — this renders
            whenever GET /api/v1/session has answered, regardless of
            whether reads are open, closed, or currently interrupted. See
            SessionPanel's own header comment for why it is not gated on
            `blockContent` below: an operator must be able to see "you are
            signed out" even while the rest of the page is showing "no
            data yet". */}
        <SessionPanel />
        <main className="app-main">
          {blockContent ? (
            <p className="text-muted" role="status">
              {model.connection.kind === 'incompatible'
                ? 'This view cannot be shown until the coordinator and this UI agree on an API version. See the message above.'
                : 'Waiting for the first response from the coordinator…'}
            </p>
          ) : (
            <Outlet />
          )}
        </main>
      </div>
    </div>
  )
}

type DescriptorState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; descriptor: ServiceDescriptor }

// `GET /` is "Always open, with no credential and regardless of whether
// reads are otherwise closed" (api/openapi.yaml's own doc comment), so
// this fetches exactly once on mount, independent of `model.connection`
// and `model.session` above -- there is no scope to gate it on and no
// stream frame that would ever refresh it. A failed fetch renders as a
// stated fact, never a blank or a guessed version: the reader must be
// able to tell "unknown" apart from "same build as before".
function CoordinatorBuildNotice() {
  const [state, setState] = useState<DescriptorState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    async function load(): Promise<void> {
      try {
        const descriptor = await getServiceDescriptor()
        if (cancelled) return
        setState({ kind: 'loaded', descriptor })
      } catch (err) {
        if (cancelled) return
        setState({ kind: 'error', message: describeApiError(err) })
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [])

  if (state.kind === 'loading') {
    return (
      <p className="text-muted coordinator-build-notice" role="status">
        Coordinator build: loading…
      </p>
    )
  }

  if (state.kind === 'error') {
    return (
      <p className="text-muted coordinator-build-notice" role="status">
        Coordinator build: could not be read ({state.message}).
      </p>
    )
  }

  const { coordinator, apiVersion } = state.descriptor
  return (
    <p
      className="text-muted coordinator-build-notice"
      role="status"
      title={`Commit ${coordinator.commit}, built ${coordinator.buildDate}, ${coordinator.goVersion}`}
    >
      Coordinator {coordinator.version} ({coordinator.commit.slice(0, 7)}, API v{apiVersion})
    </p>
  )
}
