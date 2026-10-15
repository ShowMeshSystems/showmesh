import type { PreviewTile } from '../api'

export type PreviewLayout = { order: string[]; hidden: string[] }

/** The layout a tile list currently shows: every key in order, plus the hidden ones. */
export function layoutOf(tiles: readonly PreviewTile[]): PreviewLayout {
  return { order: tiles.map((tile) => tile.key), hidden: tiles.filter((tile) => tile.hidden).map((tile) => tile.key) }
}

/** Orders tiles by the layout and applies its hidden set. A tile the layout does not name goes last, in its given order. */
export function applyLayout(tiles: readonly PreviewTile[], layout: PreviewLayout): PreviewTile[] {
  const rank = new Map(layout.order.map((key, index) => [key, index]))
  const hidden = new Set(layout.hidden)
  return tiles
    .map((tile, index) => ({ tile: { ...tile, hidden: hidden.has(tile.key) }, index }))
    .sort((a, b) => (rank.get(a.tile.key) ?? Infinity) - (rank.get(b.tile.key) ?? Infinity) || a.index - b.index)
    .map(({ tile }) => tile)
}

/** Moves a key to an index of the full order, clamped to its ends. An unknown key changes nothing. */
export function moveTo(layout: PreviewLayout, key: string, toIndex: number): PreviewLayout {
  const from = layout.order.indexOf(key)
  if (from < 0) return layout
  const to = Math.max(0, Math.min(layout.order.length - 1, toIndex))
  if (to === from) return layout
  const order = layout.order.filter((k) => k !== key)
  order.splice(to, 0, key)
  return { ...layout, order }
}

/** Moves a key one visible tile earlier (-1) or later (1), stepping over hidden tiles. At either end it changes nothing. */
export function moveStep(layout: PreviewLayout, key: string, direction: -1 | 1): PreviewLayout {
  const hidden = new Set(layout.hidden)
  const visible = layout.order.filter((k) => !hidden.has(k))
  const at = visible.indexOf(key)
  const neighbour = visible[at + direction]
  if (at < 0 || neighbour === undefined) return layout
  return moveTo(layout, key, layout.order.indexOf(neighbour))
}

export function hideTile(layout: PreviewLayout, key: string): PreviewLayout {
  return layout.hidden.includes(key) ? layout : { ...layout, hidden: [...layout.hidden, key] }
}

export function showTile(layout: PreviewLayout, key: string): PreviewLayout {
  return layout.hidden.includes(key) ? { ...layout, hidden: layout.hidden.filter((k) => k !== key) } : layout
}

export function sameLayout(a: PreviewLayout, b: PreviewLayout): boolean {
  return a.order.join('\n') === b.order.join('\n') && [...a.hidden].sort().join('\n') === [...b.hidden].sort().join('\n')
}
