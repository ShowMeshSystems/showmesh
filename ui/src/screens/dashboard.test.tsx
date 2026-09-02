import { cleanup, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { FPPInstance, Model, NightSessionState, Node } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'
import { Dashboard } from './Dashboard'
import { fleetCounts, fppDetail, nextStartVerdict, nodesDetail } from './dashboardModel'

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, getCurrentNightSession: () => new Promise(() => {}) }
})

const evidence = (state: string) => ({
  resource: { kind: 'node', id: 'n' },
  signal: 's',
  value: 1,
  unit: null,
  state,
  reason: null,
  observedAt: '2026-08-28T21:00:00Z',
  collectedAt: '2026-08-28T21:00:00Z',
  source: 'agent',
  quality: 'reported',
}) as unknown as Node['render'][number]

function node(nodeId: string, state: 'online' | 'offline' | 'unknown', signals: string[] = []): Node {
  return {
    nodeId,
    label: nodeId,
    platform: null,
    agentVersion: null,
    bootId: null,
    startedAt: null,
    firstSeenAt: '2026-08-28T12:00:00Z',
    updatedAt: '2026-08-28T21:00:00Z',
    capabilities: [],
    controlPlane: { state, reason: state === 'offline' ? 'No heartbeat.' : null },
    evidence: {
      hello: evidence('current'),
      lastWill: evidence('not_collected'),
      heartbeat: evidence(state === 'offline' ? 'stale' : 'current'),
    },
    declaration: {} as Node['declaration'],
    render: signals.map(evidence),
    audio: [],
    fppConnect: [],
  } as unknown as Node
}

function fpp(instanceId: string, health: FPPInstance['health'], uuidChanged = false): FPPInstance {
  return {
    instanceId,
    endpoint: 'http://198.51.100.1',
    health,
    observations: [],
    lastPollAt: null,
    lastPollError: null,
    instanceUuid: 'u',
    instanceUuidFirstObservedAt: null,
    instanceUuidChange: uuidChanged ? ({ previousUuid: 'old', changedAt: '2026-08-28T20:54:00Z' }) : null,
    duplicateInstanceUuidEndpointIds: [],
  } as unknown as FPPInstance
}

function session(readiness: Partial<NightSessionState['readiness']>): NightSessionState {
  return {
    id: 'n1',
    state: 'live',
    stateEnteredAt: '2026-08-28T18:00:00Z',
    cycle: 3,
    degraded: false,
    readiness: {
      state: 'recorded',
      reason: '',
      outcome: 'ready',
      completedAt: '2026-08-28T20:55:00Z',
      sameEpoch: true,
      fresh: true,
      checks: Array.from({ length: 14 }, () => ({ name: 'c', state: 'healthy', reason: '' })),
      ...readiness,
    },
  } as unknown as NightSessionState
}

function renderDashboard(model: Partial<Model>) {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model }}>
      <MemoryRouter>
        <Dashboard />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Dashboard', () => {
  afterEach(cleanup)

  it('renders the mock’s three blocks, in order, as real headings', () => {
    renderDashboard({})
    const headings = screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)
    expect(headings).toEqual(['Readiness', 'Needs you', 'System health'])
  })

  it('says the night session has not reported rather than inventing a verdict', () => {
    renderDashboard({ nightSession: null })
    expect(screen.getByText('The night session has not reported to this device yet.')).toBeInTheDocument()
    expect(screen.queryByText('Running')).not.toBeInTheDocument()
  })

  it('keeps the empty attention list’s caveat', () => {
    renderDashboard({})
    expect(screen.getByText('Nothing needs you')).toBeInTheDocument()
    expect(screen.getByText(/not proof the show looks right/)).toBeInTheDocument()
  })

  it('reports an offline node as a labelled pair with its own state word', () => {
    renderDashboard({ nodes: [node('media-garage', 'offline')], serverTime: '2026-08-28T21:07:00Z', serverTimeReceivedAt: Date.now() })
    expect(screen.getByText(/OFFLINE|Offline/)).toBeInTheDocument()
    expect(screen.getByText(/stopped reporting/)).toBeInTheDocument()
  })

  it('holds bindings as its own item when an FPP instance changes identity', () => {
    renderDashboard({ fpp: [fpp('barn-player', 'healthy', true)] })
    expect(screen.getByText(/Bindings held/i)).toBeInTheDocument()
    expect(screen.getByText(/changed its instance identity/)).toBeInTheDocument()
  })

  it('counts an unknown node as neither online nor offline', () => {
    const counts = fleetCounts({ ...initialModel(), nodes: [node('a', 'online'), node('b', 'unknown')] })
    expect(counts.nodesOnline).toBe(1)
    expect(counts.nodesUnknown).toBe(1)
    expect(nodesDetail(counts)).toBe('1 unknown')
  })

  it('counts signals by their evidence state', () => {
    const counts = fleetCounts({
      ...initialModel(),
      nodes: [node('a', 'online', ['current', 'current', 'stale', 'not_collected', 'unsupported'])],
    })
    expect(counts.signals).toMatchObject({ total: 5, current: 2, stale: 1, unobserved: 1, unavailable: 1 })
  })

  it('names a held import in the FPP tile', () => {
    expect(fppDetail([fpp('a', 'healthy'), fpp('b', 'healthy', true)])).toBe('healthy · 1 import held')
  })

  it('gates the next start when readiness ran in an earlier epoch', () => {
    const verdict = nextStartVerdict(session({ sameEpoch: false }), '2026-08-29T01:34:00Z')
    expect(verdict?.state).toBe('Next start gated')
    expect(verdict?.fact).toContain('earlier epoch')
    expect(verdict?.gated).toBe(true)
  })

  it('clears the next start only when the run is ready, fresh and this epoch', () => {
    const verdict = nextStartVerdict(session({}), '2026-08-28T21:07:00Z')
    expect(verdict?.state).toBe('Next start clear')
    expect(verdict?.fact).toContain('14 of 14 checks')
  })

  it('never reads an unknown readiness state as a pass', () => {
    const base = session({})
    const readiness = { ...base.readiness }
    delete readiness.outcome
    const unknown: NightSessionState = { ...base, readiness: { ...readiness, state: 'unknown' } }
    const verdict = nextStartVerdict(unknown, '2026-08-28T21:07:00Z')
    expect(verdict?.tone).toBe('unknown')
    expect(verdict?.state).toBe('Readiness unknown')
  })

  it('puts the health footnote under the tiles, not above them', () => {
    renderDashboard({})
    const section = screen.getByRole('region', { name: 'System health' })
    expect(within(section).getByText(/Health is each resource's own report/)).toBeInTheDocument()
  })
})
