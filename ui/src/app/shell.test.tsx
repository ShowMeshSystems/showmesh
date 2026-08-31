import { readdirSync, readFileSync, statSync } from 'node:fs'
import path from 'node:path'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { CSRFRejectedError, TooManyRequestsError } from '../api'
import type { Model, SessionResponse } from '../api'
import { clearStoredToken, setStoredToken } from '../api/token'
import { initialModel } from '../api/domain'
import { ModelContext } from './ModelContext'
import { Layout } from './Layout'
import { NotFound } from '../screens/NotFound'

const loginMock = vi.fn()
const claimBootstrapMock = vi.fn()
const logoutMock = vi.fn()

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    login: (...args: unknown[]) => loginMock(...args),
    claimBootstrap: (...args: unknown[]) => claimBootstrapMock(...args),
    logout: (...args: unknown[]) => logoutMock(...args),
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
    clearStoredToken()
  })
  afterEach(cleanup)

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
