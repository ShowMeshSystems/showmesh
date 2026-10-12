import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { AudioNodeSummary, Model, Node } from '../api'
import { ModelContext } from '../app/ModelContext'
import { audioNodeIdentityText, audioNodeRouteText, channelText, routeName, routeShortName } from './audioRouteModel'
import { AudioNodeRoute } from './audioRouteName'

const M4 = 'alsa_output.usb-MOTU_M4-00.pro-output-0'

function fleetNode(outputs: unknown): Node {
  return { nodeId: 'audio-01', capabilities: [{ id: 'audio.output.local', version: 1, attributes: { outputs } }] } as unknown as Node
}

function summary(overrides: Partial<AudioNodeSummary> = {}): AudioNodeSummary {
  return { id: 'audio-01', label: M4, programChannels: [1, 2], currentRevision: 1, updatedAt: '2026-10-12T00:00:00Z', ...overrides }
}

describe('audio route short names', () => {
  it('reads the short name the node advertises for a route', () => {
    expect(routeShortName(fleetNode([{ route: M4, shortName: 'M4' }]), M4)).toBe('M4')
  })

  it('reports none for an older agent, another route, or an unreadable list', () => {
    expect(routeShortName(fleetNode([{ route: M4 }]), M4)).toBeUndefined()
    expect(routeShortName(fleetNode([{ route: 'hw:0', shortName: 'PCH' }]), M4)).toBeUndefined()
    expect(routeShortName(fleetNode('not a list'), M4)).toBeUndefined()
    expect(routeShortName(undefined, M4)).toBeUndefined()
  })

  it('writes adjacent channels as a range and others as a list', () => {
    expect(channelText([1, 2])).toBe('ch 1-2')
    expect(channelText([3])).toBe('ch 3')
    expect(channelText([1, 3])).toBe('ch 1, 3')
    expect(channelText([])).toBe('')
  })

  it('shows the short name with the channels, and the full route unchanged without one', () => {
    expect(audioNodeRouteText(summary(), [fleetNode([{ route: M4, shortName: 'M4' }])])).toBe('M4 ch 1-2')
    expect(audioNodeRouteText(summary(), [fleetNode([{ route: M4 }])])).toBe(M4)
    expect(audioNodeRouteText(summary(), [])).toBe(M4)
    expect(routeName(M4, undefined)).toBe(M4)
  })

  it('joins the node id and the route, and collapses to the id when the route adds nothing', () => {
    expect(audioNodeIdentityText(summary(), [fleetNode([{ route: M4, shortName: 'M4' }])])).toBe('audio-01 · M4 ch 1-2')
    expect(audioNodeIdentityText(summary({ label: 'audio-01' }), [])).toBe('audio-01')
  })

  it('renders the short form with the full device string as its title', () => {
    const model = { nodes: [fleetNode([{ route: M4, shortName: 'M4' }])] } as unknown as Model
    render(
      <ModelContext.Provider value={model}>
        <AudioNodeRoute node={summary()} />
      </ModelContext.Provider>,
    )
    expect(screen.getByText('M4 ch 1-2')).toHaveAttribute('title', M4)
  })

  it('renders the full device string outside a model provider', () => {
    render(<AudioNodeRoute node={summary()} />)
    expect(screen.getByText(M4)).toBeInTheDocument()
  })
})
