import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { RenderSurfacePanel } from './RenderSurfacePanel'
import { ModelContext } from '../app/ModelContext'
import { makeEvidence, makeModel } from '../app/test-support/fixtures'
import type { Model, ObservationEntry, RenderCommandResult, SessionResponse } from '../app/types'

// Mirrors FPPStopPlaylistControl.test.tsx's own mocking shape: mocked at
// the '../api' boundary, isolating this component's own job (ScopedButton
// wiring to "render:command", grouping by surface, and rendering
// confirmed/unconfirmed honestly per ADR-003) from store.ts's own network
// behavior.
const { applyRenderSurface, clearRenderSurface, restartRenderPipeline } = vi.hoisted(() => ({
  applyRenderSurface: vi.fn(),
  clearRenderSurface: vi.fn(),
  restartRenderPipeline: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, applyRenderSurface, clearRenderSurface, restartRenderPipeline }
})

afterEach(() => {
  cleanup()
  applyRenderSurface.mockReset()
  clearRenderSurface.mockReset()
  restartRenderPipeline.mockReset()
})

const NOW = '2026-08-17T00:00:00.000Z'

function signedIn(overrides: Partial<SessionResponse> = {}): SessionResponse {
  return {
    serverTime: NOW,
    authenticated: true,
    principal: { id: 'p1', name: 'alice', kind: 'human', role: 'operator' },
    session: { id: 's1', deviceLabel: 'porch tablet', createdAt: NOW },
    credentialForm: 'session',
    scopes: ['render:command'],
    scopesState: 'current',
    bootstrapRequired: false,
    ...overrides,
  }
}

function entry(overrides: Partial<ObservationEntry> = {}): ObservationEntry {
  return {
    resource: { kind: 'surface', id: 'wall-1' },
    ...makeEvidence({ signal: 'surface.pipeline.state', value: 'running' }),
    ...overrides,
  }
}

function commandResult(overrides: Partial<RenderCommandResult> = {}): RenderCommandResult {
  return {
    commandId: 'cmd-1',
    idempotencyKey: 'key-1',
    action: 'render.surface.clear',
    nodeId: 'media-01',
    surfaceId: 'wall-1',
    replay: false,
    outcome: 'confirmed',
    outcomeState: 'current',
    outcomeReason: 'surface.pipeline.state = "stopped"',
    dispatchedAt: NOW,
    resolvedAt: NOW,
    // Empty rather than omitted: this fixture's action is a clear, which
    // carries no idleOutput, and the contract states that as "" rather than
    // leaving the field out.
    idleOutput: '',
    ...overrides,
  }
}

function renderPanel(model: Model, entries: ObservationEntry[]) {
  render(
    <ModelContext.Provider value={model}>
      <RenderSurfacePanel nodeId="media-01" entries={entries} />
    </ModelContext.Provider>,
  )
}

describe('RenderSurfacePanel', () => {
  it('renders "never published" distinctly when a node has no render evidence at all', () => {
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [])
    expect(screen.getByRole('status')).toHaveTextContent('has never published a render report')
  })

  it('groups entries by surface and renders each through EvidenceValue', () => {
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [
      entry({ resource: { kind: 'surface', id: 'wall-1' }, signal: 'surface.pipeline.state', value: 'running' }),
      entry({ resource: { kind: 'surface', id: 'wall-2' }, signal: 'surface.pipeline.state', value: 'stopped' }),
    ])
    expect(screen.getByText('Surface: wall-1')).toBeInTheDocument()
    expect(screen.getByText('Surface: wall-2')).toBeInTheDocument()
  })

  it('renders apply/clear/restart disabled, never enabled, when the operator lacks render:command', () => {
    const model = makeModel({ session: signedIn({ scopes: ['node:read'] }) })
    renderPanel(model, [entry()])
    expect(screen.getByRole('button', { name: 'Apply' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Clear' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Restart' })).toBeDisabled()
  })

  it('dispatches clear and renders a confirmed outcome honestly', async () => {
    clearRenderSurface.mockResolvedValue(commandResult({ outcome: 'confirmed' }))
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry()])

    await userEvent.click(screen.getByRole('button', { name: 'Clear' }))

    await waitFor(() => expect(clearRenderSurface).toHaveBeenCalledWith('media-01', 'wall-1'))
    expect(await screen.findByText(/Confirmed:/)).toBeInTheDocument()
  })

  it('never renders an unconfirmed outcome as success', async () => {
    restartRenderPipeline.mockResolvedValue(
      commandResult({ outcome: 'unconfirmed', outcomeReason: 'no evidence arrived before the deadline' }),
    )
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry()])

    await userEvent.click(screen.getByRole('button', { name: 'Restart' }))

    const unconfirmed = await screen.findByRole('alert')
    expect(unconfirmed).toHaveTextContent('Unconfirmed: no evidence arrived before the deadline')
  })

  it('passes the sequenceId input through to applyRenderSurface', async () => {
    applyRenderSurface.mockResolvedValue(commandResult({ action: 'render.surface.apply' }))
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry()])

    await userEvent.type(screen.getByLabelText('Sequence ID for apply:'), 'opener')
    await userEvent.click(screen.getByRole('button', { name: 'Apply' }))

    await waitFor(() => expect(applyRenderSurface).toHaveBeenCalledWith('media-01', 'wall-1', 'opener'))
  })
})
