import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api'
import type { Event, FPPInstance, Model, Node } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'
import { Monitor } from './Monitor'
import { activityRows, facetCounts, fleetRows, fleetSummary } from './monitorModel'

const PENDING = () => new Promise<never>(() => {})

const fallbackStubs = vi.hoisted(() => ({
  listFallbackPrograms: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getFallbackProgram: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
}))

// The node drawer embeds NodeDetail's own reads: stubbed here as pending so
// a node-selection test never depends on their resolved content.
const nodeStubs = vi.hoisted(() => ({
  listShowSurfacesForNode: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShowSurface: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getNodeAssetManifest: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    listFallbackPrograms: (...args: never[]) => fallbackStubs.listFallbackPrograms(...args),
    getFallbackProgram: (...args: never[]) => fallbackStubs.getFallbackProgram(...args),
    listShowSurfacesForNode: (...args: never[]) => nodeStubs.listShowSurfacesForNode(...args),
    getShowSurface: (...args: never[]) => nodeStubs.getShowSurface(...args),
    getNodeAssetManifest: (...args: never[]) => nodeStubs.getNodeAssetManifest(...args),
  }
})

const observation = (signal: string, state = 'current', kind = 'surface', id = 'front') =>
  ({
    resource: { kind, id },
    signal,
    value: 'x',
    unit: null,
    state,
    reason: null,
    observedAt: '2026-08-28T21:06:58Z',
    collectedAt: '2026-08-28T21:06:58Z',
    source: 'agent',
    quality: 'reported',
  }) as unknown as Node['render'][number]

function node(nodeId: string, state: 'online' | 'offline' | 'unknown', render: Node['render'] = [], audio: Node['audio'] = []): Node {
  return {
    nodeId,
    label: nodeId,
    agentVersion: '0.9.4',
    capabilities: [{ id: 'transport.ndi.send', version: 1 }],
    controlPlane: { state, reason: state === 'offline' ? 'Last will received.' : null },
    evidence: { hello: observation('node.hello'), lastWill: observation('node.last_will'), heartbeat: observation('node.heartbeat') },
    declaration: {},
    render,
    audio,
    fppConnect: [],
  } as unknown as Node
}

const fpp = (instanceId: string, health: FPPInstance['health'], uuidChanged = false) =>
  ({
    instanceId,
    endpoint: 'http://198.51.100.1',
    health,
    observations: [],
    lastPollAt: '2026-08-28T21:06:58Z',
    lastPollError: null,
    instanceUuidChange: uuidChanged ? { previousUuid: 'old', changedAt: '2026-08-28T20:54:00Z' } : null,
  }) as unknown as FPPInstance

function renderScreen(model: Partial<Model>, initialPath = '/monitor/fleet') {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model, serverTime: '2026-08-28T21:07:00Z', serverTimeReceivedAt: Date.now() }}>
      <MemoryRouter initialEntries={[initialPath]}>
        <Routes>
          <Route path="/monitor/fleet" element={<Monitor />} />
          <Route path="/monitor/fleet/node/:nodeId" element={<Monitor />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Monitor · Fleet', () => {
  beforeEach(() => {
    fallbackStubs.listFallbackPrograms = () => PENDING()
    fallbackStubs.getFallbackProgram = () => PENDING()
    nodeStubs.listShowSurfacesForNode = () => PENDING()
    nodeStubs.getShowSurface = () => PENDING()
    nodeStubs.getNodeAssetManifest = () => PENDING()
  })
  afterEach(cleanup)

  it('renders the mock’s blocks in order', () => {
    renderScreen({})
    expect(screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)).toEqual([
      'Needs an operator',
      'Fleet',
      'Activity',
    ])
  })

  it('puts nodes, FPP and Resolume in one table with kind as a column', () => {
    renderScreen({ nodes: [node('media-front', 'online')], fpp: [fpp('barn-player', 'healthy')], resolume: [{ instanceId: 'arena', health: 'healthy', observations: [], composition: null } as never] })
    const table = screen.getByRole('region', { name: 'Fleet resources, scrollable' })
    expect(within(table).getByText('media-front')).toBeInTheDocument()
    expect(within(table).getByText('barn-player')).toBeInTheDocument()
    expect(within(table).getByText('arena')).toBeInTheDocument()
    expect(within(table).getAllByRole('columnheader').map((h) => h.textContent)).toEqual([
      'Resource',
      'Kind',
      'Health',
      'Last report',
    ])
  })

  it('counts the fleet as inventory, not as attention', () => {
    const counts = facetCounts({
      ...initialModel(),
      nodes: [node('a', 'online', [observation('surface.pipeline.state')])],
      fpp: [fpp('b', 'healthy')],
    })
    expect(counts.fleet).toBe(2)
    expect(counts.signals).toBe(1)
    expect(counts.capabilities).toBe(1)
  })

  it('reports each resource’s own health rather than a combined verdict', () => {
    const rows = fleetRows({ ...initialModel(), fpp: [fpp('barn-player', 'healthy', true)] }, '2026-08-28T21:07:00Z')
    expect(rows[0]?.health).toBe('healthy')
    expect(rows[0]?.detail).toContain('bindings held')
  })

  it('keeps an FPP deep selection in the Fleet inspector and sends transport to Live Control', () => {
    renderScreen({
      fpp: [
        {
          ...fpp('barn-player', 'healthy', true),
          instanceUuid: 'new-uuid',
          observations: [observation('fpp.playlist.state')],
        } as FPPInstance,
      ],
    })

    fireEvent.click(screen.getByRole('row', { name: 'View barn-player' }))
    const inspector = screen.getByRole('dialog')
    expect(within(inspector).getByRole('heading', { name: 'barn-player', level: 2 })).toBeInTheDocument()
    expect(within(inspector).getByText('FPP player · healthy as FPP reports')).toBeInTheDocument()
    expect(within(inspector).getByText('Bindings held')).toBeInTheDocument()
    expect(within(inspector).getByRole('link', { name: 'Open Live Control' })).toHaveAttribute('href', '/control')
  })

  it('clicking a node row opens the full node drawer at its route, portaled into document.body', () => {
    renderScreen({ nodes: [node('media-garage', 'online')] })
    fireEvent.click(screen.getByRole('row', { name: 'View media-garage' }))
    const dialog = screen.getByRole('dialog')
    expect(dialog.parentElement).toBe(document.body)
    expect(dialog.className).toContain('sm-drawer--wide')
    expect(within(dialog).getByRole('heading', { name: 'media-garage', level: 2 })).toBeInTheDocument()
    expect(within(dialog).getByRole('heading', { name: 'Identity', level: 2 })).toBeInTheDocument()
    expect(within(dialog).getByRole('heading', { name: 'Remove this node', level: 2 })).toBeInTheDocument()
  })

  it('deep-links the node drawer from the route and closing it navigates back to Fleet', () => {
    renderScreen({ nodes: [node('media-garage', 'online')] }, '/monitor/fleet/node/media-garage')
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByRole('heading', { name: 'media-garage', level: 2 })).toBeInTheDocument()

    fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Fleet', level: 2 })).toBeInTheDocument()
  })

  it('a stale node id in the route still opens the drawer with the not-found treatment', () => {
    renderScreen({}, '/monitor/fleet/node/gone-node')
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText(/no record of this node/i)).toBeInTheDocument()
  })

  it('says what the fleet contains without arithmetic that does not close', () => {
    const rows = fleetRows(
      { ...initialModel(), nodes: [node('a', 'online')], fpp: [fpp('b', 'healthy')] },
      '2026-08-28T21:07:00Z',
    )
    expect(fleetSummary(rows)).toContain('2 resources')
    expect(fleetSummary(rows)).toContain('1 node, 1 FPP player, 0 Resolume instances')
  })

  it('keeps operator actions and system events in one stream, with the scope caveat', () => {
    const events = [
      { seq: 2, recordedAt: '2026-08-28T21:02:20Z', occurredAt: '2026-08-28T21:02:20Z', source: 'coordinator', resource: { kind: 'node', id: 'n' }, category: 'action', severity: 'critical', summary: 'Projector strike refused', details: {}, correlationId: null },
      { seq: 1, recordedAt: '2026-08-28T21:02:14Z', occurredAt: null, source: 'erbartos', resource: { kind: 'coordinator', id: 'c' }, category: 'night', severity: 'informational', summary: 'Night session started', details: {}, correlationId: null },
    ] as unknown as Event[]
    renderScreen({ events, snapshotReceivedAt: Date.now() })
    expect(screen.getByText('Projector strike refused')).toBeInTheDocument()
    expect(screen.getByText(/need an audit-read scope/)).toBeInTheDocument()
    expect(activityRows(events, 5)).toHaveLength(2)
  })

  it('says a retention gap out loud when the stream has one', () => {
    const events = [
      { seq: 1, recordedAt: '2026-08-28T21:02:14Z', occurredAt: null, source: 'broker', resource: { kind: 'node', id: 'n' }, category: 'lifecycle', severity: 'warning', summary: 'last will', details: {}, correlationId: null },
    ] as unknown as Event[]
    renderScreen({ events, eventsGap: true, snapshotReceivedAt: Date.now() })
    expect(screen.getByText(/permanently lost to retention/)).toBeInTheDocument()
  })
})

// SM-460: the read-only "Fallback program" group in the FPP inspector.
describe('Monitor · Fleet · FPP inspector · Fallback program', () => {
  beforeEach(() => {
    fallbackStubs.listFallbackPrograms = () => PENDING()
    fallbackStubs.getFallbackProgram = () => PENDING()
  })
  afterEach(cleanup)

  const withFallbackUuid = { ...fpp('barn-player', 'healthy'), instanceUuid: 'barn-uuid' } as FPPInstance

  function openInspector() {
    renderScreen({ fpp: [withFallbackUuid] })
    fireEvent.click(screen.getByRole('row', { name: 'View barn-player' }))
    return within(screen.getByRole('dialog'))
  }

  it('reads as loading before either fetch resolves', () => {
    const inspector = openInspector()
    expect(inspector.getByText("Reading this host's fallback program.")).toBeInTheDocument()
  })

  it('says the coordinator does not support fallback programs on a 404 for the list', async () => {
    fallbackStubs.listFallbackPrograms = () => Promise.reject(new ApiError('not found', 404))
    const inspector = openInspector()
    await waitFor(() => expect(inspector.getByText(/does not support fallback programs/)).toBeInTheDocument())
  })

  it('says no fallback program exists for a host absent from the list', async () => {
    fallbackStubs.listFallbackPrograms = () => Promise.resolve({ serverTime: '2026-09-01T00:00:00Z', programs: [] })
    const inspector = openInspector()
    await waitFor(() => expect(inspector.getByText('No fallback program exists for this host.')).toBeInTheDocument())
  })

  it('says this device may not read fallback programs on a 403 for the host read, keeping the list metadata visible', async () => {
    fallbackStubs.listFallbackPrograms = () =>
      Promise.resolve({
        serverTime: '2026-09-01T00:00:00Z',
        programs: [
          {
            fppInstanceUuid: 'barn-uuid',
            packageId: 'pkg-barn',
            revision: 'rev-9',
            show: 'holiday-2026',
            generation: 4,
            expiresAt: '2026-09-02T00:00:00Z',
            compiledAt: '2026-09-01T00:00:00Z',
          },
        ],
      })
    fallbackStubs.getFallbackProgram = () => Promise.reject(new ApiError('forbidden', 403))
    const inspector = openInspector()
    await waitFor(() => expect(inspector.getByText('This device may not read fallback programs.')).toBeInTheDocument())
    expect(inspector.getByText('pkg-barn')).toBeInTheDocument()
    expect(inspector.getByText('rev-9')).toBeInTheDocument()
  })

  it('renders every loaded field: identity, show and generation, timestamps, acknowledgement and signature presence', async () => {
    fallbackStubs.listFallbackPrograms = () =>
      Promise.resolve({
        serverTime: '2026-09-01T00:00:00Z',
        programs: [
          {
            fppInstanceUuid: 'barn-uuid',
            packageId: 'pkg-barn',
            revision: 'rev-9',
            show: 'holiday-2026',
            generation: 4,
            expiresAt: '2026-09-02T00:00:00Z',
            compiledAt: '2026-09-01T00:00:00Z',
          },
        ],
      })
    fallbackStubs.getFallbackProgram = () =>
      Promise.resolve({
        serverTime: '2026-09-01T00:00:00Z',
        fppInstanceUuid: 'barn-uuid',
        published: true,
        signatureBase64: 'c2ln',
        acknowledgedStatus: 'fallback-program-current',
        acknowledgedPackageId: 'pkg-barn',
        acknowledgedAt: '2026-09-01T00:05:00Z',
      })
    const inspector = openInspector()

    await waitFor(() => expect(inspector.getByText('pkg-barn')).toBeInTheDocument())
    expect(inspector.getByText('rev-9')).toBeInTheDocument()
    expect(inspector.getByText('holiday-2026')).toBeInTheDocument()
    expect(inspector.getByText('4')).toBeInTheDocument()
    await waitFor(() => expect(inspector.getByText('Current')).toBeInTheDocument())
    expect(inspector.getByText('Present')).toBeInTheDocument()
  })
})
