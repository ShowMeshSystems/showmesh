import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { NodeDiscoveryPanel } from './NodesList'
import { ModelContext } from '../app/ModelContext'
import { makeModel, makeNodeDeclaration } from '../app/test-support/fixtures'
import type { Model, SessionResponse } from '../app/types'

// BUILD-PLAN Step 7 seam B (RES-008 D2/D6), narrowed by the Monitor
// overhaul: NodesList.tsx no longer owns a route or a per-node table --
// Monitor's Fleet facet (Monitor.tsx / monitor/fleetRows.ts) now renders
// every declared node as a Fleet row, and "Remove declaration" moved to
// NodeDetail.tsx's own danger-zone section (matching Node.dc.html). What
// is left here, and what this file now tests, is `NodeDiscoveryPanel`:
// the fleet-wide "surface an undeclared candidate and let an operator
// declare it" flow that has no per-resource home. The node-table-layout
// test and the "Remove declaration" tests that used to live in this file
// moved to Monitor.test.tsx (fleet listing) and NodeDetail.test.tsx
// (removal), covering the same behaviour where it now lives, rather than
// being dropped.
const { runDiscovery, declareNode } = vi.hoisted(() => ({
  runDiscovery: vi.fn(),
  declareNode: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, runDiscovery, declareNode }
})

afterEach(() => {
  cleanup()
  runDiscovery.mockReset()
  declareNode.mockReset()
})

const NOW = '2026-08-12T00:00:00.000Z'

function signedInWithConfigWrite(): SessionResponse {
  return {
    serverTime: NOW,
    authenticated: true,
    principal: { id: 'p1', name: 'admin-1', kind: 'human', role: 'admin' },
    session: { id: 's1', deviceLabel: 'laptop', createdAt: NOW },
    credentialForm: 'session',
    scopes: ['config:write'],
    scopesState: 'current',
    bootstrapRequired: false,
  }
}

function renderPanel(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter>
        <NodeDiscoveryPanel />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

function wrapPanel(model: Model) {
  return (
    <ModelContext.Provider value={model}>
      <MemoryRouter>
        <NodeDiscoveryPanel />
      </MemoryRouter>
    </ModelContext.Provider>
  )
}

describe('NodeDiscoveryPanel', () => {
  it('the "Run discovery" control is disabled when the scope list is stale (ADR-024 decision 12), verified against a real render', () => {
    renderPanel(makeModel({ session: signedInWithConfigWrite(), sessionFetchFailed: true }))
    expect(screen.getByRole('button', { name: 'Run discovery' })).toBeDisabled()
  })

  it('the "Run discovery" control is disabled when signed out', () => {
    renderPanel(makeModel({ session: null }))
    expect(screen.getByRole('button', { name: 'Run discovery' })).toBeDisabled()
  })

  it('the "Run discovery" control is disabled when signed in without config:write', () => {
    renderPanel(
      makeModel({
        session: {
          serverTime: NOW,
          authenticated: true,
          principal: { id: 'p2', name: 'viewer-1', kind: 'human', role: 'viewer' },
          session: { id: 's2', deviceLabel: 'laptop', createdAt: NOW },
          credentialForm: 'session',
          scopes: ['node:read'],
          scopesState: 'current',
          bootstrapRequired: false,
        },
      }),
    )
    expect(screen.getByRole('button', { name: 'Run discovery' })).toBeDisabled()
  })

  it('the "Run discovery" control is disabled when scopesState is "unknown" (stale), even though the fetch itself succeeded', () => {
    renderPanel(
      makeModel({
        session: {
          serverTime: NOW,
          authenticated: true,
          principal: { id: 'p1', name: 'admin-1', kind: 'human', role: 'admin' },
          session: { id: 's1', deviceLabel: 'laptop', createdAt: NOW },
          credentialForm: 'session',
          scopes: ['config:write'],
          scopesState: 'unknown',
          bootstrapRequired: false,
        },
      }),
    )
    expect(screen.getByRole('button', { name: 'Run discovery' })).toBeDisabled()
  })

  it('disables "Run discovery" while a run is in flight, so a double-click cannot start two overlapping runs', async () => {
    const user = userEvent.setup()
    let resolveRun!: (value: unknown) => void
    runDiscovery.mockReturnValue(
      new Promise((resolve) => {
        resolveRun = resolve
      }),
    )
    renderPanel(makeModel({ session: signedInWithConfigWrite() }))

    await user.click(screen.getByRole('button', { name: 'Run discovery' }))

    const busyButton = screen.getByRole('button', { name: 'Running discovery…' })
    expect(busyButton).toBeDisabled()

    await user.click(busyButton)
    expect(runDiscovery).toHaveBeenCalledOnce()

    resolveRun({
      serverTime: NOW,
      run: {
        id: 'run-1', startedAt: NOW, finishedAt: NOW, complete: true, reason: null, foundCount: 0,
        initiatedByPrincipalId: 'p1', initiatedByPrincipalName: 'admin-1',
      },
      proposals: [],
    })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Run discovery' })).toBeEnabled())
  })

  it('running discovery calls the API and renders returned proposals with a Declare action', async () => {
    const user = userEvent.setup()
    runDiscovery.mockResolvedValue({
      serverTime: NOW,
      run: {
        id: 'run-1', startedAt: NOW, finishedAt: NOW, complete: true, reason: null, foundCount: 1,
        initiatedByPrincipalId: 'p1', initiatedByPrincipalName: 'admin-1',
      },
      proposals: [{ nodeId: 'shed-01', source: 'node' }],
    })
    renderPanel(makeModel({ session: signedInWithConfigWrite() }))

    await user.click(screen.getByRole('button', { name: 'Run discovery' }))
    await waitFor(() => expect(screen.getByText('shed-01')).toBeInTheDocument())
    expect(runDiscovery).toHaveBeenCalledOnce()
    expect(screen.getByRole('button', { name: 'Declare' })).toBeEnabled()
  })

  it('a discovery run with no proposals states the no-active-probing limitation honestly, never implying a full sweep happened', async () => {
    const user = userEvent.setup()
    runDiscovery.mockResolvedValue({
      serverTime: NOW,
      run: {
        id: 'run-1', startedAt: NOW, finishedAt: NOW, complete: true, reason: null, foundCount: 0,
        initiatedByPrincipalId: 'p1', initiatedByPrincipalName: 'admin-1',
      },
      proposals: [],
    })
    renderPanel(makeModel({ session: signedInWithConfigWrite() }))

    await user.click(screen.getByRole('button', { name: 'Run discovery' }))
    await waitFor(() => expect(screen.getByText('No undeclared entities observed.')).toBeInTheDocument())
  })

  it('declaring a proposal calls declareNode with the proposed id and removes it from the local list', async () => {
    const user = userEvent.setup()
    runDiscovery.mockResolvedValue({
      serverTime: NOW,
      run: {
        id: 'run-1', startedAt: NOW, finishedAt: NOW, complete: true, reason: null, foundCount: 1,
        initiatedByPrincipalId: 'p1', initiatedByPrincipalName: 'admin-1',
      },
      proposals: [{ nodeId: 'shed-01', source: 'node' }],
    })
    declareNode.mockResolvedValue({
      serverTime: NOW,
      declaration: makeNodeDeclaration({ declared: true, discoveryState: 'unknown', discoveryReason: 'no discovery run history is available' }),
    })
    renderPanel(makeModel({ session: signedInWithConfigWrite() }))

    await user.click(screen.getByRole('button', { name: 'Run discovery' }))
    await waitFor(() => expect(screen.getByText('shed-01')).toBeInTheDocument())

    await user.click(screen.getByRole('button', { name: 'Declare' }))
    expect(declareNode).toHaveBeenCalledWith('shed-01', '', '')
    await waitFor(() => expect(screen.queryByText('shed-01')).not.toBeInTheDocument())
  })

  it('the "Declare" control (once a proposal exists) is disabled in every non-permitted or stale-scope state, and re-enables when permitted again', async () => {
    const user = userEvent.setup()
    runDiscovery.mockResolvedValue({
      serverTime: NOW,
      run: {
        id: 'run-1', startedAt: NOW, finishedAt: NOW, complete: true, reason: null, foundCount: 1,
        initiatedByPrincipalId: 'p1', initiatedByPrincipalName: 'admin-1',
      },
      proposals: [{ nodeId: 'shed-01', source: 'node' }],
    })
    const adminModel = makeModel({ session: signedInWithConfigWrite() })
    const { rerender } = render(wrapPanel(adminModel))

    await user.click(screen.getByRole('button', { name: 'Run discovery' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Declare' })).toBeEnabled())

    rerender(wrapPanel(makeModel({ session: null })))
    expect(screen.getByRole('button', { name: 'Declare' })).toBeDisabled()

    rerender(
      wrapPanel(
        makeModel({
          session: {
            serverTime: NOW,
            authenticated: true,
            principal: { id: 'p2', name: 'viewer-1', kind: 'human', role: 'viewer' },
            session: { id: 's2', deviceLabel: 'laptop', createdAt: NOW },
            credentialForm: 'session',
            scopes: ['node:read'],
            scopesState: 'current',
            bootstrapRequired: false,
          },
        }),
      ),
    )
    expect(screen.getByRole('button', { name: 'Declare' })).toBeDisabled()

    rerender(wrapPanel(makeModel({ session: signedInWithConfigWrite(), sessionFetchFailed: true })))
    expect(screen.getByRole('button', { name: 'Declare' })).toBeDisabled()

    rerender(
      wrapPanel(
        makeModel({
          session: {
            serverTime: NOW,
            authenticated: true,
            principal: { id: 'p1', name: 'admin-1', kind: 'human', role: 'admin' },
            session: { id: 's1', deviceLabel: 'laptop', createdAt: NOW },
            credentialForm: 'session',
            scopes: ['config:write'],
            scopesState: 'unknown',
            bootstrapRequired: false,
          },
        }),
      ),
    )
    expect(screen.getByRole('button', { name: 'Declare' })).toBeDisabled()

    rerender(wrapPanel(adminModel))
    expect(screen.getByRole('button', { name: 'Declare' })).toBeEnabled()
  })
})
