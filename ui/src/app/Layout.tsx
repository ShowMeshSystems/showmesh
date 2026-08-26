import { useEffect, useState } from 'react'
import { NavLink, Outlet } from 'react-router-dom'
import { getServiceDescriptor, type ServiceDescriptor } from '../api'
import { ConnectionBanner } from '../components/ConnectionBanner'
import { TokenPrompt } from '../components/TokenPrompt'
import { SessionPanel } from '../components/SessionPanel'
import { ShowModeIndicator } from '../components/ShowModeIndicator'
import { describeApiError } from './session'
import { useHighContrast } from './useHighContrast'
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
const NAV_GROUPS: Array<{
  heading: string
  items: Array<{ to: string; label: string; end: boolean }>
}> = [
  {
    heading: 'Show night',
    items: [
      // Track F seam F2 (UI half): the night-session lifecycle operating
      // view — observes and commands the RUNNING controller. It is what
      // an operator opens first while the installation is running.
      { to: '/night', label: 'Night session', end: false },
      { to: '/config/show.active', label: 'Active show', end: false },
      // TRACK-H-H2-SPEC.md §5/§6: whether a show's Playlists are actually
      // ready to run, and whether each FPP instance's latest observation
      // still matches what the show declares. This is the show-night
      // question a stale import, missing asset, or unbound Playlist
      // would otherwise only surface from `showmeshctl fpp`.
      { to: '/playlists/readiness', label: 'Playlist readiness', end: false },
      { to: '/', label: 'Dashboard', end: true },
    ],
  },
  {
    heading: 'Monitor',
    items: [
      { to: '/nodes', label: 'Nodes', end: false },
      { to: '/fpp', label: 'FPP', end: false },
      // Track D seam D-4: monitor and control both live on this one route
      // (build contract §2.2/§2.3), so it belongs in Monitor rather than
      // splitting it across two nav groups.
      { to: '/resolume', label: 'Resolume', end: false },
      { to: '/events', label: 'Events', end: false },
    ],
  },
  {
    heading: 'Diagnostics',
    items: [
      { to: '/capabilities', label: 'Capabilities', end: false },
      // Track G seam G-8: read-only surfaces — a node's own asset
      // readiness and the append-only audit log.
      { to: '/assets/manifest', label: 'Asset manifest', end: false },
      { to: '/audit', label: 'Audit log', end: false },
    ],
  },
  {
    heading: 'Control',
    items: [{ to: '/macros', label: 'Macros', end: false }],
  },
  {
    heading: 'Configure',
    // /config holds both the FPP endpoints and (Track G seam G-2, ADR-039)
    // the Resolume instance connection, so the label names both. Before
    // that seam this label named Resolume while Configuration.tsx held no
    // Resolume content at all — the composition upload lives on /resolume,
    // not here — which TRACK-G-surface-parity.md's own audit named as a
    // placement fault seam G-2 was scoped to fix by making the label true
    // rather than by changing it.
    items: [
      // `end: true`, unlike every other item in this list (Track G seam
      // G-8 finding): NavLink's non-end matching is "current path starts
      // with this path plus a segment boundary", and every new
      // /config/show* route below satisfies that boundary against bare
      // /config — this link would otherwise render as "active" on every
      // page this seam added.
      { to: '/config', label: 'FPP & Resolume', end: true },
      { to: '/actions', label: 'Show actions', end: false },
      // Track G seam G-5: identity administration's own nav entry.
      { to: '/access', label: 'Access', end: false },
      // Track G seam G-8: Track E's authoring surfaces, previously
      // reachable only from showmeshctl.
      { to: '/config/show', label: 'Shows', end: false },
      { to: '/config/show.surface', label: 'Surfaces', end: false },
      // Track H seam H6: the show.cue authoring surface, previously
      // reachable only from showmeshctl.
      { to: '/config/show.cue', label: 'Cues', end: false },

      // Track H seam H6: show.playlist authoring, previously reachable
      // only from showmeshctl.
      { to: '/config/show.playlist', label: 'Playlists', end: false },
      // TRACK-H-H2-SPEC.md §3.6/§4: the stored FPP playlist-definition
      // import evidence -- what an author sees to decide whether a
      // Playlist binding still matches what FPP will actually play.
      // Previously reachable only from `showmeshctl fpp
      // playlist-definitions`.
      { to: '/config/fpp-playlist-definitions', label: 'FPP playlist definitions', end: false },
      // Track F seam F1 (UI half): the night.session/night.session.active
      // authoring surfaces, previously reachable only from showmeshctl.
      { to: '/config/night.session', label: 'Night sessions', end: false },
      { to: '/config/night.session.active', label: 'Active night session', end: false },
      { to: '/assets', label: 'Assets', end: false },
    ],
  },
]

export function Layout({ onSubmitToken }: LayoutProps) {
  const model = useModelContext()
  const [highContrast, toggleHighContrast] = useHighContrast()

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
      <nav className="app-nav" aria-label="Primary">
        {NAV_GROUPS.map((group) => (
          // The group wrapper is `display: contents` at phone width, so the
          // links stay direct flex children of the bottom tab bar and that
          // layout is unchanged by this grouping. It becomes a real column
          // only at the sidebar breakpoint.
          <div key={group.heading} className="app-nav__group">
            <h2 className="app-nav__group-heading">{group.heading}</h2>
            {group.items.map((item) => (
              <NavLink key={item.to} to={item.to} end={item.end} className="app-nav__link">
                {/* react-router-dom's NavLink sets aria-current="page" on the active
                    link automatically; styles/global.css's [aria-current='page']
                    rule uses that rather than a className toggle. */}
                {item.label}
              </NavLink>
            ))}
          </div>
        ))}
      </nav>
      <div style={{ flex: 1, display: 'flex', flexDirection: 'column', minWidth: 0 }}>
        <header className="app-header">
          <h1 className="app-header__title">ShowMesh Operator</h1>
          {/* ADR-033 decision 3: the installation-wide operating mode is
              visible PERSISTENTLY, on every route, not on a settings page.
              It sits in this header for the same reason SessionPanel below
              is not gated on `blockContent`: an operator must be able to
              see which mode they are in even while the rest of the page is
              showing "no data yet", because that is exactly the moment
              every surface behaves differently and nothing says why. */}
          <ShowModeIndicator />
          <button
            type="button"
            className="icon-button"
            aria-pressed={highContrast}
            onClick={toggleHighContrast}
          >
            {highContrast ? 'High contrast: on' : 'High contrast: off'}
          </button>
        </header>
        <ConnectionBanner connection={model.connection} />
        {/* GET / (getServiceDescriptor): "is this the thing I just
            deployed" has no other answer anywhere in this UI during a
            fleet upgrade -- showmeshctl version reports it, this did not.
            Placed right next to ConnectionBanner, not on a settings page,
            because it is about the SAME coordinator this UI is currently
            talking to; low-noise reference text, never an alert, since an
            unreachable coordinator already has ConnectionBanner's own
            alert above it. */}
        <CoordinatorBuildNotice />
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
