import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  getResolumeComposition: (() => new Promise(() => {})) as () => Promise<unknown>,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, getResolumeComposition: () => stubs.getResolumeComposition() }
})

const { ResolumeControl } = await import('./ResolumeControl')

describe('Resolume Control', () => {
  afterEach(cleanup)

  it('names the layer master column and its per-layer input', async () => {
    stubs.getResolumeComposition = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        revision: 1,
        activatedAt: '2026-08-30T20:00:00Z',
        composition: {},
        decks: [],
        layerGroups: [],
        layers: [{ id: 'l1', name: 'Layer 1' }],
        columns: [],
        clips: [],
        persistentClips: [],
      })
    render(
      <ModelContext.Provider value={initialModel()}>
        <MemoryRouter>
          <ResolumeControl />
        </MemoryRouter>
      </ModelContext.Provider>,
    )
    expect(await screen.findByRole('columnheader', { name: 'Layer master' })).toBeInTheDocument()
    expect(screen.queryByRole('columnheader', { name: 'Master' })).not.toBeInTheDocument()
    expect(screen.getByRole('spinbutton', { name: 'Master for Layer 1' })).toBeInTheDocument()
  })
})
