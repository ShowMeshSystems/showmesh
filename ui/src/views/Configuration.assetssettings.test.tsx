import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Configuration } from './Configuration'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'
import { ApiError } from '../api/errors'

// Track G seam G-4 (ADR-039): the assets.settings section's own behavior,
// mirroring Configuration.resolume.test.tsx's coverage for the identical
// shape (load, 404-as-not-configured, error, save, 409). The FPP and
// Resolume sections' own API functions are mocked here too, defaulted to a
// resolved "nothing configured" state so this file's tests exercise only
// the assets.settings section, undisturbed by the other two sections'
// fetches.
const {
  getFPPEndpointsConfig,
  getFPPEndpointsConfigRevisions,
  getResolumeInstancesConfig,
  getResolumeInstancesConfigRevisions,
  getAssetsSettingsConfig,
  putAssetsSettingsConfig,
  getAssetsSettingsConfigRevisions,
} = vi.hoisted(() => ({
  getFPPEndpointsConfig: vi.fn(),
  getFPPEndpointsConfigRevisions: vi.fn(),
  getResolumeInstancesConfig: vi.fn(),
  getResolumeInstancesConfigRevisions: vi.fn(),
  getAssetsSettingsConfig: vi.fn(),
  putAssetsSettingsConfig: vi.fn(),
  getAssetsSettingsConfigRevisions: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return {
    ...actual,
    getFPPEndpointsConfig,
    getFPPEndpointsConfigRevisions,
    getResolumeInstancesConfig,
    getResolumeInstancesConfigRevisions,
    getAssetsSettingsConfig,
    putAssetsSettingsConfig,
    getAssetsSettingsConfigRevisions,
  }
})

const emptyFPPRevisions = { serverTime: '2026-08-17T00:00:00Z', kind: 'fpp.endpoints', revisions: [] }
const emptyResolumeRevisions = { serverTime: '2026-08-17T00:00:00Z', kind: 'resolume.instances', revisions: [] }
const emptyAssetsSettingsRevisions = { serverTime: '2026-08-17T00:00:00Z', kind: 'assets.settings', revisions: [] }

const activeAssetsSettingsConfig = {
  serverTime: '2026-08-17T00:00:00Z',
  kind: 'assets.settings',
  revision: 1,
  payload: {
    contentBaseUrl: 'https://coordinator.example',
    maxUploadBytes: 1048576,
    syncIntervalSeconds: 300,
    inventoryIntervalSeconds: 120,
  },
  updatedAt: '2026-08-17T00:00:00Z',
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'admin-1',
  source: 'api',
  restartRequired: false,
  restartRequiredReason:
    'this change is already in effect: the asset sync service follows this configuration promptly (within about ten seconds). No restart is needed.',
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

async function assetsSettingsSection() {
  const heading = await screen.findByText('Asset store settings')
  return heading.closest('section')!
}

beforeEach(() => {
  // Default the FPP and Resolume sections to a resolved "nothing
  // configured" 404 so their own render is quiet and does not interfere
  // with this file's assets.settings assertions.
  getFPPEndpointsConfig.mockRejectedValue(
    new ApiError('no fpp.endpoints configuration has been created yet; PUT one to create it', 404,
      'https://showmesh.dev/problems/resource-not-found'),
  )
  getFPPEndpointsConfigRevisions.mockResolvedValue(emptyFPPRevisions)
  getResolumeInstancesConfig.mockRejectedValue(
    new ApiError('no resolume.instances configuration has been created yet; PUT one to create it', 404,
      'https://showmesh.dev/problems/resource-not-found'),
  )
  getResolumeInstancesConfigRevisions.mockResolvedValue(emptyResolumeRevisions)
})

afterEach(() => {
  cleanup()
  getFPPEndpointsConfig.mockReset()
  getFPPEndpointsConfigRevisions.mockReset()
  getResolumeInstancesConfig.mockReset()
  getResolumeInstancesConfigRevisions.mockReset()
  getAssetsSettingsConfig.mockReset()
  putAssetsSettingsConfig.mockReset()
  getAssetsSettingsConfigRevisions.mockReset()
})

describe('Configuration: assets.settings section', () => {
  it('fetches and renders the active configuration and revision history for an admin', async () => {
    getAssetsSettingsConfig.mockResolvedValue(activeAssetsSettingsConfig)
    getAssetsSettingsConfigRevisions.mockResolvedValue({
      serverTime: '2026-08-17T00:00:00Z',
      kind: 'assets.settings',
      revisions: [
        {
          revision: 1, createdAt: '2026-08-17T00:00:00Z', createdByPrincipalId: 'p-1',
          createdByPrincipalName: 'admin-1', source: 'api', note: '', active: true,
        },
      ],
    })
    renderConfiguration(makeModel({ session: adminSession }))

    await waitFor(() => expect(getAssetsSettingsConfig).toHaveBeenCalled())
    expect(await screen.findByDisplayValue('https://coordinator.example')).toBeInTheDocument()
    expect(screen.getByDisplayValue('1048576')).toBeInTheDocument()
    expect(screen.getByDisplayValue('300')).toBeInTheDocument()
    expect(screen.getByDisplayValue('120')).toBeInTheDocument()
    expect(screen.getByText(/already in effect/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /save asset settings/i })).toBeEnabled()
  })

  it("renders the coordinator's own 404 reason with an empty editor, not as an error", async () => {
    getAssetsSettingsConfig.mockRejectedValue(
      new ApiError('no assets.settings configuration has been created yet; PUT one to create it', 404,
        'https://showmesh.dev/problems/resource-not-found'),
    )
    getAssetsSettingsConfigRevisions.mockResolvedValue(emptyAssetsSettingsRevisions)
    renderConfiguration(makeModel({ session: adminSession }))

    const section = await assetsSettingsSection()
    expect(await within(section).findByText(/configuration has been created yet/i)).toBeInTheDocument()
    expect(within(section).queryByRole('alert')).not.toBeInTheDocument()
  })

  it('renders a real fetch failure as an error, distinct from 404', async () => {
    getAssetsSettingsConfig.mockRejectedValue(new ApiError('the store is unreachable', 500))
    getAssetsSettingsConfigRevisions.mockResolvedValue(emptyAssetsSettingsRevisions)
    renderConfiguration(makeModel({ session: adminSession }))

    const section = await assetsSettingsSection()
    expect(await within(section).findByRole('alert')).toHaveTextContent(/unreachable/i)
  })

  it('saves all four fields via PUT, and reloads on success', async () => {
    getAssetsSettingsConfig.mockResolvedValue(activeAssetsSettingsConfig)
    getAssetsSettingsConfigRevisions.mockResolvedValue(emptyAssetsSettingsRevisions)
    putAssetsSettingsConfig.mockResolvedValue({ ...activeAssetsSettingsConfig, revision: 2 })
    const user = userEvent.setup()
    renderConfiguration(makeModel({ session: adminSession }))

    await screen.findByDisplayValue('https://coordinator.example')
    const syncIntervalInput = screen.getByDisplayValue('300')
    await user.clear(syncIntervalInput)
    await user.type(syncIntervalInput, '600')

    await user.click(screen.getByRole('button', { name: /save asset settings/i }))

    await waitFor(() =>
      expect(putAssetsSettingsConfig).toHaveBeenCalledWith({
        contentBaseUrl: 'https://coordinator.example',
        maxUploadBytes: 1048576,
        syncIntervalSeconds: 600,
        inventoryIntervalSeconds: 120,
      }),
    )
    // A successful save triggers a reload, matching the other two
    // sections' own "must re-fetch, never guess" contract.
    await waitFor(() => expect(getAssetsSettingsConfig).toHaveBeenCalledTimes(2))
  })

  it('saving a blank content base URL PUTs an explicitly empty string', async () => {
    getAssetsSettingsConfig.mockResolvedValue(activeAssetsSettingsConfig)
    getAssetsSettingsConfigRevisions.mockResolvedValue(emptyAssetsSettingsRevisions)
    putAssetsSettingsConfig.mockResolvedValue({
      ...activeAssetsSettingsConfig, revision: 2, payload: { ...activeAssetsSettingsConfig.payload, contentBaseUrl: '' },
    })
    const user = userEvent.setup()
    renderConfiguration(makeModel({ session: adminSession }))

    await screen.findByDisplayValue('https://coordinator.example')
    await user.clear(screen.getByDisplayValue('https://coordinator.example'))

    await user.click(screen.getByRole('button', { name: /save asset settings/i }))

    await waitFor(() =>
      expect(putAssetsSettingsConfig).toHaveBeenCalledWith({
        contentBaseUrl: '',
        maxUploadBytes: 1048576,
        syncIntervalSeconds: 300,
        inventoryIntervalSeconds: 120,
      }),
    )
  })

  // The PUT is a partial update (per-field optional, absent means
  // keep-stored/default). In the first-time zero-to-one setup path the
  // numeric fields start blank, and Number('') is 0 — a blank numeric
  // field must be OMITTED from the payload, never sent as an explicit 0.
  it('omits blank numeric fields from the PUT instead of coercing them to 0', async () => {
    getAssetsSettingsConfig.mockRejectedValue(
      new ApiError('no assets.settings configuration has been created yet; PUT one to create it', 404,
        'https://showmesh.dev/problems/resource-not-found'),
    )
    getAssetsSettingsConfigRevisions.mockResolvedValue(emptyAssetsSettingsRevisions)
    putAssetsSettingsConfig.mockResolvedValue(activeAssetsSettingsConfig)
    const user = userEvent.setup()
    renderConfiguration(makeModel({ session: adminSession }))

    await assetsSettingsSection()
    await user.type(screen.getByLabelText('Asset content base URL'), 'https://coordinator.example')
    // A field the operator explicitly filled still goes out as typed.
    await user.type(screen.getByLabelText('Asset sync interval seconds'), '600')

    await user.click(screen.getByRole('button', { name: /save asset settings/i }))

    await waitFor(() =>
      expect(putAssetsSettingsConfig).toHaveBeenCalledWith({
        contentBaseUrl: 'https://coordinator.example',
        syncIntervalSeconds: 600,
      }),
    )
    const sentRequest = putAssetsSettingsConfig.mock.calls.at(0)?.at(0)
    expect(sentRequest).not.toHaveProperty('maxUploadBytes')
    expect(sentRequest).not.toHaveProperty('inventoryIntervalSeconds')
  })

  it("renders the coordinator's 409 refusal (a SHOWMESH_ASSET_* variable still set) as an actionable message", async () => {
    getAssetsSettingsConfig.mockResolvedValue(activeAssetsSettingsConfig)
    getAssetsSettingsConfigRevisions.mockResolvedValue(emptyAssetsSettingsRevisions)
    putAssetsSettingsConfig.mockRejectedValue(
      new ApiError(
        'This write is refused because one or more of SHOWMESH_ASSET_CONTENT_BASE_URL, ' +
          'SHOWMESH_ASSET_MAX_UPLOAD_BYTES, SHOWMESH_ASSET_SYNC_INTERVAL, or SHOWMESH_ASSET_INVENTORY_INTERVAL is ' +
          'still set in this coordinator\'s environment — accepting it now would conflict with those variables on ' +
          'the next restart. Remove all four from your environment and restart this coordinator once, then retry.',
        409,
        'https://showmesh.dev/problems/conflict',
      ),
    )
    const user = userEvent.setup()
    renderConfiguration(makeModel({ session: adminSession }))

    await screen.findByDisplayValue('https://coordinator.example')
    await user.click(screen.getByRole('button', { name: /save asset settings/i }))

    const section = await assetsSettingsSection()
    const alert = await within(section).findByRole('alert')
    expect(alert).toHaveTextContent(/SHOWMESH_ASSET_CONTENT_BASE_URL/)
    expect(alert).toHaveTextContent(/restart this coordinator once/i)
  })
})
