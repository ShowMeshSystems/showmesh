import { cleanup, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import type { Model, Node } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'
import { MonitorSignals } from './MonitorSignals'
import { countSignals, signalGroups, signalTally } from '../domain/evidence'
import { signalRows, signalSummary, facetCounts } from './monitorModel'
import { fleetCounts } from './dashboardModel'

const observation = (signal: string, state = 'current', value: string | null = 'x') =>
  ({
    resource: { kind: 'surface', id: 'front' },
    signal,
    value,
    unit: null,
    state,
    reason: state === 'current' ? null : 'because it says so',
    observedAt: state === 'not_collected' ? null : '2026-08-28T21:06:58Z',
    collectedAt: '2026-08-28T21:06:58Z',
    source: 'agent',
    quality: 'reported',
  }) as unknown as Node['render'][number]

function node(nodeId: string, render: Node['render'] = []): Node {
  return {
    nodeId,
    label: nodeId,
    agentVersion: '0.9.4',
    capabilities: [],
    controlPlane: { state: 'online', reason: null },
    evidence: { hello: observation('node.hello'), lastWill: observation('node.last_will'), heartbeat: observation('node.heartbeat') },
    declaration: {},
    render,
    audio: [],
    clock: [],
    fppConnect: [],
  } as unknown as Node
}

function renderScreen(model: Partial<Model>) {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model, serverTime: '2026-08-28T21:07:00Z', serverTimeReceivedAt: Date.now() }}>
      <MemoryRouter>
        <MonitorSignals />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Monitor · Signals', () => {
  afterEach(cleanup)

  it('names the facet heading', () => {
    renderScreen({})
    expect(screen.getByRole('heading', { level: 2, name: 'Signals' })).toBeInTheDocument()
  })

  it('lists every observation across nodes, FPP and Resolume in one table', () => {
    renderScreen({
      nodes: [node('media-front', [observation('surface.pipeline.state')])],
      fpp: [{ instanceId: 'barn-player', health: 'healthy', observations: [observation('fpp.playlist.state')], lastPollAt: null, lastPollError: null, instanceUuidChange: null } as never],
      resolume: [{ instanceId: 'arena', health: 'healthy', observations: [observation('resolume.reachable')], composition: null } as never],
      snapshotReceivedAt: Date.now(),
    })
    const table = screen.getByRole('region', { name: 'Signals, scrollable' })
    expect(within(table).getByText('surface.pipeline.state')).toBeInTheDocument()
    expect(within(table).getByText('fpp.playlist.state')).toBeInTheDocument()
    expect(within(table).getByText('resolume.reachable')).toBeInTheDocument()
  })

  it('shows the loading absence before the first snapshot, distinct from a settled empty', () => {
    renderScreen({ snapshotReceivedAt: null })
    expect(screen.getByText('No signal history has arrived yet.')).toBeInTheDocument()
    cleanup()
    renderScreen({ snapshotReceivedAt: Date.now() })
    expect(screen.getByText('No resource has reported a signal.')).toBeInTheDocument()
  })

  it('gives an unobserved signal the dashed unknown tone, never a failure tone', () => {
    const rows = signalRows({ ...initialModel(), nodes: [node('a', [observation('surface.frames.rate', 'not_collected', null)])] }, '2026-08-28T21:07:00Z')
    expect(rows[0]?.state).toBe('Unobserved')
    expect(rows[0]?.tone).toBe('unknown')
  })

  it('labels a signal with nothing to measure N/A, with no unobserved or warning treatment', () => {
    const entry = observation('audio_session.restore.next_attempt_ms', 'not_applicable', null)
    const rows = signalRows({ ...initialModel(), nodes: [node('a', [entry])] }, '2026-08-28T21:07:00Z')
    expect(rows[0]?.state).toBe('N/A')
    expect(rows[0]?.tone).toBe('pending')
    expect(rows[0]?.value).toBe('None')
    expect(signalSummary(rows)).toBe('1 signals · 0 current, 0 stale, 0 unobserved, 0 failed, 0 unavailable, 1 N/A.')
  })

  it('never confuses a stale signal with an unobserved one', () => {
    const rows = signalRows({ ...initialModel(), nodes: [node('a', [observation('surface.frames.rate', 'stale')])] }, '2026-08-28T21:07:00Z')
    expect(rows[0]?.state).toBe('Stale')
    expect(rows[0]?.tone).toBe('warn')
  })

  it('counts the same signals from the tile path and the footer path, for every state', () => {
    const states = ['current', 'stale', 'not_collected', 'collection_failed', 'unsupported', 'unknown_age', 'not_applicable']
    const base = node('a', states.map((state) => observation(`surface.${state}`, state, state === 'not_applicable' ? null : 'x')))
    const clock = [
      observation('node.audio.clock.alignment.state', 'current', 'within_threshold'),
      observation('node.clock.offset', 'collection_failed', null),
      observation('node.clock.skew', 'stale'),
    ]
    const model = { ...initialModel(), nodes: [{ ...base, clock } as Node] }
    const rows = signalRows(model, '2026-08-28T21:07:00Z')
    expect(rows.find((row) => row.signal === 'node.audio.clock.alignment.state')?.state).toBe('within threshold')

    const tile = fleetCounts(model).signals
    expect(tile).toMatchObject({ total: 10, current: 2, stale: 2, unobserved: 1, failed: 2, unavailable: 2, notApplicable: 1 })
    expect(facetCounts(model).signals).toBe(tile.total)
    expect(signalSummary(rows)).toBe('10 signals · 2 current, 2 stale, 1 unobserved, 2 failed, 2 unavailable, 1 N/A.')
    const footer = signalTally(countSignals(signalGroups(model)))
    expect(footer.map((entry) => entry.count)).toEqual([2, 2, 1, 2, 2, 1])
  })
})
