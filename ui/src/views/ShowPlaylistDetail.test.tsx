import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ShowPlaylistDetail } from './ShowPlaylistDetail'
import { ModelContext } from '../app/ModelContext'
import { makeModel, makeShowList } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import { ApiError } from '../api/errors'
import type { Model } from '../app/types'

const { getShowPlaylist, getShowPlaylistRevisions, putShowPlaylist, listConfigObjects } = vi.hoisted(() => ({
  getShowPlaylist: vi.fn(),
  getShowPlaylistRevisions: vi.fn(),
  putShowPlaylist: vi.fn(),
  listConfigObjects: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getShowPlaylist, getShowPlaylistRevisions, putShowPlaylist, listConfigObjects }
})

afterEach(() => {
  cleanup()
  getShowPlaylist.mockReset()
  getShowPlaylistRevisions.mockReset()
  putShowPlaylist.mockReset()
  listConfigObjects.mockReset()
})

const adminSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['config:write'],
})

function renderNew(model: Model = makeModel({ session: adminSession })) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={['/config/show.playlist/new']}>
        <ShowPlaylistDetail isNew />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

function renderExisting(id: string, model: Model = makeModel({ session: adminSession })) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={[`/config/show.playlist/${id}`]}>
        <Routes>
          <Route path="/config/show.playlist/:id" element={<ShowPlaylistDetail />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

const storedPlaylist = {
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
      { id: 'e2', cue: 'closing-number' },
    ],
  },
  updatedAt: '2026-08-25T00:00:00Z',
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'admin-1',
  source: 'api' as const,
}

const emptyRevisions = { serverTime: '2026-08-25T00:00:00Z', revisions: [] }
const emptyCueList = { serverTime: '2026-08-25T00:00:00Z', kind: 'show.cue' as const, objects: [] }

/**
 * Discriminates by `kind` (api/store.ts's own listConfigObjects
 * signature): the pre-existing cue-label lookup (`show.cue`) stays
 * empty, matching every test below that never cared about it, while the
 * show-select's own `GET /config/show` now resolves to a small, non-empty
 * list containing the one show these fixtures already use throughout.
 */
function mockShowAndCueLists(): void {
  listConfigObjects.mockImplementation((kind: string) =>
    kind === 'show' ? Promise.resolve(makeShowList(['halloween-2026'])) : Promise.resolve(emptyCueList),
  )
}

describe('ShowPlaylistDetail (viewing an existing playlist)', () => {
  it('renders the current payload including its ordered entries in order', async () => {
    getShowPlaylist.mockResolvedValue(storedPlaylist)
    getShowPlaylistRevisions.mockResolvedValue(emptyRevisions)
    mockShowAndCueLists()
    renderExisting('main-run')

    await waitFor(() => expect(screen.getByDisplayValue('halloween-2026')).toBeVisible())
    expect(screen.getByDisplayValue('Main run')).toBeVisible()
    expect(screen.getByLabelText('Runner')).toHaveValue('fpp')
    expect(screen.getByDisplayValue('fpp-uuid-1')).toBeVisible()
    expect(screen.getByDisplayValue('Main Playlist')).toBeVisible()

    const table = screen.getByRole('table', { name: 'Entries' })
    const rows = within(table).getAllByRole('row').slice(1) // drop header row
    expect(rows).toHaveLength(2)
    expect(within(rows[0]!).getByLabelText('Entry 1 id')).toHaveValue('e1')
    expect(within(rows[0]!).getByLabelText('Entry 1 cue')).toHaveValue('opening-number')
    expect(within(rows[1]!).getByLabelText('Entry 2 id')).toHaveValue('e2')
    expect(within(rows[1]!).getByLabelText('Entry 2 cue')).toHaveValue('closing-number')
  })

  it('renders the revision history', async () => {
    getShowPlaylist.mockResolvedValue(storedPlaylist)
    getShowPlaylistRevisions.mockResolvedValue({
      serverTime: '2026-08-25T00:00:00Z',
      revisions: [
        { revision: 3, active: true, createdAt: '2026-08-25T00:00:00Z', createdByPrincipalId: 'p-1', createdByPrincipalName: 'admin-1' },
        { revision: 2, active: false, createdAt: '2026-08-20T00:00:00Z', createdByPrincipalId: 'p-1', createdByPrincipalName: 'admin-1' },
      ],
    })
    mockShowAndCueLists()
    renderExisting('main-run')

    await waitFor(() => expect(screen.getByText('Revision history')).toBeVisible())
    const table = screen.getByRole('table', { name: 'Revision history' })
    expect(table).toHaveTextContent('3')
    expect(table).toHaveTextContent('2')
    expect(table).toHaveTextContent('active')
  })

  it('reordering an entry changes what is submitted', async () => {
    getShowPlaylist.mockResolvedValue(storedPlaylist)
    getShowPlaylistRevisions.mockResolvedValue(emptyRevisions)
    mockShowAndCueLists()
    putShowPlaylist.mockResolvedValue({ ...storedPlaylist, revision: 4 })
    const user = userEvent.setup()
    renderExisting('main-run')

    await waitFor(() => expect(screen.getByDisplayValue('halloween-2026')).toBeVisible())
    const table = screen.getByRole('table', { name: 'Entries' })
    const secondRow = within(table).getAllByRole('row')[2]!
    await user.click(within(secondRow).getByRole('button', { name: 'Move up' }))
    await user.click(screen.getByRole('button', { name: 'Save playlist' }))

    await waitFor(() => expect(putShowPlaylist).toHaveBeenCalledTimes(1))
    expect(putShowPlaylist).toHaveBeenCalledWith(
      'main-run',
      expect.objectContaining({
        entries: [
          { id: 'e2', cue: 'closing-number' },
          { id: 'e1', cue: 'opening-number' },
        ],
      }),
    )
  })

  it('renders the coordinator’s refusal reason on a rejected save, without reading as saved', async () => {
    getShowPlaylist.mockResolvedValue(storedPlaylist)
    getShowPlaylistRevisions.mockResolvedValue(emptyRevisions)
    mockShowAndCueLists()
    putShowPlaylist.mockRejectedValue(
      new ApiError(
        'show "another-show" does not match the existing object\'s show "halloween-2026"; show is immutable',
        400,
        'https://showmesh.dev/problems/show-config-cross-show-reference',
      ),
    )
    const user = userEvent.setup()
    renderExisting('main-run')

    await waitFor(() => expect(screen.getByDisplayValue('halloween-2026')).toBeVisible())
    await user.click(screen.getByRole('button', { name: 'Save playlist' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/show is immutable/)
    // A refused write must not read as saved: the active revision banner
    // still names the ORIGINAL revision, and only one read happened (the
    // initial load): a successful save would trigger a second.
    expect(screen.getByText(/Active revision 3/)).toBeVisible()
    expect(getShowPlaylist).toHaveBeenCalledTimes(1)
  })
})

describe('ShowPlaylistDetail (new playlist authoring)', () => {
  it('refuses to submit with no runner chosen', async () => {
    mockShowAndCueLists()
    const user = userEvent.setup()
    renderNew()

    await user.type(screen.getByLabelText('Playlist id'), 'main-run')
    await user.selectOptions(await screen.findByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Name'), 'Main run')
    await user.type(screen.getByLabelText('Entry 1 id'), 'e1')
    await user.type(screen.getByLabelText('Entry 1 cue'), 'opening-number')
    await user.click(screen.getByRole('button', { name: 'Create playlist' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/Runner is required/)
    expect(putShowPlaylist).not.toHaveBeenCalled()
  })

  it('refuses an FPP-run playlist missing its playlist hash, client-side, before dispatch', async () => {
    mockShowAndCueLists()
    const user = userEvent.setup()
    renderNew()

    await user.type(screen.getByLabelText('Playlist id'), 'main-run')
    await user.selectOptions(await screen.findByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Name'), 'Main run')
    await user.selectOptions(screen.getByLabelText('Runner'), 'fpp')
    await user.type(screen.getByLabelText('FPP instance UUID'), 'fpp-uuid-1')
    await user.type(screen.getByLabelText(/FPP playlist name/), 'Main Playlist')
    // Playlist hash deliberately left blank.
    await user.type(screen.getByLabelText('Entry 1 id'), 'e1')
    await user.type(screen.getByLabelText('Entry 1 cue'), 'opening-number')
    await user.click(screen.getByRole('button', { name: 'Create playlist' }))

    expect(await screen.findByText(/FPP playlist hash is required/)).toBeVisible()
    expect(putShowPlaylist).not.toHaveBeenCalled()
  })

  it('submits a valid showmesh-audio-run playlist', async () => {
    mockShowAndCueLists()
    putShowPlaylist.mockResolvedValue({
      ...storedPlaylist,
      revision: 1,
      payload: {
        show: 'halloween-2026',
        name: 'Main run',
        runner: 'showmesh-audio',
        entries: [{ id: 'e1', cue: 'opening-number' }],
      },
    })
    const user = userEvent.setup()
    renderNew()

    await user.type(screen.getByLabelText('Playlist id'), 'main-run')
    await user.selectOptions(await screen.findByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Name'), 'Main run')
    await user.selectOptions(screen.getByLabelText('Runner'), 'showmesh-audio')
    await user.type(screen.getByLabelText('Entry 1 id'), 'e1')
    await user.type(screen.getByLabelText('Entry 1 cue'), 'opening-number')
    await user.click(screen.getByRole('button', { name: 'Create playlist' }))

    await waitFor(() => expect(putShowPlaylist).toHaveBeenCalledTimes(1))
    expect(putShowPlaylist).toHaveBeenCalledWith(
      'main-run',
      expect.objectContaining({
        show: 'halloween-2026',
        name: 'Main run',
        runner: 'showmesh-audio',
        entries: [{ id: 'e1', cue: 'opening-number' }],
      }),
    )
  })
})

describe('ShowPlaylistDetail (scope gating)', () => {
  it('is unavailable, with a stated reason, without the config:write scope for a new playlist', () => {
    renderNew(makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) }))

    expect(screen.getByRole('status')).toHaveTextContent(/config:write/)
    expect(screen.queryByLabelText('Playlist id')).not.toBeInTheDocument()
  })

  it('renders view-only, with editing disabled, for a reader without config:write on an existing playlist', async () => {
    getShowPlaylist.mockResolvedValue(storedPlaylist)
    getShowPlaylistRevisions.mockResolvedValue(emptyRevisions)
    mockShowAndCueLists()
    renderExisting('main-run', makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) }))

    await waitFor(() => expect(screen.getByDisplayValue('halloween-2026')).toBeVisible())
    expect(screen.getByText(/Viewing only/)).toBeVisible()
    expect(screen.queryByRole('button', { name: 'Save playlist' })).not.toBeInTheDocument()
  })
})
