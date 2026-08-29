import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { Assets } from './Assets'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Asset, ConfigObjectSummary, Model, NodeAssetManifest, ShowSurfaceConfigResponse } from '../app/types'

// Sequence coverage (Show Assets.dc.html, revision-1: the owner's answer to
// `gaps`/`extra`) needs one more thing than the existing manifest/assets
// fetch already gives ShowScopedAssets: which nodes carry a surface in
// THIS show (SequenceCoverageSection's own useShowSurfaceNodeIds hook).
// Mocked at the '../api' boundary, same isolation as RenderSurfacePanel.test.tsx.
const { listAssets, getAssetManifest, getShow, listConfigObjects, getShowSurface } = vi.hoisted(() => ({
  listAssets: vi.fn(),
  getAssetManifest: vi.fn(),
  getShow: vi.fn(),
  listConfigObjects: vi.fn(),
  getShowSurface: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, listAssets, getAssetManifest, getShow, listConfigObjects, getShowSurface }
})

afterEach(() => {
  cleanup()
  listAssets.mockReset()
  getAssetManifest.mockReset()
  getShow.mockReset()
  listConfigObjects.mockReset()
  getShowSurface.mockReset()
})

function asset(overrides: Partial<Asset> = {}): Asset {
  return {
    id: 'asset-1',
    show: 'wr26',
    sequence: 'yard-arch',
    targetKind: 'node',
    target: 'media-front',
    mediaType: 'fseq',
    contentHash: 'sha256:abc',
    runtimeFilename: 'yard-arch.fseq',
    sizeBytes: 1024,
    createdAt: '2026-08-27T00:00:00Z',
    createdByPrincipalId: null,
    createdByPrincipalName: null,
    supersededAt: null,
    current: true,
    ...overrides,
  }
}

function manifest(node: string, overrides: Partial<NodeAssetManifest> = {}): NodeAssetManifest {
  return {
    node,
    state: 'ready',
    reason: null,
    missing: [],
    gaps: [],
    extra: [],
    observedAt: '2026-08-28T20:41:07Z',
    ...overrides,
  }
}

function surfaceSummary(id: string): ConfigObjectSummary {
  return { id, label: id, show: 'wr26', currentRevision: 1, updatedAt: '2026-08-27T00:00:00Z' }
}

function surfaceResponse(id: string, node: string): ShowSurfaceConfigResponse {
  return {
    serverTime: '2026-08-28T21:00:00Z',
    kind: 'show.surface',
    id,
    revision: 1,
    payload: {
      show: 'wr26',
      name: id,
      node,
      channelRange: { startChannel: 1, channelCount: 3 },
      geometry: { width: 1, height: 1, pixelFormat: 'rgb' },
      frameRate: 40,
      output: { transport: 'ndi', ndi: { sourceName: 'n' } },
    },
    updatedAt: '2026-08-27T00:00:00Z',
    createdByPrincipalId: null,
    createdByPrincipalName: null,
    source: 'api',
  }
}

function renderShowAssets(model: Model = makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) })) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={['/shows/wr26/assets']}>
        <Routes>
          <Route path="/shows/:showId/assets" element={<Assets />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Shows / Assets, sequence coverage roll-up', () => {
  it('reports yard-arch uncovered on both judged nodes, and states media-garage as not judged rather than counting it clean', async () => {
    listAssets.mockResolvedValue({ serverTime: '2026-08-28T21:00:00Z', assets: [asset()] })
    getAssetManifest.mockResolvedValue({
      serverTime: '2026-08-28T21:00:00Z',
      nodes: [
        manifest('media-front', { gaps: [{ sequence: 'yard-arch', surfaces: ['front-arch'] }] }),
        manifest('media-side', { gaps: [{ sequence: 'yard-arch', surfaces: ['side-arch'] }] }),
        manifest('media-garage', { state: 'unknown', reason: 'No manifest since last observed.', observedAt: '2026-08-28T20:41:07Z' }),
      ],
    })
    getShow.mockResolvedValue({ payload: { name: 'WR26' } })
    listConfigObjects.mockResolvedValue({
      serverTime: '2026-08-28T21:00:00Z',
      kind: 'show.surface',
      objects: [surfaceSummary('front-arch'), surfaceSummary('side-arch'), surfaceSummary('garage-arch')],
    })
    getShowSurface.mockImplementation((id: string) => {
      const node = id === 'front-arch' ? 'media-front' : id === 'side-arch' ? 'media-side' : 'media-garage'
      return Promise.resolve(surfaceResponse(id, node))
    })

    renderShowAssets()

    const label = await screen.findByText('No coverage')
    const row = label.closest('.asset-coverage__row') as HTMLElement
    expect(row).not.toBeNull()
    expect(within(row).getByText(/no node holds anything for it/)).toBeInTheDocument()
    expect(within(row).getByText(/2 of 2 node/)).toBeInTheDocument()

    // media-garage is stated as its own not-judged fact, never folded into "covered".
    expect(within(row).getByText(/media-garage/)).toBeInTheDocument()
    expect(within(row).getByText(/was not judged either way/)).toBeInTheDocument()
    expect(within(row).getByText(/2 of 3, not 3 of 3/)).toBeInTheDocument()
  })

  it('opens the upload pane from "Upload for <sequence>", a real control, not a drawing', async () => {
    const user = userEvent.setup()
    listAssets.mockResolvedValue({ serverTime: '2026-08-28T21:00:00Z', assets: [asset()] })
    getAssetManifest.mockResolvedValue({
      serverTime: '2026-08-28T21:00:00Z',
      nodes: [manifest('media-front', { gaps: [{ sequence: 'yard-arch', surfaces: ['front-arch'] }] })],
    })
    getShow.mockResolvedValue({ payload: { name: 'WR26' } })
    listConfigObjects.mockResolvedValue({
      serverTime: '2026-08-28T21:00:00Z',
      kind: 'show.surface',
      objects: [surfaceSummary('front-arch')],
    })
    getShowSurface.mockResolvedValue(surfaceResponse('front-arch', 'media-front'))

    renderShowAssets(makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run', 'asset:write'] }) }))

    const uploadButton = await screen.findByRole('button', { name: 'Upload for yard-arch' })
    await user.click(uploadButton)
    expect(screen.getByRole('heading', { name: 'Upload asset' })).toBeInTheDocument()
  })

  it('stamps the missing "Run sync" control as a planned feature, never a working button', async () => {
    listAssets.mockResolvedValue({ serverTime: '2026-08-28T21:00:00Z', assets: [asset()] })
    getAssetManifest.mockResolvedValue({
      serverTime: '2026-08-28T21:00:00Z',
      nodes: [manifest('media-front', { gaps: [{ sequence: 'yard-arch', surfaces: ['front-arch'] }] })],
    })
    getShow.mockResolvedValue({ payload: { name: 'WR26' } })
    listConfigObjects.mockResolvedValue({
      serverTime: '2026-08-28T21:00:00Z',
      kind: 'show.surface',
      objects: [surfaceSummary('front-arch')],
    })
    getShowSurface.mockResolvedValue(surfaceResponse('front-arch', 'media-front'))

    renderShowAssets()

    await screen.findByText('No coverage')
    const planned = screen.getByRole('note', { name: 'Not built: Run sync from here' })
    expect(within(planned).getByText('Not built')).toBeInTheDocument()
    // The drawn button is aria-hidden and inert -- a picture of a control,
    // never a working one -- so it is found by text, not by accessible role.
    const drawnButton = within(planned).getByText('Run sync')
    expect(drawnButton.closest('[aria-hidden="true"]')).not.toBeNull()
    expect(drawnButton).toHaveAttribute('tabIndex', '-1')
  })

  it('never counts a sequence as a finding when every judged node covers it', async () => {
    listAssets.mockResolvedValue({ serverTime: '2026-08-28T21:00:00Z', assets: [asset()] })
    getAssetManifest.mockResolvedValue({
      serverTime: '2026-08-28T21:00:00Z',
      nodes: [manifest('media-front')],
    })
    getShow.mockResolvedValue({ payload: { name: 'WR26' } })
    listConfigObjects.mockResolvedValue({
      serverTime: '2026-08-28T21:00:00Z',
      kind: 'show.surface',
      objects: [surfaceSummary('front-arch')],
    })
    getShowSurface.mockResolvedValue(surfaceResponse('front-arch', 'media-front'))

    renderShowAssets()

    await waitFor(() => expect(getShowSurface).toHaveBeenCalled())
    expect(await screen.findByText('No judged node reports a sequence with no coverage at all.')).toBeInTheDocument()
    expect(screen.queryByText('No coverage')).not.toBeInTheDocument()
  })
})
