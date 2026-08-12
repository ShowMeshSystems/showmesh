import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SessionPanel } from './SessionPanel'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import type { Model, SessionResponse } from '../app/types'
import { CSRFRejectedError } from '../api/errors'

// SessionPanel is this application's one integration point for the
// session actions (login/logout/claimBootstrap/submitToken), the same
// role App.tsx plays for `useModel`/`submitToken` — see that file's own
// comment. Mocked here (not faking network behavior, which store.test.ts
// and client.test.ts own — this isolates SessionPanel's OWN branching
// logic: which of the four states renders which sub-component, and that
// each button calls the right action).
const { login, logout, claimBootstrap, submitToken } = vi.hoisted(() => ({
  login: vi.fn(),
  logout: vi.fn(),
  claimBootstrap: vi.fn(),
  submitToken: vi.fn(),
}))
// Overrides only the four action functions; every other export (the
// error classes app/session.ts's describeApiError dispatches on, in
// particular) passes through real, via importOriginal — otherwise
// SessionPanel's own `describeApiError(err)` call would be dispatching
// against `undefined` classes rather than testing anything real.
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, login, logout, claimBootstrap, submitToken }
})

afterEach(() => {
  cleanup()
  login.mockReset()
  logout.mockReset()
  claimBootstrap.mockReset()
  submitToken.mockReset()
})

const NOW = '2026-08-12T00:00:00.000Z'

function signedOut(overrides: Partial<SessionResponse> = {}): SessionResponse {
  return {
    serverTime: NOW,
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

function signedIn(overrides: Partial<SessionResponse> = {}): SessionResponse {
  return signedOut({
    authenticated: true,
    principal: { id: 'p1', name: 'alice', kind: 'human', role: 'operator' },
    session: { id: 's1', deviceLabel: 'porch tablet', createdAt: NOW },
    credentialForm: 'session',
    scopes: ['node:read'],
    scopesState: 'current',
    ...overrides,
  })
}

function renderPanel(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <SessionPanel />
    </ModelContext.Provider>,
  )
}

describe('SessionPanel', () => {
  it('renders nothing before the first /session response (never guesses signed-in or signed-out)', () => {
    const { container } = renderPanel(makeModel({ session: null }))
    expect(container).toBeEmptyDOMElement()
  })

  it('renders the loud bootstrap banner, never the ordinary sign-in banner, when bootstrapRequired is true', () => {
    renderPanel(makeModel({ session: signedOut({ bootstrapRequired: true }) }))
    expect(screen.getByRole('alert')).toHaveTextContent(/no administrator exists/i)
    expect(screen.queryByText('Signed out on this device.')).not.toBeInTheDocument()
  })

  it('renders the persistent signed-out banner, covering a brand-new device exactly like a revoked one', () => {
    renderPanel(makeModel({ session: signedOut() }))
    expect(screen.getByText('Signed out on this device.')).toBeInTheDocument()
    // Both a form-based sign-in AND the break-glass token path must be
    // reachable from this one persistent state, not gated behind a modal
    // that only appears when a control is pressed elsewhere.
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Use a token instead' })).toBeInTheDocument()
  })

  it('calls login with the form values when the sign-in form succeeds, revealing it via the "Sign in" toggle', async () => {
    const user = userEvent.setup()
    login.mockResolvedValue(undefined)
    renderPanel(makeModel({ session: signedOut() }))

    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await user.type(screen.getByLabelText('Name'), 'alice')
    await user.type(screen.getByLabelText('Password'), 'secret123')
    await user.type(screen.getByLabelText('This device’s name'), 'porch tablet')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))

    expect(login).toHaveBeenCalledWith('alice', 'secret123', 'porch tablet')
  })

  it('calls submitToken with the pasted token via the break-glass path', async () => {
    const user = userEvent.setup()
    renderPanel(makeModel({ session: signedOut() }))

    await user.click(screen.getByRole('button', { name: 'Use a token instead' }))
    await user.type(screen.getByPlaceholderText('API token'), 'machine-token-value')
    await user.click(screen.getByRole('button', { name: 'Connect' }))

    expect(submitToken).toHaveBeenCalledWith('machine-token-value')
  })

  it('renders signed-in state with the principal name and role, and a working sign-out button', async () => {
    const user = userEvent.setup()
    logout.mockResolvedValue(undefined)
    renderPanel(makeModel({ session: signedIn() }))

    expect(screen.getByText(/Signed in as alice/)).toHaveTextContent('operator')
    await user.click(screen.getByRole('button', { name: /Sign out/ }))
    expect(logout).toHaveBeenCalledWith()
  })

  it('shows the CSRF-rejection explanation, not a silent failure, when sign-out is rejected', async () => {
    const user = userEvent.setup()
    logout.mockRejectedValue(new CSRFRejectedError('missing header'))
    renderPanel(makeModel({ session: signedIn() }))

    await user.click(screen.getByRole('button', { name: /Sign out/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/Safari|16\.4/i)
  })
})
