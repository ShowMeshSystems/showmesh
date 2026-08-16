import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ResolumeView } from './ResolumeView'
import { ModelContext } from '../app/ModelContext'
import {
  makeEvidence,
  makeModel,
  makeResolumeAction,
  makeResolumeCompositionClip,
  makeResolumeCompositionDeck,
  makeResolumeCompositionLayer,
  makeResolumeCompositionResponse,
  makeResolumeInstance,
  makeResolumeRecoveryResponse,
} from '../app/test-support/fixtures'
import type { Model } from '../app/types'

const { getResolumeComposition, getResolumeRecovery, listResolumeActions } = vi.hoisted(() => ({
  getResolumeComposition: vi.fn(),
  getResolumeRecovery: vi.fn(),
  listResolumeActions: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getResolumeComposition, getResolumeRecovery, listResolumeActions }
})

// This view fetches listResolumeActions unconditionally on mount,
// regardless of whether a Resolume instance is configured — every test
// below sets it explicitly for that reason, even the ones that do not
// otherwise care about the controller panel.
afterEach(() => {
  cleanup()
  getResolumeComposition.mockReset()
  getResolumeRecovery.mockReset()
  listResolumeActions.mockReset()
})

function renderView(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter>
        <ResolumeView />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

const emptyRecovery = makeResolumeRecoveryResponse()

describe('ResolumeView', () => {
  // Acceptance criterion 1 (this view's own share of it): "not configured"
  // rather than an error or an empty box when GET /resolume/instances
  // answers with an empty array by design.
  it('renders "not configured" for a coordinator with no Resolume instance', () => {
    listResolumeActions.mockResolvedValue({ serverTime: '2026-08-16T00:00:00Z', actions: [] })
    getResolumeRecovery.mockResolvedValue(emptyRecovery)
    renderView(makeModel({ resolume: [] }))
    expect(screen.getByText(/not configured/i)).toBeInTheDocument()
  })

  // Acceptance criterion 3: no Arena object id appears anywhere in this
  // view's rendered output. Both fixtures below reproduce the genuine gap
  // this view's own sanitizer exists for (resolumeComposition.ts's own
  // header comment): the collector's own formatRef embeds a raw id in
  // TWO real signal values.
  it('never renders an Arena object id, even though the coordinator embeds one in these two signal values', async () => {
    getResolumeComposition.mockResolvedValue(
      makeResolumeCompositionResponse({
        decks: [makeResolumeCompositionDeck({ id: 'deck-secret-id', name: 'Main Deck' })],
        clips: [makeResolumeCompositionClip({ id: 'clip-secret-id', name: 'Snowfall', deckId: 'deck-secret-id' })],
      }),
    )
    getResolumeRecovery.mockResolvedValue(emptyRecovery)
    listResolumeActions.mockResolvedValue({ serverTime: '2026-08-16T00:00:00Z', actions: [] })

    const instance = makeResolumeInstance('resolume-1', {
      composition: { name: 'Christmas 25' },
      observations: [
        makeEvidence({
          signal: 'resolume.composition.selected_deck',
          value: 'Main Deck (id deck-secret-id)',
        }),
        makeEvidence({
          signal: 'resolume.layer.some-layer-id.active_clip',
          value: 'id clip-secret-id',
        }),
      ],
    })
    renderView(makeModel({ resolume: [instance] }))

    await waitFor(() => expect(getResolumeComposition).toHaveBeenCalled())
    // Let the composition-dependent re-render settle. "Snowfall" renders
    // in more than one panel (this view's own inventory AND the moved-in
    // ResolumeCompositionUpload summary, both fed by the same mocked
    // fetch) — findAllByText tolerates that; this test only cares that
    // the id never appears anywhere.
    await screen.findAllByText('Snowfall')

    const text = document.body.textContent ?? ''
    expect(text).not.toContain('deck-secret-id')
    expect(text).not.toContain('clip-secret-id')
    expect(text).not.toContain('some-layer-id')
    // The NAMES resolved in their place are what actually renders.
    expect(text).toContain('Main Deck')
  })

  // Acceptance criterion 4: a generated label renders as generated,
  // visually distinguishable from an authored one.
  it('marks a generated deck label as generated in the composition inventory', async () => {
    getResolumeComposition.mockResolvedValue(
      makeResolumeCompositionResponse({
        decks: [makeResolumeCompositionDeck({ id: 'deck-1', name: 'Deck 1', nameGenerated: true })],
      }),
    )
    getResolumeRecovery.mockResolvedValue(emptyRecovery)
    listResolumeActions.mockResolvedValue({ serverTime: '2026-08-16T00:00:00Z', actions: [] })

    const instance = makeResolumeInstance('resolume-1', { composition: { name: 'Christmas 25' } })
    renderView(makeModel({ resolume: [instance] }))

    // "Deck 1" (with no marker) also renders in the moved-in
    // ResolumeCompositionUpload summary, fed by the same mocked fetch —
    // findAllByText tolerates that ambiguity; the exact "(generated)"
    // string below is unique to this view's own inventory table.
    await screen.findAllByText(/Deck 1/)
    expect(screen.getByText((_, node) => node?.textContent === 'Deck 1 (generated)')).toBeInTheDocument()
  })

  // Acceptance criterion 5: the ambiguous clips are listed in one place
  // with what to do about them.
  it('lists an ambiguous clip with what to do about it', async () => {
    getResolumeComposition.mockResolvedValue(
      makeResolumeCompositionResponse({
        decks: [makeResolumeCompositionDeck({ id: 'deck-1', name: 'Main' })],
        layers: [makeResolumeCompositionLayer({ id: 'layer-1', index: 0, name: 'Layer A' })],
        clips: [
          makeResolumeCompositionClip({
            id: 'clip-1',
            name: 'Snow',
            deckId: 'deck-1',
            layerIndex: 0,
            ambiguous: true,
          }),
        ],
      }),
    )
    getResolumeRecovery.mockResolvedValue(emptyRecovery)
    listResolumeActions.mockResolvedValue({ serverTime: '2026-08-16T00:00:00Z', actions: [] })

    const instance = makeResolumeInstance('resolume-1', { composition: { name: 'Christmas 25' } })
    renderView(makeModel({ resolume: [instance] }))

    await waitFor(() => screen.getAllByText('Snow'))
    expect(screen.getByText(/rename one of each colliding pair/i)).toBeInTheDocument()
  })

  it('states "no ambiguous clips" when none exist', async () => {
    getResolumeComposition.mockResolvedValue(makeResolumeCompositionResponse({ clips: [], persistentClips: [] }))
    getResolumeRecovery.mockResolvedValue(emptyRecovery)
    listResolumeActions.mockResolvedValue({ serverTime: '2026-08-16T00:00:00Z', actions: [] })

    const instance = makeResolumeInstance('resolume-1', { composition: { name: 'Christmas 25' } })
    renderView(makeModel({ resolume: [instance] }))

    await screen.findByText(/No ambiguous clips/i)
  })

  // Acceptance criterion 8: a recovery layer whose state is "unknown"
  // renders its reason, and never as dark or blank.
  it('renders a recovery layer state of "unknown" with its own reason, never blank', async () => {
    getResolumeComposition.mockRejectedValue(Object.assign(new Error('none stored'), { status: 404 }))
    getResolumeRecovery.mockResolvedValue(
      makeResolumeRecoveryResponse({
        record: [
          // clip/deck/establishedAt/source are genuinely ABSENT for a
          // never-established "unknown" entry (never present-as-undefined
          // — schema's own doc comment) — built directly rather than
          // through the fixture's own defaults, which
          // `exactOptionalPropertyTypes` forbids overriding with a
          // literal `undefined`.
          {
            layer: 'Layer 1',
            layerNameGenerated: false,
            state: 'unknown',
            reason: 'never observed since this coordinator started',
          },
        ],
      }),
    )
    listResolumeActions.mockResolvedValue({ serverTime: '2026-08-16T00:00:00Z', actions: [] })

    const instance = makeResolumeInstance('resolume-1')
    renderView(makeModel({ resolume: [instance] }))

    await screen.findByText('unknown')
    expect(screen.getByText('never observed since this coordinator started')).toBeInTheDocument()
  })

  it('renders the controller once actions load, listing every registered action', async () => {
    getResolumeComposition.mockRejectedValue(Object.assign(new Error('none stored'), { status: 404 }))
    getResolumeRecovery.mockResolvedValue(emptyRecovery)
    listResolumeActions.mockResolvedValue({
      serverTime: '2026-08-16T00:00:00Z',
      actions: [makeResolumeAction({ name: 'blackout', params: [] })],
    })

    const instance = makeResolumeInstance('resolume-1')
    renderView(makeModel({ resolume: [instance] }))

    await screen.findByRole('combobox', { name: 'Action' })
  })
})
