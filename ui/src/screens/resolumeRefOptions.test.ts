import { describe, expect, it } from 'vitest'
import { compositionFixture } from './resolumeComposition.fixture'
import { clipOptions, columnOptions, deckOptions, layerOptions, selectedClip } from './resolumeRefOptions'

describe('Resolume reference options', () => {
  it('lists decks, layers in index order, and only the chosen deck’s columns', () => {
    const comp = compositionFixture()
    expect(deckOptions(comp).map((o) => o.label)).toEqual(['Show', 'Resting · closed'])
    expect(layerOptions(comp).map((o) => o.value)).toEqual(['Base', 'Overlay'])
    expect(columnOptions(comp, 'Show').map((o) => o.value)).toEqual(['Intro', 'Finale'])
    expect(columnOptions(comp, '')).toEqual([])
  })

  it('carries a layer only for a clip whose name repeats in the deck', () => {
    const options = clipOptions(compositionFixture(), 'Show', false)
    expect(options.map((o) => [o.clip, o.layer])).toEqual([
      ['Pumpkins', 'Base'],
      ['Ghosts', ''],
      ['Pumpkins', 'Overlay'],
    ])
  })

  it('lists persistent clips without a deck', () => {
    expect(clipOptions(compositionFixture(), '', true).map((o) => o.label)).toEqual(['Logo · Overlay'])
  })

  it('disables a clip no reference can address', () => {
    const comp = compositionFixture()
    comp.clips = comp.clips.map((c) => (c.id === 'k3' ? { ...c, ambiguous: true } : c))
    expect(clipOptions(comp, 'Show', false).find((o) => o.key === 'k3')?.disabled).toBe(true)
  })

  it('finds a stored reference, and refuses to guess between same-named clips', () => {
    const options = clipOptions(compositionFixture(), 'Show', false)
    expect(selectedClip(options, 'Ghosts', '')?.key).toBe('k3')
    expect(selectedClip(options, 'Pumpkins', 'Overlay')?.key).toBe('k2')
    expect(selectedClip(options, 'Pumpkins', '')).toBeUndefined()
    expect(selectedClip(options, 'Bats', '')).toBeUndefined()
  })
})
