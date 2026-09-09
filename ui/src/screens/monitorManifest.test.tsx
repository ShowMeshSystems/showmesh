import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Model, Node, NodeAssetManifest } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const getAssetManifest = vi.fn()
const listShowSurfacesForNode = vi.fn()
const getNodeAssetManifest = vi.fn()
vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    getAssetManifest: (...args: unknown[]) => getAssetManifest(...args),
    listShowSurfacesForNode: (...args: unknown[]) => listShowSurfacesForNode(...args),
    getNodeAssetManifest: (...args: unknown[]) => getNodeAssetManifest(...args),
  }
})

const { MonitorManifest } = await import('./MonitorManifest')

function manifest(overrides: Partial<NodeAssetManifest> = {}): NodeAssetManifest {
  return {
    node: 'media-front',
    state: 'ready',
    reason: null,
    missing: [],
    gaps: [],
    extra: [],
    observedAt: '2026-08-28T21:06:00Z',
    ...overrides,
  } as unknown as NodeAssetManifest
}

function testNode(overrides: Partial<Node> = {}): Node {
  return {
    nodeId: 'media-front',
    label: 'Front media node',
    platform: null,
    agentVersion: '0.9.4',
    bootId: 'b1',
    startedAt: null,
    firstSeenAt: '2026-08-12T09:41:00Z',
    updatedAt: '2026-08-28T20:41:00Z',
    capabilities: [],
    controlPlane: { state: 'online', reason: null },
    evidence: {
      heartbeat: { observedAt: '2026-08-28T20:41:00Z', state: 'current' },
      hello: { observedAt: '2026-08-12T09:41:00Z', state: 'current' },
      lastWill: { observedAt: null, state: 'not_collected' },
    },
    declaration: {
      declared: true,
      label: 'Front media node',
      notes: null,
      declaredAt: '2026-08-12T09:41:00Z',
      declaredByPrincipalId: 'p1',
      declaredByPrincipalName: 'operator1',
      discoveryState: 'present',
      discoveryReason: null,
      lastDiscoveryRunId: 'run1',
      lastDiscoveredAt: '2026-08-28T20:00:00Z',
      notSeenAsOfRunId: null,
      notSeenAsOfRunFinishedAt: null,
    },
    render: [],
    audio: [],
    fppConnect: [],
    ...overrides,
  } as unknown as Node
}

function renderScreen(model: Partial<Model> = {}) {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model, serverTime: '2026-08-28T21:07:00Z', serverTimeReceivedAt: Date.now() }}>
      <MemoryRouter>
        <MonitorManifest />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Monitor · Manifest', () => {
  afterEach(() => {
    cleanup()
    getAssetManifest.mockReset()
  })

  it('names the facet heading', () => {
    getAssetManifest.mockReturnValue(new Promise(() => {}))
    renderScreen()
    expect(screen.getByRole('heading', { level: 2, name: 'Manifest' })).toBeInTheDocument()
  })

  it('shows the loading absence before the first read', () => {
    getAssetManifest.mockReturnValue(new Promise(() => {}))
    renderScreen()
    expect(screen.getByText('Reading')).toBeInTheDocument()
  })

  it('lists each node’s readiness once the read succeeds', async () => {
    getAssetManifest.mockResolvedValue({ serverTime: '2026-08-28T21:07:00Z', nodes: [manifest()] })
    renderScreen()
    await waitFor(() => expect(screen.getByText('media-front')).toBeInTheDocument())
    expect(screen.getByText('Ready')).toBeInTheDocument()
  })

  it('opens the manifest detail as a dialog portaled into document.body and clears it on Escape', async () => {
    getAssetManifest.mockResolvedValue({ serverTime: '2026-08-28T21:07:00Z', nodes: [manifest()] })
    renderScreen()
    await waitFor(() => expect(screen.getByText('media-front')).toBeInTheDocument())
    screen.getByRole('row', { name: 'View manifest for media-front' }).click()
    const dialog = await waitFor(() => screen.getByRole('dialog'))
    expect(dialog.parentElement).toBe(document.body)

    fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('never offers a delete control for an extra asset', async () => {
    getAssetManifest.mockResolvedValue({
      serverTime: '2026-08-28T21:07:00Z',
      nodes: [manifest({ extra: [{ contentHash: 'abc123', filename: 'stray.fseq', sizeBytes: 512 }] })],
    })
    renderScreen()
    await waitFor(() => expect(screen.getByText('media-front')).toBeInTheDocument())
    screen.getByRole('row', { name: 'View manifest for media-front' }).click()
    await waitFor(() => expect(screen.getByText('stray.fseq')).toBeInTheDocument())
    expect(screen.getAllByText(/never a basis for deletion/).length).toBeGreaterThan(0)
    expect(screen.queryByRole('button', { name: /delete/i })).not.toBeInTheDocument()
  })

  it('says none is declared when the fleet is empty, distinct from a read failure', async () => {
    getAssetManifest.mockResolvedValue({ serverTime: '2026-08-28T21:07:00Z', nodes: [] })
    renderScreen()
    await waitFor(() => expect(screen.getByText('No node is declared, so there is nothing to check.')).toBeInTheDocument())
  })

  it('retains the last read manifest and says when it was read after a refresh failure', async () => {
    getAssetManifest.mockResolvedValueOnce({ serverTime: '2026-08-28T21:07:00Z', nodes: [manifest()] })
    renderScreen()
    await waitFor(() => expect(screen.getByText('media-front')).toBeInTheDocument())
    getAssetManifest.mockRejectedValueOnce(new Error('network down'))
    screen.getByRole('button', { name: 'Refresh' }).click()
    // Retained data that is no longer being refreshed is stale, not a bare failure.
    await waitFor(() => expect(screen.getByText('Stale')).toBeInTheDocument())
    expect(screen.getByText('media-front')).toBeInTheDocument()
  })

  it('says a first read that never succeeded failed, not that it is stale', async () => {
    getAssetManifest.mockRejectedValueOnce(new Error('network down'))
    renderScreen()
    await waitFor(() => expect(screen.getByText('Read failed')).toBeInTheDocument())
    expect(screen.queryByText('Stale')).not.toBeInTheDocument()
  })

  it('never reads an unknown verdict as a settled zero', async () => {
    getAssetManifest.mockResolvedValueOnce({
      serverTime: '2026-08-28T21:07:00Z',
      nodes: [manifest({ state: 'unknown', reason: 'no inventory report', observedAt: null })],
    })
    renderScreen()
    await waitFor(() => expect(screen.getByText('media-front')).toBeInTheDocument())
    screen.getByRole('row', { name: 'View manifest for media-front' }).click()
    // missing and gaps are populated only when state is not_ready, so an empty
    // list under unknown means no verdict, never "nothing is missing".
    await waitFor(() => expect(screen.getAllByText('No verdict').length).toBeGreaterThan(0))
    expect(screen.queryByText('Nothing this node should hold is missing.')).not.toBeInTheDocument()
    expect(screen.queryByText('No sequence the active show holds an asset for is uncovered.')).not.toBeInTheDocument()
  })

  it('reads an empty list under a ready verdict as a settled zero', async () => {
    getAssetManifest.mockResolvedValueOnce({ serverTime: '2026-08-28T21:07:00Z', nodes: [manifest()] })
    renderScreen()
    await waitFor(() => expect(screen.getByText('media-front')).toBeInTheDocument())
    screen.getByRole('row', { name: 'View manifest for media-front' }).click()
    await waitFor(() => expect(screen.getByText('Nothing this node should hold is missing.')).toBeInTheDocument())
    expect(screen.queryByText('No verdict')).not.toBeInTheDocument()
  })
})

describe('Monitor · Manifest · node inspector', () => {
  afterEach(() => {
    cleanup()
    getAssetManifest.mockReset()
    listShowSurfacesForNode.mockReset()
    getNodeAssetManifest.mockReset()
  })

  function renderAtManifestRoute(model: Partial<Model> = {}) {
    let location: ReturnType<typeof useLocation> | null = null
    function LocationSpy() {
      location = useLocation()
      return null
    }
    render(
      <ModelContext.Provider
        value={{ ...initialModel(), ...model, serverTime: '2026-08-28T21:07:00Z', serverTimeReceivedAt: Date.now() }}
      >
        <MemoryRouter initialEntries={['/monitor/manifest']}>
          <LocationSpy />
          <Routes>
            <Route path="/monitor/manifest" element={<MonitorManifest />} />
            <Route path="/monitor/manifest/node/:nodeId" element={<MonitorManifest />} />
          </Routes>
        </MemoryRouter>
      </ModelContext.Provider>,
    )
    return () => location
  }

  it('opens the node inspector over the manifest inspector instead of navigating to the Monitor page', async () => {
    getAssetManifest.mockResolvedValue({ serverTime: '2026-08-28T21:07:00Z', nodes: [manifest()] })
    listShowSurfacesForNode.mockReturnValue(new Promise(() => {}))
    getNodeAssetManifest.mockReturnValue(new Promise(() => {}))

    const getLocation = renderAtManifestRoute({ nodes: [testNode()] })

    await waitFor(() => expect(screen.getByText('media-front')).toBeInTheDocument())
    screen.getByRole('row', { name: 'View manifest for media-front' }).click()
    await waitFor(() => expect(screen.getAllByRole('dialog').length).toBe(1))

    // Still on the manifest route with the manifest inspector open.
    expect(getLocation()?.pathname).toBe('/monitor/manifest')

    fireEvent.click(screen.getByRole('link', { name: 'media-front' }))

    // The node inspector opens as a second, wider drawer stacked over the manifest one.
    await waitFor(() => expect(screen.getAllByRole('dialog').length).toBe(2))
    const dialogs = screen.getAllByRole('dialog')
    expect(dialogs.some((dialog) => dialog.className.includes('sm-drawer--wide'))).toBe(true)
    expect(screen.getByRole('heading', { level: 2, name: 'Front media node' })).toBeInTheDocument()

    // The manifest inspector and its table row are still present, not replaced.
    expect(screen.getByRole('row', { name: 'View manifest for media-front' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'media-front' })).toBeInTheDocument()

    // The route stayed within the manifest family; it never redirected to /monitor/fleet.
    expect(getLocation()?.pathname).toBe('/monitor/manifest/node/media-front')
  })
})
