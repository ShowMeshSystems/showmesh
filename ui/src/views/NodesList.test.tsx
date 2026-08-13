import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { NodesList } from './NodesList'
import { ModelContext } from '../app/ModelContext'
import { makeCapability, makeModel, makeNode, makeNodeDeclaration } from '../app/test-support/fixtures'
import type { Model, SessionResponse } from '../app/types'

// BUILD-PLAN Step 7 seam B (RES-008 D2/D6). Mocked the same way
// SessionPanel.test.tsx mocks ../api: this isolates NodesList's OWN
// branching (which button calls which action, how a response updates
// local state) from store.test.ts/client.test.ts's job of proving the
// real network behavior.
const { runDiscovery, declareNode, deleteNodeDeclaration } = vi.hoisted(() => ({
  runDiscovery: vi.fn(),
  declareNode: vi.fn(),
  deleteNodeDeclaration: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, runDiscovery, declareNode, deleteNodeDeclaration }
})

afterEach(() => {
  cleanup()
  runDiscovery.mockReset()
  declareNode.mockReset()
  deleteNodeDeclaration.mockReset()
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

function renderNodesList(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter>
        <NodesList />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

function wrapNodesList(model: Model) {
  return (
    <ModelContext.Provider value={model}>
      <MemoryRouter>
        <NodesList />
      </MemoryRouter>
    </ModelContext.Provider>
  )
}

describe('NodesList', () => {
  it('lists each node with its control-plane state and capability count', () => {
    const nodes = [
      makeNode('node-a', { label: 'Front Yard', controlPlane: { state: 'online', reason: null } }),
      makeNode('node-b', {
        label: null,
        controlPlane: { state: 'offline', reason: 'lost' },
        capabilities: [makeCapability('render.matrix')],
      }),
    ]
    renderNodesList(makeModel({ nodes }))

    expect(screen.getByText('Front Yard')).toBeInTheDocument()
    expect(screen.getByText('node-b')).toBeInTheDocument()
    expect(screen.getByText('control-plane connected')).toBeInTheDocument()
    expect(screen.getByText('control-plane connection lost')).toBeInTheDocument()
    expect(screen.getByText(/1 capability advertised/)).toBeInTheDocument()
  })

  it('states plainly when no node has advertised itself yet', () => {
    renderNodesList(makeModel({ nodes: [] }))
    expect(screen.getByText('No nodes have advertised themselves yet.')).toBeInTheDocument()
  })

  it('renders a declaration badge for every node, distinguishing declared from merely observed', () => {
    const nodes = [
      makeNode('node-a', { declaration: makeNodeDeclaration({ declared: true, discoveryState: 'present' }) }),
      makeNode('node-b', { declaration: makeNodeDeclaration() }),
    ]
    renderNodesList(makeModel({ nodes }))
    expect(screen.getByText(/seen by discovery/)).toBeInTheDocument()
    expect(screen.getByText('not declared')).toBeInTheDocument()
  })

  it('the "Run discovery" control is disabled when the scope list is stale (ADR-024 decision 12), verified against a real render', () => {
    // The control must render unknown/disabled rather than enabled when
    // the scope list cannot currently be vouched for — the exact defect
    // class BUILD-PLAN Step 7 seam B names Step 4 was burned by.
    renderNodesList(makeModel({ session: signedInWithConfigWrite(), sessionFetchFailed: true }))
    expect(screen.getByRole('button', { name: 'Run discovery' })).toBeDisabled()
  })

  // CLAUDE.md DEFECT 3: the test above pins ONE of the four ways
  // evaluateScope (app/session.ts) can decide "not currently vouched
  // for" (a failed session refresh). The other three — never signed in
  // at all, signed in but lacking config:write, and a scope list the
  // coordinator itself reports as unreliable (scopesState: 'unknown',
  // no fetch failure at all) — were previously uncovered by THIS file,
  // even though ScopedButton itself already renders all four correctly;
  // this view's own test file making only one of the four claims does
  // not prove the other three, and each is a distinct way a regression
  // in evaluateScope's own branching could slip past a suite that only
  // ever exercises one branch of it from here.
  it('the "Run discovery" control is disabled when signed out', () => {
    renderNodesList(makeModel({ session: null }))
    expect(screen.getByRole('button', { name: 'Run discovery' })).toBeDisabled()
  })

  it('the "Run discovery" control is disabled when signed in without config:write', () => {
    renderNodesList(
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
    renderNodesList(
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

  // BUILD-PLAN Step 7 seam B review defect 7: a double-click used to start
  // two overlapping discovery runs (the label changed to "Running
  // discovery…" but the control stayed a plain enabled button underneath).
  // Before this test's fix, this assertion failed: runDiscovery was called
  // twice, because userEvent.click only refuses to fire on a genuinely
  // `disabled` element, and the button was never actually disabled.
  it('disables "Run discovery" while a run is in flight, so a double-click cannot start two overlapping runs', async () => {
    const user = userEvent.setup()
    let resolveRun!: (value: unknown) => void
    runDiscovery.mockReturnValue(
      new Promise((resolve) => {
        resolveRun = resolve
      }),
    )
    renderNodesList(makeModel({ session: signedInWithConfigWrite() }))

    await user.click(screen.getByRole('button', { name: 'Run discovery' }))

    const busyButton = screen.getByRole('button', { name: 'Running discovery…' })
    expect(busyButton).toBeDisabled()

    // userEvent.click on a real `disabled` button fires no click event,
    // mirroring an actual browser — so a second click here must not
    // invoke handleRunDiscovery a second time.
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
    renderNodesList(makeModel({ session: signedInWithConfigWrite() }))

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
    renderNodesList(makeModel({ session: signedInWithConfigWrite() }))

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
    renderNodesList(makeModel({ session: signedInWithConfigWrite() }))

    await user.click(screen.getByRole('button', { name: 'Run discovery' }))
    await waitFor(() => expect(screen.getByText('shed-01')).toBeInTheDocument())

    await user.click(screen.getByRole('button', { name: 'Declare' }))
    expect(declareNode).toHaveBeenCalledWith('shed-01', '', '')
    await waitFor(() => expect(screen.queryByText('shed-01')).not.toBeInTheDocument())
  })

  it('removing a declaration requires the browser confirm dialog, in addition to the server\'s own required confirmation', async () => {
    const user = userEvent.setup()
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(false)
    const nodes = [makeNode('node-a', { declaration: makeNodeDeclaration({ declared: true, discoveryState: 'present' }) })]
    renderNodesList(makeModel({ nodes, session: signedInWithConfigWrite() }))

    await user.click(screen.getByRole('button', { name: /Remove declaration/ }))
    expect(confirmSpy).toHaveBeenCalledOnce()
    // Declined confirmation: the API must never be called.
    expect(deleteNodeDeclaration).not.toHaveBeenCalled()

    confirmSpy.mockReturnValue(true)
    await user.click(screen.getByRole('button', { name: /Remove declaration/ }))
    expect(deleteNodeDeclaration).toHaveBeenCalledWith('node-a')

    confirmSpy.mockRestore()
  })

  it('a declared node with no discovery evidence renders the "Remove declaration" control, an undeclared node does not', () => {
    const nodes = [
      makeNode('declared-node', { declaration: makeNodeDeclaration({ declared: true, discoveryState: 'present' }) }),
      makeNode('undeclared-node', { declaration: makeNodeDeclaration() }),
    ]
    renderNodesList(makeModel({ nodes, session: signedInWithConfigWrite() }))
    expect(screen.getAllByRole('button', { name: /Remove declaration/ })).toHaveLength(1)
  })

  // CLAUDE.md DEFECT 3, audit of the "Declare" control: same gap as "Run
  // discovery" above, but for the button that only exists once a
  // proposal is present. Populates the proposal list once under a
  // permitted session (the only way to reach it at all, since the
  // component holds `proposals` as local state, not something a test can
  // inject directly), then re-renders the SAME component instance — via
  // `rerender`, not a fresh `render` — under each of the four session
  // states, so `proposals`/`lastRun` survive the transition exactly the
  // way a live scope change would for a real operator (Configuration.test.tsx's
  // "updates ... without remounting" test uses the identical technique
  // for the same reason).
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
    const { rerender } = render(wrapNodesList(adminModel))

    await user.click(screen.getByRole('button', { name: 'Run discovery' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Declare' })).toBeEnabled())

    rerender(wrapNodesList(makeModel({ session: null })))
    expect(screen.getByRole('button', { name: 'Declare' })).toBeDisabled()

    rerender(
      wrapNodesList(
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

    rerender(wrapNodesList(makeModel({ session: signedInWithConfigWrite(), sessionFetchFailed: true })))
    expect(screen.getByRole('button', { name: 'Declare' })).toBeDisabled()

    rerender(
      wrapNodesList(
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

    // And back to permitted: proves the disabling above tracked the
    // session, not something that broke the control permanently.
    rerender(wrapNodesList(adminModel))
    expect(screen.getByRole('button', { name: 'Declare' })).toBeEnabled()
  })

  // CLAUDE.md DEFECT 3, audit of "Remove declaration": same four states,
  // this control does not need a proposal first, so no discovery run is
  // needed to reach it.
  it('the "Remove declaration" control is disabled in every non-permitted or stale-scope state', () => {
    const nodes = [makeNode('node-a', { declaration: makeNodeDeclaration({ declared: true, discoveryState: 'present' }) })]

    const { rerender } = render(wrapNodesList(makeModel({ nodes, session: signedInWithConfigWrite() })))
    expect(screen.getByRole('button', { name: /Remove declaration/ })).toBeEnabled()

    rerender(wrapNodesList(makeModel({ nodes, session: null })))
    expect(screen.getByRole('button', { name: /Remove declaration/ })).toBeDisabled()

    rerender(
      wrapNodesList(
        makeModel({
          nodes,
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
    expect(screen.getByRole('button', { name: /Remove declaration/ })).toBeDisabled()

    rerender(wrapNodesList(makeModel({ nodes, session: signedInWithConfigWrite(), sessionFetchFailed: true })))
    expect(screen.getByRole('button', { name: /Remove declaration/ })).toBeDisabled()

    rerender(
      wrapNodesList(
        makeModel({
          nodes,
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
    expect(screen.getByRole('button', { name: /Remove declaration/ })).toBeDisabled()
  })
})
