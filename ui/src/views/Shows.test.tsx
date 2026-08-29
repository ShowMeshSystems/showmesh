import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Shows } from './Shows'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import { ApiError } from '../api/errors'
import type { Model } from '../app/types'

// ROUTE-MAP.md owner ruling 2026-08-29: show activation (formerly
// ShowActive.tsx) now lives directly on the Shows list, above "All
// shows" - a dropdown fed by the real show list plus a Select button
// that arms a second, distinct confirm click, with activation history
// below the list.
const { listConfigObjects, listAssets, getShowActive, getShowActiveRevisions, putShowActive } = vi.hoisted(() => ({
  listConfigObjects: vi.fn(),
  listAssets: vi.fn(),
  getShowActive: vi.fn(),
  getShowActiveRevisions: vi.fn(),
  putShowActive: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, listConfigObjects, listAssets, getShowActive, getShowActiveRevisions, putShowActive }
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

function renderShows(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter>
        <Shows />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

const operatorSession = makeAuthenticatedSession({ scopes: ['show:macro:run'] })
const adminSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['config:write'],
})

const showList = {
  serverTime: '2026-08-25T00:00:00Z',
  kind: 'show' as const,
  objects: [
    { id: 'winter-ridge-2026', label: 'Winter Ridge 2026', show: '', currentRevision: 47, updatedAt: '2026-08-25T18:22:00Z' },
    { id: 'spring-test-bench', label: 'Spring Test Bench', show: '', currentRevision: 1, updatedAt: '2026-08-14T16:02:00Z' },
  ],
}

function emptyList(kind: string) {
  return { serverTime: '2026-08-25T00:00:00Z', kind, objects: [] }
}

function mockBaseline(): void {
  listConfigObjects.mockImplementation((kind: string) => Promise.resolve(kind === 'show' ? showList : emptyList(kind)))
  listAssets.mockResolvedValue({ serverTime: '2026-08-25T00:00:00Z', assets: [] })
  getShowActiveRevisions.mockResolvedValue({ serverTime: '2026-08-25T00:00:00Z', revisions: [] })
}

describe('Shows', () => {
  it('renders the active show badge and per-show contents summary', async () => {
    mockBaseline()
    getShowActive.mockResolvedValue({
      serverTime: '2026-08-25T00:00:00Z',
      kind: 'show.active',
      id: 'show.active',
      revision: 1,
      payload: { show: 'winter-ridge-2026' },
      updatedAt: '2026-08-25T18:22:00Z',
      createdByPrincipalId: 'p-1',
      createdByPrincipalName: 'erbartos',
      source: 'api',
    })
    renderShows(makeModel({ session: operatorSession }))

    await waitFor(() => expect(screen.getByText('Winter Ridge 2026')).toBeVisible())
    expect(screen.getByText('Active')).toBeVisible()
  })

  it('feeds the activate dropdown from the real show list, never an empty <select>', async () => {
    mockBaseline()
    getShowActive.mockRejectedValue(new ApiError('not configured', 404, 'https://showmesh.dev/problems/not-found'))
    renderShows(makeModel({ session: adminSession }))

    await waitFor(() => expect(screen.getByLabelText('Show to activate')).toBeVisible())
    const options = screen.getByLabelText('Show to activate').querySelectorAll('option')
    expect(options.length).toBeGreaterThan(1)
    expect(screen.getAllByText(/Winter Ridge 2026/).length).toBeGreaterThan(0)
  })

  it('arms a confirmation before activating, and requires a second distinct click to submit', async () => {
    mockBaseline()
    getShowActive.mockRejectedValue(new ApiError('not configured', 404, 'https://showmesh.dev/problems/not-found'))
    putShowActive.mockResolvedValue({
      serverTime: '2026-08-25T00:00:00Z',
      kind: 'show.active',
      id: 'show.active',
      revision: 2,
      payload: { show: 'spring-test-bench' },
      updatedAt: '2026-08-25T18:30:00Z',
      createdByPrincipalId: 'p-1',
      createdByPrincipalName: 'admin-1',
      source: 'api',
    })
    const user = userEvent.setup()
    renderShows(makeModel({ session: adminSession }))

    await waitFor(() => expect(screen.getByLabelText('Show to activate')).toBeVisible())
    await user.selectOptions(screen.getByLabelText('Show to activate'), 'spring-test-bench')
    await user.click(screen.getByRole('button', { name: 'Select' }))

    expect(putShowActive).not.toHaveBeenCalled()
    expect(screen.getByRole('alertdialog', { name: /Confirm show activation/ })).toBeVisible()

    await user.click(screen.getByRole('button', { name: /Confirm: activate/ }))
    await waitFor(() => expect(putShowActive).toHaveBeenCalledWith({ show: 'spring-test-bench' }))
  })

  it('gates Select with ScopedButton and states the reason when the principal cannot activate', async () => {
    mockBaseline()
    getShowActive.mockRejectedValue(new ApiError('not configured', 404, 'https://showmesh.dev/problems/not-found'))
    renderShows(makeModel({ session: operatorSession }))

    await waitFor(() => expect(screen.getByLabelText('Show to activate')).toBeVisible())
    const selectButton = screen.getByRole('button', { name: 'Select' })
    expect(selectButton).toBeDisabled()
    expect(screen.getAllByText(/config:write/).length).toBeGreaterThan(0)
  })

  it('renders the missing-scope reason and does not fetch for a principal with neither read scope', () => {
    renderShows(makeModel({ session: makeAuthenticatedSession({ scopes: [] }) }))

    expect(screen.getAllByRole('status').length).toBeGreaterThan(0)
    expect(listConfigObjects).not.toHaveBeenCalled()
  })
})
