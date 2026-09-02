import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, PROBLEM_TYPE, type FPPInstance, type Model, type NightSessionState } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'
import { ShowNight } from './ShowNight'
import { ShowsNightSession } from './ShowsNightSession'
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
  listAssets: (() => Promise.resolve({ serverTime: '', assets: [] })) as (...args: never[]) => Promise<unknown>,
  listFPPPlaylistDefinitions: (() => Promise.resolve({ serverTime: '', definitions: [] })) as (...args: never[]) => Promise<unknown>,
  getShowAction: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
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
    listAssets: (...args: never[]) => stubs.listAssets(...args),
    listFPPPlaylistDefinitions: (...args: never[]) => stubs.listFPPPlaylistDefinitions(...args),
    getShowAction: (...args: never[]) => stubs.getShowAction(...args),
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

/** The definition-authoring inspector: Codex moved it into the Shows workspace's Night session tab. */
function renderDefinitions(model: Partial<Model>, showId = 'winter-ridge') {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model }}>
      <MemoryRouter initialEntries={[`/shows/${showId}/night-session`]}>
        <Routes>
          <Route path="/shows/:id/night-session" element={<ShowsNightSession />} />
        </Routes>
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

  it('keeps activation on Show Night but moves definition authoring to the Show workspace', () => {
    renderScreen({ nightSession: session() })
    expect(screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)).toEqual([
      'Lifecycle commands',
      'Run of Show',
      'Evidence',
      'Night session activation',
    ])
    expect(screen.queryByRole('heading', { name: 'Night session definitions' })).not.toBeInTheDocument()
  })

  it('offers Run readiness only from the lifecycle cell and the Evidence link, not a page-header button', () => {
    renderScreen({ nightSession: session(), session: allowedSession })
    expect(screen.getAllByRole('button', { name: 'Run readiness' })).toHaveLength(1)
    const pageHead = screen.getByRole('link', { name: 'Edit definition' }).closest('.sm-page__head') as HTMLElement
    expect(within(pageHead).queryByRole('button', { name: 'Run readiness' })).not.toBeInTheDocument()
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

  it('renders the complete timeline without adding a banner that is absent from the approved layout', () => {
    renderScreen({ nightSession: session({ cycle: 3, state: 'live' }) })
    expect(screen.queryByText('The night timeline does nothing yet.')).not.toBeInTheDocument()
    expect(screen.getByText('Preshow')).toBeInTheDocument()
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

  it('groups the lifecycle commands into Prepare, Start, End the night: the same one element Live Control renders', () => {
    renderScreen({ nightSession: session(), session: allowedSession })
    const region = screen.getByRole('region', { name: 'Lifecycle commands' })
    expect(within(region).getAllByRole('heading', { level: 3 }).map((h) => h.textContent)).toEqual(['Prepare', 'Start', 'End the night'])
  })

  it('renders Prepare as Prepare site, Run readiness and Start as Start preshow, Start night: the group spec order, not blocks.css’s unscoped per-command order', () => {
    renderScreen({ nightSession: session(), session: allowedSession })
    const region = screen.getByRole('region', { name: 'Lifecycle commands' })
    const prepareSection = within(region).getByRole('heading', { name: 'Prepare', level: 3 }).closest('section') as HTMLElement
    expect(within(prepareSection).getAllByRole('button').map((b) => b.textContent)).toEqual(['Prepare site', 'Run readiness'])
    const startSection = within(region).getByRole('heading', { name: 'Start', level: 3 }).closest('section') as HTMLElement
    expect(within(startSection).getAllByRole('button').map((b) => b.textContent)).toEqual(['Start preshow', 'Start night'])
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

  it('sends skipEnterShowLead: true for start-night only when the late-start box is checked', () => {
    const calls: unknown[][] = []
    stubs.dispatchNightCommand = (...args: unknown[]) => {
      calls.push(args)
      return new Promise(() => {})
    }
    renderScreen({ nightSession: session(), session: allowedSession })
    fireEvent.click(screen.getByLabelText(/Skip the enter-show lead/))
    fireEvent.click(screen.getByRole('button', { name: 'Start night' }))
    expect(calls[0]?.[3]).toBe(true)
  })

  it('sends no skipEnterShowLead field for start-night when the box is left unchecked', () => {
    const calls: unknown[][] = []
    stubs.dispatchNightCommand = (...args: unknown[]) => {
      calls.push(args)
      return new Promise(() => {})
    }
    renderScreen({ nightSession: session(), session: allowedSession })
    fireEvent.click(screen.getByRole('button', { name: 'Start night' }))
    expect(calls[0]?.[3]).toBe(false)
  })

  it('never sends skipEnterShowLead for a command other than start-night, even with the box checked', () => {
    const calls: unknown[][] = []
    stubs.dispatchNightCommand = (...args: unknown[]) => {
      calls.push(args)
      return new Promise(() => {})
    }
    renderScreen({ nightSession: session(), session: allowedSession })
    fireEvent.click(screen.getByLabelText(/Skip the enter-show lead/))
    fireEvent.click(screen.getByRole('button', { name: 'End session' }))
    expect(calls[0]?.[3]).toBeUndefined()
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

  it('keeps the full cycle skeleton visible before the repeating cycle begins', () => {
    const rail = cycleRail(session({ state: 'preshow' }), '2026-08-28T21:07:00Z')
    expect(rail).toHaveLength(5)
    expect(rail.map((step) => step.label)).toEqual(['Resting', 'To show', 'Live', 'To resting', 'Back to resting'])
    expect(rail.every((step) => step.status === 'ahead')).toBe(true)
  })

  it('does not invent cycle progress for another off-cycle state', () => {
    const rail = cycleRail(session({ state: 'fading-out' }), '2026-08-28T21:07:00Z')
    expect(rail).toHaveLength(5)
    expect(rail.slice(0, 4).every((step) => step.detail === 'not started')).toBe(true)
  })

  it('renders the four cycle phases and the return boundary for a state inside the cycle', () => {
    const rail = cycleRail(session({ state: 'live' }), '2026-08-28T21:07:00Z')
    expect(rail).toHaveLength(5)
    expect(rail.map((step) => step.label)).toEqual(['Resting', 'To show', 'Live', 'To resting', 'Back to resting'])
  })

  it('links Edit definition to the armed Show’s Night session tab', () => {
    renderScreen({ nightSession: session({ cycle: 1, armedShowId: 'halloween-2026' }) })
    expect(screen.getByRole('link', { name: 'Edit definition' })).toHaveAttribute('href', '/shows/halloween-2026/night-session')
  })

  it('keeps three structural cycle slots when the session is on cycle 1', () => {
    const rail = nightRail(session({ cycle: 1, state: 'live' }))
    const cycleSteps = rail.filter((step) => step.key.startsWith('cycle-'))
    expect(cycleSteps).toHaveLength(3)
    expect(cycleSteps[0]?.status).toBe('now')
    expect(rail.some((step) => step.status === 'notWired')).toBe(false)

    renderScreen({ nightSession: session({ cycle: 1, state: 'live' }) })
    expect(screen.queryByText('The night timeline does nothing yet.')).not.toBeInTheDocument()
    expect(screen.getByText('Cycle 3')).toBeInTheDocument()
    expect(screen.getAllByText('not started').length).toBeGreaterThan(0)
  })

  it('renders preshow and three cycle slots even when the coordinator reports cycle zero', () => {
    const rail = nightRail(session({ cycle: 0, state: 'inactive' }))
    expect(rail.slice(0, 4).map((step) => step.label)).toEqual(['Preshow', 'Cycle 1', 'Cycle 2', 'Cycle 3'])
    expect(rail.slice(0, 4).every((step) => step.detail === 'not started')).toBe(true)
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
    expect(screen.getAllByText('audio-01')).toHaveLength(2)
  })

  it('renders the node id per background audio step, including one addressed to a second node', () => {
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
              sequence: 'background',
              phase: 'enterShow',
              cueName: 'duck-bed',
              nodeId: 'audio-zone-2',
              kind: 'gain',
              actionRevision: 4,
              state: 'resolved',
              outcome: 'confirmed',
              dispatchedAt: '2026-08-28T21:02:20Z',
              resolvedAt: '2026-08-28T21:02:21Z',
            },
          ],
        },
      }),
    })
    expect(screen.getByText('audio-01')).toBeInTheDocument()
    expect(screen.getByText('audio-zone-2')).toBeInTheDocument()
  })

  it('renders a step of kind announcementApply', () => {
    renderScreen({
      nightSession: session({
        backgroundAudio: {
          state: 'recorded',
          reason: '',
          pinnedMaxGainDb: -18,
          steps: [
            {
              sequence: 'announcement',
              phase: 'enterShow',
              cueName: 'welcome',
              nodeId: 'audio-01',
              kind: 'announcementApply',
              actionRevision: 2,
              state: 'dispatched',
              dispatchedAt: '2026-08-28T21:02:20Z',
              resolvedAt: null,
            },
          ],
        },
      }),
    })
    expect(screen.getByText('announcementApply')).toBeInTheDocument()
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

  const fullDefinitionResponse = (label: string) => ({
    serverTime: '2026-08-28T21:07:00Z',
    kind: 'night.session',
    id: 'winter-ridge-2026',
    revision: 1,
    payload: {
      show: 'winter-ridge',
      label,
      showPlaylist: { fppInstanceId: 'fpp-1', playlist: 'show' },
      resting: {
        fppInstanceId: 'fpp-1', playlist: 'resting', endOfNightPlaylist: 'end-of-night', endOfNightRepeat: true,
        timelineAsset: { show: 'winter-ridge', sequence: 'seq', target: 'target' },
        backgroundAudio: {
          items: [{ itemId: 'bed-1', show: 'winter-ridge', sequence: 'bed-seq', target: 'audio-01' }],
          repeat: 'playlist', resume: 'restart', itemTransition: 'crossfade', crossfadeMs: 500, maxGainDb: -12,
          fadeOutMs: 2000, fadeInMs: 1500,
        },
      },
      enterShow: {
        blackoutHoldMs: 0,
        cues: [{ name: 'Announce', role: 'announcement', action: 'play-announcement', offsetMs: 0, barrier: true, onFailure: 'abort', fadeDurationMs: 500, announcementPolicy: 'duck' }],
      },
      enterResting: { blackoutAfterShowMs: 0, cues: [] },
      announcementDefaultPolicy: 'duck',
      siteControl: {
        requestThermalProfile: 'winter-thermal',
        presentationPowerOn: { action: 'power-on', powerDomain: 'presentation', domainProvenance: 'operator-declared' },
        presentationPowerOff: {
          action: 'power-off', powerDomain: 'presentation', domainProvenance: 'operator-declared', removalPolicy: 'after-actions',
          prerequisites: [{ kind: 'delay', delayMs: 5000 }],
        },
      },
      interlocks: [
        { name: 'door-sensor', phase: 'prepare-site', posture: 'block', signal: 'door-check', freshnessSeconds: 30, failureText: 'Door not confirmed shut.', onUnavailable: 'block', overridePolicy: 'authorized-operator' },
      ],
    },
    updatedAt: '2026-08-28T16:00:00Z',
    createdByPrincipalId: 'p',
    createdByPrincipalName: 'erbartos',
    source: 'api',
  })

  function mockListConfigObjects() {
    stubs.listConfigObjects = ((kind: string) =>
      kind === 'show.action'
        ? Promise.resolve({ serverTime: '', kind, objects: [] })
        : Promise.resolve({
            serverTime: '',
            kind: 'night.session',
            objects: [{ id: 'winter-ridge-2026', label: 'Winter Ridge', show: 'winter-ridge', currentRevision: 1, updatedAt: '2026-08-28T00:00:00Z' }],
          })) as typeof stubs.listConfigObjects
  }

  async function openWinterRidgeDefinition() {
    fireEvent.click(await screen.findByRole('row', { name: 'Edit Winter Ridge' }))
    return screen.findByLabelText('Label')
  }

  it('round-trips every definition field through a label-only save', async () => {
    const captured: { body: Record<string, unknown> | null } = { body: null }
    mockListConfigObjects()
    stubs.getNightSessionConfig = () => Promise.resolve(fullDefinitionResponse('Winter Ridge'))
    stubs.putNightSessionConfig = (...args: unknown[]) => {
      captured.body = args[1] as Record<string, unknown>
      return Promise.resolve(fullDefinitionResponse('Winter Ridge Updated'))
    }
    renderDefinitions({ session: configWriteSession })
    const labelInput = await openWinterRidgeDefinition()
    expect(labelInput).toHaveValue('Winter Ridge')
    fireEvent.change(labelInput, { target: { value: 'Winter Ridge Updated' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save definition' }))
    await screen.findByDisplayValue('Winter Ridge Updated')

    expect(captured.body).toEqual(fullDefinitionResponse('Winter Ridge Updated').payload)

    // Changing the role off announcement must drop the policy, which the coordinator refuses on any other role.
    fireEvent.change(screen.getByLabelText('Role'), { target: { value: 'lighting' } })
    fireEvent.change(screen.getByLabelText('Label'), { target: { value: 'Winter Ridge Relit' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save definition' }))
    await screen.findByDisplayValue('Winter Ridge Relit')
    expect(captured.body?.enterShow).toMatchObject({
      cues: [{ name: 'Announce', role: 'lighting', action: 'play-announcement', offsetMs: 0, barrier: true, onFailure: 'abort', fadeDurationMs: 500 }],
    })
    expect((captured.body?.enterShow as { cues: Record<string, unknown>[] }).cues[0]).not.toHaveProperty('announcementPolicy')
  })

  it('sends the announcement default policy, end-of-night playlist, and end-of-night repeat as edited', async () => {
    const captured: { body: Record<string, unknown> | null } = { body: null }
    mockListConfigObjects()
    stubs.getNightSessionConfig = () => Promise.resolve(fullDefinitionResponse('Winter Ridge'))
    stubs.putNightSessionConfig = (...args: unknown[]) => {
      captured.body = args[1] as Record<string, unknown>
      return Promise.resolve(fullDefinitionResponse('Winter Ridge'))
    }
    stubs.listFPPPlaylistDefinitions = () =>
      Promise.resolve({
        serverTime: '',
        definitions: [{ instanceUuid: 'uuid-1', playlistName: 'holiday-cooldown', playlistHash: 'a'.repeat(64), capturedAt: '', receivedAt: '', entryCount: 1, referenced: false }],
      })
    renderDefinitions({ session: configWriteSession, fpp: [{ instanceId: 'fpp-1', instanceUuid: 'uuid-1' } as never] })
    await openWinterRidgeDefinition()
    fireEvent.change(screen.getByLabelText('Default announcement policy'), { target: { value: 'interrupt' } })
    fireEvent.change(screen.getByLabelText('End-of-night playlist'), { target: { value: 'holiday-cooldown' } })
    fireEvent.click(screen.getByLabelText('Repeat the end-of-night playlist'))
    fireEvent.click(screen.getByRole('button', { name: 'Save definition' }))
    await screen.findByDisplayValue('Winter Ridge')
    expect(captured.body?.announcementDefaultPolicy).toBe('interrupt')
    expect((captured.body?.resting as Record<string, unknown>).endOfNightPlaylist).toBe('holiday-cooldown')
    expect((captured.body?.resting as Record<string, unknown>).endOfNightRepeat).toBe(false)
  })

  it('sends the background audio subsection with the edited item, repeat, and ceiling', async () => {
    const captured: { body: Record<string, unknown> | null } = { body: null }
    mockListConfigObjects()
    stubs.getNightSessionConfig = () => Promise.resolve(fullDefinitionResponse('Winter Ridge'))
    stubs.putNightSessionConfig = (...args: unknown[]) => {
      captured.body = args[1] as Record<string, unknown>
      return Promise.resolve(fullDefinitionResponse('Winter Ridge'))
    }
    renderDefinitions({ session: configWriteSession })
    await openWinterRidgeDefinition()
    fireEvent.change(screen.getByLabelText('Item id'), { target: { value: 'bed-2' } })
    fireEvent.change(screen.getByLabelText('Maximum gain (dB)'), { target: { value: '-6' } })
    fireEvent.change(screen.getByLabelText('Repeat'), { target: { value: 'item' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save definition' }))
    await screen.findByDisplayValue('Winter Ridge')
    const backgroundAudio = (captured.body?.resting as Record<string, unknown>).backgroundAudio as Record<string, unknown>
    expect((backgroundAudio.items as Record<string, unknown>[])[0]).toMatchObject({ itemId: 'bed-2', target: 'audio-01' })
    expect(backgroundAudio.maxGainDb).toBe(-6)
    expect(backgroundAudio.repeat).toBe('item')
  })

  it('sends the site control subsection with its authored bindings', async () => {
    const captured: { body: Record<string, unknown> | null } = { body: null }
    mockListConfigObjects()
    stubs.getNightSessionConfig = () => Promise.resolve(fullDefinitionResponse('Winter Ridge'))
    stubs.putNightSessionConfig = (...args: unknown[]) => {
      captured.body = args[1] as Record<string, unknown>
      return Promise.resolve(fullDefinitionResponse('Winter Ridge'))
    }
    renderDefinitions({ session: configWriteSession })
    await openWinterRidgeDefinition()
    fireEvent.change(screen.getByLabelText('Request thermal profile'), { target: { value: 'summer-thermal' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save definition' }))
    await screen.findByDisplayValue('Winter Ridge')
    const siteControl = captured.body?.siteControl as Record<string, unknown>
    expect(siteControl.requestThermalProfile).toBe('summer-thermal')
    expect(siteControl.presentationPowerOn).toMatchObject({ action: 'power-on', powerDomain: 'presentation', domainProvenance: 'operator-declared' })
    expect(siteControl.presentationPowerOff).toMatchObject({ action: 'power-off', removalPolicy: 'after-actions', prerequisites: [{ kind: 'delay', delayMs: 5000 }] })
  })

  it('sends interlocks with an edited entry', async () => {
    const captured: { body: Record<string, unknown> | null } = { body: null }
    mockListConfigObjects()
    stubs.getNightSessionConfig = () => Promise.resolve(fullDefinitionResponse('Winter Ridge'))
    stubs.putNightSessionConfig = (...args: unknown[]) => {
      captured.body = args[1] as Record<string, unknown>
      return Promise.resolve(fullDefinitionResponse('Winter Ridge'))
    }
    renderDefinitions({ session: configWriteSession })
    await openWinterRidgeDefinition()
    fireEvent.change(screen.getByLabelText('Failure text'), { target: { value: 'Door sensor unreachable.' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save definition' }))
    await screen.findByDisplayValue('Winter Ridge')
    expect(captured.body?.interlocks).toEqual([
      { name: 'door-sensor', phase: 'prepare-site', posture: 'block', signal: 'door-check', freshnessSeconds: 30, failureText: 'Door sensor unreachable.', onUnavailable: 'block', overridePolicy: 'authorized-operator' },
    ])
  })

  it('blocks save when only one side of the background audio fade pair is set', async () => {
    mockListConfigObjects()
    stubs.getNightSessionConfig = () => Promise.resolve(fullDefinitionResponse('Winter Ridge'))
    const putSpy = vi.fn(() => Promise.resolve(fullDefinitionResponse('Winter Ridge')))
    stubs.putNightSessionConfig = putSpy
    renderDefinitions({ session: configWriteSession })
    await openWinterRidgeDefinition()
    fireEvent.change(screen.getByLabelText('Fade-in (ms)'), { target: { value: '' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save definition' }))
    expect(await screen.findByText('Background audio fade-out and fade-in must be configured together, or both left empty for an instant cut.')).toBeInTheDocument()
    expect(putSpy).not.toHaveBeenCalled()
  })
})
