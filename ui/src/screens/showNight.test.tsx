import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { FPPInstance, Model, NightSessionState } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'
import { ShowNight } from './ShowNight'
import { evidenceReadouts, nextTransition, runOfShow } from './showNightModel'

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, getCurrentNightSession: () => new Promise(() => {}) }
})

function session(overrides: Partial<NightSessionState> = {}): NightSessionState {
  return {
    id: 'winter-ridge-night',
    configObjectId: 'night.session/winter-ridge',
    configRevision: 4,
    state: 'live',
    stateEnteredAt: '2026-08-28T21:02:22Z',
    cycle: 3,
    finalShowRequested: false,
    finalShowRequestedAt: null,
    admissionClosed: false,
    admissionClosedAt: null,
    shutdownIntent: '',
    armedShowId: 'winter-ridge-2026',
    showCommitted: true,
    readiness: { state: 'recorded', reason: '', outcome: 'ready', completedAt: '2026-08-28T16:28:00Z', sameEpoch: false, fresh: false, checks: [] },
    powerPhase: { state: 'recorded', reason: 'Garage projector never reported on.' },
    transition: { state: 'recorded', reason: 'Enter-show transition completed.' },
    cues: { state: 'recorded', reason: '', cues: [] },
    backgroundAudio: { state: 'recorded', reason: 'Ducked to -18 dB.', steps: [] },
    degraded: false,
    attributionDegraded: false,
    authorization: { state: 'recorded', reason: '', principalName: 'erbartos', command: 'start-night', recordedAt: '2026-08-28T21:02:14Z' },
    updatedAt: '2026-08-28T21:07:00Z',
    ...overrides,
  } as unknown as NightSessionState
}

const cue = (over: Record<string, unknown>) =>
  ({ name: 'Step', phase: 'enterResting', role: 'Resolume', action: 'blackout', actionRevision: 8, state: 'not_dispatched', dispatchedAt: null, resolvedAt: null, ...over }) as never

function renderScreen(model: Partial<Model>) {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model }}>
      <MemoryRouter>
        <ShowNight />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Show Night', () => {
  afterEach(cleanup)

  it('says the session has not reported rather than showing a cycle it does not know', () => {
    renderScreen({ nightSession: null })
    expect(screen.getByText('The night session has not reported yet')).toBeInTheDocument()
    expect(screen.queryByText(/Cycle \d of the night/)).not.toBeInTheDocument()
  })

  it('renders the mock’s three h2 blocks in order', () => {
    renderScreen({ nightSession: session() })
    expect(screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)).toEqual([
      'Lifecycle commands',
      'Run of Show',
      'Evidence',
    ])
  })

  it('titles the page with the cycle the session reports', () => {
    renderScreen({ nightSession: session({ cycle: 3 }) })
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Cycle 3 of the night')
  })

  it('gives an armed step a settled state, not the never-collected one', () => {
    const steps = runOfShow(session({ cues: { state: 'recorded', reason: '', cues: [cue({})] } } as never))
    expect(steps[0]?.state).toBe('Armed')
    expect(steps[0]?.tone).toBe('pending')
    expect(steps[0]?.tone).not.toBe('unknown')
  })

  it('treats unconfirmable as unavailable, not as a failure', () => {
    const steps = runOfShow(
      session({ cues: { state: 'recorded', reason: '', cues: [cue({ state: 'resolved', outcome: 'unconfirmable' })] } } as never),
    )
    expect(steps[0]?.tone).toBe('pending')
  })

  it('marks a refused step as a failure with its reason', () => {
    const steps = runOfShow(
      session({
        cues: { state: 'recorded', reason: '', cues: [cue({ state: 'resolved', outcome: 'refused', reason: 'no route to host' })] },
      } as never),
    )
    expect(steps[0]?.tone).toBe('bad')
    expect(steps[0]?.resolved).toBe('no route to host')
  })

  it('never reads "recorded" as a health verdict', () => {
    const readouts = evidenceReadouts(session(), '2026-08-28T21:07:00Z')
    const power = readouts.find((entry) => entry.key === 'power')
    expect(power?.label).toBe('Power phase recorded')
    expect(power?.tone).not.toBe('good')
  })

  it('says an earlier-epoch readiness is gated, with both reasons', () => {
    const readouts = evidenceReadouts(session(), '2026-08-28T21:07:00Z')
    const readiness = readouts.find((entry) => entry.key === 'readiness')
    expect(readiness?.tone).toBe('warn')
    expect(readiness?.fact).toContain('from an earlier epoch')
    expect(readiness?.fact).toContain('no longer fresh')
  })

  it('says a degraded attribution never clears', () => {
    const readouts = evidenceReadouts(session({ attributionDegraded: true }), '2026-08-28T21:07:00Z')
    const attribution = readouts.find((entry) => entry.key === 'attribution')
    expect(attribution?.tone).toBe('bad')
    expect(attribution?.fact).toContain('never clears')
  })

  it('makes the boundary unknown when the observed position is stale', () => {
    const instance = {
      instanceId: 'main',
      observations: [
        { signal: 'fpp.position.elapsed.seconds', value: 102, state: 'stale', resource: { kind: 'fpp', id: 'main' } },
        { signal: 'fpp.position.seconds', value: 168, state: 'stale', resource: { kind: 'fpp', id: 'main' } },
      ],
    } as unknown as FPPInstance
    const next = nextTransition({ ...initialModel(), fpp: [instance] })
    expect(next.known).toBe(false)
    if (!next.known) expect(next.reason).toContain('unknown rather than assumed')
  })
})
