import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SessionPanel, SessionIdentity } from './SessionPanel'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import type { Model, SessionResponse } from '../app/types'
import { CSRFRejectedError } from '../api/errors'
import { setStoredToken } from '../api/token'

// SessionPanel is this application's one integration point for the
// session actions (login/logout/claimBootstrap/submitToken/clearToken),
// the same role App.tsx plays for `useModel`/`submitToken` — see that
// file's own comment. Mocked here (not faking network behavior, which
// store.test.ts and client.test.ts own — this isolates SessionPanel's OWN
// branching logic: which of the four states renders which sub-component,
// and that each button calls the right action). `getStoredToken` is
// deliberately NOT mocked (passes through real, via importOriginal): the
// "Clear stored token" button's visibility is driven by real
// `sessionStorage`, exactly as it is in production, so tests exercise it
// with `setStoredToken`/`sessionStorage.clear()` rather than a fake
// return value that could drift from what token.ts actually does.
const { login, logout, claimBootstrap, submitToken, clearToken } = vi.hoisted(() => ({
  login: vi.fn(),
  logout: vi.fn(),
  claimBootstrap: vi.fn(),
  submitToken: vi.fn(),
  clearToken: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, login, logout, claimBootstrap, submitToken, clearToken }
})

afterEach(() => {
  cleanup()
  login.mockReset()
  logout.mockReset()
  claimBootstrap.mockReset()
  submitToken.mockReset()
  clearToken.mockReset()
  sessionStorage.clear()
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

// SessionIdentity is SessionPanel's signed-in case, moved out for
// Layout.tsx's header (Operator-reported: it used to render inline here,
// as a full-width band). Its own render helper, since SessionPanel now
// renders nothing at all for the signed-in state -- see the two tests
// below that use it instead of renderPanel.
function renderIdentity(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <SessionIdentity />
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
    expect(screen.queryByText('Signed out on this device')).not.toBeInTheDocument()
  })

  // The bootstrap band's claim form is always visible, never behind a
  // toggle -- there is no ordinary use of an unclaimed coordinator for a
  // plain page to hide underneath, unlike the signed-out band's optional
  // sign-in form.
  it('shows the bootstrap claim form immediately, with no expand step, and calls claimBootstrap on submit', async () => {
    const user = userEvent.setup()
    claimBootstrap.mockResolvedValue(undefined)
    renderPanel(makeModel({ session: signedOut({ bootstrapRequired: true }) }))

    await user.type(screen.getByLabelText('Bootstrap code'), 'abc123')
    await user.type(screen.getByLabelText('Administrator name'), 'root')
    await user.type(screen.getByLabelText('Password'), 'secret123')
    await user.type(screen.getByLabelText('This device’s name'), 'install laptop')
    await user.click(screen.getByRole('button', { name: 'Claim and sign in' }))

    expect(claimBootstrap).toHaveBeenCalledWith('abc123', 'root', 'secret123', 'install laptop')
  })

  it('renders the persistent signed-out banner, covering a brand-new device exactly like a revoked one', () => {
    renderPanel(makeModel({ session: signedOut() }))
    expect(screen.getByRole('heading', { name: 'Signed out on this device' })).toBeInTheDocument()
    // Both a form-based sign-in AND the break-glass token path must be
    // reachable from this one persistent state, not gated behind a modal
    // that only appears when a control is pressed elsewhere. "Sign in"
    // appears twice by design -- once as the band's own toggle, once again
    // as the unobserved plate's shortcut below it (Session States.dc.html)
    // -- so this only asserts at least one of each is present.
    expect(screen.getAllByRole('button', { name: 'Sign in' }).length).toBeGreaterThanOrEqual(2)
    expect(screen.getByRole('button', { name: 'Use a token instead' })).toBeInTheDocument()
  })

  it('renders the unobserved blanking plate below the band, with its dashed "No cred" stamp', () => {
    renderPanel(makeModel({ session: signedOut() }))
    expect(screen.getByRole('heading', { name: 'Nothing here has ever been collected' })).toBeInTheDocument()
    expect(screen.getByText('No cred')).toBeInTheDocument()
  })

  it('calls login with the form values when the sign-in form succeeds, revealing it via the band\'s "Sign in" toggle', async () => {
    const user = userEvent.setup()
    login.mockResolvedValue(undefined)
    renderPanel(makeModel({ session: signedOut() }))

    // The band's own toggle is the first "Sign in" button in the document
    // -- it precedes the always-present plate shortcut below it.
    await user.click(screen.getAllByRole('button', { name: 'Sign in' })[0]!)

    const form = screen.getByRole('form', { name: 'Sign in' })
    await user.type(within(form).getByLabelText('Name'), 'alice')
    await user.type(within(form).getByLabelText('Password'), 'secret123')
    await user.type(within(form).getByLabelText('This device’s name'), 'porch tablet')
    await user.click(within(form).getByRole('button', { name: 'Sign in' }))

    expect(login).toHaveBeenCalledWith('alice', 'secret123', 'porch tablet')
  })

  it('also reveals the sign-in form from the unobserved plate\'s own "Sign in" shortcut', async () => {
    const user = userEvent.setup()
    renderPanel(makeModel({ session: signedOut() }))

    // Before the band's toggle is pressed, the only "Sign in" buttons are
    // the band toggle and the plate shortcut -- the plate one is last.
    const signInButtons = screen.getAllByRole('button', { name: 'Sign in' })
    await user.click(signInButtons[signInButtons.length - 1]!)

    expect(screen.getByRole('form', { name: 'Sign in' })).toBeInTheDocument()
  })

  it('calls submitToken with the pasted token via the break-glass path', async () => {
    const user = userEvent.setup()
    renderPanel(makeModel({ session: signedOut() }))

    await user.click(screen.getByRole('button', { name: 'Use a token instead' }))
    await user.type(screen.getByPlaceholderText('API token'), 'machine-token-value')
    await user.click(screen.getByRole('button', { name: 'Connect' }))

    expect(submitToken).toHaveBeenCalledWith('machine-token-value')
  })

  it('also reveals the break-glass token field from the plate\'s "Paste a machine token" shortcut', async () => {
    const user = userEvent.setup()
    renderPanel(makeModel({ session: signedOut() }))

    await user.click(screen.getByRole('button', { name: 'Paste a machine token' }))
    await user.type(screen.getByPlaceholderText('API token'), 'machine-token-value')
    await user.click(screen.getByRole('button', { name: 'Connect' }))

    expect(submitToken).toHaveBeenCalledWith('machine-token-value')
  })

  // Finding: a stored break-glass token can shadow a valid session cookie
  // forever (client.ts always prefers a present Authorization header,
  // with no cookie fallthrough), and this signed-out banner is exactly
  // what that looks like from here. The operator needs a way to clear it
  // directly, without a working sign-in to trigger store.ts's own
  // clear-on-success path — this pair of tests covers both halves: the
  // button must be absent when there is nothing to clear (it must not
  // assert a fact that is not true), and present and wired to
  // `clearToken()` when there is.
  it('offers no "Clear stored token" button when no token is stored', () => {
    renderPanel(makeModel({ session: signedOut() }))
    expect(screen.queryByRole('button', { name: 'Clear stored token' })).not.toBeInTheDocument()
  })

  it('offers "Clear stored token" when one is stored, and calls clearToken() when pressed', async () => {
    const user = userEvent.setup()
    setStoredToken('a-stale-token')
    renderPanel(makeModel({ session: signedOut() }))

    const button = screen.getByRole('button', { name: 'Clear stored token' })
    await user.click(button)

    expect(clearToken).toHaveBeenCalledTimes(1)
  })

  // Signed-in identity now renders via SessionIdentity (Layout.tsx's
  // header), not inline in SessionPanel's own band -- Operator-reported.
  it('renders nothing for the signed-in state; SessionIdentity renders it instead', () => {
    const { container } = renderPanel(makeModel({ session: signedIn() }))
    expect(container).toBeEmptyDOMElement()
  })

  it('renders signed-in state with the principal name and role, and a working sign-out button', async () => {
    const user = userEvent.setup()
    logout.mockResolvedValue(undefined)
    renderIdentity(makeModel({ session: signedIn() }))

    expect(screen.getByText(/Signed in as alice/)).toHaveTextContent('operator')
    await user.click(screen.getByRole('button', { name: /Sign out/ }))
    expect(logout).toHaveBeenCalledWith()
  })

  it('shows the CSRF-rejection explanation, not a silent failure, when sign-out is rejected', async () => {
    const user = userEvent.setup()
    logout.mockRejectedValue(new CSRFRejectedError('missing header'))
    renderIdentity(makeModel({ session: signedIn() }))

    await user.click(screen.getByRole('button', { name: /Sign out/ }))
    // Asserts the panel surfaces describeApiError's CSRF text at all, keyed
    // on the cause that text now names (a host disagreement) rather than on
    // the browser version it used to name — see session.test.ts for why
    // that changed.
    expect(await screen.findByRole('alert')).toHaveTextContent(/host/i)
  })
})
