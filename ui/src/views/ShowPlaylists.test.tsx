import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ShowPlaylists } from './ShowPlaylists'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

// The Playlists workspace tab now reads its show from the real nested
// route param (/shows/:showId/playlists), not a ?show= query, and shows
// a card per playlist (runner + entry summary) rather than a bare table
// row - both the show identity and each card's runner summary come from
// real API reads, mocked here.
const { getShow, listConfigObjects, listAssets, getShowPlaylist } = vi.hoisted(() => ({
  getShow: vi.fn(),
  listConfigObjects: vi.fn(),
  listAssets: vi.fn(),
  getShowPlaylist: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getShow, listConfigObjects, listAssets, getShowPlaylist }
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

function renderShowPlaylists(model: Model, showId = 'halloween-2026') {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={[`/shows/${showId}/playlists`]}>
        <Routes>
          <Route path="/shows/:showId/playlists" element={<ShowPlaylists />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

const operatorSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'operator-1', kind: 'human', role: 'operator' },
  scopes: ['show:macro:run'],
})

const showResponse = {
  serverTime: '2026-08-25T00:00:00Z',
  kind: 'show' as const,
  id: 'halloween-2026',
  revision: 3,
  payload: { name: 'Halloween 2026', notes: '' },
  updatedAt: '2026-08-25T00:00:00Z',
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'operator-1',
  source: 'api' as const,
}

const playlistList = {
  serverTime: '2026-08-25T00:00:00Z',
  kind: 'show.playlist' as const,
  objects: [
    { id: 'main-run', label: 'Main run', show: 'halloween-2026', currentRevision: 1, updatedAt: '2026-08-25T00:00:00Z' },
    { id: 'finale-run', label: 'Finale run', show: 'halloween-2026', currentRevision: 2, updatedAt: '2026-08-25T00:00:00Z' },
  ],
}

function emptyList(kind: string) {
  return { serverTime: '2026-08-25T00:00:00Z', kind, objects: [] }
}

describe('ShowPlaylists', () => {
  it('renders one card per playlist with its runner', async () => {
    getShow.mockResolvedValue(showResponse)
    listAssets.mockResolvedValue({ serverTime: '2026-08-25T00:00:00Z', assets: [] })
    listConfigObjects.mockImplementation((kind: string) => Promise.resolve(kind === 'show.playlist' ? playlistList : emptyList(kind)))
    getShowPlaylist.mockImplementation((id: string) =>
      Promise.resolve({
        serverTime: '2026-08-25T00:00:00Z',
        kind: 'show.playlist',
        id,
        revision: 1,
        payload: {
          show: 'halloween-2026',
          name: id === 'main-run' ? 'Main run' : 'Finale run',
          runner: 'fpp',
          entries: [{ id: 'e1', cue: 'opening-number' }],
        },
        updatedAt: '2026-08-25T00:00:00Z',
        createdByPrincipalId: 'p-1',
        createdByPrincipalName: 'operator-1',
        source: 'api',
      }),
    )
    renderShowPlaylists(makeModel({ session: operatorSession }))

    await waitFor(() => expect(screen.getByText('Main run')).toBeVisible())
    expect(screen.getByText('Finale run')).toBeVisible()
    expect(screen.getAllByText('FPP runner')).toHaveLength(2)
    expect(listConfigObjects).toHaveBeenCalledWith('show.playlist', 'halloween-2026')
  })

  it('renders empty state when the show has no playlists yet', async () => {
    getShow.mockResolvedValue(showResponse)
    listAssets.mockResolvedValue({ serverTime: '2026-08-25T00:00:00Z', assets: [] })
    listConfigObjects.mockImplementation((kind: string) => Promise.resolve(emptyList(kind)))
    renderShowPlaylists(makeModel({ session: operatorSession }))

    await waitFor(() => expect(screen.getByText(/No playlists are authored/)).toBeVisible())
  })

  it('renders the missing-scope reason and does not fetch for a principal with neither read scope', () => {
    renderShowPlaylists(makeModel({ session: makeAuthenticatedSession({ scopes: [] }) }))

    expect(screen.getByRole('status')).toBeVisible()
    expect(getShow).not.toHaveBeenCalled()
  })
})
