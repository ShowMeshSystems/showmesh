import { cleanup, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import type { Event, FPPInstance, Model, Node } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'
import { Monitor } from './Monitor'
import { activityRows, facetCounts, fleetRows, fleetSummary, nodeInspector } from './monitorModel'

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

function renderScreen(model: Partial<Model>) {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model, serverTime: '2026-08-28T21:07:00Z', serverTimeReceivedAt: Date.now() }}>
      <MemoryRouter>
        <Monitor />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Monitor · Fleet', () => {
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

  it('says what the fleet contains without arithmetic that does not close', () => {
    const rows = fleetRows(
      { ...initialModel(), nodes: [node('a', 'online')], fpp: [fpp('b', 'healthy')] },
      '2026-08-28T21:07:00Z',
    )
    expect(fleetSummary(rows)).toContain('2 resources')
    expect(fleetSummary(rows)).toContain('1 node, 1 FPP player, 0 Resolume instances')
  })

  it('says a node never advertised audio rather than showing it as failing', () => {
    const inspector = nodeInspector(node('media-garage', 'offline'), '2026-08-28T21:07:00Z')
    const audio = inspector.groups.find((group) => group.name === 'Audio')
    expect(audio?.absent).toContain('never claimed an audio capability')
    expect(audio?.absent).toContain('Distinct from an audio path that is failing')
  })

  it('never wraps a signal value in a status chip', () => {
    const inspector = nodeInspector(
      node('a', 'online', [observation('surface.frames.rate', 'stale')]),
      '2026-08-28T21:07:00Z',
    )
    const render = inspector.groups.find((group) => group.name === 'Render')
    expect(render?.rows[0]?.value).toBe('x')
    expect(render?.rows[0]?.state).toContain('stale')
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
