import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ShowPlaylistDetail } from './ShowPlaylistDetail'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import { ApiError } from '../api/errors'
import type { Model } from '../app/types'

// The real nested route now carries both :showId and :playlistId
// (ROUTE-MAP.md: /shows/:showId/playlists/:playlistId), and the runner
// decides which authority section renders - fpp's entries carry a
// Verdict column and cannot be reordered (no Move up/down); showmesh-
// audio's entries stay reorderable.
const { getShow, getShowPlaylist, getShowPlaylistRevisions, putShowPlaylist, listConfigObjects, listAssets } = vi.hoisted(() => ({
  getShow: vi.fn(),
  getShowPlaylist: vi.fn(),
  getShowPlaylistRevisions: vi.fn(),
  putShowPlaylist: vi.fn(),
  listConfigObjects: vi.fn(),
  listAssets: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getShow, getShowPlaylist, getShowPlaylistRevisions, putShowPlaylist, listConfigObjects, listAssets }
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

function mockWorkspaceLists(): void {
  getShow.mockResolvedValue(showResponse)
  listAssets.mockResolvedValue({ serverTime: '2026-08-25T00:00:00Z', assets: [] })
  listConfigObjects.mockImplementation((kind: string) => Promise.resolve(emptyList(kind)))
}

function renderNew(model: Model = makeModel({ session: adminSession })) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={['/shows/halloween-2026/playlists/new']}>
        <Routes>
          <Route path="/shows/:showId/playlists/new" element={<ShowPlaylistDetail isNew />} />
          <Route path="/shows/:showId/playlists/:playlistId" element={<ShowPlaylistDetail />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

function renderExisting(id: string, model: Model = makeModel({ session: adminSession })) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={[`/shows/halloween-2026/playlists/${id}`]}>
        <Routes>
          <Route path="/shows/:showId/playlists/:playlistId" element={<ShowPlaylistDetail />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

const storedFppPlaylist = {
  serverTime: '2026-08-25T00:00:00Z',
  kind: 'show.playlist' as const,
  id: 'main-run',
  revision: 3,
  payload: {
    show: 'halloween-2026',
    name: 'Main run',
    runner: 'fpp' as const,
    fpp: {
      instanceUuid: 'fpp-uuid-1',
      playlistName: 'Main Playlist',
      playlistHash: 'a'.repeat(64),
    },
    entries: [
      { id: 'e1', cue: 'opening-number' },
      { id: 'e2', cue: '' },
    ],
  },
  updatedAt: '2026-08-25T00:00:00Z',
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'admin-1',
  source: 'api' as const,
}

const emptyRevisions = { serverTime: '2026-08-25T00:00:00Z', revisions: [] }

describe('ShowPlaylistDetail (viewing an existing fpp playlist)', () => {
  it('renders the current payload, cannot reorder, and shows a per-entry verdict', async () => {
    getShowPlaylist.mockResolvedValue(storedFppPlaylist)
    getShowPlaylistRevisions.mockResolvedValue(emptyRevisions)
    mockWorkspaceLists()
    renderExisting('main-run')

    await waitFor(() => expect(screen.getByDisplayValue('Main run')).toBeVisible())
    expect(screen.getByLabelText('Runner')).toHaveValue('fpp')
    expect(screen.getByDisplayValue('fpp-uuid-1')).toBeVisible()

    const table = screen.getByRole('table', { name: 'Entries' })
    expect(within(table).queryByRole('button', { name: 'Move up' })).not.toBeInTheDocument()
    expect(within(table).getByText('✓ Bound')).toBeVisible()
    expect(within(table).getByText('Unbound')).toBeVisible()
  })

  // PUT /config/show.playlist/{id} refuses any request that changes
  // `show` (`show-config-cross-show-reference`; api/openapi.yaml's own
  // `putShowPlaylist` description says so in as many words: "`show` is
  // immutable"). No re-assignment picker is offered here because the
  // API has no working path for it, not because the capability was
  // overlooked.
  it('offers no show re-assignment control: show is immutable server-side for a playlist', async () => {
    getShowPlaylist.mockResolvedValue(storedFppPlaylist)
    getShowPlaylistRevisions.mockResolvedValue(emptyRevisions)
    mockWorkspaceLists()
    renderExisting('main-run')

    await waitFor(() => expect(screen.getByDisplayValue('Main run')).toBeVisible())
    expect(screen.queryByText(/Move to another show/i)).not.toBeInTheDocument()
  })

  it('renders the revision history', async () => {
    getShowPlaylist.mockResolvedValue(storedFppPlaylist)
    getShowPlaylistRevisions.mockResolvedValue({
      serverTime: '2026-08-25T00:00:00Z',
      revisions: [
        { revision: 3, active: true, createdAt: '2026-08-25T00:00:00Z', createdByPrincipalId: 'p-1', createdByPrincipalName: 'admin-1' },
        { revision: 2, active: false, createdAt: '2026-08-20T00:00:00Z', createdByPrincipalId: 'p-1', createdByPrincipalName: 'admin-1' },
      ],
    })
    mockWorkspaceLists()
    renderExisting('main-run')

    await waitFor(() => expect(screen.getByText('Revision history')).toBeVisible())
    const table = screen.getByRole('table', { name: 'Revision history' })
    expect(table).toHaveTextContent('3')
    expect(table).toHaveTextContent('active')
  })

  it('renders the coordinator’s refusal reason on a rejected save, without reading as saved', async () => {
    getShowPlaylist.mockResolvedValue({
      ...storedFppPlaylist,
      payload: { ...storedFppPlaylist.payload, entries: [{ id: 'e1', cue: 'opening-number' }] },
    })
    getShowPlaylistRevisions.mockResolvedValue(emptyRevisions)
    mockWorkspaceLists()
    putShowPlaylist.mockRejectedValue(
      new ApiError('show "another-show" does not match the existing object\'s show "halloween-2026"; show is immutable', 400, 'https://showmesh.dev/problems/show-config-cross-show-reference'),
    )
    const user = userEvent.setup()
    renderExisting('main-run')

    await waitFor(() => expect(screen.getByDisplayValue('Main run')).toBeVisible())
    await user.click(screen.getByRole('button', { name: 'Save playlist' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/show is immutable/)
    expect(screen.getByText(/Active revision 3/)).toBeVisible()
    expect(getShowPlaylist).toHaveBeenCalledTimes(1)
  })
})

describe('ShowPlaylistDetail (showmesh-audio playlist reorder)', () => {
  const storedAudioPlaylist = {
    ...storedFppPlaylist,
    payload: {
      show: 'halloween-2026',
      name: 'Background bed',
      runner: 'showmesh-audio' as const,
      entries: [
        { id: 'e1', cue: 'track-1' },
        { id: 'e2', cue: 'track-2' },
      ],
    },
  }

  it('reordering an entry with Move up/down changes what is submitted', async () => {
    getShowPlaylist.mockResolvedValue(storedAudioPlaylist)
    getShowPlaylistRevisions.mockResolvedValue(emptyRevisions)
    mockWorkspaceLists()
    putShowPlaylist.mockResolvedValue({ ...storedAudioPlaylist, revision: 4 })
    const user = userEvent.setup()
    renderExisting('bg-run')

    await waitFor(() => expect(screen.getByDisplayValue('Background bed')).toBeVisible())
    const table = screen.getByRole('table', { name: 'Entries' })
    const secondRow = within(table).getAllByRole('row')[2]!
    await user.click(within(secondRow).getByRole('button', { name: 'Move up' }))
    await user.click(screen.getByRole('button', { name: 'Save playlist' }))

    await waitFor(() => expect(putShowPlaylist).toHaveBeenCalledTimes(1))
    expect(putShowPlaylist).toHaveBeenCalledWith(
      'bg-run',
      expect.objectContaining({
        entries: [
          { id: 'e2', cue: 'track-2' },
          { id: 'e1', cue: 'track-1' },
        ],
      }),
    )
  })
})

describe('ShowPlaylistDetail (new playlist authoring)', () => {
  it('refuses to submit with no runner chosen', async () => {
    mockWorkspaceLists()
    const user = userEvent.setup()
    renderNew()

    await user.type(screen.getByLabelText('Playlist id'), 'main-run')
    await user.type(screen.getByLabelText('Name'), 'Main run')
    await user.click(screen.getByRole('button', { name: 'Create playlist' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/Runner is required/)
    expect(putShowPlaylist).not.toHaveBeenCalled()
  })

  it('submits a valid showmesh-audio-run playlist', async () => {
    mockWorkspaceLists()
    putShowPlaylist.mockResolvedValue({
      serverTime: '2026-08-25T00:00:00Z',
      kind: 'show.playlist',
      id: 'bg-run',
      revision: 1,
      payload: { show: 'halloween-2026', name: 'Main run', runner: 'showmesh-audio', entries: [{ id: 'e1', cue: 'opening-number' }] },
      updatedAt: '2026-08-25T00:00:00Z',
      createdByPrincipalId: 'p-1',
      createdByPrincipalName: 'admin-1',
      source: 'api',
    })
    const user = userEvent.setup()
    renderNew()

    await user.type(screen.getByLabelText('Playlist id'), 'bg-run')
    await user.type(screen.getByLabelText('Name'), 'Main run')
    await user.selectOptions(screen.getByLabelText('Runner'), 'showmesh-audio')
    await user.type(screen.getByLabelText('Entry 1 id'), 'e1')
    await user.type(screen.getByLabelText('Entry 1 cue'), 'opening-number')
    await user.click(screen.getByRole('button', { name: 'Create playlist' }))

    await waitFor(() => expect(putShowPlaylist).toHaveBeenCalledTimes(1))
    expect(putShowPlaylist).toHaveBeenCalledWith(
      'bg-run',
      expect.objectContaining({ show: 'halloween-2026', name: 'Main run', runner: 'showmesh-audio', entries: [{ id: 'e1', cue: 'opening-number' }] }),
    )
  })
})

describe('ShowPlaylistDetail (scope gating)', () => {
  it('is unavailable, with a stated reason, without the config:write scope for a new playlist', () => {
    mockWorkspaceLists()
    renderNew(makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) }))

    expect(screen.getByRole('status')).toHaveTextContent(/config:write/)
    expect(screen.queryByLabelText('Playlist id')).not.toBeInTheDocument()
  })

  it('renders view-only, with editing disabled, for a reader without config:write on an existing playlist', async () => {
    getShowPlaylist.mockResolvedValue(storedFppPlaylist)
    getShowPlaylistRevisions.mockResolvedValue(emptyRevisions)
    mockWorkspaceLists()
    renderExisting('main-run', makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) }))

    await waitFor(() => expect(screen.getByDisplayValue('Main run')).toBeVisible())
    expect(screen.getByText(/Viewing only/)).toBeVisible()
    expect(screen.queryByRole('button', { name: 'Save playlist' })).not.toBeInTheDocument()
  })
})
