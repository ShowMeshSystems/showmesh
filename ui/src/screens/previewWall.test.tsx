import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, type Model, type PreviewTile, type PreviewsResponse, type SessionResponse } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  getPreviews: vi.fn(),
  getDashboardLayout: vi.fn(),
  putDashboardLayout: vi.fn(),
  startFeed: vi.fn(),
  stopFeed: vi.fn(),
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, getPreviews: stubs.getPreviews, getDashboardLayout: stubs.getDashboardLayout, putDashboardLayout: stubs.putDashboardLayout }
})

vi.mock('./previewFeed', () => ({ startFeed: stubs.startFeed }))

const { PreviewWall } = await import('./PreviewWall')

const tile = (key: string, label: string, over: Partial<PreviewTile> = {}): PreviewTile => ({
  key,
  kind: 'camera',
  label,
  state: 'live',
  message: '',
  hidden: false,
  observedAt: '2026-10-15T12:00:00Z',
  ...over,
})

const response = (tiles: PreviewTile[], gateway: Partial<PreviewsResponse['gateway']> = {}): PreviewsResponse => ({
  serverTime: '2026-10-15T12:00:00Z',
  gateway: { state: 'reachable', message: '', observedAt: '2026-10-15T12:00:00Z', ...gateway },
  tiles,
})

function signedIn(scopes: string[]): SessionResponse {
  return {
    serverTime: '2026-10-15T12:00:00Z',
    authenticated: true,
    principal: { id: 'p1', name: 'op', kind: 'human', role: 'admin' },
    session: { id: 's1', deviceLabel: 'tablet', createdAt: '2026-10-15T12:00:00Z' },
    credentialForm: 'session',
    scopes,
    scopesState: 'current',
    bootstrapRequired: false,
  } as unknown as SessionResponse
}

function renderWall(scopes = ['dashboard:write'], model: Partial<Model> = {}) {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), session: signedIn(scopes), ...model }}>
      <PreviewWall />
    </ModelContext.Provider>,
  )
}

const MSG_UNREACHABLE = 'The preview gateway cannot be reached. Check that it is running and that its address and token are right.'
const MSG_NDI_GONE = 'This source is no longer announced on the network. Check that its sender is running.'

const layoutResponse = (revision: number, order: string[] = [], hidden: string[] = [], by: string | null = null) => ({
  serverTime: '2026-10-15T12:00:00Z',
  kind: 'dashboard.layout' as const,
  revision,
  payload: { previews: { order, hidden } },
  updatedAt: '2026-10-15T12:00:00Z',
  createdByPrincipalId: null,
  createdByPrincipalName: by,
  source: 'api' as const,
})

const labels = () => screen.getAllByRole('heading', { level: 3 }).map((h) => h.textContent)

beforeEach(() => {
  stubs.startFeed.mockReturnValue(stubs.stopFeed)
  stubs.getDashboardLayout.mockResolvedValue(layoutResponse(3))
  stubs.putDashboardLayout.mockResolvedValue(layoutResponse(4))
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.clearAllMocks()
})

describe('PreviewWall', () => {
  it('renders nothing while no gateway is configured', async () => {
    stubs.getPreviews.mockResolvedValue(response([], { state: 'not_configured' }))
    const { container } = renderWall()
    await waitFor(() => expect(stubs.getPreviews).toHaveBeenCalled())
    await act(async () => {})
    expect(container).toBeEmptyDOMElement()
  })

  it('stays hidden before the first answer', async () => {
    stubs.getPreviews.mockReturnValue(new Promise(() => {}))
    const { container } = renderWall()
    expect(container).toBeEmptyDOMElement()
  })

  it('says the read failed when the first read fails', async () => {
    stubs.getPreviews.mockRejectedValue(new ApiError('The coordinator did not answer.'))
    renderWall()
    expect(await screen.findByText('Previews unread')).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 2, name: 'Live previews' })).toBeInTheDocument()
  })

  it('names the missing permission when the read is refused', async () => {
    stubs.getPreviews.mockRejectedValue(new ApiError('Forbidden', 403))
    renderWall()
    expect(await screen.findByText('Previews not shown')).toBeInTheDocument()
    expect(screen.getByText(/"observation:read"/)).toBeInTheDocument()
  })

  it('shows each tile with its label, state word and message, and connects only the visible ones', async () => {
    stubs.getPreviews.mockResolvedValue(
      response([
        tile('ndi:a', 'Driveway', { kind: 'ndi', state: 'not_found', message: MSG_NDI_GONE }),
        tile('camera:b', 'Porch', { hidden: true }),
      ]),
    )
    renderWall()
    await screen.findByRole('heading', { level: 3, name: 'Driveway' })
    expect(screen.getByText('Not found')).toBeInTheDocument()
    expect(screen.getByText(MSG_NDI_GONE)).toBeInTheDocument()
    expect(screen.queryByRole('heading', { level: 3, name: 'Porch' })).toBeNull()
    expect(stubs.startFeed).not.toHaveBeenCalled()
  })

  it('moves a tile later and saves the new order with the hidden set', async () => {
    stubs.getPreviews.mockResolvedValue(response([tile('camera:a', 'A'), tile('camera:b', 'B'), tile('camera:c', 'C', { hidden: true })]))
    renderWall()
    await screen.findByRole('heading', { level: 3, name: 'A' })
    fireEvent.click(screen.getByRole('button', { name: 'Move A later' }))
    expect(labels()).toEqual(['B', 'A'])
    await waitFor(() =>
      expect(stubs.putDashboardLayout).toHaveBeenCalledWith({ previews: { order: ['camera:b', 'camera:a', 'camera:c'], hidden: ['camera:c'] } }, 3),
    )
  })

  it('disables moving past either end, with the reason', async () => {
    stubs.getPreviews.mockResolvedValue(response([tile('camera:a', 'A'), tile('camera:b', 'B')]))
    renderWall()
    await screen.findByRole('heading', { level: 3, name: 'A' })
    expect(screen.getByRole('button', { name: 'Move A earlier' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Move A earlier' })).toHaveAttribute('title', 'Already first.')
    expect(screen.getByRole('button', { name: 'Move B later' })).toBeDisabled()
  })

  it('reorders by dragging one tile onto another', async () => {
    stubs.getPreviews.mockResolvedValue(response([tile('camera:a', 'A'), tile('camera:b', 'B'), tile('camera:c', 'C')]))
    renderWall()
    await screen.findByRole('heading', { level: 3, name: 'A' })
    const dataTransfer = { setData: vi.fn(), effectAllowed: '' }
    const source = screen.getByRole('article', { name: 'C' })
    const target = screen.getByRole('article', { name: 'A' })
    fireEvent.dragStart(source, { dataTransfer })
    fireEvent.dragOver(target, { dataTransfer })
    fireEvent.drop(target, { dataTransfer })
    await waitFor(() =>
      expect(stubs.putDashboardLayout).toHaveBeenCalledWith({ previews: { order: ['camera:c', 'camera:a', 'camera:b'], hidden: [] } }, 3),
    )
  })

  it('hides a tile, then shows it again from the hidden list', async () => {
    let tiles = [tile('camera:a', 'A'), tile('camera:b', 'B')]
    stubs.getPreviews.mockImplementation(async () => response(tiles))
    let revision = 3
    stubs.getDashboardLayout.mockImplementation(async () => layoutResponse(revision))
    stubs.putDashboardLayout.mockImplementation(async ({ previews }: { previews: { hidden: string[] } }) => {
      tiles = tiles.map((t) => ({ ...t, hidden: previews.hidden.includes(t.key) }))
      return layoutResponse(++revision)
    })
    renderWall()
    await screen.findByRole('heading', { level: 3, name: 'A' })
    fireEvent.click(screen.getByRole('button', { name: 'Hide A' }))
    await waitFor(() => expect(screen.queryByRole('heading', { level: 3, name: 'A' })).toBeNull())
    expect(stubs.putDashboardLayout).toHaveBeenLastCalledWith({ previews: { order: ['camera:a', 'camera:b'], hidden: ['camera:a'] } }, 3)
    fireEvent.click(screen.getByRole('button', { name: 'Hidden tiles (1)' }))
    fireEvent.click(screen.getByRole('button', { name: 'Show A' }))
    await screen.findByRole('heading', { level: 3, name: 'A' })
    expect(stubs.putDashboardLayout).toHaveBeenLastCalledWith({ previews: { order: ['camera:a', 'camera:b'], hidden: [] } }, 4)
  })

  it('puts the tiles back and says so when the layout save fails', async () => {
    stubs.getPreviews.mockResolvedValue(response([tile('camera:a', 'A'), tile('camera:b', 'B')]))
    stubs.putDashboardLayout.mockRejectedValue(new ApiError('nope'))
    renderWall()
    await screen.findByRole('heading', { level: 3, name: 'A' })
    fireEvent.click(screen.getByRole('button', { name: 'Move A later' }))
    await screen.findByText('Layout not saved')
    expect(labels()).toEqual(['A', 'B'])
  })

  it('applies only the latest refresh when an older one answers last', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const resolvers: Array<(value: PreviewsResponse) => void> = []
    stubs.getPreviews.mockImplementation(() => new Promise<PreviewsResponse>((resolve) => resolvers.push(resolve)))
    renderWall()
    await act(async () => {})
    await act(async () => {
      vi.advanceTimersByTime(5000)
    })
    expect(resolvers).toHaveLength(2)
    await act(async () => resolvers[1]!(response([tile('camera:b', 'B'), tile('camera:a', 'A')])))
    expect(labels()).toEqual(['B', 'A'])
    await act(async () => resolvers[0]!(response([tile('camera:a', 'A'), tile('camera:b', 'B')])))
    expect(labels()).toEqual(['B', 'A'])
    vi.useRealTimers()
  })

  it('sends layout saves one at a time and only the latest queued order', async () => {
    stubs.getPreviews.mockResolvedValue(response([tile('camera:a', 'A'), tile('camera:b', 'B'), tile('camera:c', 'C')]))
    const releases: Array<() => void> = []
    stubs.putDashboardLayout.mockImplementation(() => new Promise<object>((resolve) => releases.push(() => resolve(layoutResponse(3)))))
    renderWall()
    await screen.findByRole('heading', { level: 3, name: 'A' })
    fireEvent.click(screen.getByRole('button', { name: 'Move A later' }))
    fireEvent.click(screen.getByRole('button', { name: 'Move C earlier' }))
    fireEvent.click(screen.getByRole('button', { name: 'Hide B' }))
    await waitFor(() => expect(stubs.putDashboardLayout).toHaveBeenCalledTimes(1))
    await act(async () => releases[0]!())
    await waitFor(() => expect(stubs.putDashboardLayout).toHaveBeenCalledTimes(2))
    expect(stubs.putDashboardLayout).toHaveBeenLastCalledWith({ previews: { order: ['camera:b', 'camera:c', 'camera:a'], hidden: ['camera:b'] } }, 3)
    await act(async () => releases[1]!())
    expect(stubs.putDashboardLayout).toHaveBeenCalledTimes(2)
  })

  it('shows no error when a failed save is followed by a queued save that succeeds', async () => {
    stubs.getPreviews.mockResolvedValue(response([tile('camera:a', 'A'), tile('camera:b', 'B')]))
    const settle: Array<{ ok: () => void; fail: () => void }> = []
    stubs.putDashboardLayout.mockImplementation(
      () => new Promise<object>((resolve, reject) => settle.push({ ok: () => resolve(layoutResponse(3)), fail: () => reject(new ApiError('nope')) })),
    )
    renderWall()
    await screen.findByRole('heading', { level: 3, name: 'A' })
    fireEvent.click(screen.getByRole('button', { name: 'Move A later' }))
    fireEvent.click(screen.getByRole('button', { name: 'Hide A' }))
    await waitFor(() => expect(settle).toHaveLength(1))
    await act(async () => settle[0]!.fail())
    expect(screen.queryByText('Layout not saved')).toBeNull()
    await waitFor(() => expect(settle).toHaveLength(2))
    await act(async () => settle[1]!.ok())
    expect(screen.queryByText('Layout not saved')).toBeNull()
  })

  it('disables the layout controls with a reason when the principal cannot write the dashboard', async () => {
    stubs.getPreviews.mockResolvedValue(response([tile('camera:a', 'A'), tile('camera:b', 'B')]))
    renderWall([])
    await screen.findByRole('heading', { level: 3, name: 'A' })
    const later = screen.getByRole('button', { name: 'Move A later' })
    expect(later).toBeDisabled()
    expect(later.getAttribute('title')).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Hide A' })).toBeDisabled()
    expect(screen.getByRole('article', { name: 'A' })).not.toHaveAttribute('draggable', 'true')
  })

  it('disables the layout controls for a principal holding only config:write', async () => {
    stubs.getPreviews.mockResolvedValue(response([tile('camera:a', 'A')]))
    renderWall(['config:write'])
    await screen.findByRole('heading', { level: 3, name: 'A' })
    expect(screen.getByRole('button', { name: 'Hide A' })).toBeDisabled()
  })

  it('says once that the gateway is unreachable and keeps only the state word on each tile', async () => {
    stubs.getPreviews.mockResolvedValue(
      response(
        [tile('camera:a', 'A', { state: 'error', message: MSG_UNREACHABLE }), tile('camera:b', 'B', { state: 'error', message: MSG_UNREACHABLE })],
        { state: 'unreachable', message: MSG_UNREACHABLE },
      ),
    )
    renderWall()
    await screen.findByText('Gateway unreachable')
    expect(screen.getAllByText(MSG_UNREACHABLE)).toHaveLength(1)
    expect(screen.getAllByText('Error')).toHaveLength(2)
    expect(screen.getByRole('heading', { level: 3, name: 'A' })).toBeInTheDocument()
  })

  it('shows a connecting gateway as a neutral state with no sentence on the tiles', async () => {
    const message = 'Connecting to the preview gateway. Previews appear when it answers.'
    stubs.getPreviews.mockResolvedValue(
      response([tile('camera:a', 'A', { state: 'connecting', message })], { state: 'connecting' as PreviewsResponse['gateway']['state'], message }),
    )
    renderWall()
    await screen.findByRole('heading', { level: 3, name: 'A' })
    expect(screen.getAllByText('Connecting')).toHaveLength(2)
    expect(screen.getAllByText(message)).toHaveLength(1)
    expect(screen.queryByText('Gateway unreachable')).toBeNull()
  })

  it('sends the saved version with a layout save, and the first save asks that none exists yet', async () => {
    stubs.getPreviews.mockResolvedValue(response([tile('camera:a', 'A'), tile('camera:b', 'B')]))
    stubs.getDashboardLayout.mockRejectedValue(new ApiError('No dashboard layout has been saved yet.', 404))
    renderWall()
    await screen.findByRole('heading', { level: 3, name: 'A' })
    fireEvent.click(screen.getByRole('button', { name: 'Move A later' }))
    await waitFor(() => expect(stubs.putDashboardLayout).toHaveBeenCalledWith({ previews: { order: ['camera:b', 'camera:a'], hidden: [] } }, 'none'))
  })

  it('refuses a layout save when someone else saved first, and writes nothing', async () => {
    stubs.getPreviews.mockResolvedValue(response([tile('camera:a', 'A'), tile('camera:b', 'B')]))
    renderWall()
    await screen.findByRole('heading', { level: 3, name: 'A' })
    stubs.getDashboardLayout.mockResolvedValue(layoutResponse(7, ['camera:b', 'camera:a'], [], 'stage manager'))
    fireEvent.click(screen.getByRole('button', { name: 'Move A later' }))
    expect(await screen.findByText('Stale write')).toBeInTheDocument()
    expect(screen.getByText(/stage manager saved a newer version/)).toBeInTheDocument()
    expect(stubs.putDashboardLayout).not.toHaveBeenCalled()
    expect(labels()).toEqual(['A', 'B'])
  })

  it('shows the stale-write refusal when the coordinator refuses the save as out of date', async () => {
    stubs.getPreviews.mockResolvedValue(response([tile('camera:a', 'A'), tile('camera:b', 'B')]))
    stubs.putDashboardLayout.mockImplementation(async () => {
      stubs.getDashboardLayout.mockResolvedValue(layoutResponse(8, ['camera:b', 'camera:a']))
      throw new ApiError('A newer version was saved first. Read the current settings again and retry.', 409)
    })
    renderWall()
    await screen.findByRole('heading', { level: 3, name: 'A' })
    fireEvent.click(screen.getByRole('button', { name: 'Move A later' }))
    expect(await screen.findByText('Stale write')).toBeInTheDocument()
    expect(screen.queryByText('Layout not saved')).toBeNull()
  })

  it('does not connect a disabled camera', async () => {
    stubs.getPreviews.mockResolvedValue(response([tile('camera:a', 'A', { state: 'disabled' })]))
    renderWall()
    await screen.findByRole('heading', { level: 3, name: 'A' })
    expect(stubs.startFeed).not.toHaveBeenCalled()
  })

  it('closes a tile\'s connection when it leaves the screen and reconnects when it returns', async () => {
    let notify: (entries: Array<{ isIntersecting: boolean }>) => void = () => {}
    vi.stubGlobal(
      'IntersectionObserver',
      class {
        constructor(callback: typeof notify) {
          notify = callback
        }
        observe() {}
        disconnect() {}
      },
    )
    stubs.getPreviews.mockResolvedValue(response([tile('camera:a', 'A')]))
    renderWall()
    await screen.findByRole('heading', { level: 3, name: 'A' })
    expect(stubs.startFeed).not.toHaveBeenCalled()
    act(() => notify([{ isIntersecting: true }]))
    expect(stubs.startFeed).toHaveBeenCalledTimes(1)
    act(() => notify([{ isIntersecting: false }]))
    expect(stubs.stopFeed).toHaveBeenCalledTimes(1)
    act(() => notify([{ isIntersecting: true }]))
    expect(stubs.startFeed).toHaveBeenCalledTimes(2)
  })

  it('closes every connection when the section unmounts', async () => {
    stubs.getPreviews.mockResolvedValue(response([tile('camera:a', 'A')]))
    const { unmount } = renderWall()
    await screen.findByRole('heading', { level: 3, name: 'A' })
    unmount()
    expect(stubs.stopFeed).toHaveBeenCalledTimes(1)
  })

  it('shows how old the picture is and marks it Stale after 10 seconds without a new frame', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
    stubs.getPreviews.mockResolvedValue(response([tile('camera:a', 'A')]))
    renderWall()
    await act(async () => {})
    await act(async () => {})
    const handlers = stubs.startFeed.mock.calls[0]![1] as { onSnapshot: (url: string) => void; onFrame: (at: number) => void }
    act(() => {
      handlers.onSnapshot('blob:frame-1')
      handlers.onFrame(Date.now())
    })
    expect(screen.getByRole('img', { name: 'A latest frame' })).toBeInTheDocument()
    expect(screen.getByText('Frame 0 s ago')).toBeInTheDocument()
    expect(screen.getByText('Live')).toBeInTheDocument()
    await act(async () => {
      vi.advanceTimersByTime(10_000)
    })
    expect(screen.getByText('Frame 10 s ago')).toBeInTheDocument()
    expect(screen.getByText('Stale')).toBeInTheDocument()
    expect(screen.queryByText('Live')).toBeNull()
    act(() => handlers.onFrame(Date.now()))
    expect(screen.getByText('Live')).toBeInTheDocument()
    vi.useRealTimers()
  })

  it('drops the picture and closes the feed when the source is no longer found', async () => {
    let tiles = [tile('ndi:a', 'A', { kind: 'ndi' })]
    stubs.getPreviews.mockImplementation(async () => response(tiles))
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    renderWall()
    await act(async () => {})
    await act(async () => {})
    const handlers = stubs.startFeed.mock.calls[0]![1] as { onSnapshot: (url: string) => void }
    act(() => handlers.onSnapshot('blob:frame-1'))
    expect(screen.getByRole('img', { name: 'A latest frame' })).toBeInTheDocument()
    tiles = [tile('ndi:a', 'A', { kind: 'ndi', state: 'not_found', message: MSG_NDI_GONE })]
    await act(async () => {
      vi.advanceTimersByTime(5000)
    })
    await act(async () => {})
    expect(screen.queryByRole('img', { name: 'A latest frame' })).toBeNull()
    expect(screen.getByText('No picture')).toBeInTheDocument()
    expect(stubs.stopFeed).toHaveBeenCalledTimes(1)
    vi.useRealTimers()
  })
})
