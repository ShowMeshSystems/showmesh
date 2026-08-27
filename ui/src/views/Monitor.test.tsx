import { cleanup, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { Monitor } from './Monitor'
import { ModelContext } from '../app/ModelContext'
import { makeEvidence, makeFPPInstance, makeModel, makeNode } from '../app/test-support/fixtures'
import type { Model } from '../app/types'

afterEach(cleanup)

function renderMonitor(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={['/monitor']}>
        <Monitor />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Monitor', () => {
  it('routes to overview, nodes, FPP players, and observations', () => {
    renderMonitor(makeModel())
    const navigation = screen.getByRole('navigation', { name: 'Monitor sections' })
    expect(within(navigation).getByRole('link', { name: /Overview/ })).toHaveAttribute('href', '/monitor')
    expect(within(navigation).getByRole('link', { name: /Nodes/ })).toHaveAttribute('href', '/nodes')
    expect(within(navigation).getByRole('link', { name: /FPP players/ })).toHaveAttribute('href', '/fpp')
    expect(within(navigation).getByRole('link', { name: /Observations/ })).toHaveAttribute('href', '/observations')
  })

  it('keeps the overview shallow while stating the current inventory and evidence coverage', () => {
    renderMonitor(makeModel({
      nodes: [makeNode('node-a', { controlPlane: { state: 'offline', reason: 'lost' } })],
      fpp: [makeFPPInstance('fpp-a', { health: 'degraded', observations: [makeEvidence({ signal: 'fpp.status' })] })],
    }))
    const summary = screen.getByRole('heading', { name: 'At a glance' }).closest('section')
    expect(within(summary!).getByText('Nodes').parentElement).toHaveTextContent('1')
    expect(within(summary!).getByText('FPP players').parentElement).toHaveTextContent('1')
    expect(within(summary!).getByText('Latest evidence').parentElement).toHaveTextContent('4')
    expect(screen.getByText(/Counts stay shallow here/)).toBeInTheDocument()
  })

  it('keeps a disconnected model visibly qualified', () => {
    renderMonitor(makeModel({
      connection: { kind: 'reconnecting', attempt: 1, nextAttemptAt: 0, lastError: 'network error' },
    }))
    expect(screen.getByText('coordinator not live')).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('Showing last known data')
  })
})
