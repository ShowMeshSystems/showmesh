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
// Finding 16: listConfigObjects/getShowSurface are the two calls
// [useConfiguredSurfaceIds] makes to discover a configured-but-unapplied
// surface — mocked here for the identical reason as the three command
// verbs below, and defaulted to "nothing configured" so tests that never
// set them keep the pre-fix behavior.
const { applyRenderSurface, clearRenderSurface, restartRenderPipeline, listConfigObjects, getShowSurface } =
  vi.hoisted(() => ({
    applyRenderSurface: vi.fn(),
    clearRenderSurface: vi.fn(),
    restartRenderPipeline: vi.fn(),
    listConfigObjects: vi.fn(),
    getShowSurface: vi.fn(),
  }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, applyRenderSurface, clearRenderSurface, restartRenderPipeline, listConfigObjects, getShowSurface }
})

afterEach(() => {
  cleanup()
  applyRenderSurface.mockReset()
  clearRenderSurface.mockReset()
  restartRenderPipeline.mockReset()
  listConfigObjects.mockReset()
  getShowSurface.mockReset()
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
    pipelineFailed: false,
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

  // Finding 16: a node with a configured show.surface but no assignment
  // yet reports zero render evidence (a supervisor entry, and so a
  // report row, exists only AFTER an apply). Before this fix,
  // entries.length === 0 always took the "never published" early return
  // and no control of any kind rendered, so the first apply for such a
  // surface was reachable from showmeshctl and NOT from the Operator UI.
  // Revert useConfiguredSurfaceIds (or the union into bySurface/order in
  // RenderSurfacePanel) and this test fails: it stays on "This node has
  // never published a render report" and finds no "Apply" button.
  it('renders apply/clear/restart for a configured surface that has never reported (Finding 16)', async () => {
    listConfigObjects.mockResolvedValue({
      serverTime: NOW,
      kind: 'show.surface',
      objects: [{ id: 'wall-1', label: 'Wall', show: 'halloween', currentRevision: 1, updatedAt: NOW }],
    })
    getShowSurface.mockResolvedValue({
      serverTime: NOW,
      kind: 'show.surface',
      id: 'wall-1',
      revision: 1,
      payload: {
        show: 'halloween',
        name: 'Wall',
        node: 'media-01',
        channelRange: { startChannel: 1, channelCount: 12 },
        geometry: { width: 2, height: 2, pixelFormat: 'rgb' },
        frameRate: 40,
        output: { transport: 'ndi', ndi: { sourceName: 'wall-1' } },
      },
      updatedAt: NOW,
      createdByPrincipalId: null,
      createdByPrincipalName: null,
    })
    const model = makeModel({ session: signedIn({ scopes: ['render:command', 'show:macro:run'] }) })
    renderPanel(model, [])

    expect(await screen.findByText('Surface: wall-1')).toBeInTheDocument()
    expect(screen.getByText(/never applied, so there is no render report yet/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Apply' })).toBeEnabled()
    expect(screen.queryByText(/has never published a render report/)).not.toBeInTheDocument()
  })

  it('does not union in a show.surface object assigned to a different node', async () => {
    listConfigObjects.mockResolvedValue({
      serverTime: NOW,
      kind: 'show.surface',
      objects: [{ id: 'other-wall', label: 'Other', show: 'halloween', currentRevision: 1, updatedAt: NOW }],
    })
    getShowSurface.mockResolvedValue({
      serverTime: NOW,
      kind: 'show.surface',
      id: 'other-wall',
      revision: 1,
      payload: {
        show: 'halloween',
        name: 'Other',
        node: 'media-02',
        channelRange: { startChannel: 1, channelCount: 12 },
        geometry: { width: 2, height: 2, pixelFormat: 'rgb' },
        frameRate: 40,
        output: { transport: 'ndi', ndi: { sourceName: 'other-wall' } },
      },
      updatedAt: NOW,
      createdByPrincipalId: null,
      createdByPrincipalName: null,
    })
    const model = makeModel({ session: signedIn({ scopes: ['render:command', 'show:macro:run'] }) })
    renderPanel(model, [])

    expect(await screen.findByText(/has never published a render report/)).toBeInTheDocument()
    expect(screen.queryByText('Surface: other-wall')).not.toBeInTheDocument()
  })

  it('never fetches configured surfaces when the operator lacks a show.surface read scope', () => {
    const model = makeModel({ session: signedIn({ scopes: ['render:command'] }) })
    renderPanel(model, [])
    expect(screen.getByText(/has never published a render report/)).toBeInTheDocument()
    expect(listConfigObjects).not.toHaveBeenCalled()
  })
})
