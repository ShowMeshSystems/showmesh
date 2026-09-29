import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { FPPInstance, Model, NightSessionState, Node, ResolumeInstance } from '../api'
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

function node(
  nodeId: string,
  state: 'online' | 'offline' | 'unknown',
  signals: string[] = [],
  showParticipation?: Node['showParticipation'],
): Node {
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
    showParticipation,
    render: signals.map(evidence),
    audio: [],
    clock: [],
    fppConnect: [],
  } as unknown as Node
}

function fpp(
  instanceId: string,
  health: FPPInstance['health'],
  uuidChanged = false,
  showParticipation?: FPPInstance['showParticipation'],
): FPPInstance {
  return {
    instanceId,
    endpoint: 'http://198.51.100.1',
    health,
    showParticipation,
    observations: [],
    lastPollAt: null,
    lastPollError: null,
    instanceUuid: 'u',
    instanceUuidFirstObservedAt: null,
    instanceUuidChange: uuidChanged ? ({ previousUuid: 'old', changedAt: '2026-08-28T20:54:00Z' }) : null,
    duplicateInstanceUuidEndpointIds: [],
    playlistObservationRefused: null,
  } as unknown as FPPInstance
}

function resolumeInstance(
  instanceId: string,
  health: ResolumeInstance['health'],
  showParticipation?: ResolumeInstance['showParticipation'],
): ResolumeInstance {
  return {
    instanceId,
    health,
    showParticipation,
    observations: [],
    composition: null,
  } as unknown as ResolumeInstance
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

  it('hides an unselected FPP and an unselected Resolume instance by default, and shows the selected ones', () => {
    renderDashboard({
      fpp: [
        fpp('fpp-selected', 'failed', false, { state: 'participating', show: 'halloween-2026', reason: null }),
        fpp('fpp-unselected', 'failed', false, { state: 'not_participating', show: 'halloween-2026', reason: null }),
      ],
      resolume: [
        resolumeInstance('res-selected', 'failed', { state: 'participating', show: 'halloween-2026', reason: null }),
        resolumeInstance('res-unselected', 'failed', { state: 'not_participating', show: 'halloween-2026', reason: null }),
      ],
    })
    expect(screen.getByText('fpp-selected')).toBeInTheDocument()
    expect(screen.getByText('res-selected')).toBeInTheDocument()
    expect(screen.queryByText('fpp-unselected')).not.toBeInTheDocument()
    expect(screen.queryByText('res-unselected')).not.toBeInTheDocument()
    expect(screen.getByText('4 items')).toBeInTheDocument()
  })

  it('reveals the hidden FPP and Resolume instances when the filter is toggled on, without changing the count', () => {
    renderDashboard({
      fpp: [
        fpp('fpp-selected', 'failed', false, { state: 'participating', show: 'halloween-2026', reason: null }),
        fpp('fpp-unselected', 'failed', false, { state: 'not_participating', show: 'halloween-2026', reason: null }),
      ],
      resolume: [
        resolumeInstance('res-unselected', 'failed', { state: 'not_participating', show: 'halloween-2026', reason: null }),
      ],
    })
    fireEvent.click(screen.getByLabelText('Show unselected instances'))
    expect(screen.getByText('fpp-unselected')).toBeInTheDocument()
    expect(screen.getByText('res-unselected')).toBeInTheDocument()
    expect(screen.getByText('3 items')).toBeInTheDocument()
  })

  it('never shows the all-clear plate when every attention item is hidden by the filter', () => {
    renderDashboard({
      fpp: [
        fpp('fpp-unselected', 'failed', false, { state: 'not_participating', show: 'halloween-2026', reason: null }),
      ],
    })
    expect(screen.queryByText('Nothing needs you')).not.toBeInTheDocument()
    expect(screen.queryByText(/not proof the show looks right/)).not.toBeInTheDocument()
    expect(screen.getByText('1 item')).toBeInTheDocument()
    expect(screen.getByText('1 item concerns an instance the active show did not select.')).toBeInTheDocument()
    expect(screen.queryByText('fpp-unselected')).not.toBeInTheDocument()
    fireEvent.click(screen.getByLabelText('Show unselected instances'))
    expect(screen.getByText('fpp-unselected')).toBeInTheDocument()
  })

  it('pluralizes the all-hidden summary correctly for more than one hidden item', () => {
    renderDashboard({
      fpp: [
        fpp('fpp-unselected', 'failed', false, { state: 'not_participating', show: 'halloween-2026', reason: null }),
      ],
      resolume: [
        resolumeInstance('res-unselected', 'failed', { state: 'not_participating', show: 'halloween-2026', reason: null }),
      ],
    })
    expect(screen.getByText('2 items concern instances the active show did not select.')).toBeInTheDocument()
  })

  it('renders every instance, unfiltered, for an older-coordinator payload without participation', () => {
    renderDashboard({
      fpp: [fpp('fpp-1', 'failed'), fpp('fpp-2', 'degraded')],
      resolume: [resolumeInstance('res-1', 'failed')],
    })
    expect(screen.getByText('fpp-1')).toBeInTheDocument()
    expect(screen.getByText('fpp-2')).toBeInTheDocument()
    expect(screen.getByText('res-1')).toBeInTheDocument()
    expect(screen.queryByLabelText('Show unselected instances')).not.toBeInTheDocument()
  })

  it('sorts a participating node above a non-participating node whose tone would otherwise put it first', () => {
    renderDashboard({
      nodes: [
        node('offline-node', 'offline', [], { state: 'not_participating', show: 'halloween-2026', reason: null }),
        node('unknown-node', 'unknown', [], { state: 'participating', show: 'halloween-2026', reason: null }),
      ],
    })
    const rows = screen.getAllByText(/-node$/).map((el) => el.textContent)
    expect(rows).toEqual(['unknown-node', 'offline-node'])
  })

  it.each([
    ['participating', "Participating in tonight's show."],
    ['not_participating', "Not participating in tonight's show."],
    ['unknown', 'Participation unknown.'],
    ['not_configured', 'No active show.'],
  ] as const)('renders the operator-facing sentence for %s on the row', (state, sentence) => {
    renderDashboard({ nodes: [node('media-garage', 'offline', [], { state, show: 'halloween-2026', reason: null })] })
    expect(screen.getByText(sentence)).toBeInTheDocument()
  })

  it('puts the participation sentence before the action link, not after', () => {
    renderDashboard({
      nodes: [node('media-garage', 'offline', [], { state: 'participating', show: 'halloween-2026', reason: null })],
    })
    const detail = screen.getByText(/Participating in tonight's show\./).closest('p')
    expect(detail).not.toBeNull()
    const text = detail!.textContent ?? ''
    expect(text.indexOf("Participating in tonight's show.")).toBeLessThan(text.indexOf('Open'))
  })

  it('says nothing about participation for an older coordinator that never sent the field', () => {
    renderDashboard({ nodes: [node('media-garage', 'offline')] })
    expect(screen.queryByText(/Participating in|Not participating in|Participation unknown|No active show/)).not.toBeInTheDocument()
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
    expect(verdict?.fact).toContain('last prepared')
    expect(verdict?.gated).toBe(true)
  })

  it('clears the next start only when the run is ready, fresh and this epoch', () => {
    const verdict = nextStartVerdict(session({}), '2026-08-28T21:07:00Z')
    expect(verdict?.state).toBe('Next start clear')
    expect(verdict?.fact).toContain('14 of 14 checks')
  })

  it('does not gate the next start when the run is ready_with_warnings, fresh and this epoch, and warns instead of reading as a pass or a refusal', () => {
    const verdict = nextStartVerdict(session({ outcome: 'ready_with_warnings' }), '2026-08-28T21:07:00Z')
    expect(verdict?.state).not.toBe('Next start clear')
    expect(verdict?.state).not.toBe('Next start gated')
    expect(verdict?.tone).toBe('warn')
    expect(verdict?.gated).not.toBe(true)
    expect(verdict?.fact).toMatch(/will start/i)
  })

  it('still gates the next start for a ready_with_warnings run that is no longer fresh, naming the freshness reason and not the outcome', () => {
    const verdict = nextStartVerdict(session({ outcome: 'ready_with_warnings', fresh: false }), '2026-08-28T21:07:00Z')
    expect(verdict?.state).toBe('Next start gated')
    expect(verdict?.gated).toBe(true)
    expect(verdict?.fact).toContain('no longer fresh')
    expect(verdict?.fact).not.toContain('ready_with_warnings')
  })

  it('still gates the next start for a ready_with_warnings run from an earlier epoch, naming the epoch reason and not the outcome', () => {
    const verdict = nextStartVerdict(session({ outcome: 'ready_with_warnings', sameEpoch: false }), '2026-08-29T01:34:00Z')
    expect(verdict?.state).toBe('Next start gated')
    expect(verdict?.gated).toBe(true)
    expect(verdict?.fact).toContain('last prepared')
    expect(verdict?.fact).not.toContain('ready_with_warnings')
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

})
