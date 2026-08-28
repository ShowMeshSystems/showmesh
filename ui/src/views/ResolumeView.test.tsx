import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ResolumeView } from './ResolumeView'
import { ModelContext } from '../app/ModelContext'
import { ApiError, ForbiddenError } from '../api/errors'
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

  // Review finding 4: ambiguousClips(null) is [] exactly like a genuinely
  // empty composition, so "No ambiguous clips ... every clip can be
  // resolved by name" used to render while still loading, on a 403, and
  // before anything had been uploaded — an operator authoring macros
  // ahead of a show would read a false all-clear. None of these four
  // states may ever show that sentence.
  describe('the ambiguous-clips panel never claims absence it does not know', () => {
    it('while the composition is still loading', async () => {
      getResolumeComposition.mockReturnValue(new Promise(() => {})) // never resolves
      getResolumeRecovery.mockResolvedValue(emptyRecovery)
      listResolumeActions.mockResolvedValue({ serverTime: '2026-08-16T00:00:00Z', actions: [] })

      const instance = makeResolumeInstance('resolume-1', { composition: { name: 'Christmas 25' } })
      renderView(makeModel({ resolume: [instance] }))

      await screen.findByRole('heading', { name: 'Ambiguous clips' })
      expect(screen.queryByText(/No ambiguous clips/i)).not.toBeInTheDocument()
    })

    it('on a 403 reading the composition', async () => {
      getResolumeComposition.mockRejectedValue(new ForbiddenError('this principal’s role does not include "config:write"'))
      getResolumeRecovery.mockResolvedValue(emptyRecovery)
      listResolumeActions.mockResolvedValue({ serverTime: '2026-08-16T00:00:00Z', actions: [] })

      const instance = makeResolumeInstance('resolume-1', { composition: { name: 'Christmas 25' } })
      renderView(makeModel({ resolume: [instance] }))

      await waitFor(() => expect(getResolumeComposition).toHaveBeenCalled())
      expect(screen.queryByText(/No ambiguous clips/i)).not.toBeInTheDocument()
    })

    it('before anything has been uploaded (not_stored)', async () => {
      getResolumeComposition.mockRejectedValue(new ApiError('no Resolume composition has been uploaded yet', 404))
      getResolumeRecovery.mockResolvedValue(emptyRecovery)
      listResolumeActions.mockResolvedValue({ serverTime: '2026-08-16T00:00:00Z', actions: [] })

      const instance = makeResolumeInstance('resolume-1', { composition: { name: 'Christmas 25' } })
      renderView(makeModel({ resolume: [instance] }))

      await waitFor(() => expect(getResolumeComposition).toHaveBeenCalled())
      expect(screen.queryByText(/No ambiguous clips/i)).not.toBeInTheDocument()
    })
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

  // Review finding 3: the recovery record's own "unknown" reason, and the
  // last restore report's per-layer skip/failure reason, are both
  // server-built free text that can embed a formatRef-style raw id
  // exactly like ResolumeActionResult.outcomeReason does — "suspected" by
  // the review at ResolumeView.tsx's own recovery-record line, never
  // actually driven before. Both get the same real formatRef shape here.
  const REAL_REASON_WITH_ID = 'this clip belongs to Deck 2 (id 2000000000002), and that deck is not selected'

  it('sanitizes a raw Arena object id out of the recovery record reason', async () => {
    getResolumeComposition.mockRejectedValue(new ApiError('nothing stored', 404))
    getResolumeRecovery.mockResolvedValue(
      makeResolumeRecoveryResponse({
        record: [
          {
            layer: 'Layer 1',
            layerNameGenerated: false,
            state: 'unknown',
            reason: REAL_REASON_WITH_ID,
          },
        ],
      }),
    )
    listResolumeActions.mockResolvedValue({ serverTime: '2026-08-16T00:00:00Z', actions: [] })

    const instance = makeResolumeInstance('resolume-1')
    renderView(makeModel({ resolume: [instance] }))

    await screen.findByText('unknown')
    const text = document.body.textContent ?? ''
    expect(text).not.toMatch(/\bid 2000000000002\b/)
    expect(text).toContain('Deck 2')
  })

  it('sanitizes a raw Arena object id out of the last restore report reason', async () => {
    getResolumeComposition.mockRejectedValue(new ApiError('nothing stored', 404))
    getResolumeRecovery.mockResolvedValue(
      makeResolumeRecoveryResponse({
        lastRestore: {
          startedAt: '2026-08-16T00:00:00Z',
          finishedAt: '2026-08-16T00:00:05Z',
          trigger: 'manual',
          outcome: 'partial',
          principal: 'operator',
          omittedLayerCount: 0,
          layers: [
            {
              layer: 'Layer 1',
              layerNameGenerated: false,
              result: 'skipped',
              reason: REAL_REASON_WITH_ID,
            },
          ],
        },
      }),
    )
    listResolumeActions.mockResolvedValue({ serverTime: '2026-08-16T00:00:00Z', actions: [] })

    const instance = makeResolumeInstance('resolume-1')
    renderView(makeModel({ resolume: [instance] }))

    await screen.findByText('Last restore')
    const text = document.body.textContent ?? ''
    expect(text).not.toMatch(/\bid 2000000000002\b/)
    expect(text).toContain('Deck 2')
  })

  // Operator report: 66 stacked observation blocks for 33 clips read as
  // one big block of text. These clips (two signals each) must render as
  // one aligned table row per clip, and nothing observed may be dropped.
  describe('clip observations table', () => {
    it('renders one row per clip with both signals in their own columns', async () => {
      getResolumeComposition.mockResolvedValue(
        makeResolumeCompositionResponse({
          decks: [makeResolumeCompositionDeck({ id: 'deck-1', name: 'Main' })],
          clips: [makeResolumeCompositionClip({ id: 'clip-1', name: 'Snowfall', deckId: 'deck-1' })],
        }),
      )
      getResolumeRecovery.mockResolvedValue(emptyRecovery)
      listResolumeActions.mockResolvedValue({ serverTime: '2026-08-16T00:00:00Z', actions: [] })

      const instance = makeResolumeInstance('resolume-1', {
        composition: { name: 'Christmas 25' },
        observations: [
          makeEvidence({
            signal: 'resolume.clip.clip-1.connected',
            state: 'stale',
            reason: 'no update since last observed',
          }),
          makeEvidence({
            signal: 'resolume.clip.clip-1.transporttype',
            value: 'Loop',
            state: 'stale',
            reason: 'no update since last observed',
          }),
        ],
      })
      renderView(makeModel({ resolume: [instance] }))

      await screen.findByRole('table', { name: 'Clip observations' })
      const row = screen.getByRole('rowheader', { name: 'clip "Snowfall"' }).closest('tr') as HTMLTableRowElement
      const cells = within(row).getAllByRole('cell')
      expect(cells).toHaveLength(2)
      expect(within(row).getByText('true')).toBeInTheDocument()
      expect(within(row).getByText('Loop')).toBeInTheDocument()
    })

    it('still renders a row for a clip with only one of its two signals, with the other cell absent', async () => {
      getResolumeComposition.mockResolvedValue(
        makeResolumeCompositionResponse({
          decks: [makeResolumeCompositionDeck({ id: 'deck-1', name: 'Main' })],
          clips: [makeResolumeCompositionClip({ id: 'clip-1', name: 'Snowfall', deckId: 'deck-1' })],
        }),
      )
      getResolumeRecovery.mockResolvedValue(emptyRecovery)
      listResolumeActions.mockResolvedValue({ serverTime: '2026-08-16T00:00:00Z', actions: [] })

      const instance = makeResolumeInstance('resolume-1', {
        composition: { name: 'Christmas 25' },
        observations: [
          makeEvidence({
            signal: 'resolume.clip.clip-1.connected',
            state: 'stale',
            reason: 'no update since last observed',
          }),
        ],
      })
      renderView(makeModel({ resolume: [instance] }))

      await screen.findByRole('table', { name: 'Clip observations' })
      const row = screen.getByRole('rowheader', { name: 'clip "Snowfall"' }).closest('tr') as HTMLTableRowElement
      const cells = within(row).getAllByRole('cell')
      expect(cells).toHaveLength(2)
      expect(cells[1]?.textContent).toBe('not reported')
    })

    it('still renders a non-clip observation like resolume.reachable, never swallowed into the clip table', async () => {
      getResolumeComposition.mockResolvedValue(
        makeResolumeCompositionResponse({
          decks: [makeResolumeCompositionDeck({ id: 'deck-1', name: 'Main' })],
          clips: [makeResolumeCompositionClip({ id: 'clip-1', name: 'Snowfall', deckId: 'deck-1' })],
        }),
      )
      getResolumeRecovery.mockResolvedValue(emptyRecovery)
      listResolumeActions.mockResolvedValue({ serverTime: '2026-08-16T00:00:00Z', actions: [] })

      const instance = makeResolumeInstance('resolume-1', {
        composition: { name: 'Christmas 25' },
        observations: [
          makeEvidence({ signal: 'resolume.reachable', value: true }),
          makeEvidence({
            signal: 'resolume.clip.clip-1.connected',
            state: 'stale',
            reason: 'no update since last observed',
          }),
        ],
      })
      renderView(makeModel({ resolume: [instance] }))

      await screen.findByRole('table', { name: 'Clip observations' })
      // resolumeObservationLabel has no per-object id to resolve for this
      // signal, so it falls back to the bare signal name -- this test only
      // asserts the observation still renders outside the clip table, not
      // swallowed by the grouping.
      expect(screen.getByText('resolume.reachable')).toBeInTheDocument()
      expect(screen.queryByRole('rowheader', { name: /reachable/i })).not.toBeInTheDocument()
    })

    it('still renders a stale clip signal loudly inside its table cell, never hidden', async () => {
      getResolumeComposition.mockResolvedValue(
        makeResolumeCompositionResponse({
          decks: [makeResolumeCompositionDeck({ id: 'deck-1', name: 'Main' })],
          clips: [makeResolumeCompositionClip({ id: 'clip-1', name: 'Snowfall', deckId: 'deck-1' })],
        }),
      )
      getResolumeRecovery.mockResolvedValue(emptyRecovery)
      listResolumeActions.mockResolvedValue({ serverTime: '2026-08-16T00:00:00Z', actions: [] })

      const instance = makeResolumeInstance('resolume-1', {
        composition: { name: 'Christmas 25' },
        observations: [
          makeEvidence({
            signal: 'resolume.clip.clip-1.connected',
            state: 'stale',
            reason: 'no update since last observed',
          }),
        ],
      })
      renderView(makeModel({ resolume: [instance] }))

      await screen.findByRole('table', { name: 'Clip observations' })
      const row = screen.getByRole('rowheader', { name: 'clip "Snowfall"' }).closest('tr') as HTMLTableRowElement
      expect(within(row).getByText('no update since last observed')).toBeInTheDocument()
      expect(row.querySelector('.evidence--attention')).not.toBeNull()
    })
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
