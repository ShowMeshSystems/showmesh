import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ResolumeActionController } from './ResolumeActionController'
import { ModelContext } from '../app/ModelContext'
import { makeModel, makeResolumeAction, makeResolumeActionResult, makeResolumeCompositionResponse, makeResolumeCompositionClip, makeResolumeCompositionDeck, makeResolumeCompositionLayer } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

const {
  launchResolumeClip,
  clearResolumeLayer,
  launchResolumeColumn,
  selectResolumeDeck,
  blackoutResolume,
  setResolumeLayerBypass,
  setResolumeLayerMaster,
} = vi.hoisted(() => ({
  launchResolumeClip: vi.fn(),
  clearResolumeLayer: vi.fn(),
  launchResolumeColumn: vi.fn(),
  selectResolumeDeck: vi.fn(),
  blackoutResolume: vi.fn(),
  setResolumeLayerBypass: vi.fn(),
  setResolumeLayerMaster: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return {
    ...actual,
    launchResolumeClip,
    clearResolumeLayer,
    launchResolumeColumn,
    selectResolumeDeck,
    blackoutResolume,
    setResolumeLayerBypass,
    setResolumeLayerMaster,
  }
})

afterEach(() => {
  cleanup()
  launchResolumeClip.mockReset()
  clearResolumeLayer.mockReset()
  launchResolumeColumn.mockReset()
  selectResolumeDeck.mockReset()
  blackoutResolume.mockReset()
  setResolumeLayerBypass.mockReset()
  setResolumeLayerMaster.mockReset()
})

const ALL_ACTIONS = [
  makeResolumeAction({ name: 'launchClip' }),
  makeResolumeAction({ name: 'clearLayer' }),
  makeResolumeAction({ name: 'blackout' }),
  makeResolumeAction({ name: 'launchColumn' }),
  makeResolumeAction({ name: 'selectDeck' }),
  makeResolumeAction({ name: 'setLayerBypass' }),
  makeResolumeAction({ name: 'setLayerMaster' }),
]

const adminModel: Model = makeModel({
  session: makeAuthenticatedSession({
    principal: { id: 'p1', name: 'operator', kind: 'human', role: 'admin' },
    scopes: ['resolume:action'],
  }),
})

function renderController(composition = makeResolumeCompositionResponse()) {
  return render(
    <ModelContext.Provider value={adminModel}>
      <ResolumeActionController actions={ALL_ACTIONS} composition={composition} />
    </ModelContext.Provider>,
  )
}

describe('ResolumeActionController', () => {
  it('populates the action dropdown with all seven registered actions', () => {
    renderController()
    const select = screen.getByRole('combobox', { name: 'Action' })
    const options = Array.from(select.querySelectorAll('option')).map((o) => o.textContent)
    for (const name of ['launchClip', 'clearLayer', 'blackout', 'launchColumn', 'selectDeck', 'setLayerBypass', 'setLayerMaster']) {
      expect(options).toContain(name)
    }
  })

  it('dispatches launchClip by name (deck and clip, never an id)', async () => {
    launchResolumeClip.mockResolvedValue(makeResolumeActionResult({ outcome: 'confirmed' }))
    const composition = makeResolumeCompositionResponse({
      decks: [makeResolumeCompositionDeck({ id: 'deck-1', name: 'Deck 1' })],
      clips: [makeResolumeCompositionClip({ id: 'clip-1', name: 'Snow', deckId: 'deck-1' })],
    })
    renderController(composition)

    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Action' }), 'launchClip')
    // Review finding 8: the deck <select>'s own option value is now the
    // deck's id, never its (possibly ambiguous) name — select by the
    // rendered option element, matching what an operator actually clicks.
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Deck' }), screen.getByRole('option', { name: /^Deck 1/ }))
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Clip' }), 'Snow')
    await userEvent.click(screen.getByRole('button', { name: /go/i }))

    await waitFor(() => expect(launchResolumeClip).toHaveBeenCalledWith({ clip: 'Snow', deck: 'Deck 1' }))
    // Never the underlying Resolume object id anywhere in the call.
    expect(launchResolumeClip).not.toHaveBeenCalledWith(expect.objectContaining({ clip: 'clip-1' }))
  })

  it('dispatches blackout with no parameters', async () => {
    blackoutResolume.mockResolvedValue(makeResolumeActionResult({ action: 'blackout', outcome: 'confirmed' }))
    renderController()

    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Action' }), 'blackout')
    await userEvent.click(screen.getByRole('button', { name: /go/i }))

    await waitFor(() => expect(blackoutResolume).toHaveBeenCalledWith())
  })

  it('dispatches clearLayer by layer name', async () => {
    clearResolumeLayer.mockResolvedValue(makeResolumeActionResult({ action: 'clearLayer', outcome: 'confirmed' }))
    const composition = makeResolumeCompositionResponse({
      layers: [makeResolumeCompositionLayer({ id: 'layer-1', index: 0, name: 'Snow layer' })],
    })
    renderController(composition)

    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Action' }), 'clearLayer')
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Layer' }), 'Snow layer')
    await userEvent.click(screen.getByRole('button', { name: /go/i }))

    await waitFor(() => expect(clearResolumeLayer).toHaveBeenCalledWith('Snow layer'))
  })

  it('dispatches selectDeck by deck name', async () => {
    selectResolumeDeck.mockResolvedValue(makeResolumeActionResult({ action: 'selectDeck', outcome: 'confirmed' }))
    const composition = makeResolumeCompositionResponse({
      decks: [makeResolumeCompositionDeck({ id: 'deck-2', name: 'Deck 2' })],
    })
    renderController(composition)

    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Action' }), 'selectDeck')
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Deck' }), screen.getByRole('option', { name: /^Deck 2/ }))
    await userEvent.click(screen.getByRole('button', { name: /go/i }))

    await waitFor(() => expect(selectResolumeDeck).toHaveBeenCalledWith('Deck 2'))
  })

  it('dispatches launchColumn by column and deck name', async () => {
    launchResolumeColumn.mockResolvedValue(makeResolumeActionResult({ action: 'launchColumn', outcome: 'confirmed' }))
    const composition = makeResolumeCompositionResponse({
      decks: [makeResolumeCompositionDeck({ id: 'deck-1', name: 'Deck 1' })],
      columns: [{ id: 'col-1', deckId: 'deck-1', index: 0, name: 'Column 1', nameGenerated: true }],
    })
    renderController(composition)

    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Action' }), 'launchColumn')
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Deck' }), screen.getByRole('option', { name: /^Deck 1/ }))
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Column' }), 'Column 1')
    await userEvent.click(screen.getByRole('button', { name: /go/i }))

    await waitFor(() => expect(launchResolumeColumn).toHaveBeenCalledWith('Column 1', 'Deck 1'))
  })

  it('dispatches setLayerBypass with layer name and the bypassed checkbox value', async () => {
    setResolumeLayerBypass.mockResolvedValue(makeResolumeActionResult({ action: 'setLayerBypass', outcome: 'confirmed' }))
    const composition = makeResolumeCompositionResponse({
      layers: [makeResolumeCompositionLayer({ id: 'layer-1', index: 0, name: 'Layer 1' })],
    })
    renderController(composition)

    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Action' }), 'setLayerBypass')
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Layer' }), 'Layer 1')
    await userEvent.click(screen.getByRole('checkbox', { name: /bypassed/i }))
    await userEvent.click(screen.getByRole('button', { name: /go/i }))

    await waitFor(() => expect(setResolumeLayerBypass).toHaveBeenCalledWith('Layer 1', true))
  })

  it('dispatches setLayerMaster with layer name and a numeric value', async () => {
    setResolumeLayerMaster.mockResolvedValue(makeResolumeActionResult({ action: 'setLayerMaster', outcome: 'confirmed' }))
    const composition = makeResolumeCompositionResponse({
      layers: [makeResolumeCompositionLayer({ id: 'layer-1', index: 0, name: 'Layer 1' })],
    })
    renderController(composition)

    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Action' }), 'setLayerMaster')
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Layer' }), 'Layer 1')
    await userEvent.type(screen.getByRole('spinbutton', { name: /master value/i }), '0.5')
    await userEvent.click(screen.getByRole('button', { name: /go/i }))

    await waitFor(() => expect(setResolumeLayerMaster).toHaveBeenCalledWith('Layer 1', 0.5))
  })

  // Acceptance criterion 7: two clips sharing a name are disambiguated in
  // the picker BEFORE selection, not only after a refusal comes back.
  it('disambiguates two clips sharing a name, in the picker, before either is selected', () => {
    const composition = makeResolumeCompositionResponse({
      layers: [
        makeResolumeCompositionLayer({ id: 'layer-1', index: 0, name: 'Layer A' }),
        makeResolumeCompositionLayer({ id: 'layer-2', index: 1, name: 'Layer B' }),
      ],
      decks: [makeResolumeCompositionDeck({ id: 'deck-1', name: 'Deck 1' })],
      clips: [
        makeResolumeCompositionClip({ id: 'clip-1', name: 'Snow', deckId: 'deck-1', layerIndex: 0 }),
        makeResolumeCompositionClip({ id: 'clip-2', name: 'Snow', deckId: 'deck-1', layerIndex: 1 }),
      ],
    })
    render(
      <ModelContext.Provider value={adminModel}>
        <ResolumeActionController actions={ALL_ACTIONS} composition={composition} />
      </ModelContext.Provider>,
    )
    // The clip picker is scoped to a chosen deck — both clips live on
    // "Deck 1" here, so the deck must be picked before the clip list has
    // anything in it at all, exactly as an operator would experience it.
    return userEvent
      .selectOptions(screen.getByRole('combobox', { name: 'Action' }), 'launchClip')
      .then(() =>
        userEvent.selectOptions(
          screen.getByRole('combobox', { name: 'Deck' }),
          screen.getByRole('option', { name: /^Deck 1/ }),
        ),
      )
      .then(() => {
        const clipSelect = screen.getByRole('combobox', { name: 'Clip' })
        const optionTexts = Array.from(clipSelect.querySelectorAll('option'))
          .map((o) => o.textContent ?? '')
          .filter((t) => t !== 'Choose one')
        expect(optionTexts).toHaveLength(2)
        expect(new Set(optionTexts).size).toBe(2) // the two option LABELS are distinguishable
      })
  })

  // Review finding B4: the clip picker used to key its <option value> on
  // the (duplicate) NAME, so selecting the SECOND "Snow" collapsed to the
  // first one at onChange time — the dispatched `layer` (and thus which
  // clip actually launches) always named the FIRST duplicate's layer
  // regardless of which option the operator picked. Selecting the SECOND
  // option here and asserting the dispatched `layer` is "Layer B" (the
  // second clip's own layer, never "Layer A") is the regression guard:
  // before the fix (Picker's Clip usage without optionValue="key", and
  // `clips.find(c => c.value === clip)`), this assertion goes red because
  // the dispatch names "Layer A" no matter which duplicate was selected.
  it('dispatches the SECOND duplicate clip actually selected, not the first', async () => {
    const composition = makeResolumeCompositionResponse({
      layers: [
        makeResolumeCompositionLayer({ id: 'layer-1', index: 0, name: 'Layer A' }),
        makeResolumeCompositionLayer({ id: 'layer-2', index: 1, name: 'Layer B' }),
      ],
      decks: [makeResolumeCompositionDeck({ id: 'deck-1', name: 'Deck 1' })],
      clips: [
        makeResolumeCompositionClip({ id: 'clip-1', name: 'Snow', deckId: 'deck-1', layerIndex: 0 }),
        makeResolumeCompositionClip({ id: 'clip-2', name: 'Snow', deckId: 'deck-1', layerIndex: 1 }),
      ],
    })
    launchResolumeClip.mockResolvedValue(makeResolumeActionResult({ outcome: 'confirmed' }))
    render(
      <ModelContext.Provider value={adminModel}>
        <ResolumeActionController actions={ALL_ACTIONS} composition={composition} />
      </ModelContext.Provider>,
    )

    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Action' }), 'launchClip')
    await userEvent.selectOptions(
      screen.getByRole('combobox', { name: 'Deck' }),
      screen.getByRole('option', { name: /^Deck 1/ }),
    )
    await userEvent.selectOptions(
      screen.getByRole('combobox', { name: 'Clip' }),
      screen.getByRole('option', { name: /Layer B/ }),
    )
    await userEvent.click(screen.getByRole('button', { name: /go/i }))

    await waitFor(() =>
      expect(launchResolumeClip).toHaveBeenCalledWith({ clip: 'Snow', deck: 'Deck 1', layer: 'Layer B' }),
    )
  })

  // Review finding 8: two decks sharing a name used to be reverse-looked-
  // up by `.find(d => d.value === deck)`, which silently returns the
  // FIRST match — so picking the SECOND same-named deck still scoped the
  // clip list to the first one's clips. The deck picker now keys its
  // option value on the deck's own id, so the two decks are independently
  // selectable and each correctly scopes its own clip list.
  it('scopes the clip list to the SECOND of two same-named decks when it is the one picked', async () => {
    const composition = makeResolumeCompositionResponse({
      decks: [
        makeResolumeCompositionDeck({ id: 'deck-1', name: 'Main', nameGenerated: false }),
        makeResolumeCompositionDeck({ id: 'deck-2', name: 'Main', nameGenerated: false }),
      ],
      clips: [
        makeResolumeCompositionClip({ id: 'clip-1', name: 'First Deck Clip', deckId: 'deck-1' }),
        makeResolumeCompositionClip({ id: 'clip-2', name: 'Second Deck Clip', deckId: 'deck-2' }),
      ],
    })
    renderController(composition)

    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Action' }), 'launchClip')
    const deckOptions = screen.getAllByRole('option', { name: /^Main/ })
    expect(deckOptions).toHaveLength(2)
    // The two same-named decks are disambiguated in their LABELS...
    expect(new Set(deckOptions.map((o) => o.textContent)).size).toBe(2)
    // ...and independently selectable: picking the second one scopes the
    // clip list to ITS clip, never the first deck's.
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Deck' }), deckOptions[1] as HTMLElement)

    const clipSelect = screen.getByRole('combobox', { name: 'Clip' })
    const clipTexts = Array.from(clipSelect.querySelectorAll('option'))
      .map((o) => o.textContent ?? '')
      .filter((t) => t !== 'Choose one')
    expect(clipTexts).toEqual(['Second Deck Clip'])
  })

  // Acceptance criterion 4: a generated label is visually distinguishable
  // from an authored one.
  it('marks a generated layer label as generated, distinctly from an authored one', async () => {
    const composition = makeResolumeCompositionResponse({
      layers: [
        makeResolumeCompositionLayer({ id: 'layer-1', index: 0, name: 'Layer 1', nameGenerated: true }),
        makeResolumeCompositionLayer({ id: 'layer-2', index: 1, name: 'Authored Layer', nameGenerated: false }),
      ],
    })
    renderController(composition)
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Action' }), 'clearLayer')
    const options = Array.from(screen.getByRole('combobox', { name: 'Layer' }).querySelectorAll('option')).map(
      (o) => o.textContent,
    )
    expect(options).toContain('Layer 1 (generated)')
    expect(options).toContain('Authored Layer')
    expect(options).not.toContain('Authored Layer (generated)')
  })

  it('renders the dispatched outcome once resolved', async () => {
    blackoutResolume.mockResolvedValue(makeResolumeActionResult({ action: 'blackout', outcome: 'unconfirmable', outcomeReason: 'no observable effect' }))
    renderController()

    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Action' }), 'blackout')
    await userEvent.click(screen.getByRole('button', { name: /go/i }))

    await waitFor(() => expect(screen.getByText(/Unconfirmable/)).toBeVisible())
  })
})
