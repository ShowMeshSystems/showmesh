import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { NodesList } from './NodesList'
import { ModelContext } from '../app/ModelContext'
import { makeCapability, makeModel, makeNode } from '../app/test-support/fixtures'
import type { Model } from '../app/types'

afterEach(cleanup)

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
})
