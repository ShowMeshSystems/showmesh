/**
 * Pure, framework-free helpers over a stored [ResolumeCompositionResponse]
 * (ADR-032's id map): resolving an observation signal's embedded Resolume
 * object id to a name, and building disambiguated picker option lists for
 * the controller page (build contract §2.3). ADR-037: every reference this
 * UI ever sends is a NAME, never an id — this module is what makes that
 * possible without the operator having to type one.
 */
import type { Evidence, ResolumeCompositionClip, ResolumeCompositionResponse } from './types'

/** Per-object observation signal ids this package's collector mints (resolume/signals.go). */
const LAYER_READY_RE = /^resolume\.layer\.(.+)\.ready$/
const LAYER_ACTIVE_CLIP_RE = /^resolume\.layer\.(.+)\.active_clip$/
const CLIP_CONNECTED_RE = /^resolume\.clip\.(.+)\.connected$/
const CLIP_TRANSPORT_TYPE_RE = /^resolume\.clip\.(.+)\.transporttype$/

function layerNameById(composition: ResolumeCompositionResponse | null, id: string): string | null {
  const layer = composition?.layers.find((l) => l.id === id)
  return layer === undefined ? null : layer.name
}

function clipNameById(composition: ResolumeCompositionResponse | null, id: string): string | null {
  const clip =
    composition?.clips.find((c) => c.id === id) ?? composition?.persistentClips.find((c) => c.id === id)
  return clip === undefined ? null : clip.name
}

function deckNameById(composition: ResolumeCompositionResponse | null, id: string): string | null {
  const deck = composition?.decks.find((d) => d.id === id)
  return deck === undefined ? null : deck.name
}

function anyNameById(composition: ResolumeCompositionResponse | null, id: string): string | null {
  return layerNameById(composition, id) ?? clipNameById(composition, id) ?? deckNameById(composition, id)
}

// -----------------------------------------------------------------------
// A genuine gap, not silently patched (build contract §1: "either you have
// missed an endpoint that already exists, or you have found a genuine gap,
// and a gap is worth reporting"). Several observation VALUES and REASONS
// this coordinator's own Resolume collector produces embed a raw Resolume
// object id directly in their string with no separate structured field
// carrying just the name: `resolume.composition.identified`'s
// deck_mismatch/not_identified branches, `resolume.composition.
// selected_deck`, and `resolume.layer.<id>.active_clip` (all via that
// collector's own `formatRef`), AND — found only by driving this against
// the operator's real composition in a real browser, never by reading the
// source or by a fixture — a THIRD, structurally different shape: a
// `by-id/<id>` REST path segment inside a plain Go error string
// (internal/coordinator/collector/resolume/client.go: `"resolume: %s
// response exceeded %d byte limit"`, where %s is the request path), which
// carries no "id " prefix and no surrounding parens at all. Criterion 3
// requires this view render no Arena object id anywhere. This is a
// browser-only mitigation over three known observed shapes — not a fix to
// the underlying signal, which stays exactly as the coordinator sends it
// in every other client (showmeshctl, raw curl), and not a guarantee no
// fourth shape exists.
// -----------------------------------------------------------------------
const FORMAT_REF_WITH_NAME_RE = /([^()]+) \(id ([^\s()]+)\)/g
const FORMAT_REF_BARE_ID_RE = /\bid ([^\s()]+)\b/g
const BY_ID_PATH_RE = /\bby-id\/([^\s/()]+)/g

/** Strips or resolves every known id-leaking shape in `value`, using `composition` to recover a name where possible. Pure string transform; never throws on a value that carries no such reference. */
export function sanitizeResolumeValueString(value: string, composition: ResolumeCompositionResponse | null): string {
  let result = value.replace(FORMAT_REF_WITH_NAME_RE, (_match, name: string) => name)
  result = result.replace(FORMAT_REF_BARE_ID_RE, (_match, id: string) => {
    const name = anyNameById(composition, id)
    return name ?? 'an object this coordinator cannot currently resolve to a name'
  })
  result = result.replace(BY_ID_PATH_RE, (_match, id: string) => {
    const name = anyNameById(composition, id)
    return name !== null ? `by-name/${name}` : 'by-id/(unresolvable)'
  })
  return result
}

/**
 * [sanitizeResolumeValueString] applied to one Evidence envelope's own
 * `value` (when a string) and `reason` (when non-null), leaving every
 * other field untouched — defense in depth in case a future collector
 * change embeds a reference in `reason` the way today's code embeds one in
 * `value` only.
 */
export function sanitizeResolumeEvidence(evidence: Evidence, composition: ResolumeCompositionResponse | null): Evidence {
  const value = typeof evidence.value === 'string' ? sanitizeResolumeValueString(evidence.value, composition) : evidence.value
  const reason = evidence.reason === null ? null : sanitizeResolumeValueString(evidence.reason, composition)
  if (value === evidence.value && reason === evidence.reason) return evidence
  return { ...evidence, value, reason }
}

/**
 * A human label for one observation `signal`, resolving any embedded
 * Resolume object id to the composition's own name for it (build contract
 * §2.2: "layer and clip names, never an object id"). When the id it names
 * is not (or no longer) present in the stored composition, this states
 * that plainly WITHOUT the raw signal string — a per-object signal id
 * embeds the very id this function exists to hide (e.g.
 * "resolume.layer.<id>.ready"), so falling back to the bare signal would
 * leak exactly what acceptance criterion 3 forbids. Falls back to the
 * bare signal only for a signal with no per-object id embedded at all
 * (e.g. "resolume.reachable"), which cannot leak anything.
 */
export function resolumeObservationLabel(signal: string, composition: ResolumeCompositionResponse | null): string {
  const layerReady = LAYER_READY_RE.exec(signal)
  if (layerReady !== null) {
    const id = layerReady[1] as string
    const name = layerNameById(composition, id)
    return name === null ? 'layer ready (layer not in the stored composition)' : `layer "${name}" ready`
  }
  const layerActiveClip = LAYER_ACTIVE_CLIP_RE.exec(signal)
  if (layerActiveClip !== null) {
    const id = layerActiveClip[1] as string
    const name = layerNameById(composition, id)
    return name === null
      ? 'layer active clip (layer not in the stored composition)'
      : `layer "${name}" active clip`
  }
  const clipConnected = CLIP_CONNECTED_RE.exec(signal)
  if (clipConnected !== null) {
    const id = clipConnected[1] as string
    const name = clipNameById(composition, id)
    return name === null ? 'clip connected (clip not in the stored composition)' : `clip "${name}" connected`
  }
  const clipTransportType = CLIP_TRANSPORT_TYPE_RE.exec(signal)
  if (clipTransportType !== null) {
    const id = clipTransportType[1] as string
    const name = clipNameById(composition, id)
    return name === null
      ? 'clip transport type (clip not in the stored composition)'
      : `clip "${name}" transport type`
  }
  return signal
}

/** One clip's per-clip observations, grouped by the clip id embedded in their signal names. */
export interface ClipObservationGroup {
  clipId: string
  /** This clip's resolved row label, in the same "clip "<name>"" / not-in-composition form resolumeObservationLabel uses. */
  label: string
  connected: Evidence | null
  transportType: Evidence | null
}

export interface GroupedResolumeObservations {
  /** One entry per distinct clip id seen, in first-seen order. */
  clips: ClipObservationGroup[]
  /** Every observation whose signal is not one of the two per-clip shapes above, in original order, unmodified. */
  other: Evidence[]
}

/**
 * Groups an instance's flat observation list by the clip id embedded in
 * `resolume.clip.<id>.connected`/`.transporttype` signals, so a caller can
 * render one row per clip (both signals as columns) instead of stacking
 * every signal as its own block — an operator-reported readability defect
 * when a composition holds many clips, each surfacing two signals. A clip
 * missing one of its two signals still gets a row, with that field left
 * `null` (rendered as absent, never skipped). Every other observation
 * (e.g. `resolume.reachable`) is returned separately, unmodified and never
 * dropped — it is not part of this grouping.
 */
export function groupClipObservations(
  observations: readonly Evidence[],
  composition: ResolumeCompositionResponse | null,
): GroupedResolumeObservations {
  const order: string[] = []
  const byClip = new Map<string, ClipObservationGroup>()
  const other: Evidence[] = []

  function rowFor(clipId: string): ClipObservationGroup {
    let row = byClip.get(clipId)
    if (row === undefined) {
      const name = clipNameById(composition, clipId)
      row = {
        clipId,
        label: name === null ? 'clip not in the stored composition' : `clip "${name}"`,
        connected: null,
        transportType: null,
      }
      byClip.set(clipId, row)
      order.push(clipId)
    }
    return row
  }

  for (const observation of observations) {
    const connected = CLIP_CONNECTED_RE.exec(observation.signal)
    if (connected !== null) {
      rowFor(connected[1] as string).connected = observation
      continue
    }
    const transportType = CLIP_TRANSPORT_TYPE_RE.exec(observation.signal)
    if (transportType !== null) {
      rowFor(transportType[1] as string).transportType = observation
      continue
    }
    other.push(observation)
  }

  return { clips: order.map((id) => byClip.get(id) as ClipObservationGroup), other }
}

// ---------------------------------------------------------------------
// Picker option lists (build contract §2.3): decks, layers, columns, and
// clips scoped to a deck or persistent, each disambiguated before
// selection when two entries share a label.
// ---------------------------------------------------------------------

export interface PickerOption {
  /** Stable React key — an object id, never sent on the wire. */
  key: string
  /** The name value ShowMesh actually sends (ADR-037). */
  value: string
  /** Display label — includes a disambiguator when `value` is not unique in this list. */
  label: string
  /** True when this composition's own `nameGenerated` flag was set — surfaced so a generated label is never presented as authored (build contract §2.2/§2.3). */
  nameGenerated: boolean
}

/**
 * Disambiguates a list of (id, name, nameGenerated) entries that share the
 * SAME `value` domain (e.g. every deck, or every clip on one deck) by
 * appending `disambiguator(entry)` in parentheses whenever a name repeats
 * — build contract §2.3: "a duplicate label must be disambiguated in the
 * list before selection." An entry whose name is already unique is left
 * exactly as authored/generated, so the common case adds no visual noise.
 */
function disambiguate<T>(
  entries: readonly T[],
  keyOf: (e: T) => string,
  nameOf: (e: T) => string,
  nameGeneratedOf: (e: T) => boolean,
  disambiguatorOf: (e: T) => string | null,
): PickerOption[] {
  const counts = new Map<string, number>()
  for (const e of entries) counts.set(nameOf(e), (counts.get(nameOf(e)) ?? 0) + 1)
  return entries.map((e) => {
    const name = nameOf(e)
    const duplicate = (counts.get(name) ?? 0) > 1
    const extra = duplicate ? disambiguatorOf(e) : null
    return {
      key: keyOf(e),
      value: name,
      label: extra !== null && extra !== '' ? `${name} (${extra})` : name,
      nameGenerated: nameGeneratedOf(e),
    }
  })
}

export function deckOptions(composition: ResolumeCompositionResponse | null): PickerOption[] {
  if (composition === null) return []
  // Review finding 8: two decks CAN share a name (an operator-authored
  // collision, not just the generated "Deck <n>" form, which is already
  // unique per position) — disambiguate by position in this response's own
  // decks list, the same convention layerOptions/columnOptions use for
  // their own index. This label alone does not make the reference itself
  // resolvable (ADR-037: a name-only reference to two same-named decks is
  // still ambiguous on the wire, and the server still refuses it) — it
  // only lets the operator SEE the collision before picking, matching
  // every other picker in this file.
  const withPosition = composition.decks.map((d, i) => ({ ...d, position: i + 1 }))
  return disambiguate(
    withPosition,
    (d) => d.id,
    (d) => d.name,
    (d) => d.nameGenerated,
    (d) => `deck ${d.position}`,
  )
}

export function layerOptions(composition: ResolumeCompositionResponse | null): PickerOption[] {
  if (composition === null) return []
  return disambiguate(
    composition.layers,
    (l) => l.id,
    (l) => l.name,
    (l) => l.nameGenerated,
    (l) => `layer ${l.index + 1}`,
  )
}

/** Columns scoped to one deck id (ResolumeCompositionColumn.deckId). */
export function columnOptions(composition: ResolumeCompositionResponse | null, deckId: string | null): PickerOption[] {
  if (composition === null || deckId === null) return []
  const scoped = composition.columns.filter((c) => c.deckId === deckId)
  return disambiguate(
    scoped,
    (c) => c.id,
    (c) => c.name,
    (c) => c.nameGenerated,
    (c) => `column ${c.index + 1}`,
  )
}

function layerNameForClip(composition: ResolumeCompositionResponse, clip: ResolumeCompositionClip): string {
  const layer = composition.layers.find((l) => l.index === clip.layerIndex)
  return layer === undefined ? `layer ${clip.layerIndex + 1}` : layer.name
}

/**
 * Clips scoped to one deck id, OR persistent clips when `persistent` is
 * true. `ambiguous` clips (server-computed: the same (deck-or-persistent,
 * layer, label) triple shared by another clip, unresolvable by name even
 * with a layer disambiguator) are still listed — the server is the
 * authority and still refuses a pick that resolves to one — but flagged so
 * the picker can warn before the operator learns that the hard way.
 */
export interface ClipPickerOption extends PickerOption {
  ambiguous: boolean
  /**
   * This clip's own layer name — sent as `params.layer` alongside `clip`
   * whenever `duplicateName` is true, since ADR-037's own `layer`
   * parameter exists exactly to disambiguate a clip name shared by more
   * than one clip in this scope (ResolumeLaunchClipActionRequest's own
   * doc comment). Always populated, even when not needed, so a caller
   * never has to look it up a second way.
   */
  layerName: string
  /** True when another clip in this SAME scope (deck or persistent) shares this clip's own name — the condition under which `layer` must be sent. */
  duplicateName: boolean
}

export function clipOptions(
  composition: ResolumeCompositionResponse | null,
  scope: { deckId: string } | { persistent: true },
): ClipPickerOption[] {
  if (composition === null) return []
  const scoped =
    'persistent' in scope ? composition.persistentClips : composition.clips.filter((c) => c.deckId === scope.deckId)
  const disambiguated = disambiguate(
    scoped,
    (c) => c.id,
    (c) => c.name,
    (c) => c.nameGenerated,
    (c) => `on ${layerNameForClip(composition, c)}`,
  )
  const counts = new Map<string, number>()
  for (const c of scoped) counts.set(c.name, (counts.get(c.name) ?? 0) + 1)
  return disambiguated.map((option, i) => {
    const clip = scoped[i]
    return {
      ...option,
      ambiguous: clip?.ambiguous ?? false,
      layerName: clip === undefined ? '' : layerNameForClip(composition, clip),
      duplicateName: (counts.get(option.value) ?? 0) > 1,
    }
  })
}

/** Every ambiguous clip in the composition (deck clips and persistent), for build contract §2.2's "ambiguous clips" panel. */
export interface AmbiguousClip {
  id: string
  name: string
  layerName: string
  deckName: string | null
}

export function ambiguousClips(composition: ResolumeCompositionResponse | null): AmbiguousClip[] {
  if (composition === null) return []
  const all = [...composition.clips, ...composition.persistentClips]
  return all
    .filter((c) => c.ambiguous)
    .map((c) => ({
      id: c.id,
      name: c.name,
      layerName: layerNameForClip(composition, c),
      deckName: c.deckId === undefined ? null : (composition.decks.find((d) => d.id === c.deckId)?.name ?? null),
    }))
}
