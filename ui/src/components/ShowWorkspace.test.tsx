import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ShowWorkspaceOverview } from './ShowWorkspace'
import { showWorkspacePath } from './showWorkspacePaths'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'

const { getShow, listConfigObjects, listAssets } = vi.hoisted(() => ({
  getShow: vi.fn(),
  listConfigObjects: vi.fn(),
  listAssets: vi.fn(),
}))

vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getShow, listConfigObjects, listAssets }
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

const operatorSession = makeAuthenticatedSession({ scopes: ['show:macro:run'] })
const showResponse = {
  serverTime: '2026-08-27T00:00:00Z',
  kind: 'show' as const,
  id: 'halloween-2026',
  revision: 3,
  payload: { name: 'Halloween 2026', notes: 'Main show' },
  updatedAt: '2026-08-27T00:00:00Z',
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'operator',
  source: 'api' as const,
}

describe('ShowWorkspace', () => {
  it('builds canonical show-local paths', () => {
    expect(showWorkspacePath('halloween 2026', 'show-night')).toBe('/config/show/halloween%202026/workspace/show-night')
  })

  it('renders resource counts and explicit unavailable modules', async () => {
    getShow.mockResolvedValue(showResponse)
    listConfigObjects.mockImplementation(async (kind: string) => ({
      serverTime: '2026-08-27T00:00:00Z',
      kind,
      objects: kind === 'show.cue' ? [{ id: 'cue-1', label: 'Opening', show: 'halloween-2026', currentRevision: 1, updatedAt: '2026-08-27T00:00:00Z' }] : [],
    }))
    listAssets.mockResolvedValue({ serverTime: '2026-08-27T00:00:00Z', assets: [] })

    render(
      <ModelContext.Provider value={makeModel({ session: operatorSession })}>
        <MemoryRouter>
          <ShowWorkspaceOverview showId="halloween-2026" />
        </MemoryRouter>
      </ModelContext.Provider>,
    )

    await waitFor(() => expect(screen.getByText('Halloween 2026')).toBeVisible())
    expect(screen.getByText('1')).toBeVisible()
    expect(screen.getByText(/combined run editor is not available yet/i)).toBeVisible()
    expect(screen.getAllByRole('link', { name: /Cues/ })[0]).toHaveAttribute('href', '/config/show/halloween-2026/workspace/cues')
  })

  it('states the permission or disconnected reason without fetching', () => {
    const model = makeModel({ session: makeAuthenticatedSession({ scopes: [] }), sessionFetchFailed: true })
    render(
      <ModelContext.Provider value={model}>
        <MemoryRouter>
          <ShowWorkspaceOverview showId="halloween-2026" />
        </MemoryRouter>
      </ModelContext.Provider>,
    )
    expect(screen.getByRole('status')).toBeVisible()
    expect(getShow).not.toHaveBeenCalled()
  })
})
