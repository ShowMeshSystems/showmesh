import { cleanup, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { NodeDetail } from './NodeDetail'
import { ModelContext } from '../app/ModelContext'
import { makeCapability, makeModel, makeNode, makeNodeDeclaration } from '../app/test-support/fixtures'
import type { Model } from '../app/types'

afterEach(cleanup)

function renderNodeDetail(nodeId: string, model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={[`/monitor/fleet/node/${nodeId}`]}>
        <Routes>
          <Route path="/monitor/fleet/node/:nodeId" element={<NodeDetail />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('NodeDetail', () => {
  // A deliberately-thrown panel below logs through both React's own
  // console.error and PanelErrorBoundary's own componentDidCatch;
  // expected noise for this test, not something to assert on (see
  // PanelErrorBoundary.test.tsx for the same pattern).
  beforeEach(() => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  // Acceptance criterion 4, at the view level. CapabilityPanel.test.tsx
  // already proves the generic renderer works in isolation, and
  // PanelErrorBoundary.test.tsx proves the boundary works in isolation,
  // but before this test nothing mounted the real NodeDetail wiring
  // (NodeDetail.tsx: each capability wrapped in its own
  // <PanelErrorBoundary>) that is what actually makes the acceptance
  // criterion true end to end.
  it('renders a mix of known-shaped and unrecognized capabilities as generic panels, and survives one that throws', () => {
    const circular: Record<string, unknown> = {}
    circular.self = circular // forces CapabilityPanel's JSON.stringify to throw

    const node = makeNode('node-a', {
      label: 'Node A',
      capabilities: [
        makeCapability('node.heartbeat', { attributes: { intervalSeconds: 10 } }),
        makeCapability('experimental.thermal-imaging.v9', {
          attributes: { resolutionPx: 640, vendor: 'unknown-future-vendor' },
        }),
        makeCapability('broken.capability', { attributes: { ref: circular } }),
      ],
    })
    const model = makeModel({ nodes: [node] })

    renderNodeDetail('node-a', model)

    // The node summary panel renders regardless of what its capabilities do.
    expect(screen.getByText('Node A')).toBeInTheDocument()

    // A capability shape this build knows nothing special about renders
    // through the one generic panel, with its raw fields intact.
    expect(screen.getByText('node.heartbeat')).toBeInTheDocument()
    expect(screen.getByText('10')).toBeInTheDocument()

    // An identifier this build has never seen renders exactly the same
    // way -- not dropped, not blanked.
    expect(screen.getByText('experimental.thermal-imaging.v9')).toBeInTheDocument()
    expect(screen.getByText('resolutionPx')).toBeInTheDocument()
    expect(screen.getByText('unknown-future-vendor')).toBeInTheDocument()

    // The capability that throws is caught by its own boundary...
    expect(screen.getByText('broken.capability failed to render')).toBeInTheDocument()

    // ...and does not take the rest of the page down with it: the other
    // two capability panels are still present alongside it.
    expect(screen.getByText('node.heartbeat')).toBeInTheDocument()
    expect(screen.getByText('experimental.thermal-imaging.v9')).toBeInTheDocument()
  })

  it('states plainly when no node matches the route, rather than rendering blank', () => {
    renderNodeDetail('missing-node', makeModel({ nodes: [makeNode('node-a')] }))
    expect(screen.getByText(/No node with ID "missing-node"/)).toBeInTheDocument()
  })

  it('renders control-plane evidence and the reason for a non-online state', () => {
    const node = makeNode('node-b', {
      controlPlane: { state: 'offline', reason: 'no MQTT LWT or heartbeat in over 60s' },
    })
    renderNodeDetail('node-b', makeModel({ nodes: [node] }))
    expect(screen.getByText('control-plane connection lost')).toBeInTheDocument()
    expect(screen.getByText('no MQTT LWT or heartbeat in over 60s')).toBeInTheDocument()
  })

  // The reported defect, reproduced through the real view wiring rather
  // than EvidenceValue in isolation: with the browser disconnected from
  // the coordinator, the control-plane evidence panel must say so, not
  // read as though its badge were a live verdict. See
  // EvidenceValue.test.tsx for the underlying age-advancement fix; this
  // pins that NodeDetail actually threads `model.connection` through to
  // `connected` on every EvidenceValue it renders.
  it('marks control-plane evidence as not live while the browser is disconnected from the coordinator', () => {
    const node = makeNode('node-c')
    const model = makeModel({
      nodes: [node],
      connection: { kind: 'reconnecting', attempt: 2, nextAttemptAt: 0, lastError: 'network error' },
    })
    renderNodeDetail('node-c', model)
    expect(screen.getAllByText(/as of last contact/i).length).toBeGreaterThan(0)
  })

  it('shows no as-of qualifier on control-plane evidence while live', () => {
    const node = makeNode('node-d')
    const model = makeModel({ nodes: [node], connection: { kind: 'live', connectedAt: 0 } })
    renderNodeDetail('node-d', model)
    expect(screen.queryByText(/as of last contact/i)).not.toBeInTheDocument()
  })

  // Layout fix: control-plane evidence used to be three stacked
  // EvidenceValue blocks; it is now one table, with one row per signal,
  // so the signal name and its value line up in columns.
  it('renders control-plane evidence as a table with one row per signal', () => {
    const node = makeNode('node-e')
    renderNodeDetail('node-e', makeModel({ nodes: [node] }))

    const table = screen.getByRole('table', { name: /control-plane evidence/i })
    expect(within(table).getByRole('columnheader', { name: 'Signal' })).toBeInTheDocument()
    expect(within(table).getByRole('columnheader', { name: 'Value' })).toBeInTheDocument()
    expect(within(table).getByRole('rowheader', { name: 'hello (advertisement)' })).toBeInTheDocument()
    expect(within(table).getByRole('rowheader', { name: 'last will' })).toBeInTheDocument()
    expect(within(table).getByRole('rowheader', { name: 'heartbeat' })).toBeInTheDocument()
  })

  it('renders the "Remove this node" danger zone only for a declared node, naming the general orphaning consequence', () => {
    const declared = makeNode('node-declared', { declaration: makeNodeDeclaration({ declared: true, discoveryState: 'present' }) })
    renderNodeDetail('node-declared', makeModel({ nodes: [declared] }))
    expect(screen.getByRole('heading', { name: 'Remove this node' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Remove declaration/ })).toBeInTheDocument()
  })

  it('renders no danger zone for an undeclared node', () => {
    const undeclared = makeNode('node-undeclared', { declaration: makeNodeDeclaration() })
    renderNodeDetail('node-undeclared', makeModel({ nodes: [undeclared] }))
    expect(screen.queryByRole('heading', { name: 'Remove this node' })).not.toBeInTheDocument()
  })

  it('carries the fleet-wide "Run discovery" control forward as a per-node header action', () => {
    renderNodeDetail('node-a', makeModel({ nodes: [makeNode('node-a')] }))
    expect(screen.getByRole('button', { name: 'Run discovery' })).toBeInTheDocument()
  })
})
