import { useEffect, useState } from 'react'
import { Outlet } from 'react-router-dom'
import { getServiceDescriptor, type ServiceDescriptor } from '../api'
import { ChromeBar } from '../components/ChromeBar'
import { NavRail } from '../components/NavRail'
import { ConnectionBanner } from '../components/ConnectionBanner'
import { TokenPrompt } from '../components/TokenPrompt'
import { SessionPanel, SessionIdentity } from '../components/SessionPanel'
import { describeApiError, describeSignInState } from './session'
import { useTheme, type Theme } from './useTheme'
import { useModelContext } from './ModelContext'
import type { ConnectionState } from '../api/domain'

export interface LayoutProps {
  /** Wired to seam B's token submission (spec section 5.6); App.tsx supplies it. */
  onSubmitToken: (token: string) => void
}

const THEME_OPTIONS: { value: Theme; label: string }[] = [
  { value: 'system', label: 'System' },
  { value: 'dark', label: 'Dark' },
  { value: 'light', label: 'Light' },
  { value: 'contrast', label: 'Contrast' },
]

/**
 * Phase 0b: the global chrome bar and the seven-destination rail
 * (UI-DESIGN-GUIDE.md sections 2/3, README.md "Global chrome" /
 * "Information architecture"). `ChromeBar` and `NavRail` own the pixel
 * detail; this file only composes them with the load-bearing behaviour
 * that predates them and must not regress:
 *
 *  - `ConnectionBanner`, the `unauthorized` -> `TokenPrompt` path, and
 *    `SessionPanel` stay OUTSIDE `blockContent` below, same as before —
 *    sign-in state must always be visible, even while the rest of the
 *    page says "no data yet" (see each component's own header comment).
 *  - `blockContent` itself is unchanged (acceptance criterion 5).
 *  - The coordinator build string (`CoordinatorBuildNotice`, still
 *    defined below and still exported) is deliberately NOT rendered here
 *    any more — the design moved it into Settings > Appearance to pay for
 *    the chrome bar's now-playing space (README.md "The bar must not
 *    wrap"). It stays in this file, unrendered, for that seam to import.
 *  - The theme control (`useTheme`, app/useTheme.ts) also moves to
 *    Settings > Appearance later; until that lands, a working four-way
 *    picker stays reachable here so switching themes is not a dead
 *    feature in the interim.
 */
export function Layout({ onSubmitToken }: LayoutProps) {
  const model = useModelContext()
  const [theme, setTheme] = useTheme()

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

  // `SessionPanel` renders the band and plate for the states with no usable
  // session, and nothing at all otherwise. Layout needs to know which, because
  // a session state replaces the routed view rather than sitting above it.
  const signInState = describeSignInState(model.session).kind
  const hasSessionState = signInState === 'signed_out' || signInState === 'bootstrap_required'

  return (
    <div>
      <ChromeBar />
      <ConnectionBanner connection={model.connection} />
      {model.connection.kind === 'unauthorized' && (
        <TokenPrompt reason={model.connection.reason} onSubmit={onSubmitToken} />
      )}
      <div className="shell">
        <div>
          <NavRail />
          {/* Temporary home for the theme picker and the sign-out control
              -- see this file's header comment. Neither is one of the
              rail's seven destinations and neither is styled as one: a
              plain labelled utility area under the rail column.
              `ChromeBar`'s principal name is compact and un-interactive
              (the mock has no button there, and the bar must not wrap);
              this is where "Signed in as X" / Sign out still lives so
              signing out stays reachable while it does. */}
          <footer className="rail__group" aria-label="Operator status and controls">
            <SessionIdentity />
            <h3 className="rail__group-label t-meta">Appearance</h3>
            <div className="segmented" role="group" aria-label="Theme">
              {THEME_OPTIONS.map((option) => (
                <button
                  key={option.value}
                  type="button"
                  className="segmented__option"
                  aria-pressed={theme === option.value}
                  onClick={() => setTheme(option.value)}
                >
                  {option.label}
                </button>
              ))}
            </div>
          </footer>
        </div>
        <main>
          {/* The session band and its blanking plate live in the MAIN column,
              beside the rail, not above it. They push the page's own content
              down rather than covering it, and they are never modals: being
              signed out is a readable state, not a wall.

              When a session state renders, it REPLACES the routed view. A
              signed-out device has read nothing, so rendering a dashboard
              underneath "nothing here has ever been collected" would state
              both at once, and the empty dashboard would read as real
              inventory rather than as an absent read. */}
          {hasSessionState
            ? <SessionPanel />
            : blockContent ? <FirstConnect connection={model.connection} /> : <Outlet />}
        </main>
      </div>
    </div>
  )
}

/* The first-connect state. Nothing has been read yet, so the panel asserts
 * nothing: two ruled strips naming what is outstanding, and the line that makes
 * the distinction the four-absences rule turns on. No spinner, and no zeroed
 * numbers, because a zero here would be a claim rather than a wait. */
function FirstConnect({ connection }: { connection: ConnectionState }) {
  const incompatible = connection.kind === 'incompatible'
  return (
    <section className="page-body" aria-labelledby="first-connect-heading">
      <h2 id="first-connect-heading" className="t-heading">
        {incompatible ? 'This view is waiting on an API version agreement' : 'Connecting to the coordinator'}
      </h2>
      <div className="first-connect__strips">
        <div className="ruled-strip ruled-strip--loading">
          <span className="ruled-strip__state t-meta">Session</span>
          <div>
            <p className="ruled-strip__fact">
              {incompatible ? 'Not read, the version check refused first' : 'Not read yet'}
            </p>
            <p className="ruled-strip__explanation">
              {incompatible
                ? 'The coordinator and this UI do not agree on an API version. The banner above names the versions.'
                : 'Being signed out is a readable state, so this is a wait rather than a refusal.'}
            </p>
          </div>
        </div>
        <div className="ruled-strip ruled-strip--loading">
          <span className="ruled-strip__state t-meta">Live updates</span>
          <div>
            <p className="ruled-strip__fact">No stream frame has arrived</p>
            <p className="ruled-strip__explanation">The first snapshot has not been received.</p>
          </div>
        </div>
      </div>
      <p className="first-connect__note">Nothing below is stale, because nothing has been read yet.</p>
    </section>
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
//
// No longer rendered by this file (see the header comment above) -- kept
// and exported so the Settings > Appearance work can mount it there.
export function CoordinatorBuildNotice() {
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
