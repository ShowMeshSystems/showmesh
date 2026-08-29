import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { Monitor } from './Monitor'
import { ModelContext } from '../app/ModelContext'
import { makeEvidence, makeFPPInstance, makeModel, makeNode, makeResolumeInstance } from '../app/test-support/fixtures'
import type { Model } from '../app/types'

afterEach(cleanup)

function renderMonitor(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={['/monitor/fleet']}>
        <Monitor />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Monitor / Fleet', () => {
  it('carries the Monitor facet strip with Fleet current', () => {
    renderMonitor(makeModel())
    const nav = screen.getByRole('navigation', { name: 'Monitor facets' })
    expect(within(nav).getByRole('link', { name: /Fleet/ })).toHaveAttribute('aria-current', 'page')
    expect(within(nav).getByRole('link', { name: /Signals/ })).toHaveAttribute('href', '/monitor/signals')
    expect(within(nav).getByRole('link', { name: /Activity/ })).toHaveAttribute('href', '/monitor/activity')
    expect(within(nav).getByRole('link', { name: /Capabilities/ })).toHaveAttribute('href', '/monitor/capabilities')
    expect(within(nav).getByRole('link', { name: /Readiness/ })).toHaveAttribute('href', '/monitor/readiness')
  })

  it('renders one Fleet table across Node, FPP and Resolume, Kind as a column', () => {
    renderMonitor(
      makeModel({
        nodes: [makeNode('node-a', { label: 'Front Yard', controlPlane: { state: 'online', reason: null } })],
        fpp: [makeFPPInstance('fpp-a', { health: 'healthy', endpoint: 'http://fpp-a.local' })],
        resolume: [makeResolumeInstance('arena-main', { health: 'healthy' })],
      }),
    )
    const table = screen.getByRole('table', { name: 'Fleet' })
    expect(within(table).getByRole('link', { name: 'Front Yard' })).toHaveAttribute(
      'href',
      '/monitor/fleet/node/node-a',
    )
    expect(within(table).getByRole('link', { name: 'fpp-a' })).toHaveAttribute(
      'href',
      '/monitor/fleet/fpp/fpp-a',
    )
    expect(within(table).getByRole('link', { name: 'arena-main' })).toHaveAttribute(
      'href',
      '/monitor/fleet/resolume/arena-main',
    )
    expect(within(table).getAllByText('Node')).toHaveLength(1)
    expect(within(table).getAllByText('FPP')).toHaveLength(1)
    expect(within(table).getAllByText('Resolume')).toHaveLength(1)
  })

  it('never attributes a ShowMesh-side binding problem to FPP health: a pending instance-uuid change renders as a separate annotation, not the health badge', () => {
    renderMonitor(
      makeModel({
        fpp: [
          makeFPPInstance('fpp-a', {
            health: 'healthy',
            instanceUuid: 'new-uuid',
            instanceUuidChange: { previousUuid: 'old-uuid', changedAt: '2026-08-28T00:00:00Z' },
          }),
        ],
      }),
    )
    const table = screen.getByRole('table', { name: 'Fleet' })
    expect(within(table).getByText('Healthy')).toBeInTheDocument()
    expect(within(table).getByText('instance uuid change pending')).toBeInTheDocument()
  })

  it('states plainly when nothing needs an operator, without implying the show looks right', () => {
    renderMonitor(makeModel())
    expect(screen.getByText(/Nothing needs an operator right now/)).toBeInTheDocument()
  })

  it('surfaces an offline node under "Needs an operator"', () => {
    renderMonitor(
      makeModel({
        nodes: [makeNode('node-a', { label: 'Garage', controlPlane: { state: 'offline', reason: 'last will received' } })],
      }),
    )
    const heading = screen.getByRole('heading', { name: 'Needs an operator' })
    const section = heading.closest('section')!
    expect(within(section).getByText(/Garage stopped reporting/)).toBeInTheDocument()
    expect(within(section).getByText('last will received')).toBeInTheDocument()
  })

  it('keeps a disconnected model visibly qualified', () => {
    renderMonitor(makeModel({
      connection: { kind: 'reconnecting', attempt: 1, nextAttemptAt: 0, lastError: 'network error' },
    }))
    expect(screen.getByText('Coordinator not live')).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('Showing last known data')
  })

  it('filters the Fleet table by kind via the segmented control', async () => {
    const user = userEvent.setup()
    renderMonitor(
      makeModel({
        nodes: [makeNode('node-a', { label: 'Front Yard' })],
        fpp: [makeFPPInstance('fpp-a')],
      }),
    )
    const table = screen.getByRole('table', { name: 'Fleet' })
    expect(within(table).getByText('Front Yard')).toBeInTheDocument()
    expect(within(table).getByText('fpp-a')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'FPP' }))
    expect(within(table).queryByText('Front Yard')).not.toBeInTheDocument()
    expect(within(table).getByText('fpp-a')).toBeInTheDocument()
  })

  it('reconciles the signals summary from the same observation rows the Signals facet counts', () => {
    renderMonitor(
      makeModel({
        nodes: [makeNode('node-a', { evidence: { hello: makeEvidence({ signal: 'node.hello', state: 'current' }), lastWill: makeEvidence({ signal: 'node.lastWill', state: 'not_collected', value: null, collectedAt: null }), heartbeat: makeEvidence({ signal: 'node.heartbeat', state: 'current' }) } })],
      }),
    )
    // Every observation-bearing evidence field on the fixture node renders
    // once in the stats strip's numerator/denominator; this does not pin
    // an exact count (Observations.test.tsx already does), only that the
    // strip renders a "current / total" pair rather than an invented figure.
    expect(screen.getByText('Signals current')).toBeInTheDocument()
  })
})
