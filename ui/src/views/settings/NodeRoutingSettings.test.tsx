import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import { NodeRoutingSettings } from './NodeRoutingSettings'
import { ModelContext } from '../../app/ModelContext'
import { makeCapability, makeModel, makeNode } from '../../app/test-support/fixtures'
import type { Model } from '../../app/types'

afterEach(cleanup)

function renderPage(model: Model) {
  return render(
    <MemoryRouter>
      <ModelContext.Provider value={model}>
        <NodeRoutingSettings />
      </ModelContext.Provider>
    </MemoryRouter>,
  )
}

describe('NodeRoutingSettings', () => {
  it('renders the Settings tab strip with Node routing current', () => {
    renderPage(makeModel({ nodes: [] }))

    const tab = screen.getByRole('link', { name: 'Node routing' })
    expect(tab).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('link', { name: 'Connections' })).not.toHaveAttribute('aria-current')
  })

  it('offers only nodes advertising audio.output.local or audio.output.ltc as selectable', () => {
    const eligible = makeNode('audio-node-01', { capabilities: [makeCapability('audio.output.local')] })
    const ineligible = makeNode('media-front', { capabilities: [makeCapability('matrix.render')] })
    renderPage(makeModel({ nodes: [eligible, ineligible] }))

    const select = screen.getByRole('combobox', { name: 'Audio node' })
    const eligibleOption = screen.getByRole('option', { name: /audio-node-01/ })
    const ineligibleOption = screen.getByRole('option', { name: /media-front.*no audio capability/ })
    expect(eligibleOption).not.toBeDisabled()
    expect(ineligibleOption).toBeDisabled()
    expect(select).toHaveValue('audio-node-01')
  })

  it('renders an EmptyBlock, never an empty select, when no node is eligible', () => {
    renderPage(makeModel({ nodes: [makeNode('media-front', { capabilities: [] })] }))

    expect(
      screen.getByRole('heading', { name: 'No eligible audio node observed' }),
    ).toBeVisible()
    expect(screen.getByRole('link', { name: 'Create an audio node manually' })).toBeVisible()
  })

  it('keeps the AudioNodes list and create controls reachable', () => {
    const eligible = makeNode('audio-node-01', { capabilities: [makeCapability('audio.output.local')] })
    renderPage(makeModel({ nodes: [eligible] }))

    expect(screen.getByRole('link', { name: 'all configured audio nodes' })).toHaveAttribute(
      'href',
      '/config/audio.node',
    )
    expect(screen.getByRole('link', { name: 'a new audio node' })).toHaveAttribute(
      'href',
      '/config/audio.node/new',
    )
  })
})
