import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, PROBLEM_TYPE, type FPPInstance, type Model, type NightSessionState } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'
import { ShowNight } from './ShowNight'
import { cycleRail, evidenceReadouts, nextTransition, nightRail, runOfShow } from './showNightModel'

const stubs = vi.hoisted(() => ({
  dispatchNightCommand: (() => Promise.resolve({})) as (...args: never[]) => Promise<unknown>,
  getNightSessionActiveConfig: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putNightSessionActiveConfig: (() => Promise.resolve({})) as (...args: never[]) => Promise<unknown>,
  getNightSessionActiveConfigRevisions: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  listConfigObjects: (() => Promise.resolve({ serverTime: '', kind: 'night.session', objects: [] })) as (
    ...args: never[]
  ) => Promise<unknown>,
  getNightSessionConfig: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putNightSessionConfig: (() => Promise.resolve({})) as (...args: never[]) => Promise<unknown>,
}))

function commandResponse(command: string, outcome: 'applied' | 'idempotent_no_op' = 'applied', attributionDegraded = false) {
  return { serverTime: '2026-08-28T21:07:00Z', command: { command, outcome, attributionDegraded }, session: {} }
}

function activeConfigResponse(session: string, overrides: Record<string, unknown> = {}) {
  return {
    serverTime: '2026-08-28T21:07:00Z',
    kind: 'night.session.active',
    id: 'night.session.active',
    revision: 3,
    payload: { session },
    updatedAt: '2026-08-28T16:00:00Z',
    createdByPrincipalId: 'p',
    createdByPrincipalName: 'erbartos',
    source: 'api',
    ...overrides,
  }
}

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    getCurrentNightSession: () => new Promise(() => {}),
    dispatchNightCommand: (...args: never[]) => stubs.dispatchNightCommand(...args),
    getNightSessionActiveConfig: (...args: never[]) => stubs.getNightSessionActiveConfig(...args),
    putNightSessionActiveConfig: (...args: never[]) => stubs.putNightSessionActiveConfig(...args),
    getNightSessionActiveConfigRevisions: (...args: never[]) => stubs.getNightSessionActiveConfigRevisions(...args),
    listConfigObjects: (...args: never[]) => stubs.listConfigObjects(...args),
    getNightSessionConfig: (...args: never[]) => stubs.getNightSessionConfig(...args),
    putNightSessionConfig: (...args: never[]) => stubs.putNightSessionConfig(...args),
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
  afterEach(() => {
    cleanup()
    stubs.getNightSessionActiveConfig = () => new Promise(() => {})
    stubs.putNightSessionActiveConfig = () => Promise.resolve({})
    stubs.listConfigObjects = () => Promise.resolve({ serverTime: '', kind: 'night.session', objects: [] })
    stubs.getNightSessionConfig = () => new Promise(() => {})
    stubs.putNightSessionConfig = () => Promise.resolve({})
  })

  it('says the session has not reported rather than showing a cycle it does not know', () => {
    renderScreen({ nightSession: null })
    expect(screen.getByText('The night session has not reported yet')).toBeInTheDocument()
    expect(screen.queryByText(/Cycle \d of the night/)).not.toBeInTheDocument()
  })

  it('keeps definition authoring after the mock’s lifecycle and evidence blocks', () => {
    renderScreen({ nightSession: session() })
    expect(screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)).toEqual([
      'Lifecycle commands',
      'Run of Show',
      'Evidence',
      'Night session activation',
      'Night session definitions',
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

  const allowedSessionBase = {
    serverTime: '2026-08-28T21:07:00Z',
    authenticated: true,
    principal: { id: 'p', name: 'op', role: 'operator', disabled: false },
    session: null,
    credentialForm: 'session',
    scopes: ['night:command'],
    scopesState: 'current',
    bootstrapRequired: false,
  }
  const allowedSession = allowedSessionBase as never
  const configWriteSession = { ...allowedSessionBase, scopes: ['config:write'] } as never

  it('shows a Refused chip, never an Accepted one, when a night command is refused', async () => {
    stubs.dispatchNightCommand = () => Promise.reject(new Error('no route to host'))
    renderScreen({ nightSession: session(), session: allowedSession })
    fireEvent.click(screen.getByRole('button', { name: 'End session' }))
    expect(await screen.findByText('Refused')).toBeInTheDocument()
    expect(screen.queryByText('Accepted')).not.toBeInTheDocument()
    expect(screen.getByText(/was refused: no route to host/)).toBeInTheDocument()
  })

  it('shows an Accepted chip, never a Refused one, when a night command is accepted', async () => {
    stubs.dispatchNightCommand = () => Promise.resolve(commandResponse('end-session', 'applied'))
    renderScreen({ nightSession: session(), session: allowedSession })
    fireEvent.click(screen.getByRole('button', { name: 'End session' }))
    expect(await screen.findByText('Accepted')).toBeInTheDocument()
    expect(screen.queryByText('Refused')).not.toBeInTheDocument()
  })

  it('renders the three previously missing lifecycle commands, in the contract’s order', () => {
    renderScreen({ nightSession: session(), session: allowedSession })
    const names = ['Prepare site', 'Start preshow', 'Start night', 'Request final show', 'Fade out night', 'Power down presentation', 'End session']
    const buttons = screen.getAllByRole('button').filter((b) => names.includes(b.textContent ?? ''))
    expect(buttons.map((b) => b.textContent)).toEqual(names)
  })

  it('leaves a command enabled regardless of session.state: the contract publishes no valid-from-state table for any command', () => {
    renderScreen({ nightSession: session({ state: 'live' }), session: allowedSession })
    expect(screen.getByRole('button', { name: 'Start night' })).not.toBeDisabled()
  })

  it('reports idempotent_no_op distinguishably from applied', async () => {
    stubs.dispatchNightCommand = () => Promise.resolve(commandResponse('end-session', 'idempotent_no_op'))
    renderScreen({ nightSession: session(), session: allowedSession })
    fireEvent.click(screen.getByRole('button', { name: 'End session' }))
    expect(await screen.findByText('No-op')).toBeInTheDocument()
    expect(screen.queryByText('Accepted')).not.toBeInTheDocument()
    expect(screen.getByText(/reports idempotent_no_op/)).toBeInTheDocument()
  })

  it('surfaces attributionDegraded on the reported outcome', async () => {
    stubs.dispatchNightCommand = () => Promise.resolve(commandResponse('end-session', 'applied', true))
    renderScreen({ nightSession: session(), session: allowedSession })
    fireEvent.click(screen.getByRole('button', { name: 'End session' }))
    await screen.findByText('Accepted')
    expect(screen.getByText(/attributionDegraded/)).toBeInTheDocument()
  })

  it('reuses the same idempotencyKey across a double press of prepare-site', () => {
    const calls: unknown[][] = []
    stubs.dispatchNightCommand = (...args: unknown[]) => {
      calls.push(args)
      return new Promise(() => {})
    }
    renderScreen({ nightSession: session({ id: '', state: 'inactive' }), session: allowedSession })
    const button = screen.getByRole('button', { name: 'Prepare site' })
    fireEvent.click(button)
    fireEvent.click(button)
    expect(calls).toHaveLength(2)
    expect(calls[0]?.[1]).toEqual(expect.any(String))
    expect(calls[0]?.[1]).toBe(calls[1]?.[1])
  })

  it('distinguishes night-not-ready from a plain refusal, and names the withheld reason verbatim', async () => {
    stubs.dispatchNightCommand = () =>
      Promise.reject(
        new ApiError('Withheld by interlock projector-cooldown: lamp above 40 °C.', 409, PROBLEM_TYPE.nightNotReady),
      )
    renderScreen({ nightSession: session(), session: allowedSession })
    fireEvent.click(screen.getByRole('button', { name: 'Power down presentation' }))
    expect(await screen.findByText('Withheld')).toBeInTheDocument()
    expect(screen.getByText('Withheld by interlock projector-cooldown: lamp above 40 °C.')).toBeInTheDocument()
  })

  it('distinguishes night-state-rejected from night-not-ready', async () => {
    stubs.dispatchNightCommand = () =>
      Promise.reject(new ApiError('start-night is not valid while live.', 409, PROBLEM_TYPE.nightStateRejected))
    renderScreen({ nightSession: session(), session: allowedSession })
    fireEvent.click(screen.getByRole('button', { name: 'Start night' }))
    expect(await screen.findByText('Refused')).toBeInTheDocument()
    expect(screen.getByText(/is not valid from the session's current state/)).toBeInTheDocument()
    expect(screen.queryByText('Withheld')).not.toBeInTheDocument()
  })

  it('offers the end-session/prepare-site recovery recipe on night-ambiguous', async () => {
    stubs.dispatchNightCommand = () =>
      Promise.reject(new ApiError('This session is degraded.', 409, PROBLEM_TYPE.nightAmbiguous))
    renderScreen({ nightSession: session(), session: allowedSession })
    fireEvent.click(screen.getByRole('button', { name: 'Fade out night' }))
    expect(await screen.findByText('Degraded')).toBeInTheDocument()
    expect(screen.getByText(/Recover with end-session, then prepare-site\./)).toBeInTheDocument()
  })

  it('reports the audit-unavailable 503 as not dispatched, not as a failure', async () => {
    stubs.dispatchNightCommand = () =>
      Promise.reject(
        new ApiError('The audit store could not be written.', 503, PROBLEM_TYPE.nightCommandRefusedAuditUnavailable),
      )
    renderScreen({ nightSession: session(), session: allowedSession })
    fireEvent.click(screen.getByRole('button', { name: 'Prepare site' }))
    expect(await screen.findByText('Not dispatched')).toBeInTheDocument()
    expect(screen.getByText(/was not dispatched and nothing was recorded, so this is not a failed command/)).toBeInTheDocument()
    expect(screen.queryByText('Refused')).not.toBeInTheDocument()
  })

  it('offers an override retry only after night-not-ready, and only with night:override', async () => {
    stubs.dispatchNightCommand = () =>
      Promise.reject(new ApiError('Withheld by interlock projector-cooldown.', 409, PROBLEM_TYPE.nightNotReady))
    renderScreen({ nightSession: session(), session: allowedSession })
    fireEvent.click(screen.getByRole('button', { name: 'Power down presentation' }))
    await screen.findByText('Withheld')
    expect(screen.queryByLabelText('Interlock rule')).not.toBeInTheDocument()
    expect(screen.getByText(/does not include "night:override"/)).toBeInTheDocument()
  })

  it('offers an override retry with night:override after night-not-ready, and rejects an empty reason', async () => {
    let calls = 0
    stubs.dispatchNightCommand = (...args: unknown[]) => {
      calls += 1
      if (calls === 1) return Promise.reject(new ApiError('Withheld by interlock projector-cooldown.', 409, PROBLEM_TYPE.nightNotReady))
      return Promise.resolve(commandResponse(args[0] as string, 'applied'))
    }
    renderScreen({
      nightSession: session(),
      session: { ...allowedSessionBase, scopes: ['night:command', 'night:override'] } as never,
    })
    fireEvent.click(screen.getByRole('button', { name: 'Power down presentation' }))
    await screen.findByText('Withheld')
    const ruleInput = screen.getByLabelText('Interlock rule')
    fireEvent.change(ruleInput, { target: { value: 'projector-cooldown' } })
    fireEvent.click(screen.getByRole('button', { name: 'Retry power-down-presentation with override' }))
    expect(await screen.findByText('A reason is required to override an interlock.')).toBeInTheDocument()
    expect(calls).toBe(1)

    const reasonInput = screen.getByLabelText('Reason')
    fireEvent.change(reasonInput, { target: { value: 'Lamp confirmed cool by operator.' } })
    fireEvent.click(screen.getByRole('button', { name: 'Retry power-down-presentation with override' }))
    expect(await screen.findByText('Accepted')).toBeInTheDocument()
    expect(calls).toBe(2)
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

  it('links Edit definition to the in-page definition editor', () => {
    renderScreen({ nightSession: session({ cycle: 1 }) })
    expect(screen.getByRole('link', { name: 'Edit definition' })).toHaveAttribute('href', '#sn-definitions')
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

  it('renders every readiness check with its state and reason, including not_verifiable and not_configured', () => {
    renderScreen({
      nightSession: session({
        readiness: {
          state: 'recorded',
          reason: '',
          outcome: 'ready',
          completedAt: '2026-08-28T21:00:00Z',
          sameEpoch: true,
          fresh: true,
          checks: [
            { name: 'interlock:power-on:projector-cooldown', state: 'healthy', reason: 'Lamp below threshold.' },
            { name: 'interlock:power-on:garage-link', state: 'failed', reason: 'No route to host.' },
            { name: 'audio-node:reachable', state: 'not_verifiable', reason: 'No audio node is configured to verify.' },
            { name: 'resolume:composition', state: 'not_configured', reason: 'No composition is configured for this show.' },
          ],
        },
      }),
    })
    expect(screen.getByText('interlock:power-on:projector-cooldown · healthy')).toBeInTheDocument()
    expect(screen.getByText('Lamp below threshold.')).toBeInTheDocument()
    expect(screen.getByText('interlock:power-on:garage-link · failed')).toBeInTheDocument()
    expect(screen.getByText('No route to host.')).toBeInTheDocument()
    expect(screen.getByText('audio-node:reachable · not verifiable')).toBeInTheDocument()
    expect(screen.getByText('No audio node is configured to verify.')).toBeInTheDocument()
    expect(screen.getByText('resolume:composition · not configured')).toBeInTheDocument()
    expect(screen.getByText('No composition is configured for this show.')).toBeInTheDocument()
  })

  it('says so honestly when readiness recorded no individual checks', () => {
    renderScreen({
      nightSession: session({
        readiness: {
          state: 'recorded',
          reason: '',
          outcome: 'ready',
          completedAt: '2026-08-28T21:00:00Z',
          sameEpoch: true,
          fresh: true,
          checks: [],
        },
      }),
    })
    expect(screen.getByText('No individual checks were recorded with this result.')).toBeInTheDocument()
  })

  it('offers "Run readiness again" inline next to a stale readiness verdict, reusing the same send path', async () => {
    stubs.dispatchNightCommand = () => new Promise(() => {})
    renderScreen({
      nightSession: session(),
      session: {
        serverTime: '2026-08-28T21:07:00Z',
        authenticated: true,
        principal: { id: 'p', name: 'op', role: 'operator', disabled: false },
        session: null,
        credentialForm: 'session',
        scopes: ['night:command'],
        scopesState: 'current',
        bootstrapRequired: false,
      } as never,
    })
    const inlineButtons = screen.getAllByRole('button', { name: 'Run readiness again' })
    expect(inlineButtons).toHaveLength(1)
    let dispatched: unknown[] = []
    stubs.dispatchNightCommand = (...args: unknown[]) => {
      dispatched = args
      return new Promise(() => {})
    }
    fireEvent.click(inlineButtons[0]!)
    expect(dispatched[0]).toBe('run-readiness')
  })

  it('does not offer "Run readiness again" when readiness is fresh, same-epoch, and ready', () => {
    renderScreen({
      nightSession: session({
        readiness: {
          state: 'recorded',
          reason: '',
          outcome: 'ready',
          completedAt: '2026-08-28T21:00:00Z',
          sameEpoch: true,
          fresh: true,
          checks: [],
        },
      }),
    })
    expect(screen.queryByRole('button', { name: 'Run readiness again' })).not.toBeInTheDocument()
  })

  it('renders background audio steps with their sequence, cue, kind, and state', () => {
    renderScreen({
      nightSession: session({
        backgroundAudio: {
          state: 'recorded',
          reason: '',
          pinnedMaxGainDb: -18,
          steps: [
            {
              sequence: 'background',
              phase: 'enterShow',
              cueName: 'duck-bed',
              nodeId: 'audio-01',
              kind: 'gain',
              actionRevision: 4,
              state: 'resolved',
              outcome: 'confirmed',
              dispatchedAt: '2026-08-28T21:02:20Z',
              resolvedAt: '2026-08-28T21:02:21Z',
            },
            {
              sequence: 'announcement',
              phase: 'enterResting',
              cueName: 'welcome-back',
              nodeId: 'audio-01',
              kind: 'announcementStart',
              actionRevision: 1,
              state: 'resolved',
              outcome: 'refused',
              reason: 'No announcement asset configured.',
              dispatchedAt: '2026-08-28T21:02:25Z',
              resolvedAt: null,
            },
          ],
        },
      }),
    })
    expect(screen.getByText('background')).toBeInTheDocument()
    expect(screen.getByText('duck-bed')).toBeInTheDocument()
    expect(screen.getByText('gain')).toBeInTheDocument()
    expect(screen.getByText('announcement')).toBeInTheDocument()
    expect(screen.getByText('welcome-back')).toBeInTheDocument()
    expect(screen.getByText('announcementStart')).toBeInTheDocument()
    expect(screen.getByText('No announcement asset configured.')).toBeInTheDocument()
  })

  it('renders the pinned background-audio ceiling distinctly from the audio.settings config value', () => {
    renderScreen({
      nightSession: session({
        backgroundAudio: { state: 'recorded', reason: '', pinnedMaxGainDb: -18, steps: [] },
      }),
    })
    expect(screen.getByText(/Pinned ceiling for this running session: -18 dB/)).toBeInTheDocument()
    expect(screen.getByText(/not whatever night\.session's config currently holds/)).toBeInTheDocument()
  })

  it('says so honestly when the pinned ceiling is null because nothing is configured', () => {
    renderScreen({
      nightSession: session({
        backgroundAudio: { state: 'recorded', reason: 'No background audio is configured on the pinned revision.', pinnedMaxGainDb: null, steps: [] },
      }),
    })
    expect(screen.getByText(/Pinned ceiling: none\. No background audio is configured on the pinned revision\./)).toBeInTheDocument()
  })

  it('renders armedShowId, showCommitted, configRevision, admissionClosedAt, and updatedAt', () => {
    renderScreen({
      nightSession: session({
        configObjectId: 'night.session/winter-ridge',
        configRevision: 4,
        armedShowId: 'winter-ridge-2026',
        showCommitted: true,
        admissionClosed: true,
        admissionClosedAt: '2026-08-28T21:05:00Z',
        updatedAt: '2026-08-28T21:07:00Z',
      }),
    })
    expect(screen.getByText('night.session/winter-ridge at revision 4.')).toBeInTheDocument()
    expect(screen.getByText('winter-ridge-2026 is armed and committed for this session.')).toBeInTheDocument()
    expect(screen.getByText(/^Admission closed \d/)).toBeInTheDocument()
    expect(screen.getByText('Session last updated')).toBeInTheDocument()
  })

  it('reports no show armed honestly when armedShowId is empty', () => {
    renderScreen({ nightSession: session({ armedShowId: '', showCommitted: false }) })
    expect(screen.getByText('No show is armed for this session.')).toBeInTheDocument()
  })

  it('reads the night session activation pointer and lists definitions to activate', async () => {
    stubs.getNightSessionActiveConfig = () => Promise.resolve(activeConfigResponse('winter-ridge-2026'))
    stubs.listConfigObjects = () =>
      Promise.resolve({
        serverTime: '2026-08-28T21:07:00Z',
        kind: 'night.session',
        objects: [
          { id: 'winter-ridge-2026', label: 'Winter Ridge 2026', show: '', currentRevision: 2, updatedAt: '2026-08-28T00:00:00Z' },
          { id: 'winter-ridge-backup', label: 'Winter Ridge Backup', show: '', currentRevision: 1, updatedAt: '2026-08-28T00:00:00Z' },
        ],
      })
    renderScreen({ nightSession: session() })
    expect(await screen.findByText('winter-ridge-2026')).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'Winter Ridge Backup (winter-ridge-backup)' })).toBeInTheDocument()
  })

  it('renders the compact active-revision summary for the activation pointer, not a list heading', async () => {
    stubs.getNightSessionActiveConfig = () => Promise.resolve(activeConfigResponse('winter-ridge-2026'))
    stubs.getNightSessionActiveConfigRevisions = () =>
      Promise.resolve({
        serverTime: '2026-08-28T21:07:00Z',
        kind: 'night.session.active',
        revisions: [
          { revision: 3, createdAt: '2026-08-28T16:00:00Z', createdByPrincipalId: 'p', createdByPrincipalName: 'erbartos', source: 'api', note: '', active: true },
        ],
      })
    renderScreen({ nightSession: session() })
    expect(await screen.findByText(/Active revision/)).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Revisions' })).not.toBeInTheDocument()
    expect(screen.queryByText('Active · 3')).not.toBeInTheDocument()
  })

  it('does not claim a read failure while the activation pointer’s revisions fetch is still pending', async () => {
    stubs.getNightSessionActiveConfig = () => Promise.resolve(activeConfigResponse('winter-ridge-2026'))
    stubs.getNightSessionActiveConfigRevisions = () => new Promise(() => {})
    renderScreen({ nightSession: session() })
    await screen.findByText('winter-ridge-2026')
    expect(screen.queryByText('Revision history could not be read just now.')).not.toBeInTheDocument()
  })

  it('reports the read failure honestly when the activation pointer’s revisions fetch is rejected', async () => {
    stubs.getNightSessionActiveConfig = () => Promise.resolve(activeConfigResponse('winter-ridge-2026'))
    stubs.getNightSessionActiveConfigRevisions = () => Promise.reject(new Error('network down'))
    renderScreen({ nightSession: session() })
    expect(await screen.findByText('Revision history could not be read just now.')).toBeInTheDocument()
  })

  it('activates a different night session definition', async () => {
    let calls: unknown[] = []
    stubs.getNightSessionActiveConfig = () => Promise.resolve(activeConfigResponse('winter-ridge-2026'))
    stubs.putNightSessionActiveConfig = (...args: unknown[]) => {
      calls = args
      return Promise.resolve(activeConfigResponse('winter-ridge-backup', { revision: 4 }))
    }
    stubs.listConfigObjects = () =>
      Promise.resolve({
        serverTime: '2026-08-28T21:07:00Z',
        kind: 'night.session',
        objects: [
          { id: 'winter-ridge-2026', label: 'Winter Ridge 2026', show: '', currentRevision: 2, updatedAt: '2026-08-28T00:00:00Z' },
          { id: 'winter-ridge-backup', label: 'Winter Ridge Backup', show: '', currentRevision: 1, updatedAt: '2026-08-28T00:00:00Z' },
        ],
      })
    renderScreen({ nightSession: session(), session: configWriteSession })
    await screen.findByText('winter-ridge-2026')
    fireEvent.change(screen.getByLabelText('Activate a definition'), { target: { value: 'winter-ridge-backup' } })
    fireEvent.click(screen.getByRole('button', { name: 'Activate' }))
    await screen.findByText('winter-ridge-backup')
    expect(calls[0]).toEqual({ session: 'winter-ridge-backup' })
  })

  it('refuses to activate over a stale pointer, matching D-014 for every other config write', async () => {
    let reads = 0
    stubs.getNightSessionActiveConfig = () => {
      reads += 1
      return Promise.resolve(
        reads === 1
          ? activeConfigResponse('winter-ridge-2026', { revision: 3 })
          : activeConfigResponse('someone-else', { revision: 5, createdByPrincipalName: 'other-operator' }),
      )
    }
    stubs.listConfigObjects = () =>
      Promise.resolve({
        serverTime: '2026-08-28T21:07:00Z',
        kind: 'night.session',
        objects: [{ id: 'winter-ridge-backup', label: 'Backup', show: '', currentRevision: 1, updatedAt: '2026-08-28T00:00:00Z' }],
      })
    renderScreen({ nightSession: session(), session: configWriteSession })
    await screen.findByText('winter-ridge-2026')
    fireEvent.change(screen.getByLabelText('Activate a definition'), { target: { value: 'winter-ridge-backup' } })
    fireEvent.click(screen.getByRole('button', { name: 'Activate' }))
    expect(await screen.findByText('Stale write')).toBeInTheDocument()
    expect(screen.getByText(/saved by other-operator/)).toBeInTheDocument()
  })

  it('treats clearing the active pointer as a confirmed, deliberate sharp control', async () => {
    let calls: unknown[] = []
    stubs.getNightSessionActiveConfig = () => Promise.resolve(activeConfigResponse('winter-ridge-2026'))
    stubs.putNightSessionActiveConfig = (...args: unknown[]) => {
      calls = args
      return Promise.resolve(activeConfigResponse('', { revision: 4 }))
    }
    renderScreen({ nightSession: session(), session: configWriteSession })
    await screen.findByText('winter-ridge-2026')
    const confirmInput = screen.getByLabelText('Type winter-ridge-2026 to confirm clearing the pointer')
    const clearButton = screen.getByRole('button', { name: 'Clear active definition' })
    expect(clearButton).toBeDisabled()
    fireEvent.change(confirmInput, { target: { value: 'winter-ridge-2026' } })
    expect(clearButton).not.toBeDisabled()
    fireEvent.click(clearButton)
    expect(await screen.findByText('none - the pointer is cleared')).toBeInTheDocument()
    expect(calls[0]).toEqual({ session: '' })
  })

  it('preserves barrier, onFailure, fadeDurationMs, and announcementPolicy on a cue when only the label changes', async () => {
    const definitionResponse = (label: string) => ({
      serverTime: '2026-08-28T21:07:00Z',
      kind: 'night.session',
      id: 'winter-ridge-2026',
      revision: 1,
      payload: {
        show: 'winter-ridge',
        label,
        showPlaylist: { fppInstanceId: 'fpp-1', playlist: 'show' },
        resting: {
          fppInstanceId: 'fpp-1', playlist: 'resting', endOfNightPlaylist: 'resting', endOfNightRepeat: true,
          timelineAsset: { show: 'winter-ridge', sequence: 'seq', target: 'target' },
        },
        enterShow: {
          blackoutHoldMs: 0,
          cues: [{ name: 'Announce', role: 'announcement', action: 'play-announcement', offsetMs: 0, barrier: true, onFailure: 'abort', fadeDurationMs: 500, announcementPolicy: 'duck' }],
        },
        enterResting: { blackoutAfterShowMs: 0, cues: [] },
        announcementDefaultPolicy: 'duck',
      },
      updatedAt: '2026-08-28T16:00:00Z',
      createdByPrincipalId: 'p',
      createdByPrincipalName: 'erbartos',
      source: 'api',
    })
    const captured: { body: { enterShow?: { cues?: unknown[] } } | null } = { body: null }
    stubs.listConfigObjects = () =>
      Promise.resolve({
        serverTime: '',
        kind: 'night.session',
        objects: [{ id: 'winter-ridge-2026', label: 'Winter Ridge', show: 'winter-ridge', currentRevision: 1, updatedAt: '2026-08-28T00:00:00Z' }],
      })
    stubs.getNightSessionConfig = () => Promise.resolve(definitionResponse('Winter Ridge'))
    stubs.putNightSessionConfig = (...args: unknown[]) => {
      captured.body = args[1] as { enterShow?: { cues?: unknown[] } }
      return Promise.resolve(definitionResponse('Winter Ridge Updated'))
    }
    renderScreen({ nightSession: session(), session: configWriteSession })
    fireEvent.change(await screen.findByLabelText('Definition'), { target: { value: 'winter-ridge-2026' } })
    const labelInput = await screen.findByLabelText('Label')
    expect(labelInput).toHaveValue('Winter Ridge')
    fireEvent.change(labelInput, { target: { value: 'Winter Ridge Updated' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save definition' }))
    await screen.findByDisplayValue('Winter Ridge Updated')
    expect(captured.body?.enterShow?.cues).toEqual([
      { name: 'Announce', role: 'announcement', action: 'play-announcement', offsetMs: 0, barrier: true, onFailure: 'abort', fadeDurationMs: 500, announcementPolicy: 'duck' },
    ])

    // Changing the role off announcement must drop the policy, which the coordinator refuses on any other role.
    fireEvent.change(screen.getByLabelText('Role'), { target: { value: 'lighting' } })
    fireEvent.change(screen.getByLabelText('Label'), { target: { value: 'Winter Ridge Relit' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save definition' }))
    await screen.findByDisplayValue('Winter Ridge Relit')
    expect(captured.body?.enterShow?.cues).toEqual([
      { name: 'Announce', role: 'lighting', action: 'play-announcement', offsetMs: 0, barrier: true, onFailure: 'abort', fadeDurationMs: 500 },
    ])
  })
})
