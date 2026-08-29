import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { Layout } from './Layout'
import { ModelContext } from './ModelContext'
import type { Model, SessionResponse } from './types'
import {
  makeConfigObjectSummary,
  makeCurrentRun,
  makeCurrentRuns,
  makeModel,
  makeShowList,
} from './test-support/fixtures'

// This file used to assert the pre-overhaul three-group collapsible rail
// (25 links split into primary/secondary, `data-open` sections persisted
// under `showmesh-ui-nav-groups`). Phase 0b (UI-DESIGN-GUIDE.md sections 2
// and 3, README.md "Global chrome" / "Information architecture") replaces
// that rail with the ChromeBar + seven-destination NavRail this file now
// tests instead. The collapsible-group behaviour is gone by design, not by
// omission -- it is not rewritten anywhere, because the seven-destination
// rail has nothing left to collapse.

const { getShowModeConfig, getServiceDescriptor, listConfigObjects } = vi.hoisted(() => ({
  getShowModeConfig: vi.fn(),
  getServiceDescriptor: vi.fn(),
  listConfigObjects: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getShowModeConfig, getServiceDescriptor, listConfigObjects }
})

const SHOW_MODE_RESPONSE = {
  serverTime: '2026-08-11T12:00:00.000Z',
  kind: 'show.mode' as const,
  revision: 4,
  payload: { mode: 'show' as const },
  updatedAt: '2026-08-11T12:00:00.000Z',
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'admin-1',
  source: 'api' as const,
  resolumeWebSocketEffect: 'show mode: the Resolume WebSocket wake-up channel is held CLOSED.',
}

const SERVICE_DESCRIPTOR = {
  serverTime: '2026-08-11T12:00:00.000Z',
  apiVersion: 3,
  supportedVersions: [3],
  coordinator: { version: '1.2.3', commit: 'abcdef1234567', buildDate: '2026-08-20T00:00:00Z', goVersion: 'go1.23.0' },
}

beforeEach(() => {
  window.localStorage.clear()
  document.documentElement.removeAttribute('data-theme')
  getServiceDescriptor.mockResolvedValue(SERVICE_DESCRIPTOR)
  getShowModeConfig.mockResolvedValue(SHOW_MODE_RESPONSE)
  listConfigObjects.mockResolvedValue(makeShowList([]))
})

afterEach(() => {
  cleanup()
  getShowModeConfig.mockReset()
  getServiceDescriptor.mockReset()
  listConfigObjects.mockReset()
  document.documentElement.removeAttribute('data-theme')
})

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

function renderLayout(model: Model, initialPath = '/') {
  return render(
    <ModelContext.Provider value={model}>
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

// href -> label, exactly the seven destinations UI-DESIGN-GUIDE.md section 3
// names, grouped Operate / Author / System. "Do not add an eighth without
// deleting one."
const RAIL_LINKS: [string, string][] = [
  ['/', 'Dashboard'],
  ['/night', 'Show Night'],
  ['/control', 'Live Control'],
  ['/shows', 'Shows'],
  ['/assets', 'Assets'],
  ['/monitor', 'Monitor'],
  ['/settings', 'Settings'],
]

describe('a session state replaces the routed view rather than sitting above it', () => {
  /* Regression guard. The blanking plate first shipped rendered ABOVE the rail
   * and grid, with the routed view still rendering underneath it, so the page
   * said "nothing here has ever been collected" and drew a dashboard at the
   * same time. Both the band and the plate belong in the main column, and they
   * take the place of the routed view: a device that has read nothing must not
   * show an empty dashboard that reads as real inventory.
   *
   * jsdom does no layout, so this asserts the DOM relationship, which is the
   * part that was actually wrong. */
  it('renders no underlying view while signed out', () => {
    renderLayout(makeModel({ session: { ...SIGNED_IN_SESSION, authenticated: false, principal: null, session: null } }))

    expect(screen.queryByText('underlying view marker')).not.toBeInTheDocument()
    expect(screen.getByText('Signed out on this device')).toBeInTheDocument()
  })

  it('renders no underlying view while the coordinator is unclaimed', () => {
    renderLayout(makeModel({ session: { ...SIGNED_IN_SESSION, authenticated: false, principal: null, session: null, bootstrapRequired: true } }))

    expect(screen.queryByText('underlying view marker')).not.toBeInTheDocument()
    expect(screen.getByText('No administrator exists on this coordinator')).toBeInTheDocument()
  })

  it('keeps the rail beside the session state, never above it', () => {
    const { container } = renderLayout(makeModel({ session: { ...SIGNED_IN_SESSION, authenticated: false, principal: null, session: null } }))

    const shell = container.querySelector('.shell')
    const rail = container.querySelector('[data-rail], .rail')
    const main = container.querySelector('main')
    expect(shell).not.toBeNull()
    expect(shell?.contains(rail as Node)).toBe(true)
    expect(shell?.contains(main as Node)).toBe(true)
    expect(main?.textContent).toContain('Signed out on this device')
  })

  it('renders the routed view normally once there is a usable session', () => {
    renderLayout(makeModel())

    expect(screen.getByText('underlying view marker')).toBeInTheDocument()
  })
})

describe('NavRail (via Layout)', () => {
  it('renders exactly the seven destinations, grouped under real headings, and no eighth', () => {
    renderLayout(makeModel())

    const nav = screen.getByRole('navigation', { name: 'Operator navigation' })
    for (const [href, label] of RAIL_LINKS) {
      expect(nav.querySelector(`a[href="${href}"]`)).toHaveTextContent(label)
    }
    expect(nav.querySelectorAll('a')).toHaveLength(RAIL_LINKS.length)

    // Group labels are real headings (UI-DESIGN-GUIDE.md section 6: "if it
    // names a section it is a heading"), not bare styled spans.
    const headings = nav.querySelectorAll('h3')
    expect(Array.from(headings, (h) => h.textContent)).toEqual(['Operate', 'Author', 'System'])
  })

  it('marks the current destination with page semantics', () => {
    renderLayout(makeModel(), '/control')

    const nav = screen.getByRole('navigation', { name: 'Operator navigation' })
    expect(nav.querySelector('a[href="/control"]')).toHaveAttribute('aria-current', 'page')
    expect(nav.querySelector('a[href="/"]')).not.toHaveAttribute('aria-current')
  })

  it('renders no rail badge for any destination when none is supplied -- a badge is an attention count, never invented', () => {
    renderLayout(makeModel())

    expect(document.querySelector('.rail__badge')).toBeNull()
  })
})

describe('ChromeBar (via Layout) — now-playing', () => {
  it('states plainly that playback could not be read on a failed current-runs fetch, rather than a fabricated now-playing', () => {
    renderLayout(makeModel({ currentRuns: null, currentRunsReceivedAt: null, currentRunsFetchFailed: true }))
    expect(screen.getByText('Playback status unavailable')).toBeInTheDocument()
  })

  it('states that it is checking, not "nothing playing," before current-runs has ever been fetched this session', () => {
    renderLayout(makeModel({ currentRuns: null, currentRunsReceivedAt: null, currentRunsFetchFailed: false }))
    expect(screen.getByText('Checking for an active run…')).toBeInTheDocument()
  })

  it('states a settled empty state when current-runs has loaded and nothing is playing', () => {
    renderLayout(
      makeModel({
        currentRuns: makeCurrentRuns({ runs: [] }),
        currentRunsReceivedAt: 1000,
        currentRunsFetchFailed: false,
      }),
    )
    expect(screen.getByText('Nothing currently playing')).toBeInTheDocument()
  })

  it('renders the playing item, its position, and the next reported item from real CurrentRun fields only', () => {
    const run = makeCurrentRun({
      status: 'playing',
      playback: {
        state: 'playing',
        reason: 'current item is playing',
        itemId: 'cue-1',
        itemIndex: 2,
        positionMs: 102_000,
        media: 'carol-of-the-bells.fseq',
        evidence: [],
      },
      next: { itemId: 'cue-2', itemIndex: 3, media: 'next-item.fseq', source: 'fpp' },
    })
    renderLayout(
      makeModel({
        currentRuns: makeCurrentRuns({ runs: [run] }),
        currentRunsReceivedAt: 1000,
        currentRunsFetchFailed: false,
      }),
    )

    expect(screen.getByText('carol-of-the-bells.fseq')).toBeInTheDocument()
    expect(screen.getByText('1:42')).toBeInTheDocument()
    expect(screen.getByText(/next: next-item\.fseq/)).toBeInTheDocument()
  })

  it('picks the run actually playing, not merely the first one reported, when more than one run exists', () => {
    const stopped = makeCurrentRun({ id: 'run-audio-1', runner: 'showmesh-audio', status: 'stopped' })
    const playing = makeCurrentRun({
      id: 'run-fpp-1',
      runner: 'fpp',
      status: 'playing',
      playback: { ...makeCurrentRun().playback, media: 'the-one-playing.fseq' },
    })
    renderLayout(
      makeModel({
        currentRuns: makeCurrentRuns({ runs: [stopped, playing] }),
        currentRunsReceivedAt: 1000,
        currentRunsFetchFailed: false,
      }),
    )

    expect(screen.getByText('the-one-playing.fseq')).toBeInTheDocument()
  })

  // CurrentRun carries positionMs but no total-duration field to pair it
  // with (verified against api/openapi.yaml's CurrentPlayback/CurrentRun
  // schemas -- see ChromeBar.tsx's own header comment). Asserting a
  // progress bar's aria-valuemax against an invented ceiling would be
  // exactly the fabrication UI-DESIGN-GUIDE.md section 4 forbids, so the
  // bar always renders the plain strip today, even while a run is playing.
  it('never renders a progressbar role, because CurrentRun has no duration to assert one against', () => {
    const run = makeCurrentRun({ status: 'playing' })
    renderLayout(
      makeModel({
        currentRuns: makeCurrentRuns({ runs: [run] }),
        currentRunsReceivedAt: 1000,
        currentRunsFetchFailed: false,
      }),
    )

    expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
    expect(document.querySelector('.chrome__progress-strip')).toBeInTheDocument()
  })
})

describe('ChromeBar (via Layout) — show picker', () => {
  it('shows the active show\'s label, resolved from the show list, not its bare id', async () => {
    listConfigObjects.mockResolvedValue(makeShowList(['winter-ridge-2026']))
    // Override the fixture's generated label so this test can tell "the id"
    // apart from "the resolved label" -- makeShowList defaults label === id.
    listConfigObjects.mockResolvedValue({
      serverTime: '2026-08-11T12:00:00.000Z',
      kind: 'show',
      objects: [makeConfigObjectSummary({ id: 'winter-ridge-2026', label: 'Winter Ridge 2026' })],
    })
    renderLayout(
      makeModel({
        currentRuns: makeCurrentRuns({ activeShow: { configured: true, show: 'winter-ridge-2026', generation: 1 } }),
        currentRunsReceivedAt: 1000,
      }),
    )

    await waitFor(() => expect(screen.getByText('Winter Ridge 2026')).toBeInTheDocument())
  })

  it('states plainly that no show is selected, rather than an empty picker', () => {
    renderLayout(
      makeModel({
        currentRuns: makeCurrentRuns({ activeShow: { configured: false, show: null, generation: null } }),
        currentRunsReceivedAt: 1000,
      }),
    )

    expect(screen.getByText('none selected')).toBeInTheDocument()
  })

  it('links to the active-show screen rather than switching shows from a bare click (activation is the sharp control)', () => {
    renderLayout(makeModel())
    const picker = document.querySelector('.chrome__show-picker')
    expect(picker).toHaveAttribute('href', '/shows')
  })
})

describe('ChromeBar (via Layout) — mode badge', () => {
  it('renders the show mode once GET /config/show.mode resolves', async () => {
    renderLayout(makeModel())
    await waitFor(() => expect(screen.getByLabelText('Show mode').textContent).toMatch(/Show mode/))
  })

  it('states that the mode could not be read, rather than defaulting to a mode, on a failed read', async () => {
    getShowModeConfig.mockReset()
    getShowModeConfig.mockRejectedValue(new Error('network unreachable'))
    renderLayout(makeModel())
    await waitFor(() => expect(screen.getByLabelText('Show mode').textContent).toMatch(/cannot be read/))
  })
})

describe('ChromeBar (via Layout) — connection and principal', () => {
  it.each([
    [{ kind: 'live', connectedAt: 0 } as const, 'Live'],
    [{ kind: 'connecting' } as const, 'Connecting'],
    [{ kind: 'reconnecting', attempt: 1, nextAttemptAt: 0, lastError: 'boom' } as const, 'Reconnecting'],
    [{ kind: 'unauthorized', reason: 'missing' } as const, 'Signed out'],
    [{ kind: 'failed', detail: 'boom' } as const, 'Offline'],
  ])('states the connection state as a word, not colour alone: %o -> %s', (connection, word) => {
    renderLayout(makeModel({ connection }))
    expect(document.querySelector('.chrome__connection')).toHaveTextContent(word)
  })

  it('renders the signed-in principal\'s name in the bar', () => {
    renderLayout(makeModel({ session: SIGNED_IN_SESSION, sessionReceivedAt: 0 }))
    expect(screen.getByText('eric')).toBeInTheDocument()
  })

  it('states "Signed out" rather than a blank when no session is signed in', () => {
    renderLayout(makeModel({ session: SIGNED_OUT_SESSION, sessionReceivedAt: 0 }))
    expect(screen.getAllByText('Signed out').length).toBeGreaterThan(0)
  })
})

describe('Layout composition', () => {
  // Acceptance criterion 5: an incompatible coordinator produces the
  // explicit error, never a partial render of the normal views.
  it('does not render the underlying view while the API version is incompatible', () => {
    renderLayout(
      makeModel({
        connection: { kind: 'incompatible', requiredVersion: 2, supportedVersions: [1], detail: 'nope' },
      }),
    )
    expect(screen.queryByText('underlying view marker')).not.toBeInTheDocument()
  })

  it('does not render the underlying view before any snapshot has ever arrived', () => {
    renderLayout(makeModel({ connection: { kind: 'connecting' }, snapshotReceivedAt: null }))
    expect(screen.queryByText('underlying view marker')).not.toBeInTheDocument()
  })

  it('renders the underlying view once data has been received, even while reconnecting', () => {
    renderLayout(
      makeModel({ connection: { kind: 'reconnecting', attempt: 2, nextAttemptAt: 0, lastError: 'boom' } }),
    )
    expect(screen.getByText('underlying view marker')).toBeInTheDocument()
  })

  it('renders the underlying view normally while live', () => {
    renderLayout(makeModel())
    expect(screen.getByText('underlying view marker')).toBeInTheDocument()
  })

  it('renders the persistent session panel when a real session is present, even outside the chrome bar\'s compact principal name', () => {
    renderLayout(makeModel({ session: SIGNED_OUT_SESSION, sessionReceivedAt: 0 }))
    expect(screen.getByText('Signed out on this device')).toBeInTheDocument()
  })

  it('renders the persistent session panel even while content is blocked (incompatible version) -- it must not be gated on blockContent', () => {
    renderLayout(
      makeModel({
        connection: { kind: 'incompatible', requiredVersion: 2, supportedVersions: [1], detail: 'nope' },
        session: SIGNED_OUT_SESSION,
        sessionReceivedAt: 0,
      }),
    )
    expect(screen.queryByText('underlying view marker')).not.toBeInTheDocument()
    expect(screen.getByText('Signed out on this device')).toBeInTheDocument()
  })

  it('renders the show mode badge even while content is blocked -- it must not be gated on blockContent', async () => {
    renderLayout(
      makeModel({
        connection: { kind: 'incompatible', requiredVersion: 2, supportedVersions: [1], detail: 'nope' },
      }),
    )
    expect(screen.queryByText('underlying view marker')).not.toBeInTheDocument()
    await waitFor(() => expect(screen.getByLabelText('Show mode').textContent).toMatch(/Show mode/))
  })

  it('keeps the signed-in identity and sign-out control reachable in the rail\'s utility area', () => {
    renderLayout(makeModel({ session: SIGNED_IN_SESSION, sessionReceivedAt: 0 }))

    const footer = screen.getByRole('contentinfo', { name: 'Operator status and controls' })
    const identity = screen.getByText(/Signed in as eric/)
    expect(footer.contains(identity)).toBe(true)
    const signOut = screen.getByRole('button', { name: /Sign out/ })
    expect(footer.contains(signOut)).toBe(true)
  })

  // The design moved the coordinator build string out of the chrome bar
  // and into Settings > Appearance to pay for the now-playing group's
  // space (README.md "The bar must not wrap"). CoordinatorBuildNotice
  // stays defined and exported (Settings > Appearance picks it up later)
  // but this shell no longer renders it.
  it('no longer renders the coordinator build string in the shell', async () => {
    renderLayout(makeModel())
    await waitFor(() => expect(getServiceDescriptor).not.toHaveBeenCalled())
    expect(screen.queryByText(/Coordinator 1\.2\.3/)).not.toBeInTheDocument()
  })

  it('offers a working theme control reachable from the shell, ahead of its eventual move to Settings > Appearance', async () => {
    const user = userEvent.setup()
    renderLayout(makeModel())

    const contrastOption = screen.getByRole('button', { name: 'Contrast' })
    expect(contrastOption).toHaveAttribute('aria-pressed', 'false')
    await user.click(contrastOption)

    expect(contrastOption).toHaveAttribute('aria-pressed', 'true')
    expect(document.documentElement.getAttribute('data-theme')).toBe('contrast')
    expect(window.localStorage.getItem('showmesh-ui-theme')).toBe('contrast')
  })
})
