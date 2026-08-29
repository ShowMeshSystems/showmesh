import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NightSessions } from './NightSessions'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import { ApiError } from '../api/errors'
import type { Model } from '../app/types'

// The Night session workspace tab now reads its show from the real
// nested route param (/shows/:showId/night-sessions), not a bare
// /config/night.session list, and renders the active-pointer panel
// (NightSessionActive.tsx's NightSessionActivePanel) inline above the
// definitions table.
const {
  getShow,
  listConfigObjects,
  listAssets,
  getNightSessionConfig,
  getNightSessionActiveConfig,
  getNightSessionActiveConfigRevisions,
} = vi.hoisted(() => ({
  getShow: vi.fn(),
  listConfigObjects: vi.fn(),
  listAssets: vi.fn(),
  getNightSessionConfig: vi.fn(),
  getNightSessionActiveConfig: vi.fn(),
  getNightSessionActiveConfigRevisions: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return {
    ...actual,
    getShow,
    listConfigObjects,
    listAssets,
    getNightSessionConfig,
    getNightSessionActiveConfig,
    getNightSessionActiveConfigRevisions,
  }
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

const showResponse = {
  serverTime: '2026-08-27T00:00:00Z',
  kind: 'show' as const,
  id: 'winter-ridge-2026',
  revision: 47,
  payload: { name: 'Winter Ridge 2026', notes: '' },
  updatedAt: '2026-08-27T00:00:00Z',
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'erbartos',
  source: 'api' as const,
}

function renderView(model: Model, showId = 'winter-ridge-2026') {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={[`/shows/${showId}/night-sessions`]}>
        <Routes>
          <Route path="/shows/:showId/night-sessions" element={<NightSessions />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

function stubWorkspaceReads(nightSessions: Array<{ id: string; label: string; currentRevision: number; updatedAt: string }>) {
  getShow.mockResolvedValue(showResponse)
  listAssets.mockResolvedValue({ serverTime: '2026-08-27T00:00:00Z', assets: [] })
  listConfigObjects.mockImplementation(async (kind: string) => ({
    serverTime: '2026-08-27T00:00:00Z',
    kind,
    objects: kind === 'night.session' ? nightSessions.map((s) => ({ ...s, show: 'winter-ridge-2026' })) : [],
  }))
  getNightSessionActiveConfig.mockRejectedValue(
    new ApiError('nothing has ever been activated', 404, 'https://showmesh.dev/problems/resource-not-found'),
  )
  getNightSessionActiveConfigRevisions.mockResolvedValue({ serverTime: '2026-08-27T00:00:00Z', kind: 'night.session.active', revisions: [] })
}

describe('NightSessions', () => {
  it('renders the list, requesting the night.session kind scoped to this show', async () => {
    stubWorkspaceReads([{ id: 'winter-standard', label: 'Standard Night', currentRevision: 12, updatedAt: '2026-08-27T00:00:00Z' }])
    getNightSessionConfig.mockResolvedValue({
      serverTime: '2026-08-27T00:00:00Z',
      kind: 'night.session',
      id: 'winter-standard',
      revision: 12,
      payload: {
        show: 'winter-ridge-2026',
        label: 'Standard Night',
        showPlaylist: { fppInstanceId: 'barn-player', playlist: 'WR26 Main Show' },
        resting: {
          fppInstanceId: 'barn-player',
          playlist: 'WR26 Resting Loop',
          endOfNightPlaylist: 'WR26 Resting Loop',
          endOfNightRepeat: false,
          timelineAsset: { show: 'winter-ridge-2026', sequence: 'resting-loop', target: 'media-front' },
        },
        enterShow: { cues: [], blackoutHoldMs: 0 },
        enterResting: { cues: [], blackoutAfterShowMs: 0 },
      },
      updatedAt: '2026-08-27T00:00:00Z',
      createdByPrincipalId: 'p-1',
      createdByPrincipalName: 'erbartos',
      source: 'api',
    })
    renderView(makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) }))
    await waitFor(() => expect(screen.getByText('Standard Night')).toBeVisible())
    expect(listConfigObjects).toHaveBeenCalledWith('night.session', 'winter-ridge-2026')
    await waitFor(() => expect(screen.getByText(/barn-player · WR26 Main Show/)).toBeVisible())
  })

  it('renders "No night-session definitions" for an empty list', async () => {
    stubWorkspaceReads([])
    renderView(makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) }))
    await waitFor(() => expect(screen.getByText(/no night-session definitions are authored/i)).toBeVisible())
  })

  it('renders a read-scope refusal without ever calling listConfigObjects for night.session', () => {
    renderView(makeModel({ session: makeAuthenticatedSession({ scopes: ['node:read'] }) }))
    expect(screen.getAllByText(/does not include/).length).toBeGreaterThan(0)
    expect(listConfigObjects).not.toHaveBeenCalled()
  })

  it('renders "New definition" as a real button when config:write is held', async () => {
    stubWorkspaceReads([])
    renderView(makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run', 'config:write'] }) }))
    const button = await screen.findByRole('button', { name: 'New definition' })
    expect(button).not.toBeDisabled()
  })

  it('renders "New definition" disabled with a stated reason when config:write is not held', async () => {
    stubWorkspaceReads([])
    renderView(makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) }))
    const button = await screen.findByRole('button', { name: 'New definition' })
    expect(button).toBeDisabled()
    expect(button).toBeVisible()
  })

  it('shows the cleared pointer as a real revision, not an error, when nothing has ever been activated', async () => {
    stubWorkspaceReads([])
    renderView(makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) }))
    await waitFor(() => expect(screen.getByText(/No night-session definition has ever been activated/)).toBeVisible())
  })

  it('marks the active definition\'s row and shows it in the active-pointer section', async () => {
    stubWorkspaceReads([{ id: 'winter-standard', label: 'Standard Night', currentRevision: 12, updatedAt: '2026-08-27T00:00:00Z' }])
    getNightSessionConfig.mockResolvedValue({
      serverTime: '2026-08-27T00:00:00Z',
      kind: 'night.session',
      id: 'winter-standard',
      revision: 12,
      payload: {
        show: 'winter-ridge-2026',
        label: 'Standard Night',
        showPlaylist: { fppInstanceId: 'barn-player', playlist: 'WR26 Main Show' },
        resting: {
          fppInstanceId: 'barn-player',
          playlist: 'WR26 Resting Loop',
          endOfNightPlaylist: 'WR26 Resting Loop',
          endOfNightRepeat: false,
          timelineAsset: { show: 'winter-ridge-2026', sequence: 'resting-loop', target: 'media-front' },
        },
        enterShow: { cues: [], blackoutHoldMs: 0 },
        enterResting: { cues: [], blackoutAfterShowMs: 0 },
      },
      updatedAt: '2026-08-27T00:00:00Z',
      createdByPrincipalId: 'p-1',
      createdByPrincipalName: 'erbartos',
      source: 'api',
    })
    getNightSessionActiveConfig.mockReset()
    getNightSessionActiveConfig.mockResolvedValue({
      serverTime: '2026-08-27T00:00:00Z',
      kind: 'night.session.active',
      revision: 4,
      payload: { session: 'winter-standard' },
      updatedAt: '2026-08-26T17:40:00Z',
      createdByPrincipalId: 'p-1',
      createdByPrincipalName: 'erbartos',
      source: 'api',
    })
    renderView(makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) }))
    await waitFor(() => expect(screen.getAllByText('Active').length).toBeGreaterThan(0))
    expect(screen.queryByText('Inactive')).not.toBeInTheDocument()
  })
})
