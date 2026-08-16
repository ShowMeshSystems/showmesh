import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ResolumeRecoveryToggle } from './ResolumeRecoveryToggle'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

// Track D seam D-3a §2.6: this component's own test, mirroring
// Configuration.test.tsx's pattern — the two API functions are mocked to
// isolate this component's own branching; store.test.ts proves
// getResolumeRecovery/putResolumeRecoveryConfig issue the right real
// requests.
const { getResolumeRecovery, putResolumeRecoveryConfig } = vi.hoisted(() => ({
  getResolumeRecovery: vi.fn(),
  putResolumeRecoveryConfig: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getResolumeRecovery, putResolumeRecoveryConfig }
})

afterEach(() => {
  cleanup()
  getResolumeRecovery.mockReset()
  putResolumeRecoveryConfig.mockReset()
})

function renderToggle(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <ResolumeRecoveryToggle />
    </ModelContext.Provider>,
  )
}

const baseResponse = {
  serverTime: '2026-08-16T00:00:00Z',
  resolumeConfigured: true,
  autoRestoreEnabled: true,
  autoRestoreConfigured: false,
  settleDelaySeconds: 8,
  record: [],
  lastRestore: null,
}

describe('ResolumeRecoveryToggle', () => {
  it('renders the current toggle state with no session at all — the open read', async () => {
    getResolumeRecovery.mockResolvedValue(baseResponse)
    renderToggle(makeModel({}))

    await waitFor(() => screen.getByRole('status'))
    expect(screen.getByRole('status').textContent).toContain('ON')
    expect(screen.getByRole('status').textContent).toContain('default')
  })

  it('renders a disabled, reasoned button for a principal without config:write', async () => {
    getResolumeRecovery.mockResolvedValue(baseResponse)
    renderToggle(makeModel({})) // no session — evaluateScope reads "not signed in"

    const button = await screen.findByRole('button', { name: /turn automatic restore off/i })
    expect(button).toBeDisabled()
  })

  it('flips the toggle for an admin principal and reflects the new state', async () => {
    getResolumeRecovery.mockResolvedValue(baseResponse)
    putResolumeRecoveryConfig.mockResolvedValue({
      serverTime: '2026-08-16T00:00:01Z',
      kind: 'resolume.recovery',
      revision: 1,
      payload: { autoRestoreEnabled: false },
      updatedAt: '2026-08-16T00:00:01Z',
      createdByPrincipalId: 'p1',
      createdByPrincipalName: 'admin',
      source: 'api',
    })
    // The reload after a successful PUT re-fetches GET /resolume/recovery
    // — the second call reports the NEW value, matching Configuration.tsx's
    // own reloadGeneration pattern.
    getResolumeRecovery.mockResolvedValueOnce(baseResponse).mockResolvedValueOnce({
      ...baseResponse,
      autoRestoreEnabled: false,
      autoRestoreConfigured: true,
    })

    renderToggle(
      makeModel({
        session: makeAuthenticatedSession({
          principal: { id: 'p1', name: 'admin', kind: 'human', role: 'admin' },
          scopes: ['config:write'],
        }),
      }),
    )

    const button = await screen.findByRole('button', { name: /turn automatic restore off/i })
    expect(button).not.toBeDisabled()

    await userEvent.click(button)

    await waitFor(() => expect(putResolumeRecoveryConfig).toHaveBeenCalledWith({ autoRestoreEnabled: false }))
    await waitFor(() => screen.getByRole('button', { name: /turn automatic restore on/i }))
    expect(screen.getByRole('status').textContent).toContain('OFF')
  })

  it('renders "not configured" rather than the toggle default when Resolume itself is unconfigured', async () => {
    getResolumeRecovery.mockResolvedValue({ ...baseResponse, resolumeConfigured: false })
    renderToggle(makeModel({}))

    await waitFor(() => screen.getByRole('status'))
    expect(screen.getByRole('status').textContent).toContain('not configured')
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })
})
