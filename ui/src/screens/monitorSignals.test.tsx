import { cleanup, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import type { Model, Node } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'
import { MonitorSignals } from './MonitorSignals'
import { signalRows } from './monitorModel'

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

  it('never confuses a stale signal with an unobserved one', () => {
    const rows = signalRows({ ...initialModel(), nodes: [node('a', [observation('surface.frames.rate', 'stale')])] }, '2026-08-28T21:07:00Z')
    expect(rows[0]?.state).toBe('Stale')
    expect(rows[0]?.tone).toBe('warn')
  })
})
