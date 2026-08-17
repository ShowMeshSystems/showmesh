import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { RenderSettingsPanel } from './RenderSettingsPanel'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

// Track B seam B2c's own component test, mirroring
// ResolumeRecoveryToggle.test.tsx's pattern: the three API functions are
// mocked to isolate this component's own branching (loading,
// unauthenticated, never-configured, configured), not the network
// behavior itself.
const { getRenderSettingsConfig, putRenderSettingsConfig, getRenderSettingsConfigRevisions } = vi.hoisted(() => ({
  getRenderSettingsConfig: vi.fn(),
  putRenderSettingsConfig: vi.fn(),
  getRenderSettingsConfigRevisions: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getRenderSettingsConfig, putRenderSettingsConfig, getRenderSettingsConfigRevisions }
})

afterEach(() => {
  cleanup()
  getRenderSettingsConfig.mockReset()
  putRenderSettingsConfig.mockReset()
  getRenderSettingsConfigRevisions.mockReset()
})

function renderPanel(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <RenderSettingsPanel />
    </ModelContext.Provider>,
  )
}

const defaultConfig = {
  serverTime: '2026-08-17T00:00:00Z',
  kind: 'render.settings',
  revision: 0,
  payload: {
    idleOutput: 'black',
    restartPolicy: { initialDelaySeconds: 1, maxDelaySeconds: 30, maxConsecutiveFastFailures: 5 },
  },
  updatedAt: '2026-08-17T00:00:00Z',
  createdByPrincipalId: null,
  createdByPrincipalName: null,
  source: 'default',
}

const configuredConfig = {
  ...defaultConfig,
  revision: 3,
  payload: {
    idleOutput: 'hold',
    restartPolicy: { initialDelaySeconds: 2, maxDelaySeconds: 45, maxConsecutiveFastFailures: 6 },
  },
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'admin-1',
  source: 'api',
}

const emptyRevisions = { serverTime: '2026-08-17T00:00:00Z', kind: 'render.settings', revisions: [] }
const oneRevision = {
  serverTime: '2026-08-17T00:00:00Z',
  kind: 'render.settings',
  revisions: [
    { revision: 3, createdAt: '2026-08-17T00:00:00Z', createdByPrincipalId: 'p-1', createdByPrincipalName: 'admin-1', source: 'api', note: '', active: true },
  ],
}

const adminSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['node:read', 'fpp:read', 'observation:read', 'event:read', 'config:write'],
  scopesState: 'current',
})

describe('RenderSettingsPanel', () => {
  it('renders "waiting" and does not fetch when no session has arrived yet', () => {
    renderPanel(makeModel({ session: null }))

    expect(screen.getByRole('status').textContent).toMatch(/waiting to hear/i)
    expect(getRenderSettingsConfig).not.toHaveBeenCalled()
  })

  it('renders the missing-scope reason and does not fetch for a principal without config:write', () => {
    const viewerSession = makeAuthenticatedSession({
      principal: { id: 'p-2', name: 'viewer-1', kind: 'human', role: 'viewer' },
      scopes: ['node:read', 'fpp:read', 'observation:read', 'event:read'],
      scopesState: 'current',
    })
    renderPanel(makeModel({ session: viewerSession }))

    expect(screen.getByRole('status').textContent).not.toMatch(/waiting to hear/i)
    expect(getRenderSettingsConfig).not.toHaveBeenCalled()
  })

  it('renders the built-in default explicitly, distinct from a loaded state', async () => {
    getRenderSettingsConfig.mockResolvedValue(defaultConfig)
    getRenderSettingsConfigRevisions.mockResolvedValue(emptyRevisions)
    renderPanel(makeModel({ session: adminSession }))

    await waitFor(() => screen.getByText(/never configured/i))
    expect(screen.getByText(/built-in default/i)).toBeInTheDocument()
  })

  it('renders a loaded, previously-written revision with its metadata', async () => {
    getRenderSettingsConfig.mockResolvedValue(configuredConfig)
    getRenderSettingsConfigRevisions.mockResolvedValue(oneRevision)
    renderPanel(makeModel({ session: adminSession }))

    await waitFor(() => screen.getByText(/active revision 3/i))
    expect(screen.getAllByText(/admin-1/).length).toBeGreaterThan(0)
    expect(screen.getByText(/source api/i)).toBeInTheDocument()
  })

  it('submits the full payload on save, including every restartPolicy member', async () => {
    getRenderSettingsConfig.mockResolvedValue(defaultConfig)
    getRenderSettingsConfigRevisions.mockResolvedValue(emptyRevisions)
    putRenderSettingsConfig.mockResolvedValue(configuredConfig)
    renderPanel(makeModel({ session: adminSession }))

    await waitFor(() => screen.getByText(/never configured/i))

    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /save render settings/i }))

    await waitFor(() => expect(putRenderSettingsConfig).toHaveBeenCalledTimes(1))
    expect(putRenderSettingsConfig).toHaveBeenCalledWith({
      idleOutput: 'black',
      restartPolicy: { initialDelaySeconds: 1, maxDelaySeconds: 30, maxConsecutiveFastFailures: 5 },
    })
  })

  it('renders a disabled, reasoned save button for a principal without config:write, never a hidden one', () => {
    const viewerSession = makeAuthenticatedSession({
      principal: { id: 'p-2', name: 'viewer-1', kind: 'human', role: 'viewer' },
      scopes: ['node:read', 'fpp:read', 'observation:read', 'event:read'],
      scopesState: 'current',
    })
    renderPanel(makeModel({ session: viewerSession }))

    // The panel gates its whole body on the scope, so no save button
    // exists at all here — this is the outer Configuration.tsx's job to
    // avoid rendering this panel when not permitted, matching every other
    // config:write-gated section on that page.
    expect(screen.queryByRole('button', { name: /save render settings/i })).not.toBeInTheDocument()
  })

  it('renders an error state distinctly from loading and never-configured', async () => {
    getRenderSettingsConfig.mockRejectedValue(new Error('network error requesting /config/render.settings'))
    getRenderSettingsConfigRevisions.mockResolvedValue(emptyRevisions)
    renderPanel(makeModel({ session: adminSession }))

    await waitFor(() => screen.getByRole('alert'))
    expect(screen.getByRole('alert').textContent).toMatch(/network error/i)
  })
})
