import { describe, expect, it } from 'vitest'
import {
  ambiguousClips,
  clipOptions,
  columnOptions,
  deckOptions,
  layerOptions,
  resolumeObservationLabel,
  sanitizeResolumeEvidence,
  sanitizeResolumeValueString,
} from './resolumeComposition'
import {
  makeResolumeCompositionClip,
  makeResolumeCompositionDeck,
  makeResolumeCompositionLayer,
  makeResolumeCompositionResponse,
} from './test-support/fixtures'
import { makeEvidence } from './test-support/fixtures'

describe('resolumeObservationLabel', () => {
  it('resolves a layer.<id>.ready signal to the layer name, never the id', () => {
    const composition = makeResolumeCompositionResponse({
      layers: [makeResolumeCompositionLayer({ id: 'layer-abc', name: 'Snow', nameGenerated: false })],
    })
    const label = resolumeObservationLabel('resolume.layer.layer-abc.ready', composition)
    expect(label).toContain('Snow')
    expect(label).not.toContain('layer-abc')
  })

  it('resolves a clip.<id>.connected signal to the clip name, never the id', () => {
    const composition = makeResolumeCompositionResponse({
      clips: [makeResolumeCompositionClip({ id: 'clip-xyz', name: 'Santa', nameGenerated: false })],
      persistentClips: [],
    })
    const label = resolumeObservationLabel('resolume.clip.clip-xyz.connected', composition)
    expect(label).toContain('Santa')
    expect(label).not.toContain('clip-xyz')
  })

  // A per-object signal id embeds the very object id this function exists
  // to hide (e.g. "resolume.layer.<id>.ready") — falling back to the bare
  // signal when the id cannot be resolved would leak exactly what
  // acceptance criterion 3 forbids, so this asserts the id never appears,
  // never that some OTHER specific wording is used.
  it('never leaks the raw id in its fallback wording when the id cannot be resolved', () => {
    const label = resolumeObservationLabel('resolume.layer.unknown-id.ready', null)
    expect(label).not.toContain('unknown-id')
    expect(label.length).toBeGreaterThan(0)
  })

  it('passes through a signal with no per-object id unchanged', () => {
    expect(resolumeObservationLabel('resolume.reachable', null)).toBe('resolume.reachable')
  })
})

describe('sanitizeResolumeValueString', () => {
  // internal/coordinator/collector/resolume/collector.go's formatRef
  // produces exactly these two shapes for a deck/clip reference embedded
  // in an observation's own VALUE — this is the genuine gap D-4's own
  // report documents: at least three signals embed a raw Resolume object
  // id with no separate structured field carrying just the name.
  it('strips the "(id ...)" suffix from a named reference, keeping only the name', () => {
    expect(sanitizeResolumeValueString('Deck 1 (id abc123)', null)).toBe('Deck 1')
  })

  it('resolves a bare "id <id>" reference to a name when the composition knows it', () => {
    const composition = makeResolumeCompositionResponse({
      clips: [makeResolumeCompositionClip({ id: 'clip-1', name: 'Reindeer' })],
    })
    expect(sanitizeResolumeValueString('id clip-1', composition)).toBe('Reindeer')
  })

  it('never renders the raw id when it cannot be resolved to a name', () => {
    const result = sanitizeResolumeValueString('id unknown-object', null)
    expect(result).not.toContain('unknown-object')
  })

  it('sanitizes an embedded reference inside a longer sentence (deck_mismatch shape)', () => {
    const composition = makeResolumeCompositionResponse({
      decks: [makeResolumeCompositionDeck({ id: 'deck-2', name: 'Deck 2' })],
    })
    const raw = 'deck_mismatch: mismatch (expected deck id deck-1, now selected Deck 2 (id deck-2))'
    const result = sanitizeResolumeValueString(raw, composition)
    expect(result).not.toContain('deck-1')
    expect(result).not.toContain('deck-2')
    expect(result).toContain('Deck 2')
  })

  // Found only by driving this against the operator's real composition in
  // a real browser (build contract §5/criterion 11): a THIRD id-leaking
  // shape, structurally different from formatRef's two — a `by-id/<id>`
  // REST path segment inside a plain Go error string
  // (internal/coordinator/collector/resolume/client.go's "response
  // exceeded ... byte limit" error), with no "id " prefix and no
  // surrounding parens.
  it('strips a raw REST-path id from a "by-id/<id>" shape, resolving it to a name when known', () => {
    const composition = makeResolumeCompositionResponse({
      layers: [makeResolumeCompositionLayer({ id: '1765224911972', name: 'Below PeakMessage Spaces' })],
    })
    const raw =
      'unknown: layergroup.bypassed (resolume: /composition/layergroups/by-id/1765224911972 response exceeded 524288 byte limit)'
    const result = sanitizeResolumeValueString(raw, composition)
    expect(result).not.toContain('1765224911972')
  })

  it('never renders the raw id from a "by-id/<id>" shape even when it cannot be resolved to a name (e.g. a layer group, which carries no name in this system)', () => {
    const raw = 'resolume: /composition/layergroups/by-id/1765224911972 response exceeded 524288 byte limit'
    const result = sanitizeResolumeValueString(raw, null)
    expect(result).not.toContain('1765224911972')
  })

  it('leaves a value with no embedded reference unchanged', () => {
    expect(sanitizeResolumeValueString('identified', null)).toBe('identified')
  })
})

describe('sanitizeResolumeEvidence', () => {
  it('sanitizes a string value and leaves every other field untouched', () => {
    const evidence = makeEvidence({ signal: 'resolume.composition.selected_deck', value: 'Deck 1 (id abc)' })
    const sanitized = sanitizeResolumeEvidence(evidence, null)
    expect(sanitized.value).toBe('Deck 1')
    expect(sanitized.signal).toBe(evidence.signal)
    expect(sanitized.state).toBe(evidence.state)
  })

  it('passes a non-string value through unchanged', () => {
    const evidence = makeEvidence({ value: true })
    expect(sanitizeResolumeEvidence(evidence, null)).toBe(evidence)
  })
})

describe('deckOptions/layerOptions/columnOptions', () => {
  it('renders no composition as an empty list, never throwing', () => {
    expect(deckOptions(null)).toEqual([])
    expect(layerOptions(null)).toEqual([])
    expect(columnOptions(null, 'deck-1')).toEqual([])
  })

  it('scopes columns to their own deck', () => {
    const composition = makeResolumeCompositionResponse({
      columns: [
        { id: 'col-1', deckId: 'deck-1', index: 0, name: 'Column 1', nameGenerated: true },
        { id: 'col-2', deckId: 'deck-2', index: 0, name: 'Column 1', nameGenerated: true },
      ],
    })
    expect(columnOptions(composition, 'deck-1').map((o) => o.key)).toEqual(['col-1'])
  })
})

describe('clipOptions: build contract §2.3 disambiguation', () => {
  it('disambiguates two clips sharing a name on the same deck, before selection', () => {
    const composition = makeResolumeCompositionResponse({
      layers: [
        makeResolumeCompositionLayer({ id: 'layer-1', index: 0, name: 'Layer A' }),
        makeResolumeCompositionLayer({ id: 'layer-2', index: 1, name: 'Layer B' }),
      ],
      clips: [
        makeResolumeCompositionClip({ id: 'clip-1', name: 'Snow', deckId: 'deck-1', layerIndex: 0 }),
        makeResolumeCompositionClip({ id: 'clip-2', name: 'Snow', deckId: 'deck-1', layerIndex: 1 }),
      ],
    })
    const options = clipOptions(composition, { deckId: 'deck-1' })
    expect(options).toHaveLength(2)
    // Both share the wire VALUE (the name itself, never changed by
    // disambiguation) but must have DISTINCT, distinguishable labels.
    expect(options[0]?.value).toBe('Snow')
    expect(options[1]?.value).toBe('Snow')
    expect(options[0]?.label).not.toBe(options[1]?.label)
    expect(options.every((o) => o.duplicateName)).toBe(true)
  })

  it('does not disambiguate a clip whose name is unique in its scope', () => {
    const composition = makeResolumeCompositionResponse({
      clips: [makeResolumeCompositionClip({ id: 'clip-1', name: 'Unique', deckId: 'deck-1' })],
    })
    const options = clipOptions(composition, { deckId: 'deck-1' })
    expect(options[0]?.label).toBe('Unique')
    expect(options[0]?.duplicateName).toBe(false)
  })

  it('surfaces the server-computed ambiguous flag per clip, unaffected by client-side disambiguation', () => {
    const composition = makeResolumeCompositionResponse({
      clips: [makeResolumeCompositionClip({ id: 'clip-1', name: 'Snow', deckId: 'deck-1', ambiguous: true })],
    })
    const options = clipOptions(composition, { deckId: 'deck-1' })
    expect(options[0]?.ambiguous).toBe(true)
  })

  it('lists persistent clips separately from deck clips', () => {
    const composition = makeResolumeCompositionResponse({
      clips: [makeResolumeCompositionClip({ id: 'clip-1', name: 'Deck clip', deckId: 'deck-1' })],
      // deckId is genuinely ABSENT for a persistent clip (never present-
      // as-undefined) — built directly rather than through the fixture's
      // own deckId default, which `exactOptionalPropertyTypes` forbids
      // overriding with a literal `undefined`.
      persistentClips: [
        { id: 'clip-2', layerIndex: 0, columnIndex: 0, name: 'Persistent clip', nameGenerated: false, ambiguous: false },
      ],
    })
    expect(clipOptions(composition, { deckId: 'deck-1' }).map((o) => o.value)).toEqual(['Deck clip'])
    expect(clipOptions(composition, { persistent: true }).map((o) => o.value)).toEqual(['Persistent clip'])
  })
})

describe('ambiguousClips', () => {
  it('lists a clip flagged ambiguous by the server, with its layer and deck named', () => {
    const composition = makeResolumeCompositionResponse({
      decks: [makeResolumeCompositionDeck({ id: 'deck-1', name: 'Main' })],
      layers: [makeResolumeCompositionLayer({ id: 'layer-1', index: 0, name: 'Layer A' })],
      clips: [
        makeResolumeCompositionClip({ id: 'clip-1', name: 'Snow', deckId: 'deck-1', layerIndex: 0, ambiguous: true }),
      ],
    })
    const list = ambiguousClips(composition)
    expect(list).toHaveLength(1)
    expect(list[0]).toMatchObject({ id: 'clip-1', name: 'Snow', layerName: 'Layer A', deckName: 'Main' })
  })

  it('is empty when no clip is ambiguous', () => {
    const composition = makeResolumeCompositionResponse({
      clips: [makeResolumeCompositionClip({ ambiguous: false })],
    })
    expect(ambiguousClips(composition)).toEqual([])
  })
})
