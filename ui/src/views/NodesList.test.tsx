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
})
