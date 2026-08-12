import { NavLink, Outlet } from 'react-router-dom'
import { ConnectionBanner } from '../components/ConnectionBanner'
import { TokenPrompt } from '../components/TokenPrompt'
import { SessionPanel } from '../components/SessionPanel'
import { useHighContrast } from './useHighContrast'
import { useModelContext } from './ModelContext'

export interface LayoutProps {
  /** Wired to seam B's token submission (spec section 5.6); App.tsx supplies it. */
  onSubmitToken: (token: string) => void
}

/**
 * Navigation is grouped by the OPERATOR-UI section 8 information
 * architecture: Monitor, Control, Configure. Only Monitor exists today,
 * and only Monitor is rendered.
 *
 * Control and Configure are deliberately NOT rendered as empty or
 * disabled groups. This is the same rule the dashboard follows for
 * subsystems the coordinator does not model: a visible-but-empty group
 * asserts that the section exists and currently has nothing in it, which
 * is a false statement about a system that has no write operations at
 * all (ADR-021 rule 5 bars the first write endpoint). They appear when
 * the behaviour behind them does.
 */
const NAV_GROUPS: Array<{
  heading: string
  items: Array<{ to: string; label: string; end: boolean }>
}> = [
  {
    heading: 'Monitor',
    items: [
      { to: '/', label: 'Dashboard', end: true },
      { to: '/nodes', label: 'Nodes', end: false },
      { to: '/fpp', label: 'FPP', end: false },
      { to: '/capabilities', label: 'Capabilities', end: false },
      { to: '/events', label: 'Events', end: false },
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
