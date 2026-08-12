import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { FPPStopPlaylistControl } from './FPPStopPlaylistControl'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import type { FPPCommandResult, Model, SessionResponse } from '../app/types'

// FPPStopPlaylistControl is this application's first write control (Step
// 7 seam C). Mocked here at the '../api' boundary — not faking network
// behavior, which store.test.ts owns — isolating this component's OWN
// job: rendering ScopedButton's scope gate correctly wired to
// "fpp:command", and rendering confirmed/unconfirmed/pending honestly
// rather than treating a resolved Promise as unqualified success
// (ADR-003). Same mocking shape as SessionPanel.test.tsx.
const { stopFPPPlaylist } = vi.hoisted(() => ({ stopFPPPlaylist: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, stopFPPPlaylist }
})

afterEach(() => {
  cleanup()
  stopFPPPlaylist.mockReset()
})

const NOW = '2026-08-12T00:00:00.000Z'

function signedIn(overrides: Partial<SessionResponse> = {}): SessionResponse {
  return {
    serverTime: NOW,
    authenticated: true,
    principal: { id: 'p1', name: 'alice', kind: 'human', role: 'operator' },
    session: { id: 's1', deviceLabel: 'porch tablet', createdAt: NOW },
    credentialForm: 'session',
    scopes: ['fpp:command'],
    scopesState: 'current',
    bootstrapRequired: false,
    ...overrides,
  }
}

function result(overrides: Partial<FPPCommandResult> = {}): FPPCommandResult {
  return {
    id: 'cmd-1',
    idempotencyKey: 'key-1',
    action: 'fpp.stop_playlist',
    instanceId: 'bench-fpp',
    replay: false,
    outcome: 'confirmed',
    outcomeState: 'current',
    outcomeReason: '',
    attributionDegraded: false,
    dispatchedAt: NOW,
    resolvedAt: NOW,
    ...overrides,
  }
}

function renderControl(model: Model) {
  render(
    <ModelContext.Provider value={model}>
      <FPPStopPlaylistControl instanceId="bench-fpp" />
    </ModelContext.Provider>,
  )
}

describe('FPPStopPlaylistControl', () => {
  it('renders disabled, never enabled, when the operator lacks fpp:command', () => {
    const model = makeModel({ session: signedIn({ scopes: ['node:read'] }) })
    renderControl(model)
    expect(screen.getByRole('button', { name: 'Stop Playlist' })).toBeDisabled()
  })

  // Acceptance criterion 7: the control renders unknown (never enabled)
  // when the scope list is stale or unavailable.
  it('renders disabled when the scope list is stale/unavailable (sessionFetchFailed), never permissive from cached data', () => {
    const model = makeModel({ session: signedIn(), sessionFetchFailed: true })
    renderControl(model)
    expect(screen.getByRole('button', { name: 'Stop Playlist' })).toBeDisabled()
  })

  it('renders disabled, never enabled, when signed out', () => {
    const model = makeModel({ session: null })
    renderControl(model)
    expect(screen.getByRole('button', { name: 'Stop Playlist' })).toBeDisabled()
  })

  it('is enabled for a principal holding fpp:command, shows an in-flight state, then renders a confirmed outcome', async () => {
    const user = userEvent.setup()
    let resolve!: (r: FPPCommandResult) => void
    stopFPPPlaylist.mockReturnValue(new Promise<FPPCommandResult>((r) => (resolve = r)))

    const model = makeModel({ session: signedIn() })
    renderControl(model)

    const button = screen.getByRole('button', { name: 'Stop Playlist' })
    expect(button).toBeEnabled()
    await user.click(button)

    // The in-flight state must be visible — this step's own acceptance
    // criterion: "a command in flight ... must be [a] visible state."
    expect(screen.getByRole('status', { name: '' })).toBeTruthy()
    expect(screen.getByText(/Waiting for the coordinator/)).toBeVisible()

    resolve(result({ outcome: 'confirmed' }))
    await waitFor(() => expect(screen.getByText(/Confirmed: playback stopped/)).toBeVisible())
    expect(stopFPPPlaylist).toHaveBeenCalledWith('bench-fpp')
  })

  it('renders an unconfirmed outcome distinctly, never as success — the exact ADR-003 property this step exists to prove', async () => {
    const user = userEvent.setup()
    stopFPPPlaylist.mockResolvedValue(
      result({ outcome: 'unconfirmed', outcomeState: 'current', outcomeReason: 'observed fpp.status = "playing"' }),
    )
    const model = makeModel({ session: signedIn() })
    renderControl(model)

    await user.click(screen.getByRole('button', { name: 'Stop Playlist' }))

    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('Unconfirmed')
    expect(alert.textContent).toContain('observed fpp.status')
    // Must never ALSO claim confirmed.
    expect(screen.queryByText(/Confirmed: playback stopped/)).toBeNull()
  })

  it('flags a replayed result distinctly from a fresh dispatch', async () => {
    const user = userEvent.setup()
    stopFPPPlaylist.mockResolvedValue(result({ replay: true, outcome: 'confirmed' }))
    const model = makeModel({ session: signedIn() })
    renderControl(model)

    await user.click(screen.getByRole('button', { name: 'Stop Playlist' }))
    await waitFor(() => expect(screen.getByText(/already used/)).toBeVisible())
  })

  it('renders a network/API error without crashing', async () => {
    const user = userEvent.setup()
    stopFPPPlaylist.mockRejectedValue(new Error('network error'))
    const model = makeModel({ session: signedIn() })
    renderControl(model)

    await user.click(screen.getByRole('button', { name: 'Stop Playlist' }))
    const alert = await screen.findByRole('alert')
    expect(alert).toBeVisible()
  })
})
