import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Model, Node } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

// D-002: this page also reads GET / (getServiceDescriptor) for the
// coordinator build string. Stubbed here so these tests never make a real
// network call; a build state of "loading" forever renders nothing extra,
// which is what every assertion below already expects.
vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, getServiceDescriptor: () => new Promise(() => {}) }
})

const { MonitorCapabilities } = await import('./MonitorCapabilities')

function node(nodeId: string, capabilities: Node['capabilities'] = []): Node {
  return {
    nodeId,
    label: nodeId,
    agentVersion: '0.9.4',
    capabilities,
    controlPlane: { state: 'online', reason: null },
    evidence: {},
    declaration: {},
    render: [],
    audio: [],
    fppConnect: [],
  } as unknown as Node
}

function renderScreen(model: Partial<Model>) {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model, serverTime: '2026-08-28T21:07:00Z', serverTimeReceivedAt: Date.now() }}>
      <MemoryRouter>
        <MonitorCapabilities />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Monitor · Capabilities', () => {
  afterEach(cleanup)

  it('names the facet heading', () => {
    renderScreen({})
    expect(screen.getByRole('heading', { level: 2, name: 'Capabilities' })).toBeInTheDocument()
  })

  it('lists what a node actually advertised, by id and version', () => {
    renderScreen({ nodes: [node('media-front', [{ id: 'transport.ndi.send', version: 1 }])] })
    expect(screen.getByText('transport.ndi.send · v1')).toBeInTheDocument()
  })

  it('reads a node with nothing advertised as nothing to observe, not a failure', () => {
    renderScreen({ nodes: [node('media-garage', [])] })
    expect(screen.getByText('Never advertised')).toBeInTheDocument()
    expect(screen.getByText('Nothing to observe')).toBeInTheDocument()
  })

  it('says no node is declared when the fleet is empty, distinct from still loading', () => {
    renderScreen({ nodes: [], snapshotReceivedAt: Date.now() })
    expect(screen.getByText('No node is declared.')).toBeInTheDocument()
  })
})
