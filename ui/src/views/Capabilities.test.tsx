import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { Capabilities } from './Capabilities'
import { ModelContext } from '../app/ModelContext'
import { makeCapability, makeModel, makeNode } from '../app/test-support/fixtures'
import type { Model } from '../app/types'

afterEach(cleanup)

function renderCapabilities(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter>
        <Capabilities />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Capabilities', () => {
  // spec section 6.4: grouped by advertised capability identifier, never
  // by a fixed node class -- an identifier this UI has never seen groups
  // exactly like a familiar one, with no special-casing.
  it('groups nodes by capability identifier, including one this build has never seen before', () => {
    const nodes = [
      makeNode('node-a', {
        label: 'Node A',
        capabilities: [makeCapability('node.heartbeat', { version: 1 })],
      }),
      makeNode('node-b', {
        label: 'Node B',
        capabilities: [
          makeCapability('node.heartbeat', { version: 1 }),
          makeCapability('experimental.thermal-imaging.v9', { version: 3 }),
        ],
      }),
    ]
    renderCapabilities(makeModel({ nodes }))

    expect(screen.getByText('node.heartbeat')).toBeInTheDocument()
    expect(screen.getByText('experimental.thermal-imaging.v9')).toBeInTheDocument()

    const heartbeatGroup = screen.getByText('node.heartbeat').closest('section')
    expect(heartbeatGroup?.textContent).toContain('Node A')
    expect(heartbeatGroup?.textContent).toContain('Node B')

    const thermalGroup = screen.getByText('experimental.thermal-imaging.v9').closest('section')
    expect(thermalGroup?.textContent).not.toContain('Node A')
    expect(thermalGroup?.textContent).toContain('Node B')
  })

  it('states plainly when no node has advertised any capability', () => {
    renderCapabilities(makeModel({ nodes: [makeNode('node-a', { capabilities: [] })] }))
    expect(screen.getByText('No node has advertised any capability yet.')).toBeInTheDocument()
  })
})
