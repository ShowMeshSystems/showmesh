import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { FPPInstance, Model, NightSessionState } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'
import { ShowNight } from './ShowNight'
import { cycleRail, evidenceReadouts, nextTransition, nightRail, runOfShow } from './showNightModel'

const stubs = vi.hoisted(() => ({
  dispatchNightCommand: (() => Promise.resolve({})) as (...args: never[]) => Promise<unknown>,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    getCurrentNightSession: () => new Promise(() => {}),
    dispatchNightCommand: (...args: never[]) => stubs.dispatchNightCommand(...args),
  }
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

  it('renders a placeholder for every earlier cycle and the live one for the current cycle', () => {
    const rail = nightRail(session({ cycle: 3, state: 'live' }))
    const cycleSteps = rail.filter((step) => step.key.startsWith('cycle-'))
    expect(cycleSteps.map((step) => step.key)).toEqual(['cycle-1', 'cycle-2', 'cycle-3'])
    expect(cycleSteps[0]?.status).toBe('notWired')
    expect(cycleSteps[0]?.detail).toBe('not reported')
    expect(cycleSteps[1]?.status).toBe('notWired')
    expect(cycleSteps[1]?.detail).toBe('not reported')
    expect(cycleSteps[2]?.status).toBe('now')
    expect(cycleSteps[2]?.detail).toBe('live')
  })

  it('never puts a clock time on an earlier-cycle placeholder', () => {
    const rail = nightRail(session({ cycle: 3, state: 'live' }))
    const earlier = rail.filter((step) => step.status === 'notWired')
    expect(earlier).not.toHaveLength(0)
    for (const step of earlier) {
      expect(step.detail).not.toMatch(/\d{1,2}:\d{2}/)
    }
  })

  it('renders the not-wired banner and placeholder steps on the page when earlier cycles exist', () => {
    renderScreen({ nightSession: session({ cycle: 3, state: 'live' }) })
    expect(screen.getByText('The night timeline does nothing yet.')).toBeInTheDocument()
    expect(screen.getByText('Cycle 1')).toBeInTheDocument()
    expect(screen.getAllByText('not reported').length).toBeGreaterThan(0)
  })

  const allowedSession = {
    serverTime: '2026-08-28T21:07:00Z',
    authenticated: true,
    principal: { id: 'p', name: 'op', role: 'operator', disabled: false },
    session: null,
    credentialForm: 'session',
    scopes: ['night:command'],
    scopesState: 'current',
    bootstrapRequired: false,
  } as never

  it('shows a Refused chip, never an Accepted one, when a night command is refused', async () => {
    stubs.dispatchNightCommand = () => Promise.reject(new Error('no route to host'))
    renderScreen({ nightSession: session(), session: allowedSession })
    fireEvent.click(screen.getByRole('button', { name: 'End session' }))
    expect(await screen.findByText('Refused')).toBeInTheDocument()
    expect(screen.queryByText('Accepted')).not.toBeInTheDocument()
    expect(screen.getByText(/was refused: no route to host/)).toBeInTheDocument()
  })

  it('shows an Accepted chip, never a Refused one, when a night command is accepted', async () => {
    stubs.dispatchNightCommand = () => Promise.resolve({})
    renderScreen({ nightSession: session(), session: allowedSession })
    fireEvent.click(screen.getByRole('button', { name: 'End session' }))
    expect(await screen.findByText('Accepted')).toBeInTheDocument()
    expect(screen.queryByText('Refused')).not.toBeInTheDocument()
  })

  it('renders a single off-cycle row, not a four-row dead rail, for a state outside the repeating cycle', () => {
    const rail = cycleRail(session({ state: 'preshow' }), '2026-08-28T21:07:00Z')
    expect(rail).toHaveLength(1)
    expect(rail[0]?.label).toBe('Preshow')
    expect(rail[0]?.detail).toContain('Not in the repeating cycle')
    expect(rail[0]?.status).toBe('now')
  })

  it('names another off-cycle state honestly instead of four rows of "not reported"', () => {
    const rail = cycleRail(session({ state: 'fading-out' }), '2026-08-28T21:07:00Z')
    expect(rail).toHaveLength(1)
    expect(rail[0]?.label).toBe('Fading out')
    expect(rail.every((step) => step.detail !== 'not reported')).toBe(true)
  })

  it('still renders the full four-step cycle rail for a state inside the cycle', () => {
    const rail = cycleRail(session({ state: 'live' }), '2026-08-28T21:07:00Z')
    expect(rail).toHaveLength(4)
    expect(rail.map((step) => step.label)).toEqual(['Resting', 'To show', 'Live', 'To resting'])
  })

  it('does not link "Edit definition" to the show config route, and marks it not wired', () => {
    renderScreen({ nightSession: session({ cycle: 1 }) })
    expect(screen.queryByRole('link', { name: 'Edit definition' })).not.toBeInTheDocument()
    const editButton = screen.getByRole('button', { name: 'Edit definition' })
    expect(editButton).toBeDisabled()
    expect(screen.getByText('Not wired')).toBeInTheDocument()
  })

  it('has no earlier cycles and no not-wired banner when the session is on cycle 1', () => {
    const rail = nightRail(session({ cycle: 1, state: 'live' }))
    const cycleSteps = rail.filter((step) => step.key.startsWith('cycle-'))
    expect(cycleSteps).toHaveLength(1)
    expect(cycleSteps[0]?.status).toBe('now')
    expect(rail.some((step) => step.status === 'notWired')).toBe(false)

    renderScreen({ nightSession: session({ cycle: 1, state: 'live' }) })
    expect(screen.queryByText('The night timeline does nothing yet.')).not.toBeInTheDocument()
    expect(screen.queryByText('not reported')).not.toBeInTheDocument()
  })
})
