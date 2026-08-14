import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { FPPStopPlaylistGracefullyControl } from './FPPStopPlaylistGracefullyControl'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import type { FPPCommandResult, SessionResponse } from '../app/types'

const { stopFPPPlaylistGracefully } = vi.hoisted(() => ({ stopFPPPlaylistGracefully: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, stopFPPPlaylistGracefully }
})

afterEach(() => {
  cleanup()
  stopFPPPlaylistGracefully.mockReset()
})

const NOW = '2026-08-13T00:00:00.000Z'

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
    action: 'fpp.stop_playlist_gracefully',
    instanceId: 'bench-fpp',
    // See FPPSetVolumeControl.test.tsx's own comment: FPPCommandResult
    // .params echoes whatever this command's own normalized params were
    // (additionalProperties: true) — nothing under test reads
    // result.params, so this fixture leaves it empty rather than
    // inventing a value.
    params: {},
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

describe('FPPStopPlaylistGracefullyControl', () => {
  it('renders disabled when signed out', () => {
    render(
      <ModelContext.Provider value={makeModel({ session: null })}>
        <FPPStopPlaylistGracefullyControl instanceId="bench-fpp" />
      </ModelContext.Provider>,
    )
    expect(screen.getByRole('button', { name: 'Stop Gracefully' })).toBeDisabled()
  })

  it('sends the afterLoop checkbox state to stopFPPPlaylistGracefully', async () => {
    const user = userEvent.setup()
    stopFPPPlaylistGracefully.mockResolvedValue(result())
    render(
      <ModelContext.Provider value={makeModel({ session: signedIn() })}>
        <FPPStopPlaylistGracefullyControl instanceId="bench-fpp" />
      </ModelContext.Provider>,
    )

    await user.click(screen.getByRole('checkbox'))
    await user.click(screen.getByRole('button', { name: 'Stop Gracefully' }))
    await waitFor(() => expect(stopFPPPlaylistGracefully).toHaveBeenCalledWith('bench-fpp', true))
  })

  // CLAUDE.md item 4 / capture section 3.3: this is the exact property
  // this control exists to prove — CONFIRMED must not be read as
  // "stopped" when the server's own reason says the show is still
  // winding down. Breaking this (rendering a fixed "Confirmed: stopped"
  // string) is exactly the regression this test would catch.
  it('renders the server outcomeReason verbatim on a confirmed result, never a fixed "stopped" summary', async () => {
    const user = userEvent.setup()
    stopFPPPlaylistGracefully.mockResolvedValue(
      result({
        outcome: 'confirmed',
        outcomeReason:
          "FPP accepted the graceful stop. The show has NOT stopped yet — it's still running and will stop once " +
          'the current item finishes (fpp.status "stopping gracefully", via fpp-rest).',
      }),
    )
    render(
      <ModelContext.Provider value={makeModel({ session: signedIn() })}>
        <FPPStopPlaylistGracefullyControl instanceId="bench-fpp" />
      </ModelContext.Provider>,
    )

    await user.click(screen.getByRole('button', { name: 'Stop Gracefully' }))
    await waitFor(() => expect(screen.getByText(/has NOT stopped yet/)).toBeVisible())
    expect(screen.queryByText('Confirmed: playback stopped')).toBeNull()
  })

  it('renders an unconfirmed outcome as an alert', async () => {
    const user = userEvent.setup()
    stopFPPPlaylistGracefully.mockResolvedValue(
      result({ outcome: 'unconfirmed', outcomeReason: 'no fpp.status observation is recorded for this instance yet' }),
    )
    render(
      <ModelContext.Provider value={makeModel({ session: signedIn() })}>
        <FPPStopPlaylistGracefullyControl instanceId="bench-fpp" />
      </ModelContext.Provider>,
    )

    await user.click(screen.getByRole('button', { name: 'Stop Gracefully' }))
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('Unconfirmed')
  })
})
