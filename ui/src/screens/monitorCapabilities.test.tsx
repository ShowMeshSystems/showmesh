import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import type { Model, Node } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'
import { MonitorCapabilities } from './MonitorCapabilities'

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
    clock: [],
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

  it('summarizes a capability’s attributes in plain words on its own line', () => {
    renderScreen({
      nodes: [
        node('media-front', [{ id: 'transport.ndi.send', version: 2, attributes: { maxLayers: 8, formats: ['ndi', 'ntp'] } }]),
      ],
    })
    expect(screen.getByText('transport.ndi.send · v2 · maxLayers: 8, formats: ndi, ntp')).toBeInTheDocument()
  })

  it('shows an audio output by its short name, with the full device string as its title', () => {
    const route = 'alsa_output.usb-MOTU_M4-00.pro-output-0'
    renderScreen({
      nodes: [
        node('audio-front', [
          { id: 'audio.output.local', version: 1, attributes: { outputs: [{ channels: 4, route, shortName: 'M4' }, { route: 'hw:0' }] } },
        ]),
      ],
    })
    expect(screen.getByText('M4')).toHaveAttribute('title', route)
    expect(screen.getByText('hw:0')).toHaveAttribute('title', 'hw:0')
    const line = screen.getByText(/^audio\.output\.local · v1/)
    expect(line).toHaveTextContent('audio.output.local · v1 · outputs: route: M4, channels: 4, route: hw:0')
    expect(line).not.toHaveTextContent(route)
  })

  it('reads a node with nothing advertised as nothing to observe, not a failure', () => {
    renderScreen({ nodes: [node('media-garage', [])] })
    expect(screen.getByText('Never advertised')).toBeInTheDocument()
    expect(screen.getByText('Nothing to observe')).toBeInTheDocument()
  })

  it('renders each node as its own group, headed by node id and label', () => {
    const front = node('media-front', [{ id: 'transport.ndi.send', version: 1 }])
    front.label = 'Media Front'
    const garage = node('media-garage', [])
    renderScreen({ nodes: [front, garage] })
    expect(screen.getByRole('heading', { level: 3, name: 'Media Front · media-front' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 3, name: 'media-garage' })).toBeInTheDocument()
  })

  it('shows the absence per node, alongside a sibling node that has advertised capabilities', () => {
    const front = node('media-front', [{ id: 'transport.ndi.send', version: 1 }])
    const garage = node('media-garage', [])
    renderScreen({ nodes: [front, garage] })
    expect(screen.getByText('transport.ndi.send · v1')).toBeInTheDocument()
    expect(screen.getByText('Never advertised')).toBeInTheDocument()
  })

  it('says no node is declared when the fleet is empty, distinct from still loading', () => {
    renderScreen({ nodes: [], snapshotReceivedAt: Date.now() })
    expect(screen.getByText('No node is declared.')).toBeInTheDocument()
  })
})
