import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, type Model, type PreviewTile, type PreviewsResponse, type SessionResponse } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  getPreviews: vi.fn(),
  putDashboardLayout: vi.fn(),
  startFeed: vi.fn(),
  stopFeed: vi.fn(),
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, getPreviews: stubs.getPreviews, putDashboardLayout: stubs.putDashboardLayout }
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

function renderWall(scopes = ['config:write'], model: Partial<Model> = {}) {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), session: signedIn(scopes), ...model }}>
      <PreviewWall />
    </ModelContext.Provider>,
  )
}

const labels = () => screen.getAllByRole('heading', { level: 3 }).map((h) => h.textContent)

beforeEach(() => {
  stubs.startFeed.mockReturnValue(stubs.stopFeed)
  stubs.putDashboardLayout.mockResolvedValue({})
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

  it('renders nothing when the first read fails', async () => {
    stubs.getPreviews.mockRejectedValue(new ApiError('down'))
    const { container } = renderWall()
    await act(async () => {})
    expect(container).toBeEmptyDOMElement()
  })

  it('shows each tile with its label, state word and message, and connects only the visible ones', async () => {
    stubs.getPreviews.mockResolvedValue(
      response([
        tile('camera:a', 'Driveway', { state: 'connecting', message: 'Waiting for the first frame.' }),
        tile('camera:b', 'Porch', { hidden: true }),
      ]),
    )
    renderWall()
    await screen.findByRole('heading', { level: 3, name: 'Driveway' })
    expect(screen.getByText('Connecting')).toBeInTheDocument()
    expect(screen.getByText('Waiting for the first frame.')).toBeInTheDocument()
    expect(screen.queryByRole('heading', { level: 3, name: 'Porch' })).toBeNull()
    expect(stubs.startFeed).toHaveBeenCalledTimes(1)
    expect(stubs.startFeed.mock.calls[0]![0]).toBe('camera:a')
  })

  it('moves a tile later and saves the new order with the hidden set', async () => {
    stubs.getPreviews.mockResolvedValue(response([tile('camera:a', 'A'), tile('camera:b', 'B'), tile('camera:c', 'C', { hidden: true })]))
    renderWall()
    await screen.findByRole('heading', { level: 3, name: 'A' })
    fireEvent.click(screen.getByRole('button', { name: 'Move A later' }))
    expect(labels()).toEqual(['B', 'A'])
    await waitFor(() =>
      expect(stubs.putDashboardLayout).toHaveBeenCalledWith({ previews: { order: ['camera:b', 'camera:a', 'camera:c'], hidden: ['camera:c'] } }),
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
      expect(stubs.putDashboardLayout).toHaveBeenCalledWith({ previews: { order: ['camera:c', 'camera:a', 'camera:b'], hidden: [] } }),
    )
  })

  it('hides a tile, then shows it again from the hidden list', async () => {
    let tiles = [tile('camera:a', 'A'), tile('camera:b', 'B')]
    stubs.getPreviews.mockImplementation(async () => response(tiles))
    stubs.putDashboardLayout.mockImplementation(async ({ previews }: { previews: { hidden: string[] } }) => {
      tiles = tiles.map((t) => ({ ...t, hidden: previews.hidden.includes(t.key) }))
      return {}
    })
    renderWall()
    await screen.findByRole('heading', { level: 3, name: 'A' })
    fireEvent.click(screen.getByRole('button', { name: 'Hide A' }))
    await waitFor(() => expect(screen.queryByRole('heading', { level: 3, name: 'A' })).toBeNull())
    expect(stubs.putDashboardLayout).toHaveBeenLastCalledWith({ previews: { order: ['camera:a', 'camera:b'], hidden: ['camera:a'] } })
    fireEvent.click(screen.getByRole('button', { name: 'Hidden tiles (1)' }))
    fireEvent.click(screen.getByRole('button', { name: 'Show A' }))
    await screen.findByRole('heading', { level: 3, name: 'A' })
    expect(stubs.putDashboardLayout).toHaveBeenLastCalledWith({ previews: { order: ['camera:a', 'camera:b'], hidden: [] } })
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
    stubs.putDashboardLayout.mockImplementation(() => new Promise<object>((resolve) => releases.push(() => resolve({}))))
    renderWall()
    await screen.findByRole('heading', { level: 3, name: 'A' })
    fireEvent.click(screen.getByRole('button', { name: 'Move A later' }))
    fireEvent.click(screen.getByRole('button', { name: 'Move C earlier' }))
    fireEvent.click(screen.getByRole('button', { name: 'Hide B' }))
    expect(stubs.putDashboardLayout).toHaveBeenCalledTimes(1)
    await act(async () => releases[0]!())
    await waitFor(() => expect(stubs.putDashboardLayout).toHaveBeenCalledTimes(2))
    expect(stubs.putDashboardLayout).toHaveBeenLastCalledWith({ previews: { order: ['camera:b', 'camera:c', 'camera:a'], hidden: ['camera:b'] } })
    await act(async () => releases[1]!())
    expect(stubs.putDashboardLayout).toHaveBeenCalledTimes(2)
  })

  it('shows no error when a failed save is followed by a queued save that succeeds', async () => {
    stubs.getPreviews.mockResolvedValue(response([tile('camera:a', 'A'), tile('camera:b', 'B')]))
    const settle: Array<{ ok: () => void; fail: () => void }> = []
    stubs.putDashboardLayout.mockImplementation(
      () => new Promise<object>((resolve, reject) => settle.push({ ok: () => resolve({}), fail: () => reject(new ApiError('nope')) })),
    )
    renderWall()
    await screen.findByRole('heading', { level: 3, name: 'A' })
    fireEvent.click(screen.getByRole('button', { name: 'Move A later' }))
    fireEvent.click(screen.getByRole('button', { name: 'Hide A' }))
    await act(async () => settle[0]!.fail())
    expect(screen.queryByText('Layout not saved')).toBeNull()
    await waitFor(() => expect(settle).toHaveLength(2))
    await act(async () => settle[1]!.ok())
    expect(screen.queryByText('Layout not saved')).toBeNull()
  })

  it('disables the layout controls with a reason when the principal cannot write configuration', async () => {
    stubs.getPreviews.mockResolvedValue(response([tile('camera:a', 'A'), tile('camera:b', 'B')]))
    renderWall([])
    await screen.findByRole('heading', { level: 3, name: 'A' })
    const later = screen.getByRole('button', { name: 'Move A later' })
    expect(later).toBeDisabled()
    expect(later.getAttribute('title')).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Hide A' })).toBeDisabled()
    expect(screen.getByRole('article', { name: 'A' })).not.toHaveAttribute('draggable', 'true')
  })

  it('says the gateway is unreachable and still lists the tiles', async () => {
    stubs.getPreviews.mockResolvedValue(response([tile('camera:a', 'A', { state: 'error' })], { state: 'unreachable', message: 'The gateway did not answer. Check that it is running.' }))
    renderWall()
    await screen.findByText('Gateway unreachable')
    expect(screen.getByText('The gateway did not answer. Check that it is running.')).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 3, name: 'A' })).toBeInTheDocument()
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
})
