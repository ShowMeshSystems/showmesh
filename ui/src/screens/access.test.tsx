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
    expect(headings).toEqual(['Principals', 'Credentials for erbartos', 'Attribution'])
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
    await waitFor(() => expect(screen.getByText('Bootstrap')).toBeInTheDocument())
    expect(screen.getByText(/Who claimed it and when are not reported/)).toBeInTheDocument()
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
