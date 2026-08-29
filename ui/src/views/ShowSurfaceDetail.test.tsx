import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ShowSurfaceDetail } from './ShowSurfaceDetail'
import { ModelContext } from '../app/ModelContext'
import { makeModel, makeShowList } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

const { getShow, getShowSurface, getShowSurfaceRevisions, putShowSurface, listConfigObjects, listAssets } = vi.hoisted(() => ({
  getShow: vi.fn(),
  getShowSurface: vi.fn(),
  getShowSurfaceRevisions: vi.fn(),
  putShowSurface: vi.fn(),
  listConfigObjects: vi.fn(),
  listAssets: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getShow, getShowSurface, getShowSurfaceRevisions, putShowSurface, listConfigObjects, listAssets }
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

const adminSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['config:write'],
})

const showResponse = {
  serverTime: '2026-08-25T00:00:00Z',
  kind: 'show' as const,
  id: 'halloween-2026',
  revision: 3,
  payload: { name: 'Halloween 2026', notes: '' },
  updatedAt: '2026-08-25T00:00:00Z',
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'admin-1',
  source: 'api' as const,
}

function emptyList(kind: string) {
  return { serverTime: '2026-08-25T00:00:00Z', kind, objects: [] }
}

// Every mounted `ShowSelect` shares one `GET /config/show` fetch
// (`listConfigObjects('show')`); this stands in for it alongside the
// per-tab workspace counts, which stay empty.
function mockWorkspaceLists(shows: string[] = ['halloween-2026', 'christmas-2026']): void {
  getShow.mockResolvedValue(showResponse)
  listAssets.mockResolvedValue({ serverTime: '2026-08-25T00:00:00Z', assets: [] })
  listConfigObjects.mockImplementation((kind: string) =>
    Promise.resolve(kind === 'show' ? makeShowList(shows) : emptyList(kind)),
  )
}

function renderNew(model: Model = makeModel({ session: adminSession })) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={['/shows/halloween-2026/presentation/new']}>
        <Routes>
          <Route path="/shows/:showId/presentation/new" element={<ShowSurfaceDetail isNew />} />
          <Route path="/shows/:showId/presentation/:id" element={<ShowSurfaceDetail />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

function renderExisting(id: string, model: Model = makeModel({ session: adminSession })) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={[`/shows/halloween-2026/presentation/${id}`]}>
        <Routes>
          <Route path="/shows/:showId/presentation/:id" element={<ShowSurfaceDetail />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

const storedSurface = {
  serverTime: '2026-08-25T00:00:00Z',
  kind: 'show.surface' as const,
  id: 'led-wall',
  revision: 2,
  payload: {
    show: 'halloween-2026',
    name: 'LED wall',
    node: 'node-1',
    channelRange: { startChannel: 1, channelCount: 4096 },
    geometry: { width: 32, height: 32, pixelFormat: 'rgbw' as const },
    frameRate: 30,
    output: { transport: 'ndi' as const, ndi: { sourceName: 'wall-1' } },
  },
  updatedAt: '2026-08-25T00:00:00Z',
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'admin-1',
  source: 'api' as const,
}

const emptyRevisions = { serverTime: '2026-08-25T00:00:00Z', revisions: [] }

describe('ShowSurfaceDetail (move to another show)', () => {
  it('renders the re-assignment control for an existing surface, defaulted to its current show', async () => {
    mockWorkspaceLists()
    getShowSurface.mockResolvedValue(storedSurface)
    getShowSurfaceRevisions.mockResolvedValue(emptyRevisions)
    renderExisting('led-wall')

    await waitFor(() => expect(screen.getByDisplayValue('LED wall')).toBeVisible())
    const select = await screen.findByRole('combobox', { name: 'Move to another show' })
    expect(select).toHaveValue('halloween-2026')
    expect(screen.getByText(/removes it from render readiness for playlists there/)).toBeVisible()
  })

  it('is absent when creating a new surface: the route already names the right show', async () => {
    mockWorkspaceLists()
    renderNew()

    await waitFor(() => expect(screen.getByLabelText('Name')).toBeVisible())
    expect(screen.queryByText(/Move to another show/i)).not.toBeInTheDocument()
  })

  it('is disabled, with the stated reason, for a reader without config:write', async () => {
    mockWorkspaceLists()
    getShowSurface.mockResolvedValue(storedSurface)
    getShowSurfaceRevisions.mockResolvedValue(emptyRevisions)
    renderExisting('led-wall', makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) }))

    await waitFor(() => expect(screen.getByDisplayValue('LED wall')).toBeVisible())
    expect(screen.getByText(/Viewing only/)).toBeVisible()
    const select = await screen.findByRole('combobox', { name: 'Move to another show' })
    expect(select).toBeDisabled()
  })

  it('saves the newly selected show and lands on the object at its new show-scoped route', async () => {
    mockWorkspaceLists()
    getShowSurface.mockResolvedValue(storedSurface)
    getShowSurfaceRevisions.mockResolvedValue(emptyRevisions)
    putShowSurface.mockResolvedValue({
      ...storedSurface,
      revision: 3,
      payload: { ...storedSurface.payload, show: 'christmas-2026' },
    })
    const user = userEvent.setup()
    renderExisting('led-wall')

    await waitFor(() => expect(screen.getByDisplayValue('LED wall')).toBeVisible())
    const select = await screen.findByRole('combobox', { name: 'Move to another show' })
    await user.selectOptions(select, 'christmas-2026')
    await user.click(screen.getByRole('button', { name: 'Save surface' }))

    await waitFor(() => expect(putShowSurface).toHaveBeenCalledTimes(1))
    expect(putShowSurface).toHaveBeenCalledWith('led-wall', expect.objectContaining({ show: 'christmas-2026' }))
    // Landed on the new show-scoped route rather than staying on the old
    // one: the revision-history refresh that would run for a same-show
    // save never fires.
    await waitFor(() => expect(screen.getByText(/removes it from render readiness for playlists there/)).toBeVisible())
    expect(screen.getByText(/Moving this surface out of christmas-2026/)).toBeVisible()
    expect(getShowSurfaceRevisions).toHaveBeenCalledTimes(1)
  })
})
