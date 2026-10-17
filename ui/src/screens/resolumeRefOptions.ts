import type { ResolumeCompositionClip, ResolumeCompositionResponse } from '../api'
import { layerForClip } from './resolumeModel'

export type RefOption = { value: string; label: string; disabled?: boolean }

/** One clip a launchClip reference can name. layer is what the reference must carry: set only when another clip shares the name. */
export type ClipOption = { key: string; label: string; clip: string; layer: string; clipLayer: string; disabled: boolean }

export function deckOptions(comp: ResolumeCompositionResponse): RefOption[] {
  return comp.decks.map((d) => ({ value: d.name, label: d.closed ? `${d.name} · closed` : d.name }))
}

export function layerOptions(comp: ResolumeCompositionResponse): RefOption[] {
  return [...comp.layers].sort((a, b) => a.index - b.index).map((l) => ({ value: l.name, label: l.name }))
}

export function columnOptions(comp: ResolumeCompositionResponse, deckName: string): RefOption[] {
  const deck = comp.decks.find((d) => d.name === deckName)
  if (deck === undefined) return []
  return comp.columns
    .filter((c) => c.deckId === deck.id)
    .sort((a, b) => a.index - b.index)
    .map((c) => ({ value: c.name, label: c.name }))
}

export function clipOptions(comp: ResolumeCompositionResponse, deckName: string, persistent: boolean): ClipOption[] {
  let clips: ResolumeCompositionClip[]
  if (persistent) {
    clips = comp.persistentClips
  } else {
    const deck = comp.decks.find((d) => d.name === deckName)
    if (deck === undefined) return []
    clips = comp.clips.filter((c) => c.deckId === deck.id)
  }
  const sorted = [...clips].sort((a, b) => a.layerIndex - b.layerIndex || a.columnIndex - b.columnIndex)
  const nameCount = new Map<string, number>()
  for (const c of sorted) nameCount.set(c.name, (nameCount.get(c.name) ?? 0) + 1)
  return sorted.map((c) => {
    const clipLayer = layerForClip(comp.layers, c.layerIndex)?.name ?? ''
    const shared = (nameCount.get(c.name) ?? 0) > 1
    const label = c.ambiguous ? `${c.name} · ${clipLayer} · shares its name and layer, rename it in Resolume` : clipLayer === '' ? c.name : `${c.name} · ${clipLayer}`
    return { key: c.id, label, clip: c.name, layer: shared ? clipLayer : '', clipLayer, disabled: c.ambiguous }
  })
}

/** The option a stored clip reference names, or undefined when the stored composition has no such clip. */
export function selectedClip(options: readonly ClipOption[], clip: string, layer: string): ClipOption | undefined {
  if (clip === '') return undefined
  const named = options.filter((o) => o.clip === clip && !o.disabled)
  if (layer !== '') return named.find((o) => o.clipLayer === layer)
  return named.length === 1 ? named[0] : undefined
}
