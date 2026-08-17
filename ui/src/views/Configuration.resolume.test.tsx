import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Configuration } from './Configuration'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'
import { ApiError } from '../api/errors'

// Track G seam G-2 (ADR-039): the resolume.instances section's own
// behavior, mirroring Configuration.test.tsx's FPP coverage for the
// identical shape (load, 404-as-not-configured, error, save, 409). The FPP
// half's own three API functions are mocked here too, defaulted to a
// resolved "already configured" state so this file's tests exercise only
// the Resolume section, undisturbed by the FPP section's own fetch.
const {
  getFPPEndpointsConfig,
  getFPPEndpointsConfigRevisions,
  getResolumeInstancesConfig,
  putResolumeInstancesConfig,
  getResolumeInstancesConfigRevisions,
} = vi.hoisted(() => ({
  getFPPEndpointsConfig: vi.fn(),
  getFPPEndpointsConfigRevisions: vi.fn(),
  getResolumeInstancesConfig: vi.fn(),
  putResolumeInstancesConfig: vi.fn(),
  getResolumeInstancesConfigRevisions: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return {
    ...actual,
    getFPPEndpointsConfig,
    getFPPEndpointsConfigRevisions,
    getResolumeInstancesConfig,
    putResolumeInstancesConfig,
    getResolumeInstancesConfigRevisions,
  }
})

const emptyFPPRevisions = { serverTime: '2026-08-17T00:00:00Z', kind: 'fpp.endpoints', revisions: [] }
const emptyResolumeRevisions = {
  serverTime: '2026-08-17T00:00:00Z',
  kind: 'resolume.instances',
  revisions: [],
}

const activeResolumeConfig = {
  serverTime: '2026-08-17T00:00:00Z',
  kind: 'resolume.instances',
  revision: 1,
  payload: { instances: [{ id: 'arena-1', url: 'http://10.0.1.30:8080' }] },
  updatedAt: '2026-08-17T00:00:00Z',
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'admin-1',
  source: 'api',
  restartRequired: false,
  restartRequiredReason:
    'this change is already in effect: the Resolume collector set follows this configuration within about ten seconds. No restart is needed.',
}

const adminSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['node:read', 'fpp:read', 'observation:read', 'event:read', 'config:write'],
  scopesState: 'current',
})

function renderConfiguration(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <Configuration />
    </ModelContext.Provider>,
  )
}

async function resolumeSection() {
  const heading = await screen.findByText('Resolume')
  return heading.closest('section')!
}

beforeEach(() => {
  // Default the FPP section to a resolved "nothing configured" 404 so its
  // own render is quiet and does not interfere with this file's Resolume
  // assertions (mirroring Configuration.test.tsx's identical default for
  // the Resolume section, in the other direction).
  getFPPEndpointsConfig.mockRejectedValue(
    new ApiError('no fpp.endpoints configuration has been created yet; PUT one to create it', 404,
      'https://showmesh.dev/problems/resource-not-found'),
  )
  getFPPEndpointsConfigRevisions.mockResolvedValue(emptyFPPRevisions)
})

afterEach(() => {
  cleanup()
  getFPPEndpointsConfig.mockReset()
  getFPPEndpointsConfigRevisions.mockReset()
  getResolumeInstancesConfig.mockReset()
  putResolumeInstancesConfig.mockReset()
  getResolumeInstancesConfigRevisions.mockReset()
})

describe('Configuration: Resolume instances section', () => {
  it('fetches and renders the active configuration and revision history for an admin', async () => {
    getResolumeInstancesConfig.mockResolvedValue(activeResolumeConfig)
    getResolumeInstancesConfigRevisions.mockResolvedValue({
      serverTime: '2026-08-17T00:00:00Z',
      kind: 'resolume.instances',
      revisions: [
        {
          revision: 1, createdAt: '2026-08-17T00:00:00Z', createdByPrincipalId: 'p-1',
          createdByPrincipalName: 'admin-1', source: 'api', note: '', active: true,
        },
      ],
    })
    renderConfiguration(makeModel({ session: adminSession }))

    await waitFor(() => expect(getResolumeInstancesConfig).toHaveBeenCalled())
    expect(await screen.findByDisplayValue('arena-1')).toBeInTheDocument()
    expect(screen.getByDisplayValue('http://10.0.1.30:8080')).toBeInTheDocument()
    expect(screen.getByText(/already in effect/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /save resolume instance/i })).toBeEnabled()
  })

  it("renders the coordinator's own 404 reason with an empty editor, not as an error", async () => {
    getResolumeInstancesConfig.mockRejectedValue(
      new ApiError('no resolume.instances configuration has been created yet; PUT one to create it', 404,
        'https://showmesh.dev/problems/resource-not-found'),
    )
    getResolumeInstancesConfigRevisions.mockResolvedValue(emptyResolumeRevisions)
    renderConfiguration(makeModel({ session: adminSession }))

    const section = await resolumeSection()
    expect(await within(section).findByText(/configuration has been created yet/i)).toBeInTheDocument()
    expect(within(section).queryByRole('alert')).not.toBeInTheDocument()
  })

  it('renders a real fetch failure as an error, distinct from 404', async () => {
    getResolumeInstancesConfig.mockRejectedValue(new ApiError('the store is unreachable', 500))
    getResolumeInstancesConfigRevisions.mockResolvedValue(emptyResolumeRevisions)
    renderConfiguration(makeModel({ session: adminSession }))

    const section = await resolumeSection()
    expect(await within(section).findByRole('alert')).toHaveTextContent(/unreachable/i)
  })

  it('saves the id/url pair via PUT with a single-element instances array', async () => {
    getResolumeInstancesConfig.mockResolvedValue(activeResolumeConfig)
    getResolumeInstancesConfigRevisions.mockResolvedValue(emptyResolumeRevisions)
    putResolumeInstancesConfig.mockResolvedValue({ ...activeResolumeConfig, revision: 2 })
    const user = userEvent.setup()
    renderConfiguration(makeModel({ session: adminSession }))

    await screen.findByDisplayValue('arena-1')
    const urlInput = screen.getByDisplayValue('http://10.0.1.30:8080')
    await user.clear(urlInput)
    await user.type(urlInput, 'http://10.0.1.99:8080')

    await user.click(screen.getByRole('button', { name: /save resolume instance/i }))

    await waitFor(() =>
      expect(putResolumeInstancesConfig).toHaveBeenCalledWith({
        instances: [{ id: 'arena-1', url: 'http://10.0.1.99:8080' }],
      }),
    )
    // A successful save triggers a reload, matching the FPP section's own
    // "must re-fetch, never guess" contract.
    await waitFor(() => expect(getResolumeInstancesConfig).toHaveBeenCalledTimes(2))
  })

  it('saving a blank id/url pair PUTs an explicitly empty instances array', async () => {
    getResolumeInstancesConfig.mockResolvedValue(activeResolumeConfig)
    getResolumeInstancesConfigRevisions.mockResolvedValue(emptyResolumeRevisions)
    putResolumeInstancesConfig.mockResolvedValue({
      ...activeResolumeConfig, revision: 2, payload: { instances: [] },
    })
    const user = userEvent.setup()
    renderConfiguration(makeModel({ session: adminSession }))

    await screen.findByDisplayValue('arena-1')
    await user.clear(screen.getByDisplayValue('arena-1'))
    await user.clear(screen.getByDisplayValue('http://10.0.1.30:8080'))

    await user.click(screen.getByRole('button', { name: /save resolume instance/i }))

    await waitFor(() =>
      expect(putResolumeInstancesConfig).toHaveBeenCalledWith({ instances: [] }),
    )
  })

  it("renders the coordinator's 409 refusal (SHOWMESH_RESOLUME_URL still set) as an actionable message", async () => {
    getResolumeInstancesConfig.mockResolvedValue(activeResolumeConfig)
    getResolumeInstancesConfigRevisions.mockResolvedValue(emptyResolumeRevisions)
    putResolumeInstancesConfig.mockRejectedValue(
      new ApiError(
        'This write is refused because SHOWMESH_RESOLUME_URL is still set in this coordinator\'s environment ' +
          '— accepting it now would conflict with that variable on the next restart. Remove SHOWMESH_RESOLUME_URL ' +
          'and SHOWMESH_RESOLUME_ID and restart this coordinator once, then retry.',
        409,
        'https://showmesh.dev/problems/conflict',
      ),
    )
    const user = userEvent.setup()
    renderConfiguration(makeModel({ session: adminSession }))

    await screen.findByDisplayValue('arena-1')
    await user.click(screen.getByRole('button', { name: /save resolume instance/i }))

    const section = await resolumeSection()
    const alert = await within(section).findByRole('alert')
    expect(alert).toHaveTextContent(/SHOWMESH_RESOLUME_URL/)
    expect(alert).toHaveTextContent(/restart this coordinator once/i)
  })
})
