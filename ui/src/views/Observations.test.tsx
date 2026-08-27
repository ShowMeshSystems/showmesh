import { cleanup, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { Observations } from './Observations'
import { ModelContext } from '../app/ModelContext'
import { makeEvidence, makeModel, makeNode } from '../app/test-support/fixtures'
import type { Model } from '../app/types'

afterEach(cleanup)

function renderObservations(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter>
        <Observations />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Observations', () => {
  it('renders each report with its resource, source, state, and freshness', () => {
    renderObservations(makeModel({
      nodes: [makeNode('node-a', {
        evidence: {
          hello: makeEvidence({ signal: 'node.hello', state: 'current' }),
          lastWill: makeEvidence({ signal: 'node.lastWill', value: null, state: 'not_collected', reason: 'not attempted', collectedAt: null }),
          heartbeat: makeEvidence({ signal: 'node.heartbeat', state: 'stale', reason: 'older than validity window' }),
        },
      })],
    }))
    const table = screen.getByRole('table', { name: 'Latest observations' })
    expect(within(table).getAllByText('node-a')).toHaveLength(3)
    expect(within(table).getByText('node.heartbeat')).toBeInTheDocument()
    expect(within(table).getByText('stale')).toBeInTheDocument()
    expect(within(table).getByText('not collected')).toBeInTheDocument()
    expect(within(table).getAllByText(/observed/).length).toBeGreaterThan(0)
  })

  it('states when no report exists', () => {
    renderObservations(makeModel())
    expect(screen.getByRole('status')).toHaveTextContent('No observations have been recorded yet.')
  })
})
