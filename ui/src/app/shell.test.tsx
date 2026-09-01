import { readdirSync, readFileSync, statSync } from 'node:fs'
import path from 'node:path'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { CSRFRejectedError, TooManyRequestsError } from '../api'
import type { Model, SessionResponse } from '../api'
import { clearStoredToken, setStoredToken } from '../api/token'
import { initialModel } from '../api/domain'
import { ModelContext } from './ModelContext'
import { Layout } from './Layout'
import { BootstrapBand, SignedOutBand, useSignedOutBand } from './SessionBand'
import { NotFound } from '../screens/NotFound'

const loginMock = vi.fn()
const claimBootstrapMock = vi.fn()
const logoutMock = vi.fn()
const getShowModeConfigMock = vi.fn()
const putShowModeConfigMock = vi.fn()
const getCurrentNightSessionMock = vi.fn()
const listConfigObjectsMock = vi.fn()
const getShowActiveMock = vi.fn()
const putShowActiveMock = vi.fn()

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    login: (...args: unknown[]) => loginMock(...args),
    claimBootstrap: (...args: unknown[]) => claimBootstrapMock(...args),
    logout: (...args: unknown[]) => logoutMock(...args),
    getShowModeConfig: (...args: unknown[]) => getShowModeConfigMock(...args),
    putShowModeConfig: (...args: unknown[]) => putShowModeConfigMock(...args),
    getCurrentNightSession: (...args: unknown[]) => getCurrentNightSessionMock(...args),
    listConfigObjects: (...args: unknown[]) => listConfigObjectsMock(...args),
    getShowActive: (...args: unknown[]) => getShowActiveMock(...args),
    putShowActive: (...args: unknown[]) => putShowActiveMock(...args),
  }
})

function session(overrides: Partial<SessionResponse>): SessionResponse {
  return {
    serverTime: '2026-08-28T21:07:00Z',
    authenticated: false,
    principal: null,
    session: null,
    credentialForm: null,
    scopes: [],
    scopesState: 'not_applicable',
    bootstrapRequired: false,
    ...overrides,
  }
}

/** SignedOutBand and SignedOutPlate share one credential-form state; the hook needs a component to run in. */
function SignedOutBandHarness() {
  const state = useSignedOutBand()
  return <SignedOutBand state={state} />
}

/** Both the heading button and the submit button read "Sign in"; only the submit button actually submits. */
function signInSubmitButton(): HTMLButtonElement {
  const button = document.querySelector<HTMLButtonElement>('form button[type="submit"]')
  if (button === null) throw new Error('no submit button in the sign-in form')
  return button
}

function renderShell(model: Partial<Model>, route = '/') {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model }}>
      <MemoryRouter initialEntries={[route]}>
        <Routes>
          <Route path="/" element={<Layout />}>
            <Route path="*" element={<NotFound />} />
          </Route>
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('app shell', () => {
  beforeEach(() => {
    loginMock.mockReset().mockResolvedValue(undefined)
    claimBootstrapMock.mockReset().mockResolvedValue(undefined)
    logoutMock.mockReset().mockResolvedValue(undefined)
    getShowModeConfigMock.mockReset().mockReturnValue(new Promise(() => {}))
    putShowModeConfigMock.mockReset()
    getCurrentNightSessionMock.mockReset().mockResolvedValue({ serverTime: '2026-09-01T00:00:00Z', session: null })
    listConfigObjectsMock.mockReset()
    getShowActiveMock.mockReset().mockReturnValue(new Promise(() => {}))
    putShowActiveMock.mockReset()
    clearStoredToken()
  })
  afterEach(cleanup)

  function authenticatedSession(overrides: Partial<SessionResponse> = {}): SessionResponse {
    return session({
      authenticated: true,
      principal: { id: 'p1', name: 'erbartos', kind: 'human', role: 'admin' },
      scopes: ['config:write'],
      scopesState: 'current',
      ...overrides,
    })
  }

  function currentRuns(show: string | null) {
    return {
      serverTime: '2026-09-01T00:00:00Z',
      activeShow: { configured: show !== null, show, generation: show !== null ? 1 : null },
      runs: [],
    }
  }

  function showActiveConfig(show: string, revision = 3) {
    return {
      serverTime: '2026-09-01T00:00:00Z',
      kind: 'show.active',
      id: 'show.active',
      revision,
      payload: { show },
      updatedAt: '2026-08-31T00:00:00Z',
      createdByPrincipalId: 'p1',
      createdByPrincipalName: 'erbartos',
      source: 'api',
    }
  }

  function showModeConfig(pin: { pinned: false } | { pinned: true; show: string; generation: number; pinnedAt: string }) {
    return {
      serverTime: '2026-08-30T21:00:00Z',
      kind: 'show.mode',
      revision: 2,
      payload: { mode: 'show' as const },
      updatedAt: '2026-08-30T18:00:00Z',
      createdByPrincipalId: 'p1',
      createdByPrincipalName: 'erbartos',
      source: 'api',
      resolumeWebSocketEffect: 'closed in show mode',
      cueActivationPin: { effect: 'A show.cue edit saved now applies immediately.', ...pin },
    }
  }

  it('shows the mode badge with no pin note when the cue activation pin is not pinned', async () => {
    getShowModeConfigMock.mockResolvedValue(showModeConfig({ pinned: false }))
    renderShell({ session: authenticatedSession() })
    const badge = await screen.findByRole('button', { name: /^show$/i })
    expect(badge.textContent).not.toContain('edit staged')
    expect(badge.title).toBe('closed in show mode. A show.cue edit saved now applies immediately.')
  })

  it('renders the mode badge when an older coordinator reports no cue activation pin', async () => {
    const withoutPin: Record<string, unknown> = { ...showModeConfig({ pinned: false }) }
    delete withoutPin['cueActivationPin']
    getShowModeConfigMock.mockResolvedValue(withoutPin as unknown as ReturnType<typeof showModeConfig>)
    renderShell({ session: authenticatedSession() })
    const badge = await screen.findByRole('button', { name: /^show$/i })
    expect(badge.textContent).not.toContain('edit staged')
    expect(badge.title).toBe('closed in show mode')
  })

  it('marks the mode badge staged and names the pin when the cue activation pin is pinned', async () => {
    getShowModeConfigMock.mockResolvedValue(
      showModeConfig({
        pinned: true,
        show: 'winter-2026',
        generation: 4,
        pinnedAt: '2026-08-30T19:00:00Z',
      }),
    )
    renderShell({ session: authenticatedSession() })
    const badge = await screen.findByRole('button', { name: /edit staged/i })
    expect(badge.title).toBe('closed in show mode. A show.cue edit saved now applies immediately.')
    expect(screen.getByText(/A show\.cue edit is staged and will not reach any node/)).toBeInTheDocument()
  })

  it('renders the Live Control Resolume sublink with the primary rail destinations', () => {
    renderShell({})
    const rail = screen.getByRole('navigation', { name: 'Primary' })
    const labels = [...rail.querySelectorAll('a')].map((link) => link.textContent)
    expect(labels).toEqual([
      'Dashboard',
      'Show Night',
      'Live Control',
      'Resolume',
      'Shows',
      'Assets',
      'Monitor',
      'Settings',
    ])
  })

  it('shows no rail badge before anything has been read', () => {
    renderShell({})
    const rail = screen.getByRole('navigation', { name: 'Primary' })
    expect(rail.querySelectorAll('.sm-rail__badge')).toHaveLength(0)
  })

  it('renders signed out as a band with the rail still present', () => {
    renderShell({ session: session({ authenticated: false }) })
    expect(screen.getByText('Signed out on this device')).toBeInTheDocument()
    expect(screen.getByRole('navigation', { name: 'Primary' })).toBeInTheDocument()
  })

  it('lets bootstrap outrank signed out', () => {
    renderShell({ session: session({ authenticated: false, bootstrapRequired: true }) })
    expect(screen.getByText('No administrator exists on this coordinator')).toBeInTheDocument()
    expect(screen.queryByText('Signed out on this device')).not.toBeInTheDocument()
  })

  it('shows the connecting band, not a sign-in one, while the first session response is outstanding', () => {
    renderShell({ session: null })
    expect(screen.getByRole('heading', { name: 'Reading the coordinator' })).toBeInTheDocument()
    expect(screen.queryByText('Signed out on this device')).not.toBeInTheDocument()
    expect(screen.queryByText('No administrator exists on this coordinator')).not.toBeInTheDocument()
  })

  it('says nothing is playing rather than inventing a title, once this device can read', () => {
    renderShell({ session: session({ authenticated: true }) })
    expect(screen.getByText('Nothing playing')).toBeInTheDocument()
  })

  it('never claims nothing is playing on a device that cannot read the show', () => {
    for (const state of [
      { model: { session: session({ authenticated: false }) }, now: 'Nothing is being read on this device', who: 'Signed out' },
      {
        model: { session: session({ authenticated: false, bootstrapRequired: true }) },
        now: 'Unclaimed coordinator, no administrator exists',
        who: 'No principal',
      },
      { model: { session: null }, now: 'Reading the coordinator', who: 'not signed in yet' },
    ]) {
      cleanup()
      renderShell(state.model)
      expect(screen.queryByText('Nothing playing')).not.toBeInTheDocument()
      expect(screen.getAllByText(state.now).length).toBeGreaterThan(0)
      expect(screen.getAllByText(state.who).length).toBeGreaterThan(0)
    }
  })

  it('maps an old address to its new home instead of redirecting', () => {
    renderShell({ session: session({ authenticated: true }) }, '/events')
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('No page at this address')
    expect(screen.getByRole('link', { name: 'Go to Monitor › Activity' })).toBeInTheDocument()
  })

  it('shows no clock skew strip when the skew has not been observed', () => {
    renderShell({ clockSkewMs: null })
    expect(screen.queryByText('Clock skew', { exact: false })).not.toBeInTheDocument()
  })

  it('shows no clock skew strip when the skew is under the threshold', () => {
    renderShell({ clockSkewMs: 4_999 })
    expect(screen.queryByText('Clock skew', { exact: false })).not.toBeInTheDocument()
  })

  it('shows the strip when the skew is exactly at the threshold', () => {
    renderShell({ clockSkewMs: 5_000 })
    expect(screen.getByRole('status')).toHaveTextContent('Clock skew')
  })

  it('names the coordinator as ahead when clockSkewMs is positive', () => {
    renderShell({ clockSkewMs: 90_000 })
    const strip = screen.getByRole('status')
    expect(strip).toHaveTextContent('Clock skew')
    expect(strip).toHaveTextContent('behind the coordinator')
  })

  it('names the browser as ahead when clockSkewMs is negative', () => {
    renderShell({ clockSkewMs: -90_000 })
    const strip = screen.getByRole('status')
    expect(strip).toHaveTextContent('Clock skew')
    expect(strip).toHaveTextContent('ahead of the coordinator')
  })

  it('requires the device-name field on the signed-out band and never calls login without it', () => {
    renderShell({ session: session({ authenticated: false }) })
    const deviceField = screen.getByLabelText(/This device.s name/)
    expect(deviceField).toBeRequired()

    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'erbartos' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'hunter2' } })
    fireEvent.click(signInSubmitButton())
    expect(loginMock).not.toHaveBeenCalled()

    fireEvent.change(deviceField, { target: { value: 'porch tablet' } })
    fireEvent.click(signInSubmitButton())
    expect(loginMock).toHaveBeenCalledExactlyOnceWith('erbartos', 'hunter2', 'porch tablet')
  })

  it('renders the cross-site refusal as a headline plus its explanation', async () => {
    loginMock.mockRejectedValue(new CSRFRejectedError('neither Sec-Fetch-Site nor a matching Origin'))
    renderShell({ session: session({ authenticated: false }) })
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'erbartos' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'hunter2' } })
    fireEvent.change(screen.getByLabelText(/This device.s name/), { target: { value: 'porch tablet' } })
    fireEvent.click(signInSubmitButton())

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(
      'This page and the coordinator disagree about which host you are on, so the sign-in was refused as a cross-site request.',
    )
    expect(alert).toHaveTextContent('Usually a proxy in front of ShowMesh rewriting the Host header.')
  })

  it('renders the rate-limit refusal as a headline naming the wait plus its explanation', async () => {
    loginMock.mockRejectedValue(new TooManyRequestsError('too many attempts', 30))
    renderShell({ session: session({ authenticated: false }) })
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'erbartos' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'hunter2' } })
    fireEvent.change(screen.getByLabelText(/This device.s name/), { target: { value: 'porch tablet' } })
    fireEvent.click(signInSubmitButton())

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Wait 30s and try again.')
    expect(alert).toHaveTextContent('This is a rate limit on the network you are on, not a lockout on your account.')
  })

  it('offers Clear stored token only when a token is stored on this device', () => {
    renderShell({ session: session({ authenticated: false }) })
    expect(screen.queryByRole('button', { name: 'Clear stored token' })).not.toBeInTheDocument()
    cleanup()

    setStoredToken('a-machine-token')
    renderShell({ session: session({ authenticated: false }) })
    expect(screen.getByRole('button', { name: 'Clear stored token' })).toBeInTheDocument()
  })

  it('gives the never-collected plate the dashed unobserved edge and the settled-empty plate a different one', () => {
    const { container: signedOutContainer } = renderShell({ session: session({ authenticated: false }) })
    expect(signedOutContainer.querySelector('.sm-plate--unobserved')).not.toBeNull()
    cleanup()

    const { container: bootstrapContainer } = renderShell({
      session: session({ authenticated: false, bootstrapRequired: true }),
    })
    expect(bootstrapContainer.querySelector('.sm-plate--unobserved')).toBeNull()
    expect(screen.getByText('No shows, no nodes, no principals')).toBeInTheDocument()
  })

  it('gives the signed-out and bootstrap bands exactly one h1, matching the mock', () => {
    const { unmount: unmountSignedOut } = render(<SignedOutBandHarness />)
    expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1)
    expect(screen.getByRole('heading', { level: 1, name: 'Signed out on this device' })).toBeInTheDocument()
    unmountSignedOut()

    render(<BootstrapBand />)
    expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1)
    expect(screen.getByRole('heading', { level: 1, name: 'No administrator exists on this coordinator' })).toBeInTheDocument()
  })

  it('puts the unobserved plate in main, in the Outlet’s place, while signed out', () => {
    renderShell({ session: session({ authenticated: false }) })
    // Exactly one h1 in the whole document: the band's. The plate's own heading is an h2.
    expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1)
    expect(screen.getByRole('heading', { level: 1, name: 'Signed out on this device' })).toBeInTheDocument()
    const main = document.querySelector('main.sm-main')
    expect(main).not.toBeNull()
    expect(main?.querySelector('.sm-plate--unobserved')).not.toBeNull()
    expect(main).toHaveTextContent('Nothing here has ever been collected')
  })

  it('puts the empty plate in main, in the Outlet’s place, during bootstrap', () => {
    renderShell({ session: session({ authenticated: false, bootstrapRequired: true }) })
    expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1)
    expect(screen.getByRole('heading', { level: 1, name: 'No administrator exists on this coordinator' })).toBeInTheDocument()
    const main = document.querySelector('main.sm-main')
    expect(main).not.toBeNull()
    expect(main).toHaveTextContent('No shows, no nodes, no principals')
  })

  it('names both outstanding reads on the connecting band', () => {
    renderShell({ session: null })
    expect(screen.getByRole('heading', { name: 'Reading the coordinator' })).toBeInTheDocument()
    expect(screen.getByText('Session')).toBeInTheDocument()
    expect(screen.getByText('Live updates')).toBeInTheDocument()
    expect(screen.getByText(/Not connected\. Observations, run outcomes and now-playing/)).toBeInTheDocument()
  })

  it('offers Sign out only once signed in, never on a signed-out or bootstrap device', () => {
    renderShell({ session: session({ authenticated: true, principal: { id: 'p1', name: 'erbartos', kind: 'human', role: 'admin' } }) })
    expect(screen.getByRole('button', { name: 'Sign out' })).toBeInTheDocument()
    cleanup()

    renderShell({ session: session({ authenticated: false }) })
    expect(screen.queryByRole('button', { name: 'Sign out' })).not.toBeInTheDocument()
    cleanup()

    renderShell({ session: session({ authenticated: false, bootstrapRequired: true }) })
    expect(screen.queryByRole('button', { name: 'Sign out' })).not.toBeInTheDocument()
  })

  it('does not call logout on the first click: it arms a confirm step first', () => {
    renderShell({ session: session({ authenticated: true, principal: { id: 'p1', name: 'erbartos', kind: 'human', role: 'admin' } }) })
    fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))
    expect(logoutMock).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Confirm sign out' })).toBeInTheDocument()
  })

  it('cancels the sign-out confirm without calling logout', () => {
    renderShell({ session: session({ authenticated: true, principal: { id: 'p1', name: 'erbartos', kind: 'human', role: 'admin' } }) })
    fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(logoutMock).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Sign out' })).toBeInTheDocument()
  })

  it('calls logout only on the confirm click, and lands on the signed-out band through the existing session state', async () => {
    logoutMock.mockImplementation(() => Promise.resolve())
    const { rerender } = renderShell({ session: session({ authenticated: true, principal: { id: 'p1', name: 'erbartos', kind: 'human', role: 'admin' } }) })
    fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm sign out' }))
    expect(logoutMock).toHaveBeenCalledExactlyOnceWith(undefined)

    // logout() leaves model.session signed out (ADR-024); the shell must
    // reflect that through the existing signed-out band, never a redirect.
    rerender(
      <ModelContext.Provider value={{ ...initialModel(), session: session({ authenticated: false }) }}>
        <MemoryRouter initialEntries={['/']}>
          <Routes>
            <Route path="/" element={<Layout />}>
              <Route path="*" element={<NotFound />} />
            </Route>
          </Routes>
        </MemoryRouter>
      </ModelContext.Provider>,
    )
    expect(await screen.findByText('Signed out on this device')).toBeInTheDocument()
  })

  it('lists the not-found table’s old addresses, grouped as the mock draws them', () => {
    renderShell({}, '/wherever')
    expect(screen.getByText('/nodes · /fpp · /resolume')).toBeInTheDocument()
    expect(screen.getByText('/events · /audit')).toBeInTheDocument()
    expect(screen.getByText('/actions · /macros')).toBeInTheDocument()
    expect(screen.getByText('/config/show/…/cues')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Shows › Automation' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Shows › Cues' })).toBeInTheDocument()
  })

  describe('the show pill and mode badge stay quiet while signed out', () => {
    it('reads nothing and shows no failure text while signed out, bootstrap-required, or still loading', () => {
      for (const blindSession of [session({ authenticated: false }), session({ authenticated: false, bootstrapRequired: true }), null]) {
        cleanup()
        renderShell({ session: blindSession })
        expect(getShowActiveMock).not.toHaveBeenCalled()
        expect(getShowModeConfigMock).not.toHaveBeenCalled()
        expect(screen.queryByText('Show not reported')).not.toBeInTheDocument()
        expect(screen.getAllByText('Show').length).toBeGreaterThan(0)
        expect(screen.getAllByText('Mode').length).toBeGreaterThan(0)
      }
    })

    it('issues the reads and renders the pill and badge only once the session authenticates', async () => {
      getShowActiveMock.mockResolvedValue(showActiveConfig('winter-2026'))
      getShowModeConfigMock.mockResolvedValue(showModeConfig({ pinned: false }))
      const { rerender } = renderShell({ session: session({ authenticated: false }) })
      expect(getShowActiveMock).not.toHaveBeenCalled()
      expect(getShowModeConfigMock).not.toHaveBeenCalled()

      rerender(
        <ModelContext.Provider value={{ ...initialModel(), session: authenticatedSession() }}>
          <MemoryRouter initialEntries={['/']}>
            <Routes>
              <Route path="/" element={<Layout />}>
                <Route path="*" element={<NotFound />} />
              </Route>
            </Routes>
          </MemoryRouter>
        </ModelContext.Provider>,
      )

      expect(await screen.findByRole('button', { name: /winter-2026/i })).toBeInTheDocument()
      expect(await screen.findByRole('button', { name: /^show$/i })).toBeInTheDocument()
      expect(getShowActiveMock).toHaveBeenCalledTimes(1)
      expect(getShowModeConfigMock).toHaveBeenCalledTimes(1)
    })
  })

  describe('D-020 the show pill popover', () => {
    it('opens even when current-runs is absent, using getShowActive as the source of truth', async () => {
      listConfigObjectsMock.mockResolvedValue({ objects: [] })
      getShowActiveMock.mockResolvedValue(showActiveConfig('winter-2026'))

      renderShell({ session: authenticatedSession(), currentRuns: null })

      const pill = await screen.findByRole('button', { name: /winter-2026/i })
      fireEvent.click(pill)
      expect(await screen.findByRole('dialog', { name: 'Choose show' })).toBeInTheDocument()
    })

    it('renders the unavailable label and opens nothing, stating the failure reason, only when getShowActive fails', async () => {
      getShowActiveMock.mockRejectedValue(new Error('coordinator unreachable'))

      renderShell({ session: authenticatedSession(), currentRuns: currentRuns('winter-2026') })

      expect(await screen.findByText('Show not reported')).toBeInTheDocument()
      expect(screen.getByText('coordinator unreachable')).toBeInTheDocument()
      expect(screen.queryByRole('button', { name: /show/i })).not.toBeInTheDocument()
    })

    it('opens, lists shows with the current one marked, picks, applies, confirms, and sends the write', async () => {
      listConfigObjectsMock.mockResolvedValue({
        objects: [
          { id: 'winter-2026', label: 'Winter 2026', show: 'winter-2026', currentRevision: 1, updatedAt: '2026-08-01T00:00:00Z' },
          { id: 'halloween-2026', label: 'Halloween 2026', show: 'halloween-2026', currentRevision: 1, updatedAt: '2026-08-01T00:00:00Z' },
        ],
      })
      getShowActiveMock.mockResolvedValue(showActiveConfig('winter-2026'))
      putShowActiveMock.mockResolvedValue(showActiveConfig('halloween-2026', 4))
      const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true)

      renderShell({ session: authenticatedSession(), currentRuns: currentRuns('winter-2026') })
      fireEvent.click(await screen.findByRole('button', { name: /winter-2026/i }))

      const dialog = await screen.findByRole('dialog', { name: 'Choose show' })
      const current = await screen.findByText('Winter 2026')
      expect(current.closest('button')).toHaveTextContent('Current')

      fireEvent.click(screen.getByText('Halloween 2026'))
      const apply = screen.getByRole('button', { name: /^apply$/i })
      expect(apply).not.toBeDisabled()
      fireEvent.click(apply)

      expect(confirmSpy).toHaveBeenCalledExactlyOnceWith(expect.stringContaining('halloween-2026'))
      expect(confirmSpy.mock.calls[0]?.[0]).toContain('winter-2026')
      await waitFor(() => expect(putShowActiveMock).toHaveBeenCalledExactlyOnceWith({ show: 'halloween-2026' }))
      await waitFor(() => expect(dialog).not.toBeInTheDocument())
    })

    it('sends nothing when the confirm is cancelled', async () => {
      listConfigObjectsMock.mockResolvedValue({
        objects: [
          { id: 'winter-2026', label: 'Winter 2026', show: 'winter-2026', currentRevision: 1, updatedAt: '2026-08-01T00:00:00Z' },
          { id: 'halloween-2026', label: 'Halloween 2026', show: 'halloween-2026', currentRevision: 1, updatedAt: '2026-08-01T00:00:00Z' },
        ],
      })
      getShowActiveMock.mockResolvedValue(showActiveConfig('winter-2026'))
      vi.spyOn(window, 'confirm').mockReturnValue(false)

      renderShell({ session: authenticatedSession(), currentRuns: currentRuns('winter-2026') })
      fireEvent.click(await screen.findByRole('button', { name: /winter-2026/i }))
      await screen.findByText('Halloween 2026')
      fireEvent.click(screen.getByText('Halloween 2026'))
      fireEvent.click(screen.getByRole('button', { name: /^apply$/i }))

      expect(putShowActiveMock).not.toHaveBeenCalled()
      expect(screen.getByRole('dialog', { name: 'Choose show' })).toBeInTheDocument()
    })

    it('closes on Escape', async () => {
      listConfigObjectsMock.mockResolvedValue({
        objects: [{ id: 'winter-2026', label: 'Winter 2026', show: 'winter-2026', currentRevision: 1, updatedAt: '2026-08-01T00:00:00Z' }],
      })
      getShowActiveMock.mockResolvedValue(showActiveConfig('winter-2026'))

      renderShell({ session: authenticatedSession(), currentRuns: currentRuns('winter-2026') })
      fireEvent.click(await screen.findByRole('button', { name: /winter-2026/i }))
      const dialog = await screen.findByRole('dialog', { name: 'Choose show' })
      fireEvent.keyDown(dialog, { key: 'Escape' })
      expect(screen.queryByRole('dialog', { name: 'Choose show' })).not.toBeInTheDocument()
    })

    it('renders the scope-denied reason and keeps Apply disabled when config:write is missing', async () => {
      listConfigObjectsMock.mockResolvedValue({
        objects: [
          { id: 'winter-2026', label: 'Winter 2026', show: 'winter-2026', currentRevision: 1, updatedAt: '2026-08-01T00:00:00Z' },
          { id: 'halloween-2026', label: 'Halloween 2026', show: 'halloween-2026', currentRevision: 1, updatedAt: '2026-08-01T00:00:00Z' },
        ],
      })
      getShowActiveMock.mockResolvedValue(showActiveConfig('winter-2026'))

      renderShell({ session: authenticatedSession({ scopes: [] }), currentRuns: currentRuns('winter-2026') })
      fireEvent.click(await screen.findByRole('button', { name: /winter-2026/i }))
      await screen.findByText('Halloween 2026')
      fireEvent.click(screen.getByText('Halloween 2026'))

      expect(screen.getByText(/does not include "config:write"/)).toBeInTheDocument()
      expect(screen.getByRole('button', { name: /^apply$/i })).toBeDisabled()
    })
  })

  describe('D-020 the mode badge popover', () => {
    it('opens, picks the other mode, applies, confirms, and sends the write', async () => {
      getShowModeConfigMock.mockResolvedValue(showModeConfig({ pinned: false }))
      putShowModeConfigMock.mockResolvedValue({ ...showModeConfig({ pinned: false }), payload: { mode: 'program' }, revision: 3 })
      const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true)

      renderShell({ session: authenticatedSession() })
      const badge = await screen.findByRole('button', { name: /^show$/i })
      fireEvent.click(badge)

      const dialog = await screen.findByRole('dialog', { name: 'Choose mode' })
      fireEvent.click(screen.getByText('Program mode'))
      fireEvent.click(screen.getByRole('button', { name: /^apply$/i }))

      expect(confirmSpy).toHaveBeenCalledExactlyOnceWith(expect.stringContaining('Program mode'))
      await waitFor(() => expect(putShowModeConfigMock).toHaveBeenCalledExactlyOnceWith({ mode: 'program' }))
      await waitFor(() => expect(dialog).not.toBeInTheDocument())
    })

    it('names the leaving-show-mode warning in the confirm during a live cycle, and sends nothing when cancelled', async () => {
      getShowModeConfigMock.mockResolvedValue(showModeConfig({ pinned: false }))
      getCurrentNightSessionMock.mockResolvedValue({ serverTime: '2026-09-01T00:00:00Z', session: { state: 'live', cycle: 3 } })
      const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(false)

      renderShell({ session: authenticatedSession() })
      const badge = await screen.findByRole('button', { name: /^show$/i })
      fireEvent.click(badge)
      await screen.findByRole('dialog', { name: 'Choose mode' })
      fireEvent.click(screen.getByText('Program mode'))
      fireEvent.click(screen.getByRole('button', { name: /^apply$/i }))

      expect(confirmSpy.mock.calls[0]?.[0]).toContain('Switching to Program mode now is allowed, but it stops treating the audience as present.')
      expect(putShowModeConfigMock).not.toHaveBeenCalled()
    })
  })
})

/* The old design is gone only when it is unreachable and a test says so. */
function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const full = path.join(dir, name)
    if (statSync(full).isDirectory()) return name === 'generated' ? [] : sourceFiles(full)
    return /\.(tsx?|css)$/.test(name) ? [full] : []
  })
}

describe('the old design system', () => {
  const src = path.join(__dirname, '..')
  const files = sourceFiles(src)

  it('has no views, components or styles directory left', () => {
    const dirs = readdirSync(src).filter((name) => statSync(path.join(src, name)).isDirectory())
    expect(dirs).not.toContain('views')
    expect(dirs).not.toContain('components')
    expect(dirs).not.toContain('styles')
  })

  it('is imported by no file: every stylesheet import points into the kit', () => {
    const offenders = files.filter((file) => {
      const source = readFileSync(file, 'utf8')
      return [...source.matchAll(/(?:@import\s+|from\s+)['"]([^'"]+\.css)['"]/g)].some(
        (match) => !(match[1] ?? '').includes('kit/styles') && !(match[1] ?? '').startsWith('./'),
      )
    })
    expect(offenders).toEqual([])
  })
})
