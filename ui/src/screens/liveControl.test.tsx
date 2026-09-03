import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api'
import { PROBLEM_TYPE } from '../api/problem'
import type { AudioSessionCommandResult, FPPCommandResult, Model, Node, ResolumeActionResult } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'
import { LiveControl } from './LiveControl'
import { describeFPPOutcome, formatPosition, outputRows, transportState } from './liveControlModel'

const stubs = vi.hoisted(() => ({
  listConfigObjects: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShowCue: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShowAction: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  listAssets: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  listObservations: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getAudioSettingsConfig: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  startFPPPlaylist: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  stopFPPPlaylistGracefully: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getResolumeComposition: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  blackoutResolume: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  prepareAudioSession: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  startAudioSession: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  pauseAudioSession: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  resumeAudioSession: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  advanceAudioSession: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  applyAudioSession: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  stopAudioSession: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  clearAudioSession: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  seekAudioSession: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  setAudioSessionGain: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  fadeAudioSessionGain: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  muteAudioSessionOutput: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  unmuteAudioSessionOutput: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  listFPPPlaylistDefinitions: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  emergencyStop: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  emergencyStopPowerDown: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  armEmergencyStopHardStop: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  fireEmergencyStopHardStop: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    listConfigObjects: (...args: never[]) => stubs.listConfigObjects(...args),
    getShowCue: (...args: never[]) => stubs.getShowCue(...args),
    getShowAction: (...args: never[]) => stubs.getShowAction(...args),
    listAssets: (...args: never[]) => stubs.listAssets(...args),
    listObservations: (...args: never[]) => stubs.listObservations(...args),
    getAudioSettingsConfig: (...args: never[]) => stubs.getAudioSettingsConfig(...args),
    startFPPPlaylist: (...args: never[]) => stubs.startFPPPlaylist(...args),
    stopFPPPlaylistGracefully: (...args: never[]) => stubs.stopFPPPlaylistGracefully(...args),
    getResolumeComposition: (...args: never[]) => stubs.getResolumeComposition(...args),
    blackoutResolume: (...args: never[]) => stubs.blackoutResolume(...args),
    prepareAudioSession: (...args: never[]) => stubs.prepareAudioSession(...args),
    startAudioSession: (...args: never[]) => stubs.startAudioSession(...args),
    pauseAudioSession: (...args: never[]) => stubs.pauseAudioSession(...args),
    resumeAudioSession: (...args: never[]) => stubs.resumeAudioSession(...args),
    advanceAudioSession: (...args: never[]) => stubs.advanceAudioSession(...args),
    applyAudioSession: (...args: never[]) => stubs.applyAudioSession(...args),
    stopAudioSession: (...args: never[]) => stubs.stopAudioSession(...args),
    clearAudioSession: (...args: never[]) => stubs.clearAudioSession(...args),
    seekAudioSession: (...args: never[]) => stubs.seekAudioSession(...args),
    setAudioSessionGain: (...args: never[]) => stubs.setAudioSessionGain(...args),
    fadeAudioSessionGain: (...args: never[]) => stubs.fadeAudioSessionGain(...args),
    muteAudioSessionOutput: (...args: never[]) => stubs.muteAudioSessionOutput(...args),
    unmuteAudioSessionOutput: (...args: never[]) => stubs.unmuteAudioSessionOutput(...args),
    listFPPPlaylistDefinitions: (...args: never[]) => stubs.listFPPPlaylistDefinitions(...args),
    emergencyStop: (...args: never[]) => stubs.emergencyStop(...args),
    emergencyStopPowerDown: (...args: never[]) => stubs.emergencyStopPowerDown(...args),
    armEmergencyStopHardStop: (...args: never[]) => stubs.armEmergencyStopHardStop(...args),
    fireEmergencyStopHardStop: (...args: never[]) => stubs.fireEmergencyStopHardStop(...args),
  }
})

const observation = (signal: string, value: unknown, state = 'current', kind = 'surface', id = 'front') =>
  ({
    resource: { kind, id },
    signal,
    value,
    unit: null,
    state,
    reason: null,
    observedAt: '2026-08-28T21:06:58Z',
    collectedAt: '2026-08-28T21:06:58Z',
    source: 'agent',
    quality: 'reported',
  }) as unknown as Node['render'][number]

function node(nodeId: string, render: Node['render']): Node {
  return {
    nodeId,
    label: nodeId,
    capabilities: [{ id: 'transport.ndi.send', version: 1 }],
    controlPlane: { state: 'online', reason: null },
    evidence: { hello: observation('h', 1), lastWill: observation('l', 1), heartbeat: observation('b', 1) },
    declaration: {},
    render,
    audio: [],
    fppConnect: [],
  } as unknown as Node
}

function renderScreen(model: Partial<Model>) {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model }}>
      <MemoryRouter>
        <LiveControl />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

const cue = (id: string, sequence: string) => ({
  id,
  payload: {
    outputs: {
      audio: { asset: sequence, startOffsetMillis: 0 },
      announcement: { policy: 'duck', duckGainDb: -18, fadeMillis: 500 },
    },
  },
})

/** Two announcement cues: one whose audio sequence has a current asset, one whose does not. */
async function renderAnnouncements() {
  const cues = [cue('welcome', 'welcome-vo'), cue('closing', 'closing-vo')]
  stubs.listConfigObjects = () => Promise.resolve({ objects: cues.map((c) => ({ id: c.id })) })
  stubs.getShowCue = ((id: string) => Promise.resolve(cues.find((c) => c.id === id))) as never
  stubs.listAssets = () =>
    Promise.resolve({ assets: [{ sequence: 'welcome-vo', current: true }, { sequence: 'closing-vo', current: false }] })
  renderScreen({ currentRuns: { activeShow: { configured: true, show: 'winter-ridge-2026' } } as never })
  await screen.findByText(/Firing an announcement does nothing yet/)
}

describe('Live Control', () => {
  afterEach(() => {
    cleanup()
    stubs.listConfigObjects = () => new Promise(() => {})
    stubs.getShowCue = () => new Promise(() => {})
    stubs.getShowAction = () => new Promise(() => {})
    stubs.listAssets = () => new Promise(() => {})
    stubs.listObservations = () => new Promise(() => {})
    stubs.getAudioSettingsConfig = () => new Promise(() => {})
    stubs.startFPPPlaylist = () => new Promise(() => {})
    stubs.stopFPPPlaylistGracefully = () => new Promise(() => {})
    stubs.getResolumeComposition = () => new Promise(() => {})
    stubs.blackoutResolume = () => new Promise(() => {})
    stubs.prepareAudioSession = () => new Promise(() => {})
    stubs.startAudioSession = () => new Promise(() => {})
    stubs.pauseAudioSession = () => new Promise(() => {})
    stubs.resumeAudioSession = () => new Promise(() => {})
    stubs.advanceAudioSession = () => new Promise(() => {})
    stubs.stopAudioSession = () => new Promise(() => {})
    stubs.clearAudioSession = () => new Promise(() => {})
    stubs.seekAudioSession = () => new Promise(() => {})
    stubs.setAudioSessionGain = () => new Promise(() => {})
    stubs.fadeAudioSessionGain = () => new Promise(() => {})
    stubs.muteAudioSessionOutput = () => new Promise(() => {})
    stubs.unmuteAudioSessionOutput = () => new Promise(() => {})
    stubs.listFPPPlaylistDefinitions = () => new Promise(() => {})
    stubs.emergencyStop = () => new Promise(() => {})
    stubs.emergencyStopPowerDown = () => new Promise(() => {})
    stubs.armEmergencyStopHardStop = () => new Promise(() => {})
    stubs.fireEmergencyStopHardStop = () => new Promise(() => {})
  })

  const fppInstance = (playerState = 'stopped') =>
    ({
      instanceId: 'main-player',
      health: 'healthy',
      observations: [observation('fpp.status.player_state', playerState, 'current', 'fpp', 'main-player')],
      instanceUuidChange: null,
    }) as never

  const commandAllowedSession = {
    serverTime: '2026-08-28T21:07:00Z',
    authenticated: true,
    principal: { id: 'p', name: 'op', role: 'operator', disabled: false },
    session: null,
    credentialForm: 'session',
    scopes: ['fpp:command'],
    scopesState: 'current',
    bootstrapRequired: false,
  } as never

  const audioAllowedSession = {
    serverTime: '2026-08-28T21:07:00Z',
    authenticated: true,
    principal: { id: 'p', name: 'op', role: 'operator', disabled: false },
    session: null,
    credentialForm: 'session',
    scopes: ['audio:command'],
    scopesState: 'current',
    bootstrapRequired: false,
  } as never

  const emergencyAllowedSession = {
    serverTime: '2026-08-28T21:07:00Z',
    authenticated: true,
    principal: { id: 'p', name: 'op', role: 'operator', disabled: false },
    session: null,
    credentialForm: 'session',
    scopes: ['show:emergencystop:invoke'],
    scopesState: 'current',
    bootstrapRequired: false,
  } as never

  function audioCommandResult(overrides: Partial<AudioSessionCommandResult> = {}): AudioSessionCommandResult {
    return {
      commandId: 'cmd-1',
      idempotencyKey: 'key-1',
      action: 'audio.session.start',
      nodeId: 'audio-node-01',
      sessionId: 'bg-holiday-01',
      replay: false,
      outcome: 'started',
      reason: '',
      dispatchedAt: '2026-08-28T21:05:42Z',
      resolvedAt: '2026-08-28T21:05:44Z',
      attributionDegraded: false,
      ...overrides,
    } as AudioSessionCommandResult
  }

  /**
   * One declared audio.node, no observed sessions, and a typed session id,
   * with the drawer opened and its session opened: the minimal ready state
   * every dispatch test builds on. Returns the drawer, where the target,
   * transport, seek, gain, mute and clear controls all now live.
   */
  async function renderAudioSessionsReady(opts: { fps?: number } = {}) {
    stubs.listConfigObjects = ((kind: string) => {
      if (kind === 'audio.node') return Promise.resolve({ objects: [{ id: 'audio-node-01', label: 'Front porch node' }] })
      return Promise.resolve({ objects: [] })
    }) as never
    stubs.listObservations = () => Promise.resolve({ observations: [] })
    stubs.getAudioSettingsConfig =
      opts.fps === undefined
        ? () => Promise.reject(new Error('unavailable'))
        : () => Promise.resolve({ payload: { ltcFrameRate: opts.fps } } as never)
    renderScreen({ session: audioAllowedSession })
    const region = await screen.findByRole('region', { name: 'Audio sessions' })
    fireEvent.click(within(region).getByRole('button', { name: /Audio sessions…/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('Session id'), { target: { value: 'bg-holiday-01' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Open' }))
    return dialog
  }

  it('keeps the Resolume emergency path between output evidence and night lifecycle', () => {
    renderScreen({})
    expect(screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)).toEqual([
      'Transport',
      'Emergency stop',
      'What each output is doing',
      'Resolume',
      'Night lifecycle',
      'Audio sessions',
      'Macros',
      'Announcements',
      'Actions',
    ])
    expect(screen.getByRole('link', { name: /open resolume control/i })).toHaveAttribute('href', '/control/resolume')
  })

  it('dispatches blackout and reports its evidence outcome', async () => {
    stubs.getResolumeComposition = () => Promise.reject(new ApiError('not found', 404))
    stubs.blackoutResolume = vi.fn(() => Promise.resolve({ outcome: 'confirmed', outcomeReason: 'Arena reported blackout.', replay: false, attributionDegraded: false } as ResolumeActionResult))
    renderScreen({ session: {
      serverTime: '2026-08-28T21:07:00Z', authenticated: true,
      principal: { id: 'p', name: 'op', role: 'operator', disabled: false }, session: null,
      credentialForm: 'session', scopes: ['resolume:action'], scopesState: 'current', bootstrapRequired: false,
    } as never })
    fireEvent.click(screen.getByRole('button', { name: 'Blackout' }))
    expect(await screen.findByText('Confirmed')).toBeInTheDocument()
    expect(stubs.blackoutResolume).toHaveBeenCalledTimes(1)
  })

  it('explains Stop now as a helper line under the transport, not a callout', () => {
    renderScreen({})
    expect(screen.getByText(/halts this player only/)).toBeInTheDocument()
  })

  it('gates every emergency-stop control on show:emergencystop:invoke, disabled with the real reason, never hidden', () => {
    renderScreen({
      session: {
        serverTime: '2026-08-28T21:07:00Z',
        authenticated: true,
        principal: { id: 'p', name: 'op', role: 'viewer', disabled: false },
        session: null,
        credentialForm: 'session',
        scopes: [],
        scopesState: 'current',
        bootstrapRequired: false,
      } as never,
    })
    const region = screen.getByRole('region', { name: 'Emergency stop' })
    const stop = within(region).getByRole('button', { name: 'Stop' })
    const stopPowerDown = within(region).getByRole('button', { name: 'Stop and power down' })
    const arm = within(region).getByRole('button', { name: 'Arm hard stop' })
    const fire = within(region).getByRole('button', { name: 'Fire hard stop' })
    for (const button of [stop, stopPowerDown, arm, fire]) {
      expect(button).toBeDisabled()
      expect(button).toHaveAttribute('title', expect.stringMatching(/operator|sign in|permission/i))
    }
  })

  it('confirms level 1 stop, dispatches it, and reports per-instance and follow-up outcomes honestly', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    stubs.emergencyStop = vi.fn(() =>
      Promise.resolve({
        level: 'stop',
        idempotencyKey: 'k1',
        stopOutcomes: [
          { instanceId: 'main-player', outcome: 'confirmed', outcomeReason: 'Stopped.', dispatchedAt: null, replay: false },
          { instanceId: 'lobby-player', outcome: 'failed', outcomeReason: 'Unreachable.', dispatchedAt: null, replay: false },
        ],
        noInstancesConfigured: false,
        followUps: [{ actionId: 'act-1', label: 'Notify staff', outcome: 'confirmed', outcomeReason: 'Ran.' }],
      }),
    )
    renderScreen({ session: emergencyAllowedSession })
    fireEvent.click(screen.getByRole('button', { name: 'Stop' }))
    expect(window.confirm).toHaveBeenCalledTimes(1)
    expect(await screen.findByText('main-player')).toBeInTheDocument()
    expect(screen.getByText('lobby-player')).toBeInTheDocument()
    expect(screen.getAllByText('confirmed')).toHaveLength(2)
    expect(screen.getByText('failed')).toBeInTheDocument()
    expect(screen.getByText('Unreachable.')).toBeInTheDocument()
    expect(screen.getByText('Notify staff')).toBeInTheDocument()
    expect(stubs.emergencyStop).toHaveBeenCalledTimes(1)
  })

  it('sends nothing when level 1 stop’s confirmation is cancelled', () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    stubs.emergencyStop = vi.fn(() => new Promise(() => {}))
    renderScreen({ session: emergencyAllowedSession })
    fireEvent.click(screen.getByRole('button', { name: 'Stop' }))
    expect(stubs.emergencyStop).not.toHaveBeenCalled()
  })

  it('arms then fires the hard-stop gate with the minted token, with Fire disabled until armed', async () => {
    stubs.armEmergencyStopHardStop = vi.fn(() =>
      Promise.resolve({ serverTime: '2026-08-28T21:07:00Z', armToken: 'token-1', expiresAt: new Date(Date.now() + 10_000).toISOString() }),
    )
    stubs.fireEmergencyStopHardStop = vi.fn((armToken: string) =>
      Promise.resolve({
        level: 'hard-stop',
        idempotencyKey: 'k2',
        stopOutcomes: [{ instanceId: 'main-player', outcome: 'confirmed', outcomeReason: `Stopped (${armToken}).`, dispatchedAt: null, replay: false }],
        noInstancesConfigured: false,
        nightSession: { present: false },
        followUps: [],
      }),
    )
    renderScreen({ session: emergencyAllowedSession })
    const fire = screen.getByRole('button', { name: 'Fire hard stop' })
    expect(fire).toBeDisabled()

    fireEvent.click(screen.getByRole('button', { name: 'Arm hard stop' }))
    await screen.findByText(/Armed\. Fire within/)
    expect(fire).not.toBeDisabled()

    fireEvent.click(fire)
    expect(await screen.findByText('No night session was active.')).toBeInTheDocument()
    expect(stubs.fireEmergencyStopHardStop).toHaveBeenCalledExactlyOnceWith('token-1')
  })

  it('reports a refused fire attempt honestly instead of retrying it', async () => {
    stubs.armEmergencyStopHardStop = vi.fn(() =>
      Promise.resolve({ serverTime: '2026-08-28T21:07:00Z', armToken: 'token-1', expiresAt: new Date(Date.now() + 10_000).toISOString() }),
    )
    stubs.fireEmergencyStopHardStop = vi.fn(() => Promise.reject(new ApiError('this arm token is unknown, expired, or already consumed', 409)))
    renderScreen({ session: emergencyAllowedSession })
    fireEvent.click(screen.getByRole('button', { name: 'Arm hard stop' }))
    await screen.findByText(/Armed\. Fire within/)
    fireEvent.click(screen.getByRole('button', { name: 'Fire hard stop' }))
    expect(await screen.findByText(/Hard stop was refused/)).toBeInTheDocument()
    expect(stubs.fireEmergencyStopHardStop).toHaveBeenCalledTimes(1)
    expect(stubs.armEmergencyStopHardStop).toHaveBeenCalledTimes(1)
  })

  it('renders an already-expired arm response as expired and leaves Fire disabled', async () => {
    stubs.armEmergencyStopHardStop = vi.fn(() =>
      Promise.resolve({ serverTime: '2026-08-28T21:07:00Z', armToken: 'token-1', expiresAt: '2026-08-28T21:06:00Z' }),
    )
    renderScreen({ serverTime: '2026-08-28T21:07:00Z', session: emergencyAllowedSession })
    fireEvent.click(screen.getByRole('button', { name: 'Arm hard stop' }))
    expect(await screen.findByText(/The arm token expired\. Arm again, then fire promptly\./)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Fire hard stop' })).toBeDisabled()
  })

  it('lets a live arm token be replaced by re-arming, and fires with the newest token', async () => {
    let armCount = 0
    stubs.armEmergencyStopHardStop = vi.fn(() => {
      armCount += 1
      return Promise.resolve({
        serverTime: '2026-08-28T21:07:00Z',
        armToken: `token-${armCount}`,
        expiresAt: new Date(Date.now() + 10_000).toISOString(),
      })
    })
    stubs.fireEmergencyStopHardStop = vi.fn((armToken: string) =>
      Promise.resolve({
        level: 'hard-stop',
        idempotencyKey: 'k3',
        stopOutcomes: [{ instanceId: 'main-player', outcome: 'confirmed', outcomeReason: `Stopped (${armToken}).`, dispatchedAt: null, replay: false }],
        noInstancesConfigured: false,
        nightSession: { present: false },
        followUps: [],
      }),
    )
    renderScreen({ session: emergencyAllowedSession })
    const arm = screen.getByRole('button', { name: 'Arm hard stop' })

    fireEvent.click(arm)
    await screen.findByText(/Armed\. Fire within/)
    expect(arm).not.toBeDisabled()

    fireEvent.click(arm)
    await waitFor(() => expect(stubs.armEmergencyStopHardStop).toHaveBeenCalledTimes(2))

    fireEvent.click(screen.getByRole('button', { name: 'Fire hard stop' }))
    expect(await screen.findByText('No night session was active.')).toBeInTheDocument()
    expect(stubs.fireEmergencyStopHardStop).toHaveBeenCalledExactlyOnceWith('token-2')
  })

  it('draws the mock’s Fire button, warns that it is not wired, and leaves it inert', async () => {
    await renderAnnouncements()
    expect(screen.getByText(/Firing an announcement does nothing yet/)).toBeInTheDocument()
    expect(screen.getByText('POST /cues/{id}/fire')).toBeInTheDocument()
    const fire = screen.getAllByRole('button', { name: 'Fire' })
    expect(fire).toHaveLength(2)
    for (const button of fire) expect(button).toBeDisabled()
  })

  it('disables an announcement whose audio asset has not been uploaded, with that reason', async () => {
    await renderAnnouncements()
    // welcome resolves to an uploaded sequence, so only it carries the not-wired tag.
    expect(document.querySelectorAll('.sm-nowire-tag__chip')).toHaveLength(1)
    expect(screen.getByText(/Its audio asset has not been uploaded/)).toBeInTheDocument()
  })

  it('disables a command with its reason when the principal lacks the scope', () => {
    renderScreen({
      fpp: [
        {
          instanceId: 'main-player',
          health: 'healthy',
          observations: [observation('fpp.status.player_state', 'playing', 'current', 'fpp', 'main-player')],
          instanceUuidChange: null,
        } as never,
      ],
      session: {
        serverTime: '2026-08-28T21:07:00Z',
        authenticated: true,
        principal: { id: 'p', name: 'op', role: 'viewer', disabled: false },
        session: null,
        credentialForm: 'session',
        scopes: [],
        scopesState: 'current',
        bootstrapRequired: false,
      } as never,
    })
    const pause = screen.getByRole('button', { name: /Pause/ })
    expect(pause).toBeDisabled()
    expect(pause).toHaveAttribute('title', expect.stringContaining('fpp:command'))
  })

  it('reports a night command as accepted, never as done', () => {
    renderScreen({})
    expect(screen.getByText(/answers 202/)).toBeInTheDocument()
    expect(screen.getByText(/never that it is done/)).toBeInTheDocument()
  })

  it('groups the lifecycle commands into Prepare, Start, End the night, with the late-start checkbox in Start night: the same element Show Night renders', () => {
    renderScreen({})
    const region = screen.getByRole('region', { name: 'Night lifecycle' })
    expect(within(region).getAllByRole('heading', { level: 3 }).map((h) => h.textContent)).toEqual(['Prepare', 'Start', 'End the night'])
    expect(within(region).getByLabelText(/Skip the enter-show lead/)).toBeInTheDocument()
  })

  it('renders Prepare as Prepare site, Run readiness and Start as Start preshow, Start night: the group spec order, not blocks.css’s unscoped per-command order', () => {
    renderScreen({})
    const region = screen.getByRole('region', { name: 'Night lifecycle' })
    const prepareSection = within(region).getByRole('heading', { name: 'Prepare', level: 3 }).closest('section') as HTMLElement
    expect(within(prepareSection).getAllByRole('button').map((b) => b.textContent)).toEqual(['Prepare site', 'Run readiness'])
    const startSection = within(region).getByRole('heading', { name: 'Start', level: 3 }).closest('section') as HTMLElement
    expect(within(startSection).getAllByRole('button').map((b) => b.textContent)).toEqual(['Start preshow', 'Start night'])
  })

  it('says an unconfirmed command was not confirmed', () => {
    const result = {
      outcome: 'unconfirmed',
      outcomeReason: 'No observation has moved since dispatch.',
      dispatchedAt: '2026-08-28T21:05:42Z',
      resolvedAt: null,
    } as unknown as FPPCommandResult
    const outcome = describeFPPOutcome(result, 'Next item')
    expect(outcome.tone).toBe('warn')
    expect(outcome.label).toBe('Not confirmed')
    expect(outcome.detail).toContain('Nothing has yet reported that it took effect')
  })

  it('confirms only from evidence that post-dates the command', () => {
    const result = {
      outcome: 'confirmed',
      outcomeReason: 'Observed playhead moved.',
      dispatchedAt: '2026-08-28T21:05:42Z',
      resolvedAt: '2026-08-28T21:05:44Z',
    } as unknown as FPPCommandResult
    expect(describeFPPOutcome(result, 'Next item').detail).toContain('confirmed by observed evidence')
  })

  it('gives a row only to observations that name a surface', () => {
    const rows = outputRows(
      {
        ...initialModel(),
        nodes: [
          node('barn', [
            observation('surface.pipeline.state', 'running'),
            observation('surface.frames.rate', 40),
            observation('node.heartbeat', true, 'current', 'node', 'barn'),
          ]),
        ],
      },
      '2026-08-28T21:07:00Z',
    )
    expect(rows).toHaveLength(1)
    expect(rows[0]?.name).toBe('front')
    expect(rows[0]?.doing).toBe('running at 40 fps')
  })

  it('reads the transport from the FPP instance’s own signals', () => {
    const state = transportState({
      observations: [
        observation('fpp.playlist.name', 'WinterRidge_Main', 'current', 'fpp', 'main'),
        observation('fpp.status.player_state', 'playing', 'current', 'fpp', 'main'),
        observation('fpp.position.elapsed.seconds', 102, 'current', 'fpp', 'main'),
      ],
    } as never)
    expect(state.playlist).toBe('WinterRidge_Main')
    expect(state.playerState).toBe('playing')
    expect(formatPosition(state.elapsedSeconds)).toBe('1:42')
  })

  it('dispatches startFPPPlaylist with the typed name, repeat, and ifBusy refuse', async () => {
    let received: unknown[] = []
    stubs.startFPPPlaylist = (...args: unknown[]) => {
      received = args
      return Promise.resolve({
        outcome: 'confirmed',
        outcomeReason: 'Observed playhead moved.',
        dispatchedAt: '2026-08-28T21:05:42Z',
        resolvedAt: '2026-08-28T21:05:44Z',
      } as unknown as FPPCommandResult)
    }
    renderScreen({ fpp: [fppInstance()], session: commandAllowedSession })

    fireEvent.change(screen.getByLabelText('Playlist name'), { target: { value: 'Standard Show' } })
    fireEvent.click(screen.getByLabelText('Repeat'))
    fireEvent.click(screen.getByRole('button', { name: 'Start playlist' }))

    expect(received).toEqual(['main-player', 'Standard Show', true, 'refuse'])
    expect(await screen.findByText(/confirmed by observed evidence/)).toBeInTheDocument()
  })

  it('offers FPP’s own imported playlist names as a dropdown, preselecting the one reported as playing', async () => {
    stubs.listFPPPlaylistDefinitions = () =>
      Promise.resolve({
        definitions: [
          { instanceUuid: 'uuid-1', playlistName: 'Standard Show', playlistHash: 'h1', capturedAt: '', receivedAt: '', entryCount: 1, referenced: true },
          { instanceUuid: 'uuid-1', playlistName: 'Holiday Show', playlistHash: 'h2', capturedAt: '', receivedAt: '', entryCount: 1, referenced: true },
          { instanceUuid: 'uuid-other', playlistName: 'Other instance only', playlistHash: 'h3', capturedAt: '', receivedAt: '', entryCount: 1, referenced: true },
        ],
      })
    renderScreen({
      fpp: [
        {
          instanceId: 'main-player',
          instanceUuid: 'uuid-1',
          health: 'healthy',
          observations: [
            observation('fpp.status.player_state', 'playing', 'current', 'fpp', 'main-player'),
            observation('fpp.playlist.name', 'Holiday Show', 'current', 'fpp', 'main-player'),
          ],
          instanceUuidChange: null,
        } as never,
      ],
      session: commandAllowedSession,
    })

    const select = await screen.findByLabelText('Playlist')
    expect(screen.queryByLabelText('Playlist name')).not.toBeInTheDocument()
    const optionLabels = Array.from(select.querySelectorAll('option')).map((o) => o.textContent)
    expect(optionLabels).toEqual(['Choose a playlist', 'Holiday Show', 'Standard Show'])
    expect(select).toHaveValue('Holiday Show')
  })

  it('adds the reported-but-unimported playlist as its own option, so the select never shows blank while the state is non-empty', async () => {
    stubs.listFPPPlaylistDefinitions = () =>
      Promise.resolve({
        definitions: [
          { instanceUuid: 'uuid-1', playlistName: 'Standard Show', playlistHash: 'h1', capturedAt: '', receivedAt: '', entryCount: 1, referenced: true },
        ],
      })
    renderScreen({
      fpp: [
        {
          instanceId: 'main-player',
          instanceUuid: 'uuid-1',
          health: 'healthy',
          observations: [
            observation('fpp.status.player_state', 'playing', 'current', 'fpp', 'main-player'),
            observation('fpp.playlist.name', 'Mystery Show', 'current', 'fpp', 'main-player'),
          ],
          instanceUuidChange: null,
        } as never,
      ],
      session: commandAllowedSession,
    })

    const select = await screen.findByLabelText('Playlist')
    const optionLabels = Array.from(select.querySelectorAll('option')).map((o) => o.textContent)
    expect(optionLabels).toEqual(['Choose a playlist', 'Mystery Show (reported by FPP)', 'Standard Show'])
    expect(select).toHaveValue('Mystery Show')
    expect(screen.getByRole('button', { name: 'Start playlist' })).not.toBeDisabled()
  })

  it('falls back to a typed playlist name when the coordinator has reported none for this instance', () => {
    stubs.listFPPPlaylistDefinitions = () => Promise.resolve({ definitions: [] })
    renderScreen({ fpp: [fppInstance()], session: commandAllowedSession })
    expect(screen.getByLabelText('Playlist name')).toBeInTheDocument()
    expect(screen.getByText(/coordinator has reported no imported playlists/)).toBeInTheDocument()
  })

  it('renders a start-playlist busy conflict distinguishably from a generic Refused outcome, and its own CTA resends ifBusy: replace', async () => {
    const calls: unknown[][] = []
    stubs.startFPPPlaylist = (...args: unknown[]) => {
      calls.push(args)
      if (args[3] === 'refuse') {
        return Promise.reject(
          new ApiError('instance "main-player" is currently playing "Other Show"', 409, PROBLEM_TYPE.fppStartPlaylistBusy),
        )
      }
      return Promise.resolve({
        outcome: 'confirmed',
        outcomeReason: 'Observed playhead moved.',
        dispatchedAt: '2026-08-28T21:05:42Z',
        resolvedAt: '2026-08-28T21:05:44Z',
      } as unknown as FPPCommandResult)
    }
    renderScreen({ fpp: [fppInstance()], session: commandAllowedSession })

    fireEvent.change(screen.getByLabelText('Playlist name'), { target: { value: 'Standard Show' } })
    fireEvent.click(screen.getByRole('button', { name: 'Start playlist' }))

    const busyAlert = await screen.findByRole('alert')
    expect(busyAlert).toHaveTextContent('Busy')
    expect(busyAlert).toHaveTextContent('currently playing "Other Show"')
    // A generic Refused outcome never renders inside role="alert" here (Outcome has no role), so this is distinguishable.
    expect(screen.queryByText('Refused')).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Start anyway (replace what is currently playing)' }))
    expect(calls).toHaveLength(2)
    expect(calls[1]).toEqual(['main-player', 'Standard Show', false, 'replace'])
    expect(await screen.findByText(/confirmed by observed evidence/)).toBeInTheDocument()
  })

  it('falls back to the generic Refused outcome for a non-conflict start-playlist failure', async () => {
    stubs.startFPPPlaylist = () => Promise.reject(new Error('no route to host'))
    renderScreen({ fpp: [fppInstance()], session: commandAllowedSession })

    fireEvent.change(screen.getByLabelText('Playlist name'), { target: { value: 'Standard Show' } })
    fireEvent.click(screen.getByRole('button', { name: 'Start playlist' }))

    expect(await screen.findByText('Refused')).toBeInTheDocument()
    expect(screen.getByText(/no route to host/)).toBeInTheDocument()
  })

  it('sends afterLoop: false for "Stop after this item"', async () => {
    let received: unknown[] = []
    stubs.stopFPPPlaylistGracefully = (...args: unknown[]) => {
      received = args
      return Promise.resolve({
        outcome: 'confirmed',
        outcomeReason: 'Observed player state moved.',
        dispatchedAt: '2026-08-28T21:05:42Z',
        resolvedAt: '2026-08-28T21:05:44Z',
      } as unknown as FPPCommandResult)
    }
    renderScreen({ fpp: [fppInstance('playing')], session: commandAllowedSession })

    fireEvent.click(screen.getByRole('button', { name: 'Stop after this item' }))

    expect(received).toEqual(['main-player', false])
    expect(await screen.findByText(/confirmed by observed evidence/)).toBeInTheDocument()
  })

  it('sends afterLoop: true for "Stop after this loop"', async () => {
    let received: unknown[] = []
    stubs.stopFPPPlaylistGracefully = (...args: unknown[]) => {
      received = args
      return Promise.resolve({
        outcome: 'confirmed',
        outcomeReason: 'Observed player state moved.',
        dispatchedAt: '2026-08-28T21:05:42Z',
        resolvedAt: '2026-08-28T21:05:44Z',
      } as unknown as FPPCommandResult)
    }
    renderScreen({ fpp: [fppInstance('playing')], session: commandAllowedSession })

    fireEvent.click(screen.getByRole('button', { name: 'Stop after this loop' }))

    expect(received).toEqual(['main-player', true])
    expect(await screen.findByText(/confirmed by observed evidence/)).toBeInTheDocument()
  })

  it('keeps the graceful-stop buttons in the transport button row, undescribed, matching the mock', () => {
    renderScreen({ fpp: [fppInstance('playing')], session: commandAllowedSession })

    const item = screen.getByRole('button', { name: 'Stop after this item' })
    const loop = screen.getByRole('button', { name: 'Stop after this loop' })
    const now = screen.getByRole('button', { name: /Stop now/ })
    // Same row as every other transport button, in mock order: no wrapper div, no description paragraph.
    expect(item.parentElement).toBe(loop.parentElement)
    expect(item.parentElement).toBe(now.parentElement)
    expect(item.parentElement).toHaveClass('sm-btn-row')
    expect(screen.queryByText('FPP finishes the current item, then stops the playlist.')).not.toBeInTheDocument()
    expect(screen.queryByText('FPP finishes the current loop through the playlist, then stops.')).not.toBeInTheDocument()
  })

  it('shows that current-run state has not loaded yet, before the first response', () => {
    renderScreen({ fpp: [fppInstance('playing')], session: commandAllowedSession })
    const fact = screen.getByText('Reading current-run state for program audio.')
    expect(fact.closest('.sm-strip')?.querySelector('.sm-strip__label')).toHaveTextContent('Reading')
  })

  it('shows current-run state as unavailable, not silently dropped, when the coordinator does not serve it', () => {
    renderScreen({
      fpp: [fppInstance('playing')],
      session: commandAllowedSession,
      currentRuns: null,
      currentRunsFetchFailed: true,
    })
    expect(screen.getByText('Now playing not reported')).toBeInTheDocument()
    expect(
      screen.getByText('This coordinator does not serve current-run state, so program audio has no row here.'),
    ).toBeInTheDocument()
  })

  it('shows no current-run absence notice once current-run state has loaded', () => {
    renderScreen({
      fpp: [fppInstance('playing')],
      session: commandAllowedSession,
      currentRuns: { serverTime: '2026-08-28T21:07:00Z', activeShow: { configured: false }, runs: [] } as never,
    })
    expect(screen.queryByText('Now playing not reported')).not.toBeInTheDocument()
    expect(screen.queryByText('Reading current-run state for program audio.')).not.toBeInTheDocument()
  })

  it('dispatches each audio session transport and mute verb with the node, session id, and derived revision', async () => {
    stubs.prepareAudioSession = vi.fn(() => Promise.resolve(audioCommandResult({ action: 'audio.session.prepare' })))
    stubs.startAudioSession = vi.fn(() => Promise.resolve(audioCommandResult({ action: 'audio.session.start' })))
    stubs.pauseAudioSession = vi.fn(() => Promise.resolve(audioCommandResult({ action: 'audio.session.pause' })))
    stubs.resumeAudioSession = vi.fn(() => Promise.resolve(audioCommandResult({ action: 'audio.session.resume' })))
    stubs.advanceAudioSession = vi.fn(() => Promise.resolve(audioCommandResult({ action: 'audio.session.advance' })))
    stubs.stopAudioSession = vi.fn(() => Promise.resolve(audioCommandResult({ action: 'audio.session.stop', outcome: 'stopped' })))
    stubs.muteAudioSessionOutput = vi.fn(() => Promise.resolve(audioCommandResult({ action: 'audio.output.mute' })))
    stubs.unmuteAudioSessionOutput = vi.fn(() => Promise.resolve(audioCommandResult({ action: 'audio.output.unmute' })))

    const region = await renderAudioSessionsReady()

    fireEvent.click(within(region).getByRole('button', { name: 'Prepare' }))
    fireEvent.click(within(region).getByRole('button', { name: 'Start' }))
    fireEvent.click(within(region).getByRole('button', { name: 'Pause' }))
    fireEvent.click(within(region).getByRole('button', { name: 'Resume' }))
    fireEvent.click(within(region).getByRole('button', { name: 'Advance' }))
    fireEvent.click(within(region).getByRole('button', { name: 'Stop' }))
    fireEvent.click(within(region).getByRole('button', { name: 'Mute' }))
    fireEvent.click(within(region).getByRole('button', { name: 'Unmute' }))

    expect(stubs.prepareAudioSession).toHaveBeenCalledWith('audio-node-01', 'bg-holiday-01', 1)
    expect(stubs.startAudioSession).toHaveBeenCalledWith('audio-node-01', 'bg-holiday-01', 1)
    expect(stubs.pauseAudioSession).toHaveBeenCalledWith('audio-node-01', 'bg-holiday-01', 1)
    expect(stubs.resumeAudioSession).toHaveBeenCalledWith('audio-node-01', 'bg-holiday-01', 1)
    expect(stubs.advanceAudioSession).toHaveBeenCalledWith('audio-node-01', 'bg-holiday-01', 1)
    expect(stubs.stopAudioSession).toHaveBeenCalledWith('audio-node-01', 'bg-holiday-01', 1)
    expect(stubs.muteAudioSessionOutput).toHaveBeenCalledWith('audio-node-01', 'bg-holiday-01', 1)
    expect(stubs.unmuteAudioSessionOutput).toHaveBeenCalledWith('audio-node-01', 'bg-holiday-01', 1)
    expect(await within(region).findByText('Started')).toBeInTheDocument()
  })

  it('sends seek in milliseconds parsed from the typed timecode at the read LTC frame rate', async () => {
    stubs.seekAudioSession = vi.fn(() => Promise.resolve(audioCommandResult({ action: 'audio.session.seek', outcome: 'position' })))
    const region = await renderAudioSessionsReady({ fps: 30 })
    await within(region).findByText(/hh:mm:ss\.ff at 30 fps/)

    fireEvent.change(within(region).getByLabelText('Position'), { target: { value: '00:01:30' } })
    fireEvent.click(within(region).getByRole('button', { name: 'Seek' }))

    expect(stubs.seekAudioSession).toHaveBeenCalledWith('audio-node-01', 'bg-holiday-01', 1, 90000)
  })

  it('sends gain set in decibels', async () => {
    stubs.setAudioSessionGain = vi.fn(() => Promise.resolve(audioCommandResult({ action: 'audio.gain.set' })))
    const region = await renderAudioSessionsReady()

    fireEvent.change(within(region).getByLabelText('Gain'), { target: { value: '-6' } })
    fireEvent.click(within(region).getByRole('button', { name: 'Set' }))

    expect(stubs.setAudioSessionGain).toHaveBeenCalledWith('audio-node-01', 'bg-holiday-01', 1, -6)
  })

  it('sends gain fade with the target dB and an optional duration in milliseconds', async () => {
    stubs.fadeAudioSessionGain = vi.fn(() => Promise.resolve(audioCommandResult({ action: 'audio.gain.fade' })))
    const region = await renderAudioSessionsReady()

    fireEvent.change(within(region).getByLabelText('Fade to'), { target: { value: '-12' } })
    fireEvent.change(within(region).getByLabelText('Over'), { target: { value: '2000' } })
    fireEvent.click(within(region).getByRole('button', { name: 'Fade' }))

    expect(stubs.fadeAudioSessionGain).toHaveBeenCalledWith('audio-node-01', 'bg-holiday-01', 1, -12, 2000)
  })

  it('leaves the fade duration off the call when the operator leaves it empty', async () => {
    stubs.fadeAudioSessionGain = vi.fn(() => Promise.resolve(audioCommandResult({ action: 'audio.gain.fade' })))
    const region = await renderAudioSessionsReady()

    fireEvent.change(within(region).getByLabelText('Fade to'), { target: { value: '-12' } })
    fireEvent.click(within(region).getByRole('button', { name: 'Fade' }))

    expect(stubs.fadeAudioSessionGain).toHaveBeenCalledWith('audio-node-01', 'bg-holiday-01', 1, -12, undefined)
  })

  it('keeps Clear disabled until the session id is typed to confirm, then sends it', async () => {
    stubs.clearAudioSession = vi.fn(() => Promise.resolve(audioCommandResult({ action: 'audio.session.clear', outcome: 'stopped' })))
    const region = await renderAudioSessionsReady()

    const clearButton = within(region).getByRole('button', { name: 'Clear' })
    expect(clearButton).toBeDisabled()

    fireEvent.change(within(region).getByLabelText('Type bg-holiday-01 to confirm'), { target: { value: 'bg-holiday-01' } })
    expect(clearButton).not.toBeDisabled()

    fireEvent.click(clearButton)
    expect(stubs.clearAudioSession).toHaveBeenCalledWith('audio-node-01', 'bg-holiday-01', 1)
  })

  /** Types the exact session id into Apply's own typed-confirmation field, the same gate Clear uses. */
  function armApply(region: HTMLElement, sessionId = 'bg-holiday-01') {
    fireEvent.change(within(region).getByLabelText(`Type ${sessionId} to confirm applying`), { target: { value: sessionId } })
  }

  it('keeps Apply disabled until the session id is typed to confirm, matching Clear', async () => {
    const region = await renderAudioSessionsReady()

    const applyButton = within(region).getByRole('button', { name: 'Apply' })
    expect(applyButton).toBeDisabled()

    armApply(region)
    expect(applyButton).not.toBeDisabled()
  })

  it('parses the Apply payload as JSON and sends it with the node, session id, and derived revision', async () => {
    stubs.applyAudioSession = vi.fn(() => Promise.resolve(audioCommandResult({ action: 'audio.session.apply', outcome: 'completed' })))
    const region = await renderAudioSessionsReady()

    fireEvent.change(within(region).getByLabelText('Params (JSON)'), { target: { value: '{"sourceRole": "primary"}' } })
    armApply(region)
    fireEvent.click(within(region).getByRole('button', { name: 'Apply' }))

    expect(stubs.applyAudioSession).toHaveBeenCalledWith('audio-node-01', 'bg-holiday-01', 1, { sourceRole: 'primary' })
    expect(await within(region).findByText('Completed')).toBeInTheDocument()
  })

  it('sends Apply with params omitted, not an empty object, when the payload is left empty', async () => {
    stubs.applyAudioSession = vi.fn(() => Promise.resolve(audioCommandResult({ action: 'audio.session.apply', outcome: 'completed' })))
    const region = await renderAudioSessionsReady()

    armApply(region)
    fireEvent.click(within(region).getByRole('button', { name: 'Apply' }))

    expect(stubs.applyAudioSession).toHaveBeenCalledWith('audio-node-01', 'bg-holiday-01', 1, undefined)
  })

  it('reports malformed JSON locally, says nothing was sent, and never dispatches Apply', async () => {
    stubs.applyAudioSession = vi.fn(() => Promise.resolve(audioCommandResult({ action: 'audio.session.apply' })))
    const region = await renderAudioSessionsReady()

    fireEvent.change(within(region).getByLabelText('Params (JSON)'), { target: { value: '{not json' } })
    armApply(region)
    fireEvent.click(within(region).getByRole('button', { name: 'Apply' }))

    expect(await within(region).findByText('Invalid JSON')).toBeInTheDocument()
    expect(within(region).getByText(/The payload was not sent\. It is not valid JSON, so it never reached the coordinator/)).toBeInTheDocument()
    expect(stubs.applyAudioSession).not.toHaveBeenCalled()
  })

  it('reports a non-object JSON payload with the same local-failure shape as a parse error, for a string, a number and an array', async () => {
    stubs.applyAudioSession = vi.fn(() => Promise.resolve(audioCommandResult({ action: 'audio.session.apply' })))
    const region = await renderAudioSessionsReady()
    armApply(region)

    for (const payload of ['"just a string"', '42', '[1, 2, 3]']) {
      fireEvent.change(within(region).getByLabelText('Params (JSON)'), { target: { value: payload } })
      fireEvent.click(within(region).getByRole('button', { name: 'Apply' }))
      expect(await within(region).findByText('Invalid JSON')).toBeInTheDocument()
      expect(within(region).getByText(/The payload was not sent\. It must be a JSON object/)).toBeInTheDocument()
    }
    expect(stubs.applyAudioSession).not.toHaveBeenCalled()
  })

  it('reports Apply as Refused, distinct from Invalid JSON, when the coordinator declines a syntactically valid payload', async () => {
    stubs.applyAudioSession = vi.fn(() => Promise.reject(new ApiError('revision is stale', 409, PROBLEM_TYPE.conflict)))
    const region = await renderAudioSessionsReady()

    fireEvent.change(within(region).getByLabelText('Params (JSON)'), { target: { value: '{"sourceRole": "primary"}' } })
    armApply(region)
    fireEvent.click(within(region).getByRole('button', { name: 'Apply' }))

    expect(await within(region).findByText('Refused')).toBeInTheDocument()
    expect(within(region).queryByText('Invalid JSON')).not.toBeInTheDocument()
  })

  it('disables every audio session control with the audio:command reason when the scope is missing', async () => {
    stubs.listConfigObjects = ((kind: string) => {
      if (kind === 'audio.node') return Promise.resolve({ objects: [{ id: 'audio-node-01', label: 'Front porch node' }] })
      return Promise.resolve({ objects: [] })
    }) as never
    stubs.listObservations = () => Promise.resolve({ observations: [] })
    renderScreen({
      session: {
        serverTime: '2026-08-28T21:07:00Z',
        authenticated: true,
        principal: { id: 'p', name: 'op', role: 'viewer', disabled: false },
        session: null,
        credentialForm: 'session',
        scopes: [],
        scopesState: 'current',
        bootstrapRequired: false,
      } as never,
    })

    const region = await screen.findByRole('region', { name: 'Audio sessions' })
    fireEvent.click(within(region).getByRole('button', { name: /Audio sessions…/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('Session id'), { target: { value: 'bg-holiday-01' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Open' }))
    const prepare = within(dialog).getByRole('button', { name: 'Prepare' })
    expect(prepare).toBeDisabled()
    expect(prepare).toHaveAttribute('title', expect.stringContaining('audio:command'))
  })

  it('renders the empty state when no audio node is declared', async () => {
    stubs.listConfigObjects = ((kind: string) => {
      if (kind === 'audio.node') return Promise.resolve({ objects: [] })
      return Promise.resolve({ objects: [] })
    }) as never
    renderScreen({ session: audioAllowedSession })

    const region = await screen.findByRole('region', { name: 'Audio sessions' })
    expect(within(region).getByText('No audio nodes')).toBeInTheDocument()
    expect(within(region).getByText('No node advertises an audio engine.')).toBeInTheDocument()
  })

  it('collapses the target, known sessions and transport into one drawer behind one button, with a one-line summary on the section itself', async () => {
    stubs.listConfigObjects = ((kind: string) => {
      if (kind === 'audio.node') return Promise.resolve({ objects: [{ id: 'audio-node-01', label: 'Front porch node' }] })
      return Promise.resolve({ objects: [] })
    }) as never
    stubs.listObservations = () =>
      Promise.resolve({ observations: [observation('audio_session.state', 'started', 'current', 'audio_session', 'bg-holiday-01')] })
    renderScreen({ session: audioAllowedSession })

    const region = await screen.findByRole('region', { name: 'Audio sessions' })
    expect(await within(region).findByText('1 known session.')).toBeInTheDocument()
    expect(within(region).queryByText(/Loading media into a session/)).not.toBeInTheDocument()
    expect(within(region).queryByLabelText('Session id')).not.toBeInTheDocument()

    fireEvent.click(within(region).getByRole('button', { name: /Audio sessions…/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByLabelText('Node')).toBeInTheDocument()
    expect(within(dialog).getByLabelText('Known sessions')).toBeInTheDocument()
    expect(within(dialog).getByLabelText('Session id')).toBeInTheDocument()
    expect(within(dialog).queryByRole('button', { name: 'Prepare' })).not.toBeInTheDocument()

    fireEvent.change(within(dialog).getByLabelText('Session id'), { target: { value: 'bg-holiday-01' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Open' }))
    expect(within(dialog).getByRole('button', { name: 'Prepare' })).toBeInTheDocument()
    // The caption states a count only; it never grows an "open" clause, reachable or not.
    expect(within(region).getByText('1 known session.')).toBeInTheDocument()
  })
})
