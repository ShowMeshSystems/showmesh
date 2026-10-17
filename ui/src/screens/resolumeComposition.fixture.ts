import type { ResolumeCompositionResponse } from '../api'

export function compositionFixture(): ResolumeCompositionResponse {
  return {
    serverTime: '2026-10-17T20:00:00Z',
    revision: 1,
    activatedAt: '2026-10-17T19:00:00Z',
    composition: { name: 'Halloween', sourceFilename: 'Halloween.avc', contentHash: 'h', sizeBytes: 1, writtenBy: { product: 'Arena', major: 7, minor: 0, micro: 0, revision: 0 }, canvas: { width: 1920, height: 1080 }, decks: [], layerCount: 2, layerGroupCount: 0, columnCount: 2, clipCount: 4, persistentClipCount: 1 },
    decks: [
      { id: 'd1', name: 'Show', nameGenerated: false, closed: false, clipCount: 3 },
      { id: 'd2', name: 'Resting', nameGenerated: false, closed: true, clipCount: 1 },
    ],
    layerGroups: [],
    layers: [
      { id: 'l2', index: 1, layerGroupIndex: -1, name: 'Overlay', nameGenerated: false },
      { id: 'l1', index: 0, layerGroupIndex: -1, name: 'Base', nameGenerated: false },
    ],
    columns: [
      { id: 'c2', deckId: 'd1', index: 1, name: 'Finale', nameGenerated: false },
      { id: 'c1', deckId: 'd1', index: 0, name: 'Intro', nameGenerated: false },
      { id: 'c3', deckId: 'd2', index: 0, name: 'Idle', nameGenerated: false },
    ],
    clips: [
      { id: 'k1', deckId: 'd1', layerIndex: 0, columnIndex: 0, name: 'Pumpkins', nameGenerated: false, ambiguous: false },
      { id: 'k2', deckId: 'd1', layerIndex: 1, columnIndex: 0, name: 'Pumpkins', nameGenerated: false, ambiguous: false },
      { id: 'k3', deckId: 'd1', layerIndex: 0, columnIndex: 1, name: 'Ghosts', nameGenerated: false, ambiguous: false },
      { id: 'k4', deckId: 'd2', layerIndex: 0, columnIndex: 0, name: 'Fog', nameGenerated: false, ambiguous: false },
    ],
    persistentClips: [{ id: 'p1', layerIndex: 1, columnIndex: 0, name: 'Logo', nameGenerated: false, ambiguous: false }],
  }
}
