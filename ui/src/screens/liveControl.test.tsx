import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { FPPCommandResult, Model, Node } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'
import { LiveControl } from './LiveControl'
import { describeFPPOutcome, formatPosition, outputRows, transportState } from './liveControlModel'

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, listConfigObjects: () => new Promise(() => {}), getShowCue: () => new Promise(() => {}) }
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

describe('Live Control', () => {
  afterEach(cleanup)

  it('renders the mock’s six blocks, in order', () => {
    renderScreen({})
    expect(screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)).toEqual([
      'Transport',
      'What each output is doing',
      'Night lifecycle',
      'Macros',
      'Announcements',
      'Actions',
    ])
  })

  it('states that no installation-wide emergency stop is advertised', () => {
    renderScreen({})
    expect(screen.getByText(/no installation-wide emergency stop/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /emergency/i })).not.toBeInTheDocument()
  })

  it('states that an announcement cannot be fired directly', () => {
    renderScreen({})
    expect(screen.getByText(/no way to fire an announcement directly/)).toBeInTheDocument()
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
})
