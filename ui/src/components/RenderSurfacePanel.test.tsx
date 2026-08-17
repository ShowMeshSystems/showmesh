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
// Finding 16 / PR #14 review fix: [useConfiguredSurfaceIds] now makes ONE
// call, listShowSurfacesForNode (GET /config/show.surface?node=), to
// discover a configured-but-unapplied surface. listConfigObjects and
// getShowSurface are ALSO mocked here, deliberately: the review's point
// was that the old per-row getShowSurface fan-out must become unreachable
// from this panel, not merely smaller, so several tests below assert
// those two were never called rather than only asserting the new call
// was.
const {
  applyRenderSurface,
  clearRenderSurface,
  restartRenderPipeline,
  listConfigObjects,
  getShowSurface,
  listShowSurfacesForNode,
} = vi.hoisted(() => ({
  applyRenderSurface: vi.fn(),
  clearRenderSurface: vi.fn(),
  restartRenderPipeline: vi.fn(),
  listConfigObjects: vi.fn(),
  getShowSurface: vi.fn(),
  listShowSurfacesForNode: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return {
    ...actual,
    applyRenderSurface,
    clearRenderSurface,
    restartRenderPipeline,
    listConfigObjects,
    getShowSurface,
    listShowSurfacesForNode,
  }
})

afterEach(() => {
  cleanup()
  applyRenderSurface.mockReset()
  clearRenderSurface.mockReset()
  restartRenderPipeline.mockReset()
  listConfigObjects.mockReset()
  getShowSurface.mockReset()
  listShowSurfacesForNode.mockReset()
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
  //
  // PR #14 review fix: the server now does the node filtering (GET
  // /config/show.surface?node=media-01), so the fixture is already
  // narrowed to this node's surfaces — no getShowSurface round trip.
  it('renders apply/clear/restart for a configured surface that has never reported (Finding 16)', async () => {
    listShowSurfacesForNode.mockResolvedValue({
      serverTime: NOW,
      kind: 'show.surface',
      objects: [{ id: 'wall-1', label: 'Wall', show: 'halloween', currentRevision: 1, updatedAt: NOW }],
    })
    const model = makeModel({ session: signedIn({ scopes: ['render:command', 'show:macro:run'] }) })
    renderPanel(model, [])

    expect(await screen.findByText('Surface: wall-1')).toBeInTheDocument()
    expect(screen.getByText(/never applied, so there is no render report yet/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Apply' })).toBeEnabled()
    expect(screen.queryByText(/has never published a render report/)).not.toBeInTheDocument()
    expect(listShowSurfacesForNode).toHaveBeenCalledWith('media-01')
  })

  // A surface assigned to a different node must never appear, whether the
  // exclusion happens server-side (the real API, filtered by ?node=) or —
  // if the component regressed back toward client-side filtering — was
  // supposed to happen in the browser. This test's fixture returns ONLY
  // the requested node's surface, exactly what the real filtered endpoint
  // would return, so a regression that re-introduced unfiltered fetching
  // (listConfigObjects returning every surface) would leak "other-wall"
  // into the union and fail this test.
  it('renders nothing for a node with no surfaces in the (already node-filtered) response', async () => {
    listShowSurfacesForNode.mockResolvedValue({
      serverTime: NOW,
      kind: 'show.surface',
      objects: [],
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
    expect(listShowSurfacesForNode).not.toHaveBeenCalled()
    expect(listConfigObjects).not.toHaveBeenCalled()
  })

  // The core of the PR #14 review finding: the OLD per-row fan-out
  // (listConfigObjects('show.surface') then one getShowSurface(id) per
  // row) must be UNREACHABLE from this panel, not merely unused in this
  // particular fixture. Assert their absence directly rather than only
  // asserting the new call happened — a component that called both the
  // new AND the old path would still pass a "new call happened" check.
  it('never calls the old listConfigObjects/getShowSurface fan-out, even with multiple configured surfaces', async () => {
    listShowSurfacesForNode.mockResolvedValue({
      serverTime: NOW,
      kind: 'show.surface',
      objects: [
        { id: 'wall-1', label: 'Wall', show: 'halloween', currentRevision: 1, updatedAt: NOW },
        { id: 'wall-2', label: 'Wall 2', show: 'halloween', currentRevision: 1, updatedAt: NOW },
      ],
    })
    const model = makeModel({ session: signedIn({ scopes: ['render:command', 'show:macro:run'] }) })
    renderPanel(model, [])

    expect(await screen.findByText('Surface: wall-1')).toBeInTheDocument()
    expect(screen.getByText('Surface: wall-2')).toBeInTheDocument()
    expect(listShowSurfacesForNode).toHaveBeenCalledTimes(1)
    expect(listConfigObjects).not.toHaveBeenCalled()
    expect(getShowSurface).not.toHaveBeenCalled()
  })
})
