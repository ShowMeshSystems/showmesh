import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Model, NightSessionState, PrincipalObject, SessionResponse, TokenObject } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  listPrincipals: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  listPrincipalTokens: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  createPrincipal: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  issuePrincipalToken: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  revokePrincipalToken: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getCurrentNightSession: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    listPrincipals: (...args: never[]) => stubs.listPrincipals(...args),
    listPrincipalTokens: (...args: never[]) => stubs.listPrincipalTokens(...args),
    createPrincipal: (...args: never[]) => stubs.createPrincipal(...args),
    issuePrincipalToken: (...args: never[]) => stubs.issuePrincipalToken(...args),
    revokePrincipalToken: (...args: never[]) => stubs.revokePrincipalToken(...args),
    getCurrentNightSession: (...args: never[]) => stubs.getCurrentNightSession(...args),
  }
})

const { Access } = await import('./Access')

function session(overrides: Partial<SessionResponse> = {}): SessionResponse {
  return {
    serverTime: '2026-08-30T21:07:00Z',
    authenticated: true,
    principal: { id: 'p1', name: 'erbartos', kind: 'human', role: 'admin' },
    session: { id: 's1', deviceLabel: 'porch tablet', createdAt: '2026-08-30T20:00:00Z' },
    credentialForm: 'session',
    scopes: ['principal:read', 'principal:write', 'audit:read'],
    scopesState: 'current',
    bootstrapRequired: false,
    ...overrides,
  } as unknown as SessionResponse
}

function principal(overrides: Partial<PrincipalObject> = {}): PrincipalObject {
  return {
    id: 'p1',
    name: 'erbartos',
    kind: 'human',
    role: 'admin',
    disabled: false,
    hasPassword: true,
    reserved: false,
    createdAt: '2026-08-01T00:00:00Z',
    ...overrides,
  } as unknown as PrincipalObject
}

function token(overrides: Partial<TokenObject> = {}): TokenObject {
  return {
    id: 'cred-77c3',
    principalId: 'p1',
    hint: 'sk...77c3',
    label: 'CLI on the bench machine',
    createdAt: '2026-08-14T16:02:00Z',
    expiresAt: null,
    lastUsedAt: '2026-08-30T20:54:03Z',
    ...overrides,
  } as unknown as TokenObject
}

function nightSession(overrides: Partial<NightSessionState> = {}): NightSessionState {
  return {
    id: 'ns1',
    configObjectId: 'c1',
    configRevision: 1,
    state: 'live',
    stateEnteredAt: '2026-08-30T20:00:00Z',
    cycle: 3,
    finalShowRequested: false,
    finalShowRequestedAt: null,
    admissionClosed: false,
    admissionClosedAt: null,
    shutdownIntent: '',
    armedShowId: '',
    showCommitted: true,
    readiness: {},
    powerPhase: {},
    transition: {},
    cues: {},
    backgroundAudio: { state: 'recorded', reason: '', steps: [] },
    degraded: false,
    attributionDegraded: false,
    authorization: { state: 'recorded', recordedAt: '2026-08-30T19:00:00Z' },
    updatedAt: '2026-08-30T21:00:00Z',
    ...overrides,
  } as unknown as NightSessionState
}

function renderScreen(model: Partial<Model> = {}) {
  return render(
    <ModelContext.Provider
      value={{
        ...initialModel(),
        session: session(),
        serverTime: '2026-08-30T21:07:00Z',
        serverTimeReceivedAt: Date.now(),
        ...model,
      }}
    >
      <MemoryRouter initialEntries={['/access']}>
        <Routes>
          <Route path="/access" element={<Access />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Access', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
    stubs.listPrincipals = () => new Promise(() => {})
    stubs.listPrincipalTokens = () => new Promise(() => {})
    stubs.createPrincipal = () => new Promise(() => {})
    stubs.issuePrincipalToken = () => new Promise(() => {})
    stubs.revokePrincipalToken = () => new Promise(() => {})
    stubs.getCurrentNightSession = () => new Promise(() => {})
  })

  it('renders the mock’s section labels', async () => {
    stubs.listPrincipals = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', principals: [principal()] })
    stubs.listPrincipalTokens = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', tokens: [token()] })
    stubs.getCurrentNightSession = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', session: null })
    renderScreen()
    await waitFor(() => expect(screen.getByText('cred-77c3', { exact: false })).toBeInTheDocument())
    const headings = screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)
    expect(headings).toEqual(['Principals', 'Credentials for erbartos', 'Attribution', 'Bootstrap'])
  })

  it('shows resolved scopes on the signed-in row and a role on every other row, and states the bundle is not reported', async () => {
    stubs.listPrincipals = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:07:00Z',
        principals: [principal(), principal({ id: 'p2', name: 'scheduler-host', kind: 'machine', role: 'scheduler' })],
      })
    stubs.listPrincipalTokens = (id: string) => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', tokens: id === 'p1' ? [token()] : [] })
    stubs.getCurrentNightSession = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', session: null })
    renderScreen()
    await waitFor(() => expect(screen.getByText('scheduler-host')).toBeInTheDocument())
    expect(screen.getByText('principal:read')).toBeInTheDocument()
    expect(screen.getByText('scheduler')).toBeInTheDocument()
    expect(screen.getByText(/no per-principal scope bundle to read/)).toBeInTheDocument()
  })

  it('never renders a token value from a list read, and shows the issue response’s value once, gone after dismissal', async () => {
    stubs.listPrincipals = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', principals: [principal()] })
    stubs.listPrincipalTokens = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', tokens: [token()] })
    stubs.getCurrentNightSession = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', session: null })
    stubs.issuePrincipalToken = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:07:00Z',
        token: token({ id: 'cred-9001', hint: 'sk...9001', lastUsedAt: null, createdAt: '2026-08-30T21:07:00Z' }),
        value: 'sk-live-secret-value-shown-once',
      })
    renderScreen()
    await waitFor(() => expect(screen.getByText('cred-77c3', { exact: false })).toBeInTheDocument())

    expect(screen.queryByText('sk-live-secret-value-shown-once')).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /Issue credential/ }))
    await waitFor(() => expect(screen.getByText('sk-live-secret-value-shown-once')).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByText('sk-live-secret-value-shown-once')).not.toBeInTheDocument()
  })

  it('keeps the issued value visible through the reload it triggers, even while the reload is still pending', async () => {
    stubs.listPrincipals = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', principals: [principal()] })
    let tokensCall = 0
    let resolveSecondTokensRead: ((value: unknown) => void) | undefined
    stubs.listPrincipalTokens = () => {
      tokensCall += 1
      if (tokensCall === 1) return Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', tokens: [token()] })
      return new Promise((resolve) => {
        resolveSecondTokensRead = resolve
      })
    }
    stubs.getCurrentNightSession = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', session: null })
    stubs.issuePrincipalToken = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:07:00Z',
        token: token({ id: 'cred-9001', hint: 'sk...9001', lastUsedAt: null, createdAt: '2026-08-30T21:07:00Z' }),
        value: 'sk-live-secret-value-shown-once',
      })
    renderScreen()
    await waitFor(() => expect(screen.getByText('cred-77c3', { exact: false })).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: /Issue credential/ }))
    await waitFor(() => expect(screen.getByText('sk-live-secret-value-shown-once')).toBeInTheDocument())

    // The reload triggered by a successful issue is now in flight (its
    // listPrincipalTokens read is the pending second call). The value must
    // still be on screen while that read is outstanding, not just before
    // and after it.
    await waitFor(() => expect(tokensCall).toBe(2))
    expect(screen.getByText('sk-live-secret-value-shown-once')).toBeInTheDocument()

    resolveSecondTokensRead?.({
      serverTime: '2026-08-30T21:07:00Z',
      tokens: [token(), token({ id: 'cred-9001', hint: 'sk...9001', lastUsedAt: null, createdAt: '2026-08-30T21:07:00Z' })],
    })
    await waitFor(() => expect(screen.getByText('cred-9001', { exact: false })).toBeInTheDocument())
    expect(screen.getByText('sk-live-secret-value-shown-once')).toBeInTheDocument()
  })

  it('reports a successful clipboard copy in a status line', async () => {
    stubs.listPrincipals = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', principals: [principal()] })
    stubs.listPrincipalTokens = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', tokens: [token()] })
    stubs.getCurrentNightSession = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', session: null })
    stubs.issuePrincipalToken = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:07:00Z',
        token: token({ id: 'cred-9001', hint: 'sk...9001', lastUsedAt: null, createdAt: '2026-08-30T21:07:00Z' }),
        value: 'sk-live-secret-value-shown-once',
      })
    const writeText = vi.fn(() => Promise.resolve())
    Object.assign(navigator, { clipboard: { writeText } })
    renderScreen()
    await waitFor(() => expect(screen.getByText('cred-77c3', { exact: false })).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: /Issue credential/ }))
    await waitFor(() => expect(screen.getByText('sk-live-secret-value-shown-once')).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: 'Copy' }))
    await waitFor(() => expect(screen.getByText('Copied.')).toBeInTheDocument())
    expect(writeText).toHaveBeenCalledWith('sk-live-secret-value-shown-once')
  })

  it('reports a failed clipboard copy as a fact rather than pretending it worked', async () => {
    stubs.listPrincipals = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', principals: [principal()] })
    stubs.listPrincipalTokens = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', tokens: [token()] })
    stubs.getCurrentNightSession = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', session: null })
    stubs.issuePrincipalToken = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:07:00Z',
        token: token({ id: 'cred-9001', hint: 'sk...9001', lastUsedAt: null, createdAt: '2026-08-30T21:07:00Z' }),
        value: 'sk-live-secret-value-shown-once',
      })
    Object.assign(navigator, { clipboard: undefined })
    renderScreen()
    await waitFor(() => expect(screen.getByText('cred-77c3', { exact: false })).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: /Issue credential/ }))
    await waitFor(() => expect(screen.getByText('sk-live-secret-value-shown-once')).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: 'Copy' }))
    await waitFor(() => expect(screen.getByText(/Copy failed/)).toBeInTheDocument())
  })

  it('sends no expiresAt key when the expiry field is left blank', async () => {
    stubs.listPrincipals = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', principals: [principal()] })
    stubs.listPrincipalTokens = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', tokens: [token()] })
    stubs.getCurrentNightSession = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', session: null })
    let received: Record<string, unknown> | undefined
    stubs.issuePrincipalToken = (...args: never[]) => {
      received = args[1] as unknown as Record<string, unknown>
      return Promise.resolve({
        serverTime: '2026-08-30T21:07:00Z',
        token: token({ id: 'cred-9002', hint: 'sk...9002' }),
        value: 'sk-live-second-value',
      })
    }
    renderScreen()
    await waitFor(() => expect(screen.getByText('cred-77c3', { exact: false })).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: /Issue credential/ }))
    await waitFor(() => expect(received).toBeDefined())
    expect(received).not.toHaveProperty('expiresAt')
  })

  it('sends expiresAt as an ISO string when an expiry is set', async () => {
    stubs.listPrincipals = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', principals: [principal()] })
    stubs.listPrincipalTokens = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', tokens: [token()] })
    stubs.getCurrentNightSession = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', session: null })
    let received: Record<string, unknown> | undefined
    stubs.issuePrincipalToken = (...args: never[]) => {
      received = args[1] as unknown as Record<string, unknown>
      return Promise.resolve({
        serverTime: '2026-08-30T21:07:00Z',
        token: token({ id: 'cred-9003', hint: 'sk...9003' }),
        value: 'sk-live-third-value',
      })
    }
    renderScreen()
    await waitFor(() => expect(screen.getByText('cred-77c3', { exact: false })).toBeInTheDocument())

    const expiryInput = screen.getByLabelText('Expires · optional')
    fireEvent.change(expiryInput, { target: { value: '2026-09-15T10:30' } })
    fireEvent.click(screen.getByRole('button', { name: /Issue credential/ }))
    await waitFor(() => expect(received?.expiresAt).toBe(new Date('2026-09-15T10:30').toISOString()))
  })

  it('sends role recovery when creating a recovery principal', async () => {
    stubs.listPrincipals = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', principals: [] })
    stubs.getCurrentNightSession = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', session: null })
    let received: Record<string, unknown> | undefined
    stubs.createPrincipal = (...args: never[]) => {
      received = args[0] as unknown as Record<string, unknown>
      return Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', principal: principal({ id: 'p9', name: 'auto-recovery', role: 'recovery' }) })
    }
    renderScreen()
    await waitFor(() => expect(screen.getByRole('button', { name: 'Add principal' })).toBeEnabled())
    fireEvent.click(screen.getByRole('button', { name: 'Add principal' }))

    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'auto-recovery' } })
    fireEvent.click(screen.getByRole('button', { name: 'recovery' }))
    fireEvent.click(screen.getByRole('button', { name: 'Create principal' }))

    await waitFor(() => expect(received?.role).toBe('recovery'))
  })

  it('keeps Revoke disabled until the typed credential id matches exactly', async () => {
    stubs.listPrincipals = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', principals: [principal()] })
    stubs.listPrincipalTokens = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', tokens: [token()] })
    stubs.getCurrentNightSession = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', session: null })
    renderScreen()
    await waitFor(() => expect(screen.getByText('cred-77c3', { exact: false })).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: 'Revoke' }))
    const confirmButton = screen.getByRole('button', { name: /Revoke credential/ })
    expect(confirmButton).toBeDisabled()

    const input = screen.getByLabelText('Type cred-77c3 to confirm')
    fireEvent.change(input, { target: { value: 'wrong-id' } })
    expect(confirmButton).toBeDisabled()

    fireEvent.change(input, { target: { value: 'cred-77c3' } })
    expect(confirmButton).not.toBeDisabled()
  })

  it('disables every write control with its reason without principal:write, and renders the read denial rather than an empty table', async () => {
    stubs.getCurrentNightSession = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', session: null })
    renderScreen({
      session: session({ scopes: ['audit:read'] }),
    })
    await waitFor(() => expect(screen.getByText(/does not include "principal:read"/)).toBeInTheDocument())
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Add principal' })).toBeDisabled()
  })

  it('renders the attribution row only when the session reports attributionDegraded, seeded rather than read from the model alone', async () => {
    stubs.listPrincipals = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', principals: [principal()] })
    stubs.listPrincipalTokens = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', tokens: [] })
    stubs.getCurrentNightSession = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', session: nightSession({ attributionDegraded: true }) })
    renderScreen()
    await waitFor(() => expect(screen.getByText(/no authorizing principal recorded/)).toBeInTheDocument())
    expect(screen.getByText(/never clears for the rest of the session/)).toBeInTheDocument()
  })

  it('does not render the attribution row when attributionDegraded is false', async () => {
    stubs.listPrincipals = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', principals: [principal()] })
    stubs.listPrincipalTokens = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', tokens: [] })
    stubs.getCurrentNightSession = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', session: nightSession({ attributionDegraded: false }) })
    renderScreen()
    await waitFor(() => expect(screen.getByText('Audit store')).toBeInTheDocument())
    expect(screen.queryByText(/no authorizing principal recorded/)).not.toBeInTheDocument()
  })

  it('prints no claimed-by and no claimed-at for bootstrap', async () => {
    stubs.listPrincipals = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', principals: [principal()] })
    stubs.listPrincipalTokens = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', tokens: [] })
    stubs.getCurrentNightSession = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', session: null })
    renderScreen()
    await waitFor(() => expect(screen.getByText(/Who claimed it and when are not reported/)).toBeInTheDocument())
    expect(screen.queryByText(/by erbartos/)).not.toBeInTheDocument()
    expect(screen.queryByText(/12 Aug/)).not.toBeInTheDocument()
  })

  it('shows the "Consider revoking" state for a long-unused credential', async () => {
    stubs.listPrincipals = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', principals: [principal({ id: 'p2', name: 'old-laptop', role: 'operator' })] })
    stubs.listPrincipalTokens = () =>
      Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', tokens: [token({ id: 'cred-old', lastUsedAt: '2026-08-04T19:11:00Z' })] })
    stubs.getCurrentNightSession = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', session: null })
    renderScreen()
    await waitFor(() => expect(screen.getByText('old-laptop')).toBeInTheDocument())
    expect(screen.getByText('Consider revoking')).toBeInTheDocument()
  })

  it('names the missing scope when audit:read is denied for the log link', async () => {
    stubs.listPrincipals = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', principals: [principal()] })
    stubs.listPrincipalTokens = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', tokens: [] })
    stubs.getCurrentNightSession = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', session: null })
    renderScreen({ session: session({ scopes: ['principal:read', 'principal:write'] }) })
    await waitFor(() => expect(screen.getByText('Audit store')).toBeInTheDocument())
    expect(within(screen.getByText('Audit store').closest('.sm-strip') as HTMLElement).getByText('audit:read')).toBeInTheDocument()
  })
})

describe('Access, the credential in use', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it('marks no credential row as in use, because nothing reports which token authenticates this device', async () => {
    stubs.listPrincipals = () => Promise.resolve({ principals: [principal({ id: 'p1', name: 'erbartos' })] })
    stubs.listPrincipalTokens = () =>
      Promise.resolve({
        tokens: [
          { id: 'cred-4a91', principalId: 'p1', hint: 'sm_live_…4a91', label: 'This browser session', createdAt: '2026-08-12T09:38:00Z', expiresAt: null, lastUsedAt: '2026-08-30T21:02:14Z' },
        ],
      })

    renderScreen({ session: session({ credentialForm: 'token' }) })

    await waitFor(() => expect(screen.getByText('sm_live_…4a91')).toBeInTheDocument())
    expect(screen.queryByRole('button', { name: 'In use' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Revoke' })).toBeInTheDocument()
    expect(screen.getByText(/Which of these it is, is not reported/)).toBeInTheDocument()
  })
})
