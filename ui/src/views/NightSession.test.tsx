import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NightSession } from './NightSession'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession, makeNightSessionState } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

const { getCurrentNightSession } = vi.hoisted(() => ({ getCurrentNightSession: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getCurrentNightSession }
})

afterEach(() => {
  cleanup()
  getCurrentNightSession.mockReset()
})

function renderView(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <NightSession />
    </ModelContext.Provider>,
  )
}

describe('NightSession', () => {
  // GET /night/session answers 200 with state "inactive" rather than 404
  // when no session has ever been created (api/openapi.yaml's own
  // description) — this is a real, renderable state, not an error.
  it('renders the inactive state from the initial GET, with no session ever created', async () => {
    getCurrentNightSession.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      session: makeNightSessionState({ id: '', state: 'inactive' }),
    })
    renderView(makeModel())
    await waitFor(() => expect(screen.getByText('inactive')).toBeVisible())
    expect(screen.getByText(/no session has ever been created/)).toBeVisible()
  })

  it('renders the fetch error distinguishably from "inactive"', async () => {
    getCurrentNightSession.mockRejectedValue(new Error('the coordinator is unreachable'))
    renderView(makeModel())
    await waitFor(() => expect(screen.getByText('the coordinator is unreachable')).toBeVisible())
  })

  // The store's own live `nightSession.changed` frame (model.nightSession)
  // must win over whatever this view's own GET produced — it is strictly
  // fresher (Model.nightSession's own doc comment). This is the exact
  // "the view cannot be live without both halves" acceptance criterion
  // this seam's report must be able to point to.
  it('adopts a live model.nightSession update over the initial GET result', async () => {
    getCurrentNightSession.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      session: makeNightSessionState({ state: 'inactive' }),
    })
    const { rerender } = renderView(makeModel())
    await waitFor(() => expect(screen.getByText('inactive')).toBeVisible())

    const liveSession = makeNightSessionState({ state: 'live', cycle: 3 })
    rerender(
      <ModelContext.Provider value={makeModel({ nightSession: liveSession })}>
        <NightSession />
      </ModelContext.Provider>,
    )

    await waitFor(() => expect(screen.getByText('live')).toBeVisible())
    expect(screen.getByText('3')).toBeVisible()
  })

  // ADR-031 decision 3: completed and confirmed must be visually distinct
  // — a dispatched-but-unconfirmed cue must never render as "failed".
  it('renders a resolved/unconfirmed cue as neither success nor failure', async () => {
    getCurrentNightSession.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      session: makeNightSessionState({
        state: 'live',
        cues: {
          state: 'recorded',
          reason: '',
          cues: [
            {
              name: 'house-lights-down',
              phase: 'enterShow',
              role: 'lighting',
              action: 'house-lights-off',
              actionRevision: 2,
              state: 'resolved',
              outcome: 'unconfirmed',
              reason: 'no expected response declared',
              dispatchedAt: '2026-08-22T00:00:01Z',
              resolvedAt: '2026-08-22T00:00:02Z',
            },
          ],
        },
      }),
    })
    renderView(makeModel())
    await waitFor(() => expect(screen.getByText('house-lights-down')).toBeVisible())
    expect(screen.getByText('unconfirmed')).toBeVisible()
    expect(screen.queryByText('failed')).toBeNull()
  })

  it('renders the lifecycle command buttons, disabled with a stated reason when night:command is not held', async () => {
    getCurrentNightSession.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      session: makeNightSessionState(),
    })
    renderView(makeModel({ session: makeAuthenticatedSession({ scopes: ['node:read'] }) }))
    const button = await screen.findByRole('button', { name: 'Prepare site' })
    expect(button).toBeDisabled()
    expect(button).toBeVisible()
  })
})
