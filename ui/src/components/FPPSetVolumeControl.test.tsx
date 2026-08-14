import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { FPPSetVolumeControl } from './FPPSetVolumeControl'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import type { FPPCommandResult, SessionResponse } from '../app/types'

const { setFPPVolume } = vi.hoisted(() => ({ setFPPVolume: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, setFPPVolume }
})

afterEach(() => {
  cleanup()
  setFPPVolume.mockReset()
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
    action: 'fpp.set_volume',
    instanceId: 'bench-fpp',
    // FPPCommandResult.params echoes whatever this command's own
    // normalized params were (api/openapi.yaml: additionalProperties:
    // true, matching AuditEntry.params) — nothing under test reads
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

function renderControl() {
  render(
    <ModelContext.Provider value={makeModel({ session: signedIn() })}>
      <FPPSetVolumeControl instanceId="bench-fpp" />
    </ModelContext.Provider>,
  )
}

describe('FPPSetVolumeControl', () => {
  it('sends a valid integer volume to setFPPVolume', async () => {
    const user = userEvent.setup()
    setFPPVolume.mockResolvedValue(result())
    renderControl()

    await user.type(screen.getByRole('spinbutton'), '55')
    await user.click(screen.getByRole('button', { name: 'Set Volume' }))
    await waitFor(() => expect(setFPPVolume).toHaveBeenCalledWith('bench-fpp', 55))
  })

  // Capture section 1.5: FPP itself silently CLAMPS an out-of-range
  // value. This is the exact behavior this control must NOT reproduce —
  // an out-of-range value must be refused with a stated reason, and
  // never sent (clamped or otherwise).
  it('refuses an out-of-range volume rather than clamping it, and never calls setFPPVolume', async () => {
    const user = userEvent.setup()
    renderControl()

    await user.type(screen.getByRole('spinbutton'), '999')
    await user.click(screen.getByRole('button', { name: 'Set Volume' }))

    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('outside')
    expect(setFPPVolume).not.toHaveBeenCalled()
  })

  // Capture section 1.5 again: FPP COERCES a non-numeric value to 0,
  // silently. This control must refuse, not coerce.
  it('refuses a non-integer value rather than coercing it, and never calls setFPPVolume', async () => {
    const user = userEvent.setup()
    renderControl()

    await user.type(screen.getByRole('spinbutton'), '1.5')
    await user.click(screen.getByRole('button', { name: 'Set Volume' }))

    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('whole number')
    expect(setFPPVolume).not.toHaveBeenCalled()
  })

  it('refuses an empty value with a stated reason', async () => {
    const user = userEvent.setup()
    renderControl()

    await user.click(screen.getByRole('button', { name: 'Set Volume' }))
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('Enter a volume')
    expect(setFPPVolume).not.toHaveBeenCalled()
  })

  it('renders a confirmed outcome after a valid dispatch', async () => {
    const user = userEvent.setup()
    setFPPVolume.mockResolvedValue(result())
    renderControl()

    await user.type(screen.getByRole('spinbutton'), '55')
    await user.click(screen.getByRole('button', { name: 'Set Volume' }))
    await waitFor(() => expect(screen.getByText(/Confirmed: volume set/)).toBeVisible())
  })
})
