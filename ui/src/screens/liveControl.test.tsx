import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api'
import { PROBLEM_TYPE } from '../api/problem'
import type { FPPCommandResult, Model, Node, ResolumeActionResult } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'
import { LiveControl } from './LiveControl'
import { describeFPPOutcome, formatPosition, outputRows, transportState } from './liveControlModel'

const stubs = vi.hoisted(() => ({
  listConfigObjects: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShowCue: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  listAssets: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  startFPPPlaylist: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getResolumeComposition: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  blackoutResolume: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    listConfigObjects: (...args: never[]) => stubs.listConfigObjects(...args),
    getShowCue: (...args: never[]) => stubs.getShowCue(...args),
    listAssets: (...args: never[]) => stubs.listAssets(...args),
    startFPPPlaylist: (...args: never[]) => stubs.startFPPPlaylist(...args),
    getResolumeComposition: (...args: never[]) => stubs.getResolumeComposition(...args),
    blackoutResolume: (...args: never[]) => stubs.blackoutResolume(...args),
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
    stubs.listAssets = () => new Promise(() => {})
    stubs.startFPPPlaylist = () => new Promise(() => {})
    stubs.getResolumeComposition = () => new Promise(() => {})
    stubs.blackoutResolume = () => new Promise(() => {})
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

  it('keeps the Resolume emergency path between output evidence and night lifecycle', () => {
    renderScreen({})
    expect(screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)).toEqual([
      'Transport',
      'What each output is doing',
      'Resolume',
      'Night lifecycle',
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

  it('distinguishes player stop from the separately gated installation-wide emergency workflow', () => {
    renderScreen({})
    expect(screen.getByText(/separate, deliberately gated API workflow/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /emergency/i })).not.toBeInTheDocument()
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
})
