import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ShowCues } from './ShowCues'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

// The Cues workspace tab now reads its show from the real nested route
// (/shows/:showId/cues) and groups the library by REACHABILITY - in a
// playlist, not in any playlist, and directly activatable - rather than
// a flat table, so this needs a playlist read and a per-cue payload read
// alongside the plain object list.
const { getShow, listConfigObjects, listAssets, getShowCue, getShowPlaylist } = vi.hoisted(() => ({
  getShow: vi.fn(),
  listConfigObjects: vi.fn(),
  listAssets: vi.fn(),
  getShowCue: vi.fn(),
  getShowPlaylist: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getShow, listConfigObjects, listAssets, getShowCue, getShowPlaylist }
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

function renderShowCues(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={['/shows/halloween-2026/cues']}>
        <Routes>
          <Route path="/shows/:showId/cues" element={<ShowCues />} />
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

const cueList = {
  serverTime: '2026-08-25T00:00:00Z',
  kind: 'show.cue' as const,
  objects: [
    { id: 'opening-number', label: 'Opening number', show: 'halloween-2026', currentRevision: 1, updatedAt: '2026-08-25T00:00:00Z' },
    { id: 'sponsor-read', label: 'Sponsor read', show: 'halloween-2026', currentRevision: 1, updatedAt: '2026-08-25T00:00:00Z' },
    { id: 'unused-cue', label: 'Unused cue', show: 'halloween-2026', currentRevision: 1, updatedAt: '2026-08-25T00:00:00Z' },
  ],
}

const playlistList = {
  serverTime: '2026-08-25T00:00:00Z',
  kind: 'show.playlist' as const,
  objects: [{ id: 'main-run', label: 'Main Show', show: 'halloween-2026', currentRevision: 1, updatedAt: '2026-08-25T00:00:00Z' }],
}

function emptyList(kind: string) {
  return { serverTime: '2026-08-25T00:00:00Z', kind, objects: [] }
}

function cuePayload(overrides: object) {
  return { serverTime: '2026-08-25T00:00:00Z', kind: 'show.cue', id: 'x', revision: 1, payload: { show: 'halloween-2026', name: 'x', outputs: {}, ...overrides }, updatedAt: '2026-08-25T00:00:00Z', createdByPrincipalId: 'p-1', createdByPrincipalName: 'operator-1', source: 'api' }
}

function mockThreeGroupWorkspace(): void {
  getShow.mockResolvedValue(showResponse)
  listAssets.mockResolvedValue({ serverTime: '2026-08-25T00:00:00Z', assets: [] })
  listConfigObjects.mockImplementation((kind: string) => {
    if (kind === 'show.cue') return Promise.resolve(cueList)
    if (kind === 'show.playlist') return Promise.resolve(playlistList)
    return Promise.resolve(emptyList(kind))
  })
  getShowPlaylist.mockResolvedValue({
    serverTime: '2026-08-25T00:00:00Z',
    kind: 'show.playlist',
    id: 'main-run',
    revision: 1,
    payload: { show: 'halloween-2026', name: 'Main Show', runner: 'fpp', entries: [{ id: 'e1', cue: 'opening-number' }] },
    updatedAt: '2026-08-25T00:00:00Z',
    createdByPrincipalId: 'p-1',
    createdByPrincipalName: 'operator-1',
    source: 'api',
  })
  getShowCue.mockImplementation((id: string) => {
    if (id === 'opening-number') return Promise.resolve(cuePayload({ outputs: { render: { sequence: 'opening' } } }))
    if (id === 'sponsor-read') return Promise.resolve(cuePayload({ outputs: { audio: { asset: 'a', startOffsetMillis: 0 }, announcement: { policy: 'duck', duckGainDb: -18, fadeMillis: 400 } } }))
    return Promise.resolve(cuePayload({ outputs: { audio: { asset: 'a', startOffsetMillis: 0 } } }))
  })
}

describe('ShowCues', () => {
  it('groups cues by reachability: in a playlist, not in any playlist, directly activatable', async () => {
    mockThreeGroupWorkspace()
    renderShowCues(makeModel({ session: operatorSession }))

    await waitFor(() => expect(screen.getByText('Opening number')).toBeVisible())
    const inPlaylist = screen.getByRole('table', { name: 'Cues in a playlist' })
    expect(within(inPlaylist).getByText('Opening number')).toBeVisible()

    const activatable = screen.getByRole('table', { name: 'Directly activatable cues' })
    expect(within(activatable).getByText('Sponsor read')).toBeVisible()
    expect(within(activatable).getByText(/Duck to -18 dB/)).toBeVisible()

    const unreachable = screen.getByRole('table', { name: 'Cues not in any playlist' })
    expect(within(unreachable).getByText('Unused cue')).toBeVisible()
  })

  it('warns that editing a shared cue changes every playlist that uses it', async () => {
    mockThreeGroupWorkspace()
    renderShowCues(makeModel({ session: operatorSession }))

    await waitFor(() => expect(screen.getByText(/Editing a cue changes every playlist/)).toBeVisible())
  })

  it('renders the missing-scope reason and does not fetch for a principal with neither read scope', () => {
    renderShowCues(makeModel({ session: makeAuthenticatedSession({ scopes: [] }) }))

    expect(screen.getByRole('status')).toBeVisible()
    expect(getShow).not.toHaveBeenCalled()
  })
})
