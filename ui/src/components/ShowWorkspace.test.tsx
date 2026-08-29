import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ShowWorkspaceFrame, ShowWorkspaceOverview, ShowWorkspaceTabs, useShowWorkspaceData } from './ShowWorkspace'
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

function TestFrame({ showId }: { showId: string }) {
  const data = useShowWorkspaceData(showId)
  return (
    <ShowWorkspaceFrame showId={showId} active="playlists" data={data}>
      <p>panel content</p>
    </ShowWorkspaceFrame>
  )
}

describe('showWorkspacePaths', () => {
  it('builds the real nested route for a tab', () => {
    expect(showWorkspacePath('halloween 2026', 'presentation')).toBe('/shows/halloween%202026/presentation')
  })

  it('falls back to the show detail page when no tab is given, for callers outside this workspace', () => {
    expect(showWorkspacePath('halloween-2026')).toBe('/shows/halloween-2026')
  })
})

describe('ShowWorkspaceTabs', () => {
  it('renders all five tabs and marks the active one current', () => {
    render(
      <MemoryRouter>
        <ShowWorkspaceTabs showId="halloween-2026" active="cues" counts={{ playlists: 2, cues: 14, assets: 22, presentation: 3, automation: 2 }} />
      </MemoryRouter>,
    )
    expect(screen.getAllByRole('link')).toHaveLength(5)
    expect(screen.getByRole('link', { name: /Cues/ })).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('link', { name: /Automation/ })).not.toHaveAttribute('aria-current')
    expect(screen.getByRole('link', { name: /Cues/ })).toHaveTextContent('14')
    expect(screen.getByRole('link', { name: /Cues/ })).toHaveAttribute('href', '/shows/halloween-2026/cues')
  })
})

describe('ShowWorkspaceFrame (via useShowWorkspaceData)', () => {
  it('fetches the show and every tab count once, and keeps the tab strip mounted with the panel', async () => {
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
          <TestFrame showId="halloween-2026" />
        </MemoryRouter>
      </ModelContext.Provider>,
    )

    await waitFor(() => expect(screen.getByRole('heading', { name: 'Halloween 2026', level: 1 })).toBeVisible())
    expect(screen.getByText('panel content')).toBeVisible()
    expect(screen.getByRole('link', { name: /Cues/ })).toHaveTextContent('1')
    expect(screen.getByRole('link', { name: /^Playlists/ })).toHaveAttribute('aria-current', 'page')
  })

  it('states the permission reason without fetching, and never claims a tab count it never read', () => {
    const model = makeModel({ session: makeAuthenticatedSession({ scopes: [] }), sessionFetchFailed: true })
    render(
      <ModelContext.Provider value={model}>
        <MemoryRouter>
          <TestFrame showId="halloween-2026" />
        </MemoryRouter>
      </ModelContext.Provider>,
    )
    expect(screen.getByRole('status')).toBeVisible()
    expect(getShow).not.toHaveBeenCalled()
    expect(screen.queryByText('panel content')).not.toBeInTheDocument()
  })
})

describe('ShowWorkspaceOverview (compatibility shim)', () => {
  it('redirects the pre-overhaul /workspace address to the show detail page', () => {
    render(
      <ModelContext.Provider value={makeModel({ session: operatorSession })}>
        <MemoryRouter initialEntries={['/config/show/halloween-2026/workspace']}>
          <Routes>
            <Route path="/config/show/:id/workspace" element={<ShowWorkspaceOverview showId="halloween-2026" />} />
            <Route path="/shows/:showId" element={<p>show detail page</p>} />
          </Routes>
        </MemoryRouter>
      </ModelContext.Provider>,
    )
    expect(screen.getByText('show detail page')).toBeVisible()
  })
})
