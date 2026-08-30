import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ConfigObjectSummary, Model, Node, NodeAssetManifest, SessionResponse, ShowSurfaceConfigResponse } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  listShowSurfacesForNode: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShowSurface: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getNodeAssetManifest: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  declareNode: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  deleteNodeDeclaration: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  runDiscovery: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    listShowSurfacesForNode: (...args: never[]) => stubs.listShowSurfacesForNode(...args),
    getShowSurface: (...args: never[]) => stubs.getShowSurface(...args),
    getNodeAssetManifest: (...args: never[]) => stubs.getNodeAssetManifest(...args),
    declareNode: (...args: never[]) => stubs.declareNode(...args),
    deleteNodeDeclaration: (...args: never[]) => stubs.deleteNodeDeclaration(...args),
    runDiscovery: (...args: never[]) => stubs.runDiscovery(...args),
  }
})

const { NodeDetail } = await import('./NodeDetail')

function node(overrides: Partial<Node> = {}): Node {
  return {
    nodeId: 'media-garage',
    label: 'Garage bay projector host',
    platform: null,
    agentVersion: '0.9.4',
    bootId: 'a3f91c2',
    startedAt: null,
    firstSeenAt: '2026-08-12T09:41:00Z',
    updatedAt: '2026-08-30T20:41:00Z',
    capabilities: [{ id: 'display.hdmi', version: 1, attributes: { outputs: 2 } }],
    controlPlane: { state: 'online', reason: null },
    evidence: {
      heartbeat: { observedAt: '2026-08-30T20:41:00Z', state: 'current' },
      hello: { observedAt: '2026-08-12T09:41:00Z', state: 'current' },
      lastWill: { observedAt: null, state: 'not_collected' },
    },
    declaration: {
      declared: true,
      label: 'Garage bay projector host',
      notes: null,
      declaredAt: '2026-08-12T09:41:00Z',
      declaredByPrincipalId: 'p1',
      declaredByPrincipalName: 'erbartos',
      discoveryState: 'present',
      discoveryReason: null,
      lastDiscoveryRunId: 'run1',
      lastDiscoveredAt: '2026-08-30T20:00:00Z',
      notSeenAsOfRunId: null,
      notSeenAsOfRunFinishedAt: null,
    },
    render: [],
    audio: [],
    fppConnect: [],
    ...overrides,
  } as unknown as Node
}

function signedIn(scopes: string[]): SessionResponse {
  return {
    serverTime: '2026-08-30T21:00:00Z',
    authenticated: true,
    principal: { id: 'p1', name: 'erbartos', kind: 'human', role: 'operator' },
    session: { id: 's1', deviceLabel: 'porch tablet', createdAt: '2026-08-30T21:00:00Z' },
    credentialForm: 'session',
    scopes,
    scopesState: 'current',
    bootstrapRequired: false,
  } as unknown as SessionResponse
}

function surfaceSummary(overrides: Partial<ConfigObjectSummary> = {}): ConfigObjectSummary {
  return { id: 'garage-door', label: 'Garage door', show: 'winter-ridge-2026', currentRevision: 6, updatedAt: '2026-08-30T20:41:00Z', ...overrides }
}

function surfaceResponse(overrides: Partial<ShowSurfaceConfigResponse['payload']> = {}): ShowSurfaceConfigResponse {
  return {
    serverTime: '2026-08-30T21:00:00Z',
    kind: 'show.surface',
    id: 'garage-door',
    revision: 6,
    payload: {
      show: 'winter-ridge-2026',
      name: 'Garage door',
      node: 'media-garage',
      channelRange: { startChannel: 15361, channelCount: 4096 },
      geometry: { width: 32, height: 32, pixelFormat: 'rgbw' },
      frameRate: 40,
      output: { transport: 'hdmi', hdmi: { display: 'HDMI-1' } },
      ...overrides,
    },
    updatedAt: '2026-08-30T20:41:00Z',
    createdByPrincipalId: 'p1',
    createdByPrincipalName: 'erbartos',
    source: 'api',
  } as unknown as ShowSurfaceConfigResponse
}

function manifest(overrides: Partial<NodeAssetManifest> = {}): NodeAssetManifest {
  return {
    node: 'media-garage',
    state: 'not_ready',
    reason: 'One expected asset has not arrived.',
    missing: [],
    gaps: [],
    extra: [],
    observedAt: '2026-08-30T20:41:00Z',
    ...overrides,
  } as unknown as NodeAssetManifest
}

function renderScreen(nodes: Node[], model: Partial<Model> = {}) {
  return render(
    <ModelContext.Provider
      value={{
        ...initialModel(),
        nodes,
        session: signedIn(['config:write']),
        serverTime: '2026-08-30T21:07:00Z',
        serverTimeReceivedAt: Date.now(),
        ...model,
      }}
    >
      <MemoryRouter initialEntries={['/monitor/fleet/node/media-garage']}>
        <Routes>
          <Route path="/monitor/fleet/node/:nodeId" element={<NodeDetail />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Node detail', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
    stubs.listShowSurfacesForNode = () => new Promise(() => {})
    stubs.getShowSurface = () => new Promise(() => {})
    stubs.getNodeAssetManifest = () => new Promise(() => {})
    stubs.declareNode = () => new Promise(() => {})
    stubs.deleteNodeDeclaration = () => new Promise(() => {})
    stubs.runDiscovery = () => new Promise(() => {})
  })

  it('renders the mock’s section labels in order', () => {
    renderScreen([node()])
    const headings = screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)
    expect(headings).toEqual(['Identity', 'Capabilities', 'Surfaces on this node', 'Assets held locally', 'Remove this node'])
  })

  it('says the agent went away when a last will was received', () => {
    renderScreen([
      node({
        controlPlane: { state: 'offline', reason: 'No heartbeat within the expected interval.' },
        evidence: {
          heartbeat: { observedAt: '2026-08-30T20:15:00Z', state: 'stale' },
          hello: { observedAt: '2026-08-12T09:41:00Z', state: 'current' },
          lastWill: { observedAt: '2026-08-30T20:41:07Z', state: 'current', value: false },
        } as unknown as Node['evidence'],
      }),
    ])
    expect(screen.getByText(/Last will received at/)).toBeInTheDocument()
    expect(screen.getByText(/so the agent went away rather than the network dropping/)).toBeInTheDocument()
  })

  it('does not claim the agent went away when the last-will record still says online', () => {
    renderScreen([
      node({
        controlPlane: { state: 'offline', reason: 'No heartbeat within the expected interval.' },
        evidence: {
          heartbeat: { observedAt: '2026-08-30T20:15:00Z', state: 'stale' },
          hello: { observedAt: '2026-08-12T09:41:00Z', state: 'current' },
          lastWill: { observedAt: '2026-08-30T20:41:07Z', state: 'current', value: true },
        } as unknown as Node['evidence'],
      }),
    ])
    expect(screen.queryByText(/Last will received at/)).not.toBeInTheDocument()
    expect(screen.getByText(/still says the agent was online/)).toBeInTheDocument()
  })

  it('does not claim a last will when the evidence is not_collected', () => {
    renderScreen([
      node({
        controlPlane: { state: 'offline', reason: 'No heartbeat within the expected interval.' },
        evidence: {
          heartbeat: { observedAt: '2026-08-30T20:15:00Z', state: 'stale' },
          hello: { observedAt: '2026-08-12T09:41:00Z', state: 'current' },
          lastWill: { observedAt: null, state: 'not_collected' },
        } as unknown as Node['evidence'],
      }),
    ])
    expect(screen.queryByText(/Last will received at/)).not.toBeInTheDocument()
    expect(screen.getByText(/nothing here distinguishes the agent going away/)).toBeInTheDocument()
  })

  it('renders capabilities from the node’s own list and states the no-audio-routing fact', () => {
    renderScreen([node({ capabilities: [{ id: 'display.hdmi', version: 1, attributes: { outputs: 2 } }] })])
    expect(screen.getAllByText('display.hdmi').length).toBeGreaterThan(0)
    expect(screen.getByText(/has no audio routing to configure/)).toBeInTheDocument()
    expect(screen.getByText(/reports a count but no names/)).toBeInTheDocument()
  })

  it('lists the manifest’s missing, gaps and extra entries', async () => {
    stubs.listShowSurfacesForNode = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', kind: 'show.surface', objects: [] })
    stubs.getNodeAssetManifest = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:07:00Z',
        manifest: manifest({
          missing: [{ assetId: 'a1', sequence: 'rooftop-finale', filename: 'rooftop-finale.fseq', contentHash: 'sha256:b1e7', sizeBytes: 4096 }],
          gaps: [{ sequence: 'carol-of-the-bells', surfaces: ['garage-door'] }],
          extra: [{ contentHash: 'sha256:aaaa', filename: 'stray.fseq', sizeBytes: 512 }],
        }),
      })
    renderScreen([node()])
    await waitFor(() => expect(screen.getByText('rooftop-finale.fseq')).toBeInTheDocument())
    expect(screen.getByText('carol-of-the-bells')).toBeInTheDocument()
    expect(screen.getByText('stray.fseq')).toBeInTheDocument()
    expect(screen.getAllByText(/never a basis for deletion/).length).toBeGreaterThan(0)
    expect(screen.queryByText(/matches/i)).not.toBeInTheDocument()
  })

  it('renders state "unknown" as the check not being performed, never as not ready', async () => {
    stubs.listShowSurfacesForNode = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', kind: 'show.surface', objects: [] })
    stubs.getNodeAssetManifest = () =>
      Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', manifest: manifest({ state: 'unknown', reason: 'No inventory report has ever arrived.' }) })
    renderScreen([node()])
    await waitFor(() => expect(screen.getByText('Unknown')).toBeInTheDocument())
    expect(screen.getAllByText('No verdict').length).toBeGreaterThan(0)
    expect(screen.queryByText('Not ready')).not.toBeInTheDocument()
  })

  it('marks Re-sync all inert and states the offline reason when the node is offline', async () => {
    stubs.listShowSurfacesForNode = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', kind: 'show.surface', objects: [] })
    stubs.getNodeAssetManifest = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', manifest: manifest() })
    renderScreen([node({ controlPlane: { state: 'offline', reason: 'No heartbeat within the expected interval.' } })])
    await waitFor(() => expect(screen.getByRole('button', { name: 'Re-sync all' })).toBeDisabled())
    expect(screen.getByText('Sync cannot run while the node is offline.')).toBeInTheDocument()
    expect(screen.getByText(/does nothing yet/)).toBeInTheDocument()
  })

  it('keeps Remove declaration disabled until the typed id matches exactly', async () => {
    stubs.listShowSurfacesForNode = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', kind: 'show.surface', objects: [] })
    stubs.getNodeAssetManifest = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', manifest: manifest() })
    renderScreen([node()])
    const button = screen.getByRole('button', { name: /Remove declaration/ })
    expect(button).toBeDisabled()
    const input = screen.getByLabelText('Type media-garage to confirm')
    fireEvent.change(input, { target: { value: 'wrong-id' } })
    expect(button).toBeDisabled()
    fireEvent.change(input, { target: { value: 'media-garage' } })
    await waitFor(() => expect(button).not.toBeDisabled())
  })

  it('disables Remove declaration with its reason without config:write', async () => {
    stubs.listShowSurfacesForNode = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', kind: 'show.surface', objects: [] })
    stubs.getNodeAssetManifest = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', manifest: manifest() })
    renderScreen([node()], { session: signedIn([]) })
    const input = screen.getByLabelText('Type media-garage to confirm')
    fireEvent.change(input, { target: { value: 'media-garage' } })
    const button = screen.getByRole('button', { name: /Remove declaration/ })
    expect(button).toBeDisabled()
    expect(button).toHaveAttribute('title')
  })

  it('saves the label edit through declareNode and reports a failure without blanking the block', async () => {
    stubs.listShowSurfacesForNode = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', kind: 'show.surface', objects: [] })
    stubs.getNodeAssetManifest = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', manifest: manifest() })
    stubs.declareNode = vi.fn().mockRejectedValue(new Error('The coordinator refused this write.'))
    renderScreen([node()])
    const input = screen.getByLabelText('Label')
    fireEvent.change(input, { target: { value: 'New label' } })
    fireEvent.click(screen.getByRole('button', { name: /Save label/ }))
    await waitFor(() => expect(screen.getByText(/refused this write/)).toBeInTheDocument())
    expect(screen.getByText('Identity')).toBeInTheDocument()
    expect(stubs.declareNode).toHaveBeenCalledWith('media-garage', 'New label', '')
  })

  it('renders surfaces assigned to this node with their own geometry and rendering state', async () => {
    stubs.listShowSurfacesForNode = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', kind: 'show.surface', objects: [surfaceSummary()] })
    stubs.getShowSurface = () => Promise.resolve(surfaceResponse())
    stubs.getNodeAssetManifest = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', manifest: manifest() })
    renderScreen([node()])
    await waitFor(() => expect(screen.getByText('Garage door')).toBeInTheDocument())
    expect(screen.getByText(/32×32 rgbw/)).toBeInTheDocument()
    expect(screen.getByText('winter-ridge-2026')).toBeInTheDocument()
  })

  it('shows the not-found treatment naming the id when the node is not in the model', () => {
    renderScreen([])
    expect(screen.getByText(/media-garage/)).toBeInTheDocument()
    expect(screen.getByText(/no record of this node/i)).toBeInTheDocument()
  })
})
