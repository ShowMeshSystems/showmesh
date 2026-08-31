import { readFileSync } from 'node:fs'
import path from 'node:path'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { Layout } from './Layout'
import { ModelContext } from './ModelContext'
import type { ConnectionState, Model, SessionResponse } from './types'

// ShowModeIndicator and CoordinatorBuildNotice (the latter defined in
// Layout.tsx itself) each fetch once on mount, so their API calls are
// mocked here rather than left to hit the real client. See the ADR-033
// tests at the bottom of this file for what the mode indicator's presence
// is proving, and the "coordinator build notice" describe block below for
// the latter.
const { getShowModeConfig, getServiceDescriptor } = vi.hoisted(() => ({
  getShowModeConfig: vi.fn(),
  getServiceDescriptor: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getShowModeConfig, getServiceDescriptor }
})

const SHOW_MODE_RESPONSE = {
  serverTime: '2026-08-11T12:00:00.000Z',
  kind: 'show.mode',
  revision: 4,
  payload: { mode: 'show' },
  updatedAt: '2026-08-11T12:00:00.000Z',
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'admin-1',
  source: 'api',
  resolumeWebSocketEffect: 'show mode: the Resolume WebSocket wake-up channel is held CLOSED.',
}

// Default coordinator descriptor every test not specifically about
// CoordinatorBuildNotice gets, so its own fetch-on-mount never needs
// separate attention in tests unrelated to it -- same reasoning as
// SHOW_MODE_RESPONSE existing purely to keep ShowModeIndicator quiet in
// those same unrelated tests.
const SERVICE_DESCRIPTOR = {
  serverTime: '2026-08-11T12:00:00.000Z',
  apiVersion: 3,
  supportedVersions: [3],
  coordinator: { version: '1.2.3', commit: 'abcdef1234567', buildDate: '2026-08-20T00:00:00Z', goVersion: 'go1.23.0' },
}

beforeEach(() => {
  // Each nav-group collapse test starts from a clean slate: no test in
  // this file relies on a preference persisted by an earlier one.
  window.localStorage.clear()
  getServiceDescriptor.mockResolvedValue(SERVICE_DESCRIPTOR)
  // Otherwise a `waitFor` anywhere in this file gives ShowModeIndicator's
  // own unrelated fetch-on-mount enough time to resolve to `undefined`
  // (vi.fn()'s own default) and crash render -- this default keeps every
  // test in this file that is not itself about the mode indicator quiet,
  // exactly like SHOW_MODE_RESPONSE already does for the tests below that
  // explicitly opt back into asserting on it.
  getShowModeConfig.mockResolvedValue(SHOW_MODE_RESPONSE)
})

afterEach(() => {
  cleanup()
  getShowModeConfig.mockReset()
  getServiceDescriptor.mockReset()
})

function model(
  connection: ConnectionState,
  snapshotReceivedAt: number | null,
  session: SessionResponse | null = null,
): Model {
  return {
    connection,
    serverTime: '2026-08-11T12:00:00.000Z',
    clockSkewMs: 0,
    snapshotReceivedAt,
    serverTimeReceivedAt: snapshotReceivedAt,
    nodes: [],
    fpp: [],
    collectors: [],
    macroRuns: [],
    resolume: [],
    auditStore: { state: 'usable', reason: null },
    nightSession: null,
    fppPlaylistEntryObservations: [],
    events: [],
    eventsGap: false,
    oldestRetainedSeq: null,
    session,
    sessionReceivedAt: session === null ? null : 0,
    sessionFetchFailed: false,
  }
}

// A real, signed-out SessionResponse (not `null`, which SessionPanel
// deliberately renders as nothing at all — see its own header comment).
// Matches the shape SessionPanel.test.tsx's own local fixture uses.
const SIGNED_OUT_SESSION: SessionResponse = {
  serverTime: '2026-08-11T12:00:00.000Z',
  authenticated: false,
  principal: null,
  session: null,
  credentialForm: null,
  scopes: [],
  scopesState: 'not_applicable',
  bootstrapRequired: false,
}

function renderLayout(m: Model, initialPath = '/') {
  return render(
    <ModelContext.Provider value={m}>
      <MemoryRouter initialEntries={[initialPath]}>
        <Routes>
          <Route element={<Layout onSubmitToken={() => {}} />}>
            <Route index element={<p>underlying view marker</p>} />
            <Route path="*" element={<p>underlying view marker</p>} />
          </Route>
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Layout', () => {
  // Acceptance criterion 5: an incompatible coordinator produces the
  // explicit error, never a partial render of the normal views.
  it('does not render the underlying view while the API version is incompatible', () => {
    renderLayout(
      model({ kind: 'incompatible', requiredVersion: 2, supportedVersions: [1], detail: 'nope' }, 12345),
    )
    expect(screen.queryByText('underlying view marker')).not.toBeInTheDocument()
  })

  it('does not render the underlying view before any snapshot has ever arrived', () => {
    renderLayout(model({ kind: 'connecting' }, null))
    expect(screen.queryByText('underlying view marker')).not.toBeInTheDocument()
  })

  it('renders the underlying view once data has been received, even while reconnecting', () => {
    renderLayout(
      model({ kind: 'reconnecting', attempt: 2, nextAttemptAt: 0, lastError: 'boom' }, 12345),
    )
    expect(screen.getByText('underlying view marker')).toBeInTheDocument()
  })

  it('renders the underlying view normally while live', () => {
    renderLayout(model({ kind: 'live', connectedAt: 0 }, 12345))
    expect(screen.getByText('underlying view marker')).toBeInTheDocument()
  })

  // ADR-024 decision 5 / OPERATOR-UI section 14: "being signed out is a
  // persistent state, never a modal at the moment of use," and
  // SessionPanel.tsx's own header comment states the design claim this
  // pair of tests exists to hold: the panel "renders unconditionally ...
  // NOT gated on `blockContent`." Every other test in this file uses
  // `session: null`, under which SessionPanel renders nothing at all
  // (its first branch) — so all four of those tests stay green whether
  // or not `<SessionPanel />` is even present in Layout.tsx. Only a
  // fixture with a REAL session can tell the difference, which is why
  // this pair supplies one.
  it('renders the persistent session panel when a real session is present', () => {
    renderLayout(model({ kind: 'live', connectedAt: 0 }, 12345, SIGNED_OUT_SESSION))
    expect(screen.getByText('Signed out on this device.')).toBeInTheDocument()
  })

  it('renders the persistent session panel even while content is blocked (incompatible version) — it must not be gated on blockContent', () => {
    renderLayout(
      model(
        { kind: 'incompatible', requiredVersion: 2, supportedVersions: [1], detail: 'nope' },
        12345,
        SIGNED_OUT_SESSION,
      ),
    )
    // The main content IS blocked (acceptance criterion 5, covered
    // above)...
    expect(screen.queryByText('underlying view marker')).not.toBeInTheDocument()
    // ...but the session panel is not part of that content and must
    // still be visible: an operator needs to see "you are signed out"
    // even while the rest of the page says "no data yet".
    expect(screen.getByText('Signed out on this device.')).toBeInTheDocument()
  })

  // Fix 2 (Track D seam D-2a): /config now holds both the FPP endpoints
  // configuration AND the Resolume composition upload (ADR-032 decision
  // 8), added to the SAME page rather than a second route. "FPP
  // endpoints" named only the first of those and gave an operator looking
  // for the composition control no reason to click it — this pins the
  // renamed label so a future edit cannot silently narrow it back to
  // naming only one of the two things this page does.
  it('labels the single Configure nav entry for both things /config now holds, not just FPP endpoints', () => {
    renderLayout(model({ kind: 'live', connectedAt: 0 }, 12345))
    const link = screen.getByRole('link', { name: 'FPP & Resolume' })
    expect(link).toHaveAttribute('href', '/config')
    expect(screen.queryByText('FPP endpoints')).not.toBeInTheDocument()
  })

  // ADR-033 decision 3: "the mode appears on the Operator UI persistently,
  // not on a settings page." These two tests are what hold that claim
  // structurally, exactly as the SessionPanel pair above holds its own:
  // the indicator has to be present on an ordinary route AND still
  // present when the main content is blocked, because a mode that
  // disappears the moment the coordinator connection degrades is missing
  // at the moment an operator most needs it.
  it('renders the persistent show mode indicator on an ordinary route', async () => {
    getShowModeConfig.mockResolvedValue(SHOW_MODE_RESPONSE)
    renderLayout(model({ kind: 'live', connectedAt: 0 }, 12345))

    await waitFor(() => expect(screen.getByLabelText('Show mode').textContent).toMatch(/Show/))
  })

  it('renders the show mode indicator even while content is blocked - it must not be gated on blockContent', async () => {
    getShowModeConfig.mockResolvedValue(SHOW_MODE_RESPONSE)
    renderLayout(
      model({ kind: 'incompatible', requiredVersion: 2, supportedVersions: [1], detail: 'nope' }, 12345),
    )

    // The main content IS blocked...
    expect(screen.queryByText('underlying view marker')).not.toBeInTheDocument()
    // ...and the mode indicator is still there.
    await waitFor(() => expect(screen.getByLabelText('Show mode').textContent).toMatch(/Show/))
  })

  it('renders the show mode indicator before any snapshot has ever arrived', async () => {
    getShowModeConfig.mockResolvedValue(SHOW_MODE_RESPONSE)
    renderLayout(model({ kind: 'connecting' }, null))

    await waitFor(() => expect(screen.getByLabelText('Show mode').textContent).toMatch(/Show/))
  })

  // GET / (getServiceDescriptor): "is this the thing I just deployed" has
  // no other answer anywhere in this UI during a fleet upgrade. Rendered
  // by CoordinatorBuildNotice, fetched once on mount, next to
  // ConnectionBanner rather than gated on `blockContent` -- this component
  // never touches `model.session`/`model.connection` at all, so it is
  // trivially present in every state, unlike the SessionPanel/mode-
  // indicator pairs above which specifically prove they are NOT gated.
  describe('coordinator build notice', () => {
    it('renders the coordinator version and the negotiated API version once the descriptor has been fetched', async () => {
      renderLayout(model({ kind: 'live', connectedAt: 0 }, 12345))

      await waitFor(() => expect(screen.getByText(/Coordinator 1\.2\.3/)).toBeInTheDocument())
      expect(screen.getByText(/API v3/)).toBeInTheDocument()
    })

    it('states plainly that the build could not be read, rather than rendering blank, on a failed fetch', async () => {
      getServiceDescriptor.mockReset()
      getServiceDescriptor.mockRejectedValue(new Error('network unreachable'))
      renderLayout(model({ kind: 'live', connectedAt: 0 }, 12345))

      await waitFor(() =>
        expect(screen.getByText(/Coordinator build: could not be read/)).toBeInTheDocument(),
      )
      expect(screen.getByText(/network unreachable/)).toBeInTheDocument()
      expect(screen.queryByText(/^Coordinator 1\.2\.3/)).not.toBeInTheDocument()
    })
  })

  // Operator-reported: at the sidebar breakpoint (>=768px), .app-nav had
  // scrollHeight 1228px against a clientHeight of 753px with no
  // overflow rule, so 475px of links were clipped below the viewport and
  // unreachable, while the document itself scrolled instead (because
  // nothing else contained that height) -- and the nav's own background
  // stopped at the viewport edge while links kept going underneath it
  // against the page background. jsdom does not run a real layout engine
  // or load global.css, so scrollHeight/clientHeight/computed overflow are
  // not observable from a render here -- this reads the actual rule from
  // the stylesheet instead, which is what this codebase's own
  // fppCommandCopyGuard.test.ts precedent does for the same reason.
  it('gives the sidebar its own internal scroll at the sidebar breakpoint, instead of clipping', () => {
    const css = readFileSync(path.resolve(__dirname, '../styles/global.css'), 'utf-8')
    const desktopBlock = css.match(/@media \(min-width: 768px\) \{([\s\S]*?)\n\}\n\n\.app-header/)
    expect(desktopBlock).not.toBeNull()
    const desktopBlockBody = desktopBlock?.[1] ?? ''
    const navRule = desktopBlockBody.match(/\.app-nav \{([\s\S]*?)\}/)
    expect(navRule).not.toBeNull()
    expect(navRule?.[1] ?? '').toMatch(/overflow-y:\s*auto/)
    // The phone (unqualified, mobile-first) rule must stay exactly as it
    // was -- a fixed bottom tab bar with no overflow rule of its own.
    const phoneRule = css.match(/\.app-nav \{([\s\S]*?)\}/)
    expect(phoneRule).not.toBeNull()
    expect(phoneRule?.[1] ?? '').not.toMatch(/overflow-y/)
  })

  // Operator-reported: the signed-in identity ("Signed in as eric
  // (admin)") and Sign out used to render as a full-width band in the
  // main column. It now renders in the header alongside the title, mode
  // indicator, and coordinator build line.
  it('renders the signed-in identity and sign-out control in the header, not as a full-width band', () => {
    const SIGNED_IN_SESSION: SessionResponse = {
      serverTime: '2026-08-11T12:00:00.000Z',
      authenticated: true,
      principal: { id: 'p1', name: 'eric', kind: 'human', role: 'admin' },
      session: { id: 's1', deviceLabel: 'console', createdAt: '2026-08-11T12:00:00.000Z' },
      credentialForm: 'session',
      scopes: [],
      scopesState: 'current',
      bootstrapRequired: false,
    }
    renderLayout(model({ kind: 'live', connectedAt: 0 }, 12345, SIGNED_IN_SESSION))

    const identity = screen.getByText(/Signed in as eric/)
    const header = document.querySelector('.app-header')
    expect(header).not.toBeNull()
    expect(header!.contains(identity)).toBe(true)
    expect(screen.getByRole('button', { name: /Sign out/ })).toBeInTheDocument()
  })
})

// The full set of every top-level nav link's href, per NAV_GROUPS in
// Layout.tsx -- kept here (rather than importing NAV_GROUPS, which is not
// exported) so a future change to that list must touch this assertion
// deliberately, the same way Layout.tsx's own "labels the single
// Configure nav entry" test above pins one entry directly.
const ALL_NAV_HREFS = [
  // Show night
  '/night',
  '/config/show.active',
  '/playlists/readiness',
  '/',
  // Monitor
  '/nodes',
  '/fpp',
  '/resolume',
  '/events',
  // Diagnostics
  '/capabilities',
  '/assets/manifest',
  '/audit',
  // Control
  '/macros',
  // Configure
  '/config',
  '/actions',
  '/access',
  '/config/show',
  '/config/show.surface',
  '/config/show.cue',
  '/config/audio.settings',
  '/config/audio.node',
  '/config/show.playlist',
  '/config/fpp-playlist-definitions',
  '/config/night.session',
  '/config/night.session.active',
  '/assets',
]

// Requirement: each of the 5 groups collapses/expands, the group holding
// the current route is always open regardless of stored state, a stored
// preference is otherwise honoured, a throwing localStorage never breaks
// rendering, and none of this touches what actually renders on the phone
// tab bar (collapsing is a sidebar-only CSS affordance -- see the
// "gives the sidebar its own internal scroll" test above for why jsdom
// cannot observe the CSS itself, and the stylesheet assertion below for
// the collapse rule's own equivalent).
describe('collapsible nav groups', () => {
  it('always reports the active route\'s group expanded, even when storage says it is collapsed', () => {
    window.localStorage.setItem('showmesh-ui-nav-groups', JSON.stringify({ Monitor: false }))
    renderLayout(model({ kind: 'live', connectedAt: 0 }, 12345), '/nodes')

    const monitorHeading = screen.getByRole('button', { name: /Monitor/ })
    expect(monitorHeading).toHaveAttribute('aria-expanded', 'true')
  })

  it('honours a stored collapsed/expanded preference for a group that does not hold the current route', () => {
    window.localStorage.setItem(
      'showmesh-ui-nav-groups',
      JSON.stringify({ 'Show night': false, Diagnostics: true }),
    )
    // /macros is Control's own route, so Control is the active group here
    // and Show night/Diagnostics are free to reflect their stored values.
    renderLayout(model({ kind: 'live', connectedAt: 0 }, 12345), '/macros')

    expect(screen.getByRole('button', { name: /Show night/ })).toHaveAttribute(
      'aria-expanded',
      'false',
    )
    expect(screen.getByRole('button', { name: /Diagnostics/ })).toHaveAttribute(
      'aria-expanded',
      'true',
    )
  })

  it('opens Show night by default on a first visit, with no stored preference at all', () => {
    // /macros keeps Show night out of the active group so its default,
    // not the active-route override, is what this observes.
    renderLayout(model({ kind: 'live', connectedAt: 0 }, 12345), '/macros')

    expect(screen.getByRole('button', { name: /Show night/ })).toHaveAttribute(
      'aria-expanded',
      'true',
    )
    expect(screen.getByRole('button', { name: /Monitor/ })).toHaveAttribute(
      'aria-expanded',
      'false',
    )
  })

  it('shows a link count on a collapsed group', () => {
    renderLayout(model({ kind: 'live', connectedAt: 0 }, 12345), '/macros')

    // Monitor is collapsed by default on this route (Control is active).
    const monitorHeading = screen.getByRole('button', { name: /Monitor/ })
    expect(monitorHeading).toHaveTextContent('4')
  })

  it('does not break rendering when localStorage throws on both read and write', () => {
    const storageProto = Object.getPrototypeOf(window.localStorage) as Storage
    const getItemSpy = vi.spyOn(storageProto, 'getItem').mockImplementation(() => {
      throw new Error('storage disabled')
    })
    const setItemSpy = vi.spyOn(storageProto, 'setItem').mockImplementation(() => {
      throw new Error('storage disabled')
    })

    expect(() => renderLayout(model({ kind: 'live', connectedAt: 0 }, 12345))).not.toThrow()

    // The nav still renders normally -- Show night's own default applies
    // (its group holds the active '/' route here too, so it is open
    // either way) and every link is still reachable.
    expect(screen.getByRole('link', { name: 'Dashboard' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Macros' })).toBeInTheDocument()

    getItemSpy.mockRestore()
    setItemSpy.mockRestore()
  })

  it('keeps every one of the 25 nav links present in the DOM, which is what the phone tab bar renders directly', () => {
    renderLayout(model({ kind: 'live', connectedAt: 0 }, 12345))

    for (const href of ALL_NAV_HREFS) {
      expect(document.querySelector(`a.app-nav__link[href="${href}"]`)).not.toBeNull()
    }
    expect(document.querySelectorAll('a.app-nav__link')).toHaveLength(ALL_NAV_HREFS.length)
  })

  // jsdom does not run layout or load global.css (see the sidebar-scroll
  // test above for the same caveat), so this reads the collapse rule
  // straight from the stylesheet: it must exist only inside the sidebar
  // (min-width: 768px) block, never in the phone-first, unqualified
  // rules, which is what keeps the phone tab bar untouched.
  it('confines the collapsed-group hiding rule to the sidebar breakpoint only', () => {
    const css = readFileSync(path.resolve(__dirname, '../styles/global.css'), 'utf-8')
    const desktopBlock = css.match(/@media \(min-width: 768px\) \{([\s\S]*?)\n\}\n\n\.app-header/)
    expect(desktopBlock).not.toBeNull()
    const desktopBlockBody = desktopBlock?.[1] ?? ''
    expect(desktopBlockBody).toMatch(/\.app-nav__group\[data-open='false'\]\s*\.app-nav__group-links\s*\{[\s\S]*?display:\s*none/)

    const phoneOnly = css.slice(0, css.indexOf('@media (min-width: 768px)'))
    expect(phoneOnly).not.toMatch(/data-open/)
    // The phone rule for the group and its links wrapper both stay
    // `display: contents`, exactly as #114 shipped for the group alone.
    expect(phoneOnly).toMatch(/\.app-nav__group\s*\{\s*display:\s*contents;\s*\}/)
    expect(phoneOnly).toMatch(/\.app-nav__group-links\s*\{\s*display:\s*contents;\s*\}/)
  })
})
